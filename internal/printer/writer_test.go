package printer

import (
	"context"
	"errors"
	"io"
	"net"
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
		if got := motivoDeRed(errors.New(crudo)); got != esperado {
			t.Errorf("%q → %q, esperaba %q", crudo, got, esperado)
		}
	}
	if got := motivoDeRed(timeoutErr{}); got != "no responde (¿está apagada o en otra red?)" {
		t.Errorf("timeout → %q", got)
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }
