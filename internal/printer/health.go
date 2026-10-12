package printer

// El pulso a una impresora: saber si está viva sin imprimirle nada. Para las
// de red es TCP + DLE EOT (el estado en tiempo real de ESC/POS, que cualquier
// Epson y la mayoría de los clones contestan con un byte sin tocar el buffer
// de impresión); para las del sistema, lo que diga el sistema, y en Windows
// el mismo DLE EOT por el USB si la impresora está enchufada ahí.

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// Health es lo que se le cuenta al servidor (los valores son suyos).
type Health string

const (
	HealthOK          Health = "OK"
	HealthPaperLow    Health = "PAPER_LOW"
	HealthNoPaper     Health = "NO_PAPER"
	HealthCoverOpen   Health = "COVER_OPEN"
	HealthSilent      Health = "SILENT"      // contesta el TCP pero no el DLE EOT
	HealthUnreachable Health = "UNREACHABLE" // ni el TCP
	HealthStopped     Health = "STOPPED"     // del sistema: existe pero frenada
	HealthMissing     Health = "MISSING"     // del sistema: ya no está en la lista
)

const (
	probeDialTimeout = 2 * time.Second
	probeReadTimeout = 700 * time.Millisecond
)

// Probe pulsa una impresora según su tipo.
func Probe(ctx context.Context, target apiTarget) Health {
	if target.Kind == "network" {
		return probeNetwork(ctx, target.Address)
	}
	return probeSystem(ctx, target.SystemName)
}

// apiTarget es lo mínimo del target que el pulso necesita; se declara acá
// para no importar el paquete api desde printer (sería circular al revés).
type apiTarget struct {
	Kind       string
	Address    string
	SystemName string
}

// NewTarget arma el target del pulso.
func NewTarget(kind, address, systemName string) apiTarget {
	return apiTarget{Kind: kind, Address: address, SystemName: systemName}
}

// probeNetwork: TCP y después las preguntas de DLE EOT (ver diagnosticar).
func probeNetwork(ctx context.Context, address string) Health {
	d := net.Dialer{Timeout: probeDialTimeout}
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return HealthUnreachable
	}
	defer conn.Close()

	return diagnosticar(func(n byte) (byte, bool) { return dleEot(conn, n) })
}

// diagnosticar hace las tres preguntas de DLE EOT por donde diga pregunta
// —el socket de una de red, el USB de una del sistema— y traduce los bytes:
// n=1 (¿en línea?), n=2 (¿por qué no? tapa, papel) y n=4 (sensores de
// papel). La que no contesta a tiempo corta: con lo ya sabido alcanza.
func diagnosticar(pregunta func(n byte) (byte, bool)) Health {
	estado, ok := pregunta(1)
	if !ok {
		return HealthSilent
	}
	// El byte de estado tiene bits fijos (b0=0, b1=1, b4=1, b7=0): si no los
	// respeta, esto no es un estado ESC/POS y mejor no inventar diagnóstico.
	if estado&0x93 != 0x12 {
		return HealthSilent
	}

	if causa, ok := pregunta(2); ok {
		if causa&0x04 != 0 { // tapa abierta
			return HealthCoverOpen
		}
		if causa&0x20 != 0 { // se paró por falta de papel
			return HealthNoPaper
		}
	}
	if papel, ok := pregunta(4); ok {
		if papel&0x60 != 0 { // sensor de fin de papel
			return HealthNoPaper
		}
		if papel&0x0C != 0 { // por acabarse
			return HealthPaperLow
		}
	}

	return HealthOK
}

// dleEot manda 0x10 0x04 n y lee el byte de respuesta.
func dleEot(conn net.Conn, n byte) (byte, bool) {
	_ = conn.SetDeadline(time.Now().Add(probeReadTimeout))
	if _, err := conn.Write([]byte{0x10, 0x04, n}); err != nil {
		return 0, false
	}
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err != nil {
		return 0, false
	}
	return buf[0], true
}

// coincidePuerto: ¿el aparato que el registro anota con «Base Name» y «Port
// Number» (USB y 1) es el del puerto de la impresora del sistema («USB001»)?
// Sin «Base Name», el USB de siempre.
func coincidePuerto(base string, numero uint64, puerto string) bool {
	if base == "" {
		base = "USB"
	}
	return puerto != "" && strings.EqualFold(puerto, fmt.Sprintf("%s%03d", base, numero))
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
