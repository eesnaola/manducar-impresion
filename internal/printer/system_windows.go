//go:build windows

package printer

import (
	"context"
	"errors"
	"fmt"

	winprinter "github.com/alexbrainman/printer"
)

// writeSystem abre la impresora por su nombre y le manda un documento RAW:
// el spooler no toca los bytes. ctx se ignora: la librería no lo soporta,
// pero la firma queda simétrica con la de system_other.go.
func writeSystem(_ context.Context, name string, payload []byte) Outcome {
	if name == "" {
		return Outcome{Err: errors.New("la impresora del sistema no tiene nombre")}
	}
	p, err := winprinter.Open(name)
	if err != nil {
		return Outcome{Err: fmt.Errorf("no se pudo abrir la impresora «%s»: %w", name, err)}
	}
	defer p.Close()
	if err := p.StartRawDocument("Manducar"); err != nil {
		return Outcome{Err: fmt.Errorf("«%s»: %w", name, err)}
	}
	// A partir de acá el spooler ya tiene un trabajo abierto: aunque el
	// primer Write falle con 0 bytes escritos, ese trabajo ya existe y no
	// hay que dejar que el servidor lo reintente como si nada hubiera pasado.
	docStarted := true
	written := 0
	for written < len(payload) {
		n, err := p.Write(payload[written:])
		written += n
		if err != nil {
			_ = p.EndDocument()
			return Outcome{WroteSomething: docStarted || written > 0, Err: fmt.Errorf("«%s»: se cortó tras %d bytes: %w", name, written, err)}
		}
	}
	if err := p.EndDocument(); err != nil {
		return Outcome{WroteSomething: true, Err: fmt.Errorf("«%s»: %w", name, err)}
	}
	return Outcome{OK: true, WroteSomething: true}
}

// listSystemCtx pregunta al spooler local. No se cuelga como el CUPS de
// Unix y la librería no toma contexto: se mira el plazo antes de llamar y
// listo, así la firma es la misma que en el otro sistema.
func listSystemCtx(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	names, err := winprinter.ReadNames()
	if err != nil {
		return nil, err
	}
	return names, nil
}
