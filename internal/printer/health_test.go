package printer

// El pulso de red, contra una térmica de mentira que contesta DLE EOT.

import (
	"context"
	"net"
	"testing"
	"time"
)

// termicaFalsa contesta DLE EOT con los bytes que le digan. respuestas mapea
// n (1, 2, 4) al byte de estado; el n que no está no se contesta (clon mudo).
func termicaFalsa(t *testing.T, respuestas map[byte]byte) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 3)
				for {
					_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					if n == 3 && buf[0] == 0x10 && buf[1] == 0x04 {
						if b, ok := respuestas[buf[2]]; ok {
							_, _ = c.Write([]byte{b})
						}
					}
				}
			}(conn)
		}
	}()
	return ln.Addr().String()
}

func TestProbeNetworkDistingueLosEstados(t *testing.T) {
	// El byte base con los bits fijos (b1 y b4 en 1): 0x12.
	casos := []struct {
		nombre     string
		respuestas map[byte]byte
		quiero     Health
	}{
		{"viva", map[byte]byte{1: 0x12, 2: 0x12, 4: 0x12}, HealthOK},
		{"tapa abierta", map[byte]byte{1: 0x12, 2: 0x12 | 0x04, 4: 0x12}, HealthCoverOpen},
		{"sin papel (se paró)", map[byte]byte{1: 0x12, 2: 0x12 | 0x20, 4: 0x12}, HealthNoPaper},
		{"sin papel (sensor)", map[byte]byte{1: 0x12, 2: 0x12, 4: 0x12 | 0x60}, HealthNoPaper},
		{"poco papel", map[byte]byte{1: 0x12, 2: 0x12, 4: 0x12 | 0x0C}, HealthPaperLow},
		{"clon mudo", map[byte]byte{}, HealthSilent},
		{"contesta cualquier cosa", map[byte]byte{1: 0xFF}, HealthSilent},
	}
	for _, c := range casos {
		addr := termicaFalsa(t, c.respuestas)
		if got := probeNetwork(context.Background(), addr); got != c.quiero {
			t.Errorf("%s: %s, esperaba %s", c.nombre, got, c.quiero)
		}
	}
}

func TestProbeNetworkApagada(t *testing.T) {
	// Un puerto sin nadie escuchando.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	if got := probeNetwork(context.Background(), addr); got != HealthUnreachable {
		t.Errorf("apagada: %s", got)
	}
}
