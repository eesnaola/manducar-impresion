//go:build windows && windowse2e

package runner

// El E2E de Windows: el agente REAL (Run) contra un servidor de mentira y el
// spooler de verdad del runner de GitHub Actions, con una impresora
// «Generic / Text Only» que escribe a un archivo (la arma el workflow
// windows-e2e.yml). Es la única prueba que ve andar en Windows lo que acá se
// programó a ciegas: la verificación del spooler, el vigilante y los
// cancelados. Corre sólo con -tags windowse2e.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	winprinter "github.com/alexbrainman/printer"

	"github.com/eesnaola/manducar-impresion/internal/api"
	"github.com/eesnaola/manducar-impresion/internal/config"
)

// sondaDatatypes prueba a mano qué acepta el spooler para esta impresora:
// es el diagnóstico para cuando el camino RAW falla en el runner y no hay
// una máquina Windows donde mirarlo.
func sondaDatatypes(t *testing.T, impresora string) {
	t.Helper()
	for _, dt := range []string{"RAW", "TEXT"} {
		p, err := winprinter.Open(impresora)
		if err != nil {
			t.Logf("sonda: Open: %v", err)
			return
		}
		err = p.StartDocument("sonda "+dt, dt)
		if err != nil {
			t.Logf("sonda: StartDocument(%s): %v", dt, err)
			p.Close()
			continue
		}
		_, werr := p.Write([]byte("sonda " + dt + "\r\n"))
		eerr := p.EndDocument()
		t.Logf("sonda: %s → start ok, write %v, end %v", dt, werr, eerr)
		p.Close()
	}
}

// servidorE2E reparte trabajos de a uno y anota todo lo que el agente reporta.
type servidorE2E struct {
	mu        sync.Mutex
	porDar    []map[string]any
	reportado map[int][]api.Result
}

func (s *servidorE2E) armar(id int, printerName, payload string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.porDar = append(s.porDar, map[string]any{
		"id":      id,
		"kind":    "TEST",
		"printer": map[string]any{"kind": "system", "systemName": printerName},
		"payload": base64.StdEncoding.EncodeToString([]byte(payload)),
	})
}

func (s *servidorE2E) resultados(id int) []api.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]api.Result(nil), s.reportado[id]...)
}

func (s *servidorE2E) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/agente/latido":
			// Mercure a un puerto cerrado: manda el poll de respaldo (5 s).
			_, _ = w.Write([]byte(`{"mercure":{"url":"http://127.0.0.1:1/.well-known/mercure","jwt":"j","topic":"t"},"agent":null,"jobsPending":1,"heartbeatSeconds":5}`))
		case r.URL.Path == "/agente/trabajos":
			s.mu.Lock()
			jobs := s.porDar
			s.porDar = nil
			s.mu.Unlock()
			if jobs == nil {
				jobs = []map[string]any{}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jobs": jobs})
		case strings.HasPrefix(r.URL.Path, "/agente/trabajos/"):
			var res api.Result
			_ = json.NewDecoder(r.Body).Decode(&res)
			id, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/agente/trabajos/"), "/resultado"))
			s.mu.Lock()
			s.reportado[id] = append(s.reportado[id], res)
			s.mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(404)
		}
	}
}

// ps corre PowerShell y devuelve la salida. Los fallos no cortan el test acá:
// cada llamador sabe si eran fatales.
func ps(t *testing.T, comando string) (string, error) {
	t.Helper()
	out, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", comando).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func psFatal(t *testing.T, comando string) string {
	t.Helper()
	out, err := ps(t, comando)
	if err != nil {
		t.Fatalf("powershell %q: %v\n%s", comando, err, out)
	}
	return out
}

func esperar(t *testing.T, plazo time.Duration, que string, ok func() bool) {
	t.Helper()
	vence := time.Now().Add(plazo)
	for time.Now().Before(vence) {
		if ok() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("me cansé de esperar: %s", que)
}

func TestE2EWindowsSpooler(t *testing.T) {
	impresora := os.Getenv("MANDUCAR_E2E_PRINTER")
	if impresora == "" {
		impresora = "ManducarE2E"
	}
	archivo := os.Getenv("MANDUCAR_E2E_ARCHIVO")
	if archivo == "" {
		archivo = `C:\e2e\salida.prn`
	}
	if _, err := ps(t, "Get-Printer -Name '"+impresora+"' | Out-Null"); err != nil {
		t.Skipf("no está la impresora %s: la arma el workflow", impresora)
	}
	// Limpieza al entrar y al salir: cola vacía y sin pausa.
	limpiar := func() {
		_, _ = ps(t, "Get-PrintJob -PrinterName '"+impresora+"' | Remove-PrintJob")
		_, _ = ps(t, "Invoke-CimMethod -MethodName Resume -InputObject (Get-CimInstance Win32_Printer -Filter \"Name='"+impresora+"'\") | Out-Null")
	}
	limpiar()
	t.Cleanup(limpiar)
	pausar := func() {
		psFatal(t, "Invoke-CimMethod -MethodName Pause -InputObject (Get-CimInstance Win32_Printer -Filter \"Name='"+impresora+"'\") | Out-Null")
	}
	reanudar := func() {
		psFatal(t, "Invoke-CimMethod -MethodName Resume -InputObject (Get-CimInstance Win32_Printer -Filter \"Name='"+impresora+"'\") | Out-Null")
	}
	trabajosEnCola := func() int {
		out, err := ps(t, "(Get-PrintJob -PrinterName '"+impresora+"' | Measure-Object).Count")
		if err != nil {
			return -1
		}
		n, _ := strconv.Atoi(out)
		return n
	}

	sondaDatatypes(t, impresora)

	srv := &servidorE2E{reportado: map[int][]api.Result{}}
	web := httptest.NewServer(srv.handler())
	defer web.Close()

	dir := t.TempDir()
	t.Setenv("MANDUCAR_IMPRESION_CONFIG", filepath.Join(dir, "impresion.json"))
	if err := config.Save(config.Config{Server: web.URL, Token: "tok", AgentID: 1, Store: "E2E"}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	terminado := make(chan error, 1)
	go func() { terminado <- Run(ctx, "e2e") }()

	// ── 1. Impresora viva: imprime y reporta OK, y los bytes llegan al archivo.
	srv.armar(1, impresora, "MANDUCAR-E2E-UNO\n")
	esperar(t, 90*time.Second, "el resultado OK del trabajo 1", func() bool {
		rs := srv.resultados(1)
		return len(rs) > 0 && rs[0].OK
	})
	esperar(t, 30*time.Second, "los bytes del trabajo 1 en el archivo de la impresora", func() bool {
		b, err := os.ReadFile(archivo)
		return err == nil && strings.Contains(string(b), "MANDUCAR-E2E-UNO")
	})

	// ── 2. Cola pausada: a los ~10 s reporta que quedó en la cola del sistema.
	pausar()
	srv.armar(2, impresora, "MANDUCAR-E2E-DOS\n")
	esperar(t, 120*time.Second, "el «seguía en su cola» del trabajo 2", func() bool {
		rs := srv.resultados(2)
		return len(rs) > 0 && !rs[0].OK && rs[0].WroteSomething && !rs[0].Canceled && strings.Contains(rs[0].Error, "cola")
	})

	// ── 3. Vuelve la impresora: el vigilante avisa que al final salió.
	reanudar()
	esperar(t, 120*time.Second, "el «al final salió» del trabajo 2", func() bool {
		for _, r := range srv.resultados(2) {
			if r.OK {
				return true
			}
		}
		return false
	})

	// ── 4. Cancelado con la cola pausada. Acá el test MIDE además de afirmar:
	// si Windows deja ver el DELETING, el veredicto es «canceled»; si el
	// trabajo desaparece entre dos miradas, sale como impreso (documentado).
	// Lo firme: alguna resolución tiene que llegar, sin colgarse.
	pausar()
	srv.armar(3, impresora, "MANDUCAR-E2E-TRES\n")
	esperar(t, 60*time.Second, "el trabajo 3 en la cola del spooler", func() bool { return trabajosEnCola() > 0 })
	psFatal(t, "Get-PrintJob -PrinterName '"+impresora+"' | Remove-PrintJob")
	esperar(t, 150*time.Second, "alguna resolución del trabajo 3", func() bool {
		return len(srv.resultados(3)) > 0
	})
	res3 := srv.resultados(3)
	switch {
	case res3[0].Canceled:
		t.Logf("cancelado detectado en Windows: %+v", res3[0])
	case res3[0].OK:
		t.Logf("el cancelado salió como impreso (Windows lo borró entre dos miradas): %+v", res3[0])
	default:
		t.Logf("el cancelado quedó «en su cola» primero: %+v (se resuelve por el vigilante)", res3[0])
	}

	cancel()
	select {
	case err := <-terminado:
		if err != nil {
			t.Fatalf("Run terminó con error: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run no terminó tras cancelar el contexto")
	}
	fmt.Println("E2E completo")
}
