//go:build !windows

package printer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
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
	// Sin -s: lp contesta «request id is Nombre-42 …», y ese id es lo que
	// permite verificar después que el papel salió de la cola del sistema.
	// Con LC_ALL=C: lp SÍ traduce esa línea («el id de la petición es …» en
	// una Mac en castellano), y un parseo por palabras se quedaba sin id.
	cmd := exec.CommandContext(ctx, "lp", "-d", name, "-o", "raw")
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
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

	// Que el spooler lo haya aceptado no es que haya salido: con la impresora
	// apagada, CUPS se queda el trabajo y reintenta él solo para siempre.
	// Se espera unos segundos a que salga de la cola del sistema; si sigue
	// ahí, se avisa —escribimos algo, así que del lado del servidor queda
	// «sin confirmar», nunca reimpreso solo—. Si no se puede verificar (el id
	// no se pudo leer, lpstat no contesta), se confía en el spooler como antes.
	id := lpRequestID(name, stdout.String())
	if id == "" {
		return Outcome{OK: true, WroteSomething: true}
	}
	sigue := func() (bool, error) { return spoolQueued(ctx, name, id) }
	trabado, err := sigueEnCola(ctx, spoolWait, spoolPoll, sigue)
	if err != nil {
		return Outcome{OK: true, WroteSomething: true}
	}
	if !trabado {
		// Se fue de la cola adentro de la espera: casi siempre es que salió,
		// pero un cancelado rápido también se va — lo dice el job-state.
		if st, ok := ippJobState(ctx, id); ok && st == SpoolCanceled {
			return Outcome{WroteSomething: true, Canceled: true, Err: errors.New("lo cancelaron en la cola de esa computadora")}
		}
		return Outcome{OK: true, WroteSomething: true}
	}

	return Outcome{
		WroteSomething: true,
		SpoolID:        id,
		Err:            fmt.Errorf("el sistema aceptó el trabajo pero %d segundos después seguía en su cola: mirá la impresora «%s» en esa computadora (¿apagada, trabada, en pausa?)", int(spoolWait/time.Second), name),
	}
}

// spoolQueued pregunta a CUPS si el trabajo sigue en la cola de esa
// impresora. Con LC_ALL=C por lo mismo que lp: la salida no puede depender
// del idioma de la computadora.
func spoolQueued(ctx context.Context, name, spoolID string) (bool, error) {
	out, err := lpstat(ctx, "-o", name)
	if err != nil {
		return false, err
	}
	return strings.Contains(out, spoolID), nil
}

// spoolState: ¿sigue en la cola, salió, o lo cancelaron? La verdad la tiene
// el job-state de IPP —los «avisos» de lpstat mienten: un impreso y un
// cancelado pueden decir los dos «processing-to-stop-point»—. Sin ipptool
// (en Linux viene en cups-ipp-utils y puede faltar), se cae al lpstat de
// antes: en cola o impreso, sin distinguir cancelados.
func spoolState(ctx context.Context, name, spoolID string) (SpoolState, error) {
	if st, ok := ippJobState(ctx, spoolID); ok {
		return st, nil
	}
	enCola, err := spoolQueued(ctx, name, spoolID)
	switch {
	case err != nil:
		return SpoolStillQueued, err
	case enCola:
		return SpoolStillQueued, nil
	default:
		return SpoolPrinted, nil
	}
}

// El pedido de ipptool: preguntar por UN trabajo y mostrar su estado.
const ippJobStateTest = `{
    OPERATION Get-Job-Attributes
    GROUP operation-attributes-tag
    ATTR charset attributes-charset utf-8
    ATTR naturalLanguage attributes-natural-language en
    ATTR uri job-uri $JOBURI
    DISPLAY job-state
}
`

// ippJobState le pregunta al CUPS local el job-state del trabajo. El false
// es «no se pudo saber por acá»: que decida el fallback.
func ippJobState(ctx context.Context, spoolID string) (SpoolState, bool) {
	num := numeroDeTrabajo(spoolID)
	if num == "" {
		return SpoolStillQueued, false
	}
	ipp, err := exec.LookPath("ipptool")
	if err != nil {
		return SpoolStillQueued, false
	}
	f, err := os.CreateTemp("", "manducar-ipp-*.test")
	if err != nil {
		return SpoolStillQueued, false
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(ippJobStateTest); err != nil {
		f.Close()
		return SpoolStillQueued, false
	}
	f.Close()
	c, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	cmd := exec.CommandContext(c, ipp, "-tv", "-d", "JOBURI=ipp://localhost/jobs/"+num, "ipp://localhost/", f.Name())
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	out, err := cmd.Output()
	if err != nil {
		return SpoolStillQueued, false
	}
	return estadoIPP(string(out))
}

// numeroDeTrabajo: de «Dummy_Ticket-48», el 48 (lo que va tras el último guión).
func numeroDeTrabajo(spoolID string) string {
	i := strings.LastIndex(spoolID, "-")
	if i < 0 || i == len(spoolID)-1 {
		return ""
	}
	num := spoolID[i+1:]
	for _, r := range num {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return num
}

// estadoIPP lee el «job-state (enum) = …» de la salida de ipptool. Los
// nombres son del estándar IPP y no se traducen.
func estadoIPP(salida string) (SpoolState, bool) {
	const marca = "job-state (enum) = "
	i := strings.Index(salida, marca)
	if i < 0 {
		return SpoolStillQueued, false
	}
	valor := salida[i+len(marca):]
	if j := strings.IndexAny(valor, " \n\r\t("); j >= 0 {
		valor = valor[:j]
	}
	switch valor {
	case "canceled", "aborted":
		return SpoolCanceled, true
	case "completed":
		return SpoolPrinted, true
	case "pending", "pending-held", "processing", "processing-stopped":
		return SpoolStillQueued, true
	}
	return SpoolStillQueued, false
}

// lpstat corre lpstat con LC_ALL=C y su plazo.
func lpstat(ctx context.Context, args ...string) (string, error) {
	c, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	cmd := exec.CommandContext(c, "lpstat", args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	out, err := cmd.Output()
	return string(out), err
}

// lpRequestID saca el id de la respuesta de lp buscando el patrón
// «Impresora-números», no las palabras de alrededor: aun con LC_ALL=C puesto,
// no hay que apostar el veredicto a que ningún CUPS traduzca la frase
// («request id is …» acá, «el id de la petición es …» en una Mac en
// castellano). Sin id, no se verifica nada: el comportamiento de siempre.
func lpRequestID(name, salida string) string {
	marca := name + "-"
	i := strings.Index(salida, marca)
	if i < 0 {
		return ""
	}
	j := i + len(marca)
	k := j
	for k < len(salida) && salida[k] >= '0' && salida[k] <= '9' {
		k++
	}
	if k == j {
		return ""
	}
	return salida[i:k]
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
