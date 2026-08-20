//go:build !windows

package printer

// Las verificaciones del spooler viven en system_other.go (!windows): sus
// tests van con la misma etiqueta o el vet de Windows no los encuentra.

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLpRequestIDSacaElIdDeLaRespuestaDeLp(t *testing.T) {
	if got := lpRequestID("Dummy_Ticket", "request id is Dummy_Ticket-42 (1 file(s))\n"); got != "Dummy_Ticket-42" {
		t.Errorf("id = %q", got)
	}
	// La misma respuesta en castellano (una Mac en español la da así).
	if got := lpRequestID("Dummy_Ticket", "el id de la petición es Dummy_Ticket-39 (0 archivos)\n"); got != "Dummy_Ticket-39" {
		t.Errorf("id en castellano = %q", got)
	}
	if got := lpRequestID("Dummy_Ticket", "algo que no es lp"); got != "" {
		t.Errorf("sin marca tiene que dar vacío, dio %q", got)
	}
	if got := lpRequestID("Dummy_Ticket", "Dummy_Ticket- sin numero"); got != "" {
		t.Errorf("sin número tiene que dar vacío, dio %q", got)
	}
}

func TestSigueEnColaDistingueTrabadoDeSalido(t *testing.T) {
	ctx := context.Background()
	// Sale en la segunda consulta: no está trabado.
	quedan := 2
	trabado, err := sigueEnCola(ctx, 200*time.Millisecond, time.Millisecond, func() (bool, error) {
		quedan--
		return quedan > 0, nil
	})
	if err != nil || trabado {
		t.Errorf("salió de la cola: trabado=%v err=%v", trabado, err)
	}
	// Nunca sale: trabado.
	trabado, err = sigueEnCola(ctx, 5*time.Millisecond, time.Millisecond, func() (bool, error) { return true, nil })
	if err != nil || !trabado {
		t.Errorf("nunca salió: trabado=%v err=%v", trabado, err)
	}
	// La consulta falla: no se inventa un problema.
	trabado, err = sigueEnCola(ctx, 5*time.Millisecond, time.Millisecond, func() (bool, error) { return false, errors.New("lpstat mudo") })
	if err == nil || trabado {
		t.Errorf("con error no hay veredicto: trabado=%v err=%v", trabado, err)
	}
}

func TestEstadoIPPDistingueCanceladoImpresoYEnCola(t *testing.T) {
	casos := []struct {
		salida string
		quiero SpoolState
		ok     bool
	}{
		{"        job-state (enum) = canceled\n        job-state-reasons (keyword) = processing-to-stop-point\n", SpoolCanceled, true},
		{"        job-state (enum) = aborted\n", SpoolCanceled, true},
		{"        job-state (enum) = completed\n        job-state-reasons (keyword) = processing-to-stop-point\n", SpoolPrinted, true},
		{"        job-state (enum) = processing\n", SpoolStillQueued, true},
		{"        job-state (enum) = pending\n", SpoolStillQueued, true},
		{"sin nada de job-state", SpoolStillQueued, false},
	}
	for _, c := range casos {
		st, ok := estadoIPP(c.salida)
		if st != c.quiero || ok != c.ok {
			t.Errorf("%q → %v/%v, esperaba %v/%v", c.salida[:20], st, ok, c.quiero, c.ok)
		}
	}
}

func TestNumeroDeTrabajo(t *testing.T) {
	if got := numeroDeTrabajo("Dummy_Ticket-48"); got != "48" {
		t.Errorf("48 → %q", got)
	}
	if got := numeroDeTrabajo("Con-guiones-en-el-nombre-102"); got != "102" {
		t.Errorf("102 → %q", got)
	}
	if got := numeroDeTrabajo("sin-numero-"); got != "" {
		t.Errorf("vacío → %q", got)
	}
	if got := numeroDeTrabajo("raro-4a"); got != "" {
		t.Errorf("no numérico → %q", got)
	}
}
