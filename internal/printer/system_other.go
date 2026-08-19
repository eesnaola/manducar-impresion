//go:build !windows

package printer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// lpTimeout: si el llamador no puso un plazo propio, no dejamos que un CUPS
// colgado bloquee para siempre.
const lpTimeout = 30 * time.Second

// writeSystem manda los bytes crudos por CUPS: `lp -d NOMBRE -o raw`.
func writeSystem(ctx context.Context, name string, payload []byte) Outcome {
	if name == "" {
		return Outcome{Err: errors.New("la impresora del sistema no tiene nombre")}
	}
	// Listar tiene su propio plazo: un CUPS colgado clavaba la cola de esa
	// impresora para siempre. Si no contesta, el trabajo falla sin escribir
	// nada y el servidor lo puede reintentar solo.
	lctx, cancel := context.WithTimeout(ctx, listTimeout)
	names, err := listSystemCtx(lctx)
	cancel()
	if err != nil {
		return Outcome{Err: fmt.Errorf("no se pudieron listar las impresoras: %w", err)}
	}
	if !contains(names, name) {
		return Outcome{Err: fmt.Errorf("la impresora «%s» no existe en esta computadora", name)}
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, lpTimeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "lp", "-d", name, "-o", "raw", "-s")
	cmd.Stdin = bytes.NewReader(payload)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// lp aceptó o no aceptó el trabajo entero: si falló, no se encoló.
		// Sin stderr no hay que quedarse mudo: algo hay que decirle al que
		// mire el log.
		motivo := strings.TrimSpace(stderr.String())
		switch {
		case motivo != "":
		case ctx.Err() != nil:
			motivo = "lp no respondió"
		default:
			motivo = err.Error()
		}
		return Outcome{Err: fmt.Errorf("lp: %s", motivo)}
	}
	return Outcome{OK: true, WroteSomething: true}
}

// listSystemCtx: `lpstat -e` lista los destinos, uno por línea. Con contexto,
// porque un CUPS que no contesta no puede colgar ni el latido ni un trabajo.
func listSystemCtx(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, "lpstat", "-e").Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("CUPS no respondió")
		}
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		if l := strings.TrimSpace(line); l != "" {
			names = append(names, l)
		}
	}
	return names, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
