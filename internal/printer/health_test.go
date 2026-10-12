package printer

// El pulso de red, contra una térmica de mentira que contesta DLE EOT.

import (
	"context"
	"fmt"
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

// Los bytes que contestó la POS-58 de El Caracol por USB (12/10/2026): con
// la tapa cerrada, 16 / 12 / 12 —el 16 trae prendido el bit del cajón, que
// no dice nada de la impresora—. Con la tapa abierta no contesta nada.
func TestDiagnosticarConLaPOS58(t *testing.T) {
	casos := []struct {
		nombre     string
		respuestas map[byte]byte
		quiero     Health
	}{
		{"tapa cerrada", map[byte]byte{1: 0x16, 2: 0x12, 4: 0x12}, HealthOK},
		{"tapa abierta (no contesta)", map[byte]byte{}, HealthSilent},
		{"sin papel", map[byte]byte{1: 0x16, 2: 0x12, 4: 0x12 | 0x60}, HealthNoPaper},
	}
	for _, c := range casos {
		pregunta := func(n byte) (byte, bool) {
			b, ok := c.respuestas[n]
			return b, ok
		}
		if got := diagnosticar(pregunta); got != c.quiero {
			t.Errorf("%s: %s, esperaba %s", c.nombre, got, c.quiero)
		}
	}
}

// El puerto de la impresora del sistema («USB001») contra lo que el
// registro dice del aparato: «Base Name» USB y «Port Number» 1.
func TestCoincidePuertoUSB(t *testing.T) {
	casos := []struct {
		base   string
		numero uint64
		puerto string
		quiero bool
	}{
		{"USB", 1, "USB001", true},
		{"USB", 1, "usb001", true},
		{"USB", 12, "USB012", true},
		{"", 1, "USB001", true},
		{"USB", 1, "USB002", false},
		{"USB", 1, "COM1", false},
		{"USB", 1, "", false},
	}
	for _, c := range casos {
		if got := coincidePuerto(c.base, c.numero, c.puerto); got != c.quiero {
			t.Errorf("coincidePuerto(%q, %d, %q) = %v", c.base, c.numero, c.puerto, got)
		}
	}
}

// A qué aparato se le pregunta. abren son las rutas que se dejan abrir (las
// enchufadas); el resto son entradas viejas del registro.
func TestElegirAparatos(t *testing.T) {
	conPuerto := func(ruta string, numero uint64) aparatoUSB {
		return aparatoUSB{ruta: ruta, base: "USB", numero: numero, conPuerto: true}
	}
	sinPuerto := func(ruta string) aparatoUSB { return aparatoUSB{ruta: ruta} }
	casos := []struct {
		nombre   string
		aparatos []aparatoUSB
		abren    []string
		quiero   []string
	}{
		{"el que tiene anotado ese puerto", []aparatoUSB{conPuerto("a", 1), conPuerto("b", 2)}, []string{"a", "b"}, []string{"b"}},
		{"los anotados en ese puerto aunque no se abran: desenchufada no responde", []aparatoUSB{conPuerto("a", 2), conPuerto("b", 2)}, nil, []string{"a", "b"}},
		// El Caracol, 12/10/2026: una sola entrada, sin puerto anotado donde se buscaba.
		{"sin puerto anotado, la única enchufada", []aparatoUSB{sinPuerto("a")}, []string{"a"}, []string{"a"}},
		{"sin puerto anotado, la única enchufada entre viejas", []aparatoUSB{sinPuerto("a"), sinPuerto("b")}, []string{"b"}, []string{"b"}},
		{"sin puerto anotado y dos enchufadas: no se adivina", []aparatoUSB{sinPuerto("a"), sinPuerto("b")}, []string{"a", "b"}, nil},
		{"la de otro puerto es otra impresora y no se toca", []aparatoUSB{conPuerto("a", 1)}, []string{"a"}, nil},
		{"la de otro puerto no cuenta para el descarte", []aparatoUSB{conPuerto("a", 1), sinPuerto("b")}, []string{"a", "b"}, []string{"b"}},
		{"sin aparatos", nil, nil, nil},
	}
	for _, c := range casos {
		abre := func(ruta string) bool {
			for _, r := range c.abren {
				if r == ruta {
					return true
				}
			}
			return false
		}
		got := elegirAparatos(c.aparatos, "USB002", abre)
		if fmt.Sprint(got) != fmt.Sprint(c.quiero) {
			t.Errorf("%s: elegirAparatos = %v, quiero %v", c.nombre, got, c.quiero)
		}
	}
}
