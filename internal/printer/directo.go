package printer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/eesnaola/manducar-impresion/internal/api"
)

// ErrNoAplica: el paso del diagnóstico no se puede hacer con esta impresora
// —por la cola en una de red, directo en una del sistema sin aparato USB ni
// puerto TCP detrás—. No es un error de la impresora: el diagnóstico lo
// anota como «no aplica» y sigue con el próximo paso.
var ErrNoAplica = errors.New("no aplica")

// Directo es la impresora sin la cola de por medio: lo que se escribe le
// llega tal cual, y lo que contesta (DLE EOT, GS I) se puede leer.
type Directo interface {
	Escribir(b []byte, plazo time.Duration) error
	// Leer espera hasta plazo a que la impresora diga algo; nil si no dijo nada.
	Leer(plazo time.Duration) []byte
	Close() error
}

// AbrirDirecto abre la impresora sin la cola: por TCP la de red; la del
// sistema, por el puerto TCP crudo o el aparato USB que tiene detrás.
func AbrirDirecto(ctx context.Context, t api.Target) (Directo, error) {
	switch t.Kind {
	case "network":
		return abrirTCP(ctx, t.Address)
	case "system":
		return abrirDirectoSistema(ctx, t.SystemName)
	}
	return nil, fmt.Errorf("%w: no sé qué impresora es %q", ErrNoAplica, t.Kind)
}

// EscribirCola manda un documento por la cola del sistema y espera a que
// salga, como un trabajo. Una impresora de red no tiene cola.
func EscribirCola(ctx context.Context, t api.Target, b []byte) error {
	if t.Kind != "system" {
		return fmt.Errorf("%w: es una impresora de red, no tiene cola", ErrNoAplica)
	}
	o := writeSystem(ctx, t.SystemName, b)
	switch {
	case o.OK:
		return nil
	case o.SpoolID != "":
		return fmt.Errorf("entró a la cola y a los %s seguía ahí", spoolWait)
	case o.Err != nil:
		return o.Err
	}
	return errors.New("no salió")
}

type tcpDirecto struct{ c net.Conn }

func abrirTCP(ctx context.Context, address string) (Directo, error) {
	d := net.Dialer{Timeout: dialTimeout}
	c, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("no se pudo conectar a %s: %w", address, err)
	}
	return &tcpDirecto{c: c}, nil
}

func (d *tcpDirecto) Escribir(b []byte, plazo time.Duration) error {
	_ = d.c.SetWriteDeadline(time.Now().Add(plazo))
	_, err := d.c.Write(b)
	return err
}

func (d *tcpDirecto) Leer(plazo time.Duration) []byte {
	_ = d.c.SetReadDeadline(time.Now().Add(plazo))
	buf := make([]byte, 1024)
	n, _ := d.c.Read(buf)
	if n == 0 {
		return nil
	}
	return buf[:n]
}

func (d *tcpDirecto) Close() error { return d.c.Close() }
