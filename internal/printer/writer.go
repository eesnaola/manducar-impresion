// Package printer escribe bytes en una impresora: por TCP a host:puerto, o
// por el spooler del sistema. No sabe qué son los bytes.
package printer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/eesnaola/manducar-impresion/internal/api"
)

const (
	dialTimeout  = 5 * time.Second
	writeTimeout = 15 * time.Second
	// listTimeout: lo que se le aguanta al sistema para decir qué impresoras
	// tiene. Es una pregunta, no una impresión: si tarda más que esto hay
	// algo colgado y conviene contestar sin la lista.
	listTimeout = 10 * time.Second
)

// Outcome distingue lo que el servidor necesita distinguir: si no se
// escribió nada, el trabajo se puede reintentar; si se escribió algo, pudo
// haber salido y decide una persona.
type Outcome struct {
	OK             bool
	WroteSomething bool
	Err            error
	// SpoolID: cuando el trabajo quedó trabado en la cola del sistema, con
	// qué nombre buscarlo ahí (el request id de CUPS, el documento en
	// Windows). Es lo que permite avisar después «al final salió».
	SpoolID string
	// Canceled: se fue de la cola del sistema porque alguien lo canceló ahí
	// (no salió ni va a salir).
	Canceled bool
}

// SpoolState: qué fue de un trabajo que quedó en la cola del sistema.
type SpoolState int

const (
	// SpoolStillQueued: sigue en la cola, esperando a la impresora.
	SpoolStillQueued SpoolState = iota
	// SpoolPrinted: ya no está en la cola y nada dice que lo hayan cancelado.
	SpoolPrinted
	// SpoolCanceled: alguien lo canceló en esa computadora (donde se
	// distingue; en Windows un cancelado sale como Printed).
	SpoolCanceled
)

// Spool dice qué fue de ese trabajo. name es la impresora; spoolID, lo que
// dejó Outcome.SpoolID.
func Spool(ctx context.Context, name, spoolID string) (SpoolState, error) {
	return spoolState(ctx, name, spoolID)
}

func Write(ctx context.Context, target api.Target, payload []byte) Outcome {
	switch target.Kind {
	case "network":
		return writeNetwork(ctx, target.Address, payload)
	case "system":
		return writeSystem(ctx, target.SystemName, payload)
	default:
		return Outcome{Err: fmt.Errorf("tipo de impresora desconocido: %q", target.Kind)}
	}
}

// ListSystem: las impresoras que conoce el sistema operativo, para que el
// panel las ofrezca por su nombre exacto. Vacío si no se pudo averiguar.
func ListSystem() []string {
	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	defer cancel()
	names, _ := listSystemCtx(ctx)
	return names
}

// ListSystemContext es lo mismo, para el que quiera poner su propio plazo y
// saber por qué falló.
func ListSystemContext(ctx context.Context) ([]string, error) { return listSystemCtx(ctx) }

func writeNetwork(ctx context.Context, address string, payload []byte) Outcome {
	if address == "" {
		return Outcome{Err: errors.New("la impresora de red no tiene dirección")}
	}
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return Outcome{Err: fmt.Errorf("no se pudo conectar a %s: %s", address, motivoDeRed(err))}
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))

	written := 0
	for written < len(payload) {
		n, err := conn.Write(payload[written:])
		written += n
		if err != nil {
			return Outcome{WroteSomething: written > 0, Err: fmt.Errorf("se cortó al escribir en %s tras %d bytes: %w", address, written, err)}
		}
	}
	return Outcome{OK: true, WroteSomething: true}
}

// motivoDeRed traduce el error de conexión a lo que la persona puede hacer:
// el texto crudo («dial tcp 192.168.0.50:9100: connect: connection refused»)
// termina en el panel del local, y ahí nadie sabe qué es un dial.
func motivoDeRed(err error) string {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "no responde (¿está apagada o en otra red?)"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"):
		return "rechazó la conexión (¿está apagada, o no es el puerto 9100?)"
	case strings.Contains(msg, "no route to host"), strings.Contains(msg, "network is unreachable"):
		return "no hay camino hasta esa dirección (¿está en la misma red?)"
	case strings.Contains(msg, "no such host"):
		return "no se encontró ese nombre en la red"
	}
	return msg
}
