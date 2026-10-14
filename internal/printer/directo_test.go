package printer

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/eesnaola/manducar-impresion/internal/api"
)

func TestDirectoPorRedPreguntaYLeeLaRespuesta(t *testing.T) {
	addr := termicaFalsa(t, map[byte]byte{1: 0x16})

	d, err := AbrirDirecto(context.Background(), api.Target{Kind: "network", Address: addr})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.Escribir([]byte{0x10, 0x04, 0x01}, time.Second); err != nil {
		t.Fatal(err)
	}
	if got := d.Leer(time.Second); len(got) != 1 || got[0] != 0x16 {
		t.Fatalf("Leer = %x, quería 16", got)
	}
}

func TestDirectoQueNoContestaDevuelveNada(t *testing.T) {
	addr := termicaFalsa(t, map[byte]byte{})
	d, err := AbrirDirecto(context.Background(), api.Target{Kind: "network", Address: addr})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	_ = d.Escribir([]byte{0x10, 0x04, 0x01}, time.Second)

	if got := d.Leer(200 * time.Millisecond); got != nil {
		t.Fatalf("Leer = %x, quería nada", got)
	}
}

func TestDirectoAUnaImpresoraApagadaEsUnErrorYNoUnNoAplica(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	_, err = AbrirDirecto(context.Background(), api.Target{Kind: "network", Address: addr})
	if err == nil || errors.Is(err, ErrNoAplica) {
		t.Fatalf("err = %v; quería un error de conexión", err)
	}
}

func TestLaColaEnUnaDeRedNoAplica(t *testing.T) {
	err := EscribirCola(context.Background(), api.Target{Kind: "network", Address: "127.0.0.1:9100"}, []byte("x"))

	if !errors.Is(err, ErrNoAplica) {
		t.Fatalf("err = %v; quería ErrNoAplica", err)
	}
}

func TestElEntornoDeUnaDeRed(t *testing.T) {
	e := DescribirEntorno(context.Background(), api.Target{Kind: "network", Address: "192.168.0.50:9100"})

	if e.Tipo != "network" || e.Direccion != "192.168.0.50:9100" || e.Impresora != "192.168.0.50:9100" {
		t.Fatalf("entorno = %+v", e)
	}
}
