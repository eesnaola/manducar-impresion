package printer

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/eesnaola/manducar-impresion/internal/api"
)

// fakePrinter escucha en 127.0.0.1:0 y devuelve lo que le escribieron.
func fakePrinter(t *testing.T) (addr string, got <-chan []byte) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan []byte, 1)
	go func() {
		defer ln.Close()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		data, _ := io.ReadAll(conn)
		ch <- data
	}()
	return ln.Addr().String(), ch
}

func TestNetworkWritesAllTheBytes(t *testing.T) {
	addr, got := fakePrinter(t)
	payload := []byte("\x1b@Hola\n\x1dV\x00")

	out := Write(context.Background(), api.Target{Kind: "network", Address: addr}, payload)

	if !out.OK || !out.WroteSomething || out.Err != nil {
		t.Fatalf("%+v", out)
	}
	select {
	case data := <-got:
		if string(data) != string(payload) {
			t.Fatalf("llegó %q", data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("la impresora falsa no recibió nada")
	}
}

func TestConnectionRefusedIsRetryable(t *testing.T) {
	// Un puerto cerrado: se pide uno y se suelta.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()

	out := Write(context.Background(), api.Target{Kind: "network", Address: addr}, []byte("x"))

	if out.OK || out.WroteSomething || out.Err == nil {
		t.Fatalf("tenía que fallar sin escribir nada: %+v", out)
	}
}

func TestUnknownKindFailsWithoutWriting(t *testing.T) {
	out := Write(context.Background(), api.Target{Kind: "fax"}, []byte("x"))
	if out.OK || out.WroteSomething {
		t.Fatalf("%+v", out)
	}
}

func TestSystemPrinterThatDoesNotExistIsRetryable(t *testing.T) {
	out := Write(context.Background(), api.Target{Kind: "system", SystemName: "no-existe-seguro-12345"}, []byte("x"))
	if out.OK || out.WroteSomething {
		t.Fatalf("%+v", out)
	}
}

func TestMotivoDeRedHablaEnCristiano(t *testing.T) {
	casos := map[string]string{
		"dial tcp 192.168.0.50:9100: connect: connection refused": "rechazó la conexión (¿está apagada, o no es el puerto 9100?)",
		"dial tcp 192.168.0.50:9100: connect: no route to host":   "no hay camino hasta esa dirección (¿está en la misma red?)",
		"dial tcp: lookup cocina: no such host":                   "no se encontró ese nombre en la red",
		"algo raro":                                               "algo raro",
	}
	for crudo, esperado := range casos {
		if got := motivoDeRedEn("windows", errors.New(crudo)); got != esperado {
			t.Errorf("%q → %q, esperaba %q", crudo, got, esperado)
		}
	}
	if got := motivoDeRedEn("windows", timeoutErr{}); got != "no responde (¿está apagada o en otra red?)" {
		t.Errorf("timeout → %q", got)
	}
}

// En una Mac, «no route to host» contra una impresora que está en la misma red
// casi nunca es la red: es macOS negándole al agente el permiso de «Red
// local» (macOS 15). Pasó en un local el 21/11/2026: la Mac contestaba «no hay
// camino hasta esa dirección (¿está en la misma red?)» y una PC con Windows,
// al lado, imprimía en esa misma dirección. El mensaje tiene que mandar a
// mirar el permiso, que es lo que la persona puede arreglar.
func TestMotivoDeRedEnMacMandaAMirarElPermisoDeRedLocal(t *testing.T) {
	got := motivoDeRedEn("darwin", errors.New("dial tcp 192.168.1.51:9100: connect: no route to host"))
	for _, quiero := range []string{"Red local", "Privacidad y seguridad", "manducar-impresion"} {
		if !strings.Contains(got, quiero) {
			t.Errorf("en Mac el mensaje no nombra «%s»: %q", quiero, got)
		}
	}
	// Fuera de Mac ese permiso no existe: el mensaje de siempre.
	if got := motivoDeRedEn("linux", errors.New("dial tcp 192.168.1.51:9100: connect: no route to host")); strings.Contains(got, "Red local") {
		t.Errorf("fuera de Mac no hay permiso de Red local que mirar: %q", got)
	}
	// Y lo que no es «no hay camino» no cambia por estar en una Mac.
	if got := motivoDeRedEn("darwin", errors.New("dial tcp 192.168.1.51:9100: connect: connection refused")); strings.Contains(got, "Red local") {
		t.Errorf("una conexión rechazada no es el permiso: %q", got)
	}
}

// El mensaje traducido es para el panel; el error tal cual lo dio el sistema
// tiene que quedar en algún lado, o desde afuera no se distingue un permiso
// negado de una red mal armada.
func TestElErrorDeRedGuardaLoQueDijoElSistema(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nadie escucha ahí: la conexión se rechaza

	out := Write(context.Background(), api.Target{Kind: "network", Address: addr}, []byte("x"))
	if out.OK || out.Err == nil {
		t.Fatalf("%+v", out)
	}
	if !strings.Contains(out.Crudo, "connection refused") {
		t.Errorf("el error del sistema no quedó guardado: %q", out.Crudo)
	}
	if strings.Contains(out.Err.Error(), "dial tcp") {
		t.Errorf("el mensaje para el panel trae el error crudo: %q", out.Err)
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }
