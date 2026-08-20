//go:build windows

package printer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	winprinter "github.com/alexbrainman/printer"
)

// docSeq numera los documentos que mandamos: el nombre único es lo que
// permite encontrar nuestro trabajo en la cola del spooler para verificarlo.
var docSeq uint64

// writeSystem abre la impresora por su nombre y le manda un documento RAW:
// el spooler no toca los bytes. La librería no toma contexto para escribir;
// el ctx se usa en la verificación de después.
func writeSystem(ctx context.Context, name string, payload []byte) Outcome {
	if name == "" {
		return Outcome{Err: errors.New("la impresora del sistema no tiene nombre")}
	}
	p, err := winprinter.Open(name)
	if err != nil {
		return Outcome{Err: fmt.Errorf("no se pudo abrir la impresora «%s»: %w", name, err)}
	}
	defer p.Close()
	doc := fmt.Sprintf("Manducar %d-%d", os.Getpid(), atomic.AddUint64(&docSeq, 1))
	// RAW primero y XPS_PASS de repuesto, a mano: StartRawDocument de la
	// librería elige uno solo mirando el driver, y esa detección yerra (el
	// Generic/Text del runner de GitHub figura como XPS, manda XPS_PASS y el
	// spooler contesta «The data is invalid»). Una térmica es RAW siempre;
	// el driver v4 que de verdad exige XPS_PASS cae en el segundo intento.
	if err := p.StartDocument(doc, "RAW"); err != nil {
		if err2 := p.StartDocument(doc, "XPS_PASS"); err2 != nil {
			return Outcome{Err: fmt.Errorf("«%s»: el spooler no aceptó abrir el documento (RAW: %v; XPS_PASS: %v)", name, err, err2)}
		}
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
		return Outcome{WroteSomething: true, Err: fmt.Errorf("«%s»: el spooler no aceptó cerrar el documento: %w", name, err)}
	}

	// Que el spooler lo haya aceptado no es que haya salido: con la impresora
	// apagada, Windows se queda el trabajo y reintenta él solo. Igual que en
	// CUPS: unos segundos de espera a que nuestro documento desaparezca de la
	// cola; si sigue ahí, «sin confirmar» del lado del servidor. Si la cola no
	// se puede consultar, se confía en el spooler como antes. Un cancelado se
	// ve mientras el documento siga listado con DELETING/DELETED (en una
	// impresora trabada se queda así un rato); el que desaparece entre dos
	// miradas no se distingue de un impreso.
	vence := time.Now().Add(spoolWait)
	for {
		st, err := estadoDelDoc(p, doc)
		if err != nil {
			return Outcome{OK: true, WroteSomething: true}
		}
		switch st {
		case SpoolCanceled:
			return Outcome{WroteSomething: true, Canceled: true, Err: errors.New("lo cancelaron en la cola de esa computadora")}
		case SpoolPrinted:
			return Outcome{OK: true, WroteSomething: true}
		}
		if time.Now().After(vence) {
			return Outcome{
				WroteSomething: true,
				SpoolID:        doc,
				Err:            fmt.Errorf("el sistema aceptó el trabajo pero %d segundos después seguía en su cola: mirá la impresora «%s» en esa computadora (¿apagada, trabada, en pausa?)", int(spoolWait/time.Second), name),
			}
		}
		select {
		case <-ctx.Done():
			return Outcome{OK: true, WroteSomething: true}
		case <-time.After(spoolPoll):
		}
	}
}

// estadoDelDoc: qué dice el spooler de nuestro documento. Listado con
// DELETING/DELETED es que alguien lo canceló; listado sin eso, sigue en
// cola; que no esté es que salió (o que un cancelado desapareció entre dos
// miradas: Windows no guarda historia y ahí no hay forma de distinguir).
func estadoDelDoc(p *winprinter.Printer, doc string) (SpoolState, error) {
	jobs, err := p.Jobs()
	if err != nil {
		return SpoolStillQueued, err
	}
	for _, j := range jobs {
		if j.DocumentName != doc {
			continue
		}
		if j.StatusCode&(winprinter.JOB_STATUS_DELETING|winprinter.JOB_STATUS_DELETED) != 0 {
			return SpoolCanceled, nil
		}
		return SpoolStillQueued, nil
	}
	return SpoolPrinted, nil
}

// spoolState: lo mismo, abriendo la impresora por su nombre (lo usa el
// vigilante, que mira trabajos de corridas anteriores).
func spoolState(ctx context.Context, name, spoolID string) (SpoolState, error) {
	if err := ctx.Err(); err != nil {
		return SpoolStillQueued, err
	}
	p, err := winprinter.Open(name)
	if err != nil {
		return SpoolStillQueued, err
	}
	defer p.Close()
	return estadoDelDoc(p, spoolID)
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
