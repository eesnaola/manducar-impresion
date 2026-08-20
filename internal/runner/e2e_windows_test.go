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
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
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
	"github.com/eesnaola/manducar-impresion/internal/printer"
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

// La autoactualización, con el binario REAL como proceso aparte: el latido
// anuncia una versión nueva servida por el test, el agente la baja, verifica
// el sha256, se reemplaza a sí mismo y rearranca ya siendo la nueva. Es el
// camino que en Windows tiene sus propias mañas (un .exe corriendo no se
// puede borrar) y que hasta acá sólo se había visto andar en Mac.
func TestE2EWindowsAutoactualizacion(t *testing.T) {
	dir := t.TempDir()
	raiz, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	vieja := filepath.Join(dir, "agente.exe")
	nueva := filepath.Join(dir, "nueva.exe")
	for bin, version := range map[string]string{vieja: "0.0.1-e2e", nueva: "0.0.2-e2e"} {
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-X main.Version="+version, "-o", bin, ".")
		cmd.Dir = raiz
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go build %s: %v\n%s", version, err, out)
		}
	}
	nuevaBytes, err := os.ReadFile(nueva)
	if err != nil {
		t.Fatal(err)
	}
	suma := sha256.Sum256(nuevaBytes)

	// La prueba de vida del binario nuevo es su LATIDO: el proceso viejo, al
	// actualizarse, larga un hijo con la consola oculta y el stdio en NUL,
	// así que su log no se puede mirar — pero su latido trae la versión.
	var mu sync.Mutex
	versiones := map[string]bool{}
	var web *httptest.Server
	web = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/agente/latido":
			var body struct {
				Version string `json:"version"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			versiones[body.Version] = true
			mu.Unlock()
			fmt.Fprintf(w, `{"mercure":{"url":"http://127.0.0.1:1/.well-known/mercure","jwt":"j","topic":"t"},"agent":{"version":"0.0.2-e2e","sha256":"%x","url":"%s/descarga"},"jobsPending":0,"heartbeatSeconds":5}`, suma, web.URL)
		case r.URL.Path == "/descarga":
			_, _ = w.Write(nuevaBytes)
		case r.URL.Path == "/agente/trabajos":
			_, _ = w.Write([]byte(`{"jobs":[]}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer web.Close()

	// El hijo rearrancado no es nuestro proceso: al final se lo baja por
	// nombre de imagen, o el TempDir no se puede borrar.
	t.Cleanup(func() {
		_, _ = ps(t, "taskkill /F /IM agente.exe 2>$null; Start-Sleep -Milliseconds 500")
	})

	cfg := filepath.Join(dir, "impresion.json")
	t.Setenv("MANDUCAR_IMPRESION_CONFIG", cfg)
	if err := config.Save(config.Config{Server: web.URL, Token: "tok", AgentID: 1, Store: "E2E"}); err != nil {
		t.Fatal(err)
	}

	salida, err := os.Create(filepath.Join(dir, "salida.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer salida.Close()
	agente := exec.Command(vieja, "correr")
	agente.Env = append(os.Environ(), "MANDUCAR_IMPRESION_CONFIG="+cfg)
	agente.Stdout = salida
	agente.Stderr = salida
	if err := agente.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agente.Process.Kill() }()

	// El primer latido anuncia la nueva; el agente baja, verifica, se pisa y
	// rearranca. La prueba de vida es el latido del binario nuevo.
	esperar(t, 120*time.Second, "el latido del 0.0.2-e2e", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return versiones["0.0.2-e2e"]
	})
	b, _ := os.ReadFile(filepath.Join(dir, "salida.log"))
	if !strings.Contains(string(b), "actualizado a 0.0.2-e2e") {
		t.Fatalf("no quedó el rastro de la actualización:\n%s", b)
	}
	// Y el archivo del agente ES el binario nuevo (byte a byte).
	instalado, err := os.ReadFile(vieja)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(instalado) != suma {
		t.Error("el binario instalado no es el nuevo")
	}
}

// El semáforo en Windows de verdad: una impresora sobre un Standard TCP/IP
// Port en crudo, con una térmica de mentira atrás que contesta DLE EOT. Es
// lo que valida GetPrinter + el registro + el pulso al socket, que acá se
// escribió sin una máquina Windows a mano.
func TestE2EWindowsSemaforo(t *testing.T) {
	const (
		imp    = "ManducarSemaforo"
		puerto = "ManducarSemaforo_TCP"
	)
	// La térmica de mentira: contesta DLE EOT 2 con «tapa abierta».
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	tcp := ln.Addr().(*net.TCPAddr)
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
						switch buf[2] {
						case 2:
							_, _ = c.Write([]byte{0x12 | 0x04}) // tapa abierta
						default:
							_, _ = c.Write([]byte{0x12})
						}
					}
				}
			}(conn)
		}
	}()

	if _, err := ps(t, fmt.Sprintf("Add-PrinterPort -Name '%s' -PrinterHostAddress '127.0.0.1' -PortNumber %d", puerto, tcp.Port)); err != nil {
		t.Skipf("no se pudo crear el puerto TCP: %v", err)
	}
	t.Cleanup(func() {
		_, _ = ps(t, "Remove-Printer -Name '"+imp+"' -ErrorAction SilentlyContinue; Remove-PrinterPort -Name '"+puerto+"' -ErrorAction SilentlyContinue")
	})
	psFatal(t, "Add-Printer -Name '"+imp+"' -DriverName 'Generic / Text Only' -PortName '"+puerto+"'")

	target := printer.NewTarget("system", "", imp)

	// Con la térmica contestando: el pulso atraviesa el spooler y trae el
	// estado ESC/POS de atrás.
	if got := printer.Probe(context.Background(), target); got != printer.HealthCoverOpen {
		t.Errorf("con la tapa abierta: %s", got)
	}

	// Pausada en Windows: frenada, sin importar el socket.
	psFatal(t, "Invoke-CimMethod -MethodName Pause -InputObject (Get-CimInstance Win32_Printer -Filter \"Name='"+imp+"'\") | Out-Null")
	if got := printer.Probe(context.Background(), target); got != printer.HealthStopped {
		t.Errorf("pausada: %s", got)
	}
	psFatal(t, "Invoke-CimMethod -MethodName Resume -InputObject (Get-CimInstance Win32_Printer -Filter \"Name='"+imp+"'\") | Out-Null")

	// Térmica apagada: no responde, aunque el spooler diga «listo».
	_ = ln.Close()
	if got := printer.Probe(context.Background(), target); got != printer.HealthUnreachable {
		t.Errorf("apagada: %s", got)
	}

	// Y la que no existe en esta computadora.
	if got := printer.Probe(context.Background(), printer.NewTarget("system", "", "NoExisteTal")); got != printer.HealthMissing {
		t.Errorf("inexistente: %s", got)
	}
}
