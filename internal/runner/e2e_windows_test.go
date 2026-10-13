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
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

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
	soltarArchivoDeCaidas(t)
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

// ── El vigía ─────────────────────────────────────────────────────────────────
//
// Lo que sigue corre el binario REAL de «vigilar», como lo arranca el .vbs de
// la clave Run: un vigía que lleva a «correr --log-archivo» de hijo y lo
// levanta si se cae. Cada prueba usa un nombre de ejecutable propio
// (vigilado.exe, doble.exe, migrado.exe) para no pisarse con las otras, y baja
// a los suyos al terminar.
//
// Los procesos de una prueba se reconocen por su línea de comandos y no por el
// nombre de imagen. Después de la primera actualización el vigía sigue vivo
// corriendo desde el «.old» (el archivo se renombró con el proceso adentro) y
// no está dicho que Windows siga llamándolo igual; la línea de comandos, que
// quedó como se lo lanzó, no cambia.

const (
	runKeyE2E   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValueE2E = "ManducarImpresion" // el mismo que anota «instalar»
)

// liberacionE2E es una versión que el servidor de mentira sabe anunciar y servir.
type liberacionE2E struct {
	version string
	bin     []byte
	suma    [sha256.Size]byte
}

// servidorVigiaE2E anota los latidos de cada versión y las caídas que le
// cuentan, y anuncia las liberaciones de a una: mientras no haya latido de la
// primera, anuncia la primera; cuando late, anuncia la que sigue; y así hasta
// quedarse con la última. Sin liberaciones no anuncia nada.
type servidorVigiaE2E struct {
	mu      sync.Mutex
	web     *httptest.Server
	libs    []liberacionE2E
	latidos map[string]int
	caidas  []api.CrashReport
}

func nuevoServidorVigiaE2E(t *testing.T, libs ...liberacionE2E) *servidorVigiaE2E {
	t.Helper()
	s := &servidorVigiaE2E{libs: libs, latidos: map[string]int{}}
	s.web = httptest.NewServer(http.HandlerFunc(s.atender))
	// Las limpiezas corren al revés: éste se cierra DESPUÉS de bajar a los
	// agentes, que todavía podrían estar en medio de un pedido.
	t.Cleanup(s.web.Close)
	return s
}

// anunciada: la primera liberación de la que todavía no llegó un latido, o la
// última si ya late cada una. Se llama con el candado tomado.
func (s *servidorVigiaE2E) anunciada() *liberacionE2E {
	for i := range s.libs {
		if s.latidos[s.libs[i].version] == 0 {
			return &s.libs[i]
		}
	}
	if len(s.libs) == 0 {
		return nil
	}
	return &s.libs[len(s.libs)-1]
}

func (s *servidorVigiaE2E) atender(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/agente/latido":
		var body struct {
			Version string `json:"version"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.latidos[body.Version]++
		var agente *api.Release
		if l := s.anunciada(); l != nil {
			agente = &api.Release{Version: l.version, SHA256: fmt.Sprintf("%x", l.suma), URL: s.web.URL + "/descarga/" + l.version}
		}
		s.mu.Unlock()
		// Mercure a un puerto cerrado: sólo importa el latido.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"mercure":          map[string]string{"url": "http://127.0.0.1:1/.well-known/mercure", "jwt": "j", "topic": "t"},
			"agent":            agente,
			"jobsPending":      0,
			"heartbeatSeconds": 5,
		})
	case strings.HasPrefix(r.URL.Path, "/descarga/"):
		version := strings.TrimPrefix(r.URL.Path, "/descarga/")
		for _, l := range s.libs {
			if l.version == version {
				_, _ = w.Write(l.bin)
				return
			}
		}
		w.WriteHeader(404)
	case r.URL.Path == "/agente/caida":
		var inf api.CrashReport
		_ = json.NewDecoder(r.Body).Decode(&inf)
		s.mu.Lock()
		s.caidas = append(s.caidas, inf)
		s.mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	case r.URL.Path == "/agente/trabajos":
		_, _ = w.Write([]byte(`{"jobs":[]}`))
	default:
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
}

func (s *servidorVigiaE2E) latidosDe(version string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latidos[version]
}

func (s *servidorVigiaE2E) caidasVistas() []api.CrashReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]api.CrashReport(nil), s.caidas...)
}

// compilarAgenteE2E compila el agente de este repositorio con esa versión
// puesta, como lo hace la actualización: es el binario de verdad.
func compilarAgenteE2E(t *testing.T, destino, version string) liberacionE2E {
	t.Helper()
	raiz, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-X main.Version="+version, "-o", destino, ".")
	cmd.Dir = raiz
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", version, err, out)
	}
	bin, err := os.ReadFile(destino)
	if err != nil {
		t.Fatal(err)
	}
	return liberacionE2E{version: version, bin: bin, suma: sha256.Sum256(bin)}
}

// configurarE2E deja la configuración del agente de prueba y apunta la
// variable de entorno a ella (la que usa config.Save y la que ven los hijos).
func configurarE2E(t *testing.T, dir, servidor string) string {
	t.Helper()
	cfg := filepath.Join(dir, "impresion.json")
	t.Setenv("MANDUCAR_IMPRESION_CONFIG", cfg)
	if err := config.Save(config.Config{Server: servidor, Token: "tok", AgentID: 1, Store: "E2E"}); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// entornoE2E es el entorno del test con la configuración puesta y SIN la marca
// del vigía: lo que se lance con esto no se cree vigilado.
func entornoE2E(cfg string) []string {
	var env []string
	for _, kv := range os.Environ() {
		clave := strings.ToUpper(strings.SplitN(kv, "=", 2)[0])
		if clave == "MANDUCAR_VIGILADO" || clave == "MANDUCAR_IMPRESION_CONFIG" {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "MANDUCAR_IMPRESION_CONFIG="+cfg)
}

// procesoE2E es un proceso de la prueba, tal como lo ve Windows.
type procesoE2E struct {
	pid, ppid int
	nombre    string
	ruta      string
	comando   string
}

// procesosE2E lista los procesos cuya línea de comandos nombra a ese
// ejecutable, sin contar al PowerShell que pregunta (su propia línea de
// comandos lo nombra).
func procesosE2E(t *testing.T, exe string) ([]procesoE2E, error) {
	t.Helper()
	out, err := ps(t, `Get-CimInstance Win32_Process | Where-Object { $_.ProcessId -ne $PID -and $_.CommandLine -like '*\`+exe+`*' } | ForEach-Object { 'PROC|{0}|{1}|{2}|{3}|{4}' -f $_.ProcessId, $_.ParentProcessId, $_.Name, $_.ExecutablePath, $_.CommandLine }`)
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, out)
	}
	var lista []procesoE2E
	for _, linea := range strings.Split(out, "\n") {
		linea = strings.TrimSpace(linea)
		if !strings.HasPrefix(linea, "PROC|") {
			continue
		}
		partes := strings.SplitN(linea, "|", 6)
		if len(partes) < 6 {
			continue
		}
		pid, err1 := strconv.Atoi(partes[1])
		ppid, err2 := strconv.Atoi(partes[2])
		if err1 != nil || err2 != nil {
			continue
		}
		lista = append(lista, procesoE2E{pid: pid, ppid: ppid, nombre: partes[3], ruta: partes[4], comando: partes[5]})
	}
	return lista, nil
}

func (p procesoE2E) String() string {
	return fmt.Sprintf("pid %d (padre %d) %s — %s — %s", p.pid, p.ppid, p.nombre, p.ruta, p.comando)
}

func textoProcesosE2E(lista []procesoE2E) string {
	var b strings.Builder
	for _, p := range lista {
		b.WriteString("  " + p.String() + "\n")
	}
	return b.String()
}

// afirmarProcesosE2E: quedan exactamente `quiero` procesos de ese ejecutable
// (el vigía y su agente son dos) y siguen siendo esos unos segundos después.
// Primero se les da un rato para que el que se está yendo termine de irse; el
// repaso final es para que se note el que sobra.
func afirmarProcesosE2E(t *testing.T, exe string, quiero int) {
	t.Helper()
	vence := time.Now().Add(20 * time.Second)
	for time.Now().Before(vence) {
		if lista, err := procesosE2E(t, exe); err == nil && len(lista) == quiero {
			break
		}
		time.Sleep(time.Second)
	}
	time.Sleep(3 * time.Second)
	lista, err := procesosE2E(t, exe)
	if err != nil {
		t.Fatalf("no pude listar los procesos de %s: %v", exe, err)
	}
	if len(lista) != quiero {
		t.Fatalf("hay %d procesos de %s; quiero %d (el vigía y su agente):\n%s", len(lista), exe, quiero, textoProcesosE2E(lista))
	}
}

// matarInstalacionE2E baja a todo lo que corra de ese ejecutable: por la línea
// de comandos y, por si acaso, por el nombre de imagen. Dos vueltas, por si el
// vigía alcanzó a levantar otro hijo en el medio. Si no, el TempDir no se
// puede borrar (hay un .exe corriendo adentro).
func matarInstalacionE2E(t *testing.T, exe string) {
	for i := 0; i < 2; i++ {
		_, _ = ps(t, `Get-CimInstance Win32_Process | Where-Object { $_.ProcessId -ne $PID -and $_.CommandLine -like '*\`+exe+`*' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }; taskkill /F /IM `+exe+` 2>$null; Start-Sleep -Milliseconds 700`)
	}
}

func colaDeArchivoE2E(ruta string, tope int) string {
	b, err := os.ReadFile(ruta)
	if err != nil {
		return "(no se pudo leer: " + err.Error() + ")"
	}
	if len(b) > tope {
		b = b[len(b)-tope:]
	}
	return string(b)
}

// volcarDiagnosticoE2E, sólo si la prueba falló, deja en el log del CI lo que
// hace falta para entender qué pasó sin una máquina Windows a mano: los
// procesos que quedaron, los archivos de la carpeta y los logs.
func volcarDiagnosticoE2E(t *testing.T, dir, exe string) {
	if !t.Failed() {
		return
	}
	if lista, err := procesosE2E(t, exe); err != nil {
		t.Logf("diagnóstico: no pude listar los procesos de %s: %v", exe, err)
	} else {
		t.Logf("diagnóstico: procesos de %s:\n%s", exe, textoProcesosE2E(lista))
	}
	if entradas, err := os.ReadDir(dir); err == nil {
		var b strings.Builder
		for _, e := range entradas {
			if info, err := e.Info(); err == nil {
				fmt.Fprintf(&b, "  %s (%d bytes)\n", e.Name(), info.Size())
			}
		}
		t.Logf("diagnóstico: archivos en %s:\n%s", dir, b.String())
	}
	for _, nombre := range []string{"impresion.log", "vigia.log", "impresion.crash", "salida.log"} {
		if _, err := os.Stat(filepath.Join(dir, nombre)); err != nil {
			continue
		}
		t.Logf("diagnóstico: final de %s:\n%s", nombre, colaDeArchivoE2E(filepath.Join(dir, nombre), 6000))
	}
}

// engordarLogE2E le agrega al log del agente hasta pasar `tam` bytes, con el
// agente escribiéndolo (abre para agregar, como él).
func engordarLogE2E(t *testing.T, p string, tam int64) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	renglon := []byte(strings.Repeat("relleno del e2e ", 63) + "\n")
	for n := int64(0); n <= tam; n += int64(len(renglon)) {
		if _, err := f.Write(renglon); err != nil {
			t.Fatal(err)
		}
	}
}

// vigiaE2E es una instalación de mentira con su vigía andando.
type vigiaE2E struct {
	dir string
	exe string // el ejecutable instalado (ruta entera)
	nom string // y su nombre, con .exe
	srv *servidorVigiaE2E
	vig *exec.Cmd
}

// arrancarVigiaE2E compila una versión por cada nombre de `versiones`: la
// primera queda instalada con `<nombre>.exe` y es la que arranca el vigía; las
// otras las sirve el servidor, de a una, como actualizaciones.
func arrancarVigiaE2E(t *testing.T, nombre string, versiones ...string) *vigiaE2E {
	t.Helper()
	dir := t.TempDir()
	bins := t.TempDir()
	exe := filepath.Join(dir, nombre+".exe")
	var libs []liberacionE2E
	for i, version := range versiones {
		if i == 0 {
			compilarAgenteE2E(t, exe, version)
			continue
		}
		libs = append(libs, compilarAgenteE2E(t, filepath.Join(bins, fmt.Sprintf("v%d.exe", i)), version))
	}
	srv := nuevoServidorVigiaE2E(t, libs...)
	cfg := configurarE2E(t, dir, srv.web.URL)

	// Las limpiezas corren al revés: primero el diagnóstico (con los procesos
	// todavía vivos), después se los baja.
	t.Cleanup(func() { matarInstalacionE2E(t, nombre+".exe") })
	t.Cleanup(func() { volcarDiagnosticoE2E(t, dir, nombre+".exe") })

	vig := exec.Command(exe, "vigilar")
	vig.Env = entornoE2E(cfg)
	if err := vig.Start(); err != nil {
		t.Fatal(err)
	}
	return &vigiaE2E{dir: dir, exe: exe, nom: nombre + ".exe", srv: srv, vig: vig}
}

// El vigía en Windows de verdad: arranca el agente, el agente se actualiza y
// el vigía levanta el binario nuevo (queda uno solo), y cuando al agente lo
// matan, el vigía lo vuelve a levantar y la caída llega al servidor.
func TestE2EWindowsVigia(t *testing.T) {
	const vieja, nueva = "0.0.1-vigia", "0.0.2-vigia"
	v := arrancarVigiaE2E(t, "vigilado", vieja, nueva)

	// 1. Se actualiza y el vigía levanta el nuevo.
	esperar(t, 120*time.Second, "el latido del "+nueva, func() bool { return v.srv.latidosDe(nueva) > 0 })
	// Quedan exactamente dos procesos: el vigía y un agente.
	afirmarProcesosE2E(t, v.nom, 2)
	// El archivo instalado ES el binario nuevo (byte a byte).
	instalado, err := os.ReadFile(v.exe)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(instalado) != v.srv.libs[0].suma {
		t.Error("el binario instalado no es el nuevo")
	}
	// Actualizarse bajo el vigía (salida con código 3) no es una caída.
	if n := len(v.srv.caidasVistas()); n != 0 {
		t.Fatalf("la actualización se contó como caída: %+v", v.srv.caidasVistas())
	}

	// 2. Se mata al agente (no al vigía): vuelve a arrancar y cuenta la caída.
	// Antes se le engorda el log: el que arranca tiene que poder rotarlo, y no
	// puede si el vigía lo tiene abierto (en Windows no se renombra un
	// archivo abierto). El aviso de que no pudo terminaba en impresion.crash
	// y se contaba como caída.
	logAgente := filepath.Join(v.dir, "impresion.log")
	engordarLogE2E(t, logAgente, 10<<20+1)
	antes := v.srv.latidosDe(nueva)
	muertos := psFatal(t, fmt.Sprintf(`Get-CimInstance Win32_Process | Where-Object { $_.ParentProcessId -eq %d -and $_.CommandLine -like '*correr*' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force; $_.ProcessId }`, v.vig.Process.Pid))
	if strings.TrimSpace(muertos) == "" {
		t.Fatalf("no encontré al agente hijo del vigía (pid %d)", v.vig.Process.Pid)
	}
	esperar(t, 60*time.Second, "el informe de la caída", func() bool { return len(v.srv.caidasVistas()) > 0 })
	caidas := v.srv.caidasVistas()
	if caidas[0].Codigo == nil || *caidas[0].Codigo == 0 || !strings.Contains(caidas[0].Resumen, "el agente terminó con código") {
		t.Errorf("informe = %+v", caidas[0])
	}
	// El que arrancó rotó el log engordado, y el vigía escribe en el suyo.
	if info, err := os.Stat(logAgente + ".1"); err != nil || info.Size() <= 10<<20 {
		t.Errorf("el agente no rotó el log al arrancar bajo el vigía (impresion.log.1: %v)", err)
	}
	if b, err := os.ReadFile(filepath.Join(v.dir, "vigia.log")); err != nil || !strings.Contains(string(b), "arranco el agente") {
		t.Errorf("vigia.log no dice que el vigía arrancó al agente (%v):\n%s", err, colaDeArchivoE2E(filepath.Join(v.dir, "vigia.log"), 2000))
	}
	// Contada, se vacía: el agente trunca impresion.crash con el vigía
	// teniéndolo abierto (ahí va la salida de error del hijo), y en Windows eso
	// tiene que poder hacerse. Se espera un poco: el informe llega al servidor
	// un instante antes de que se vacíe.
	crash := filepath.Join(v.dir, "impresion.crash")
	esperar(t, 15*time.Second, "impresion.crash vacío después de contar la caída", func() bool {
		info, err := os.Stat(crash)
		return err == nil && info.Size() == 0
	})
	// El agente que contó la caída es el que levantó el vigía: late y quedan dos.
	if v.srv.latidosDe(nueva) <= antes {
		t.Errorf("después de la caída no llegó ningún latido nuevo (había %d)", antes)
	}
	afirmarProcesosE2E(t, v.nom, 2)
}

// Dos actualizaciones seguidas bajo el mismo vigía. Después de la primera el
// vigía sigue vivo corriendo desde el «.old», que queda en uso: la segunda no
// puede pisarlo ni borrarlo, y antes eso la dejaba sin actualizar.
func TestE2EWindowsVigiaDosActualizaciones(t *testing.T) {
	const v1, v2, v3 = "0.0.1-vigia", "0.0.2-vigia", "0.0.3-vigia"
	v := arrancarVigiaE2E(t, "doble", v1, v2, v3)

	esperar(t, 120*time.Second, "el latido del "+v2+" (la primera actualización)", func() bool { return v.srv.latidosDe(v2) > 0 })
	// El servidor anuncia la tercera apenas late la segunda.
	esperar(t, 120*time.Second, "el latido del "+v3+" (la segunda actualización, con el .old de la primera en uso por el vigía)", func() bool { return v.srv.latidosDe(v3) > 0 })

	afirmarProcesosE2E(t, v.nom, 2)
	instalado, err := os.ReadFile(v.exe)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(instalado) != v.srv.libs[1].suma {
		t.Error("el binario instalado no es el 0.0.3")
	}
	if n := len(v.srv.caidasVistas()); n != 0 {
		t.Errorf("las actualizaciones se contaron como caídas: %+v", v.srv.caidasVistas())
	}
	// Dónde quedó cada anterior, para el que mire el log del CI.
	if entradas, err := os.ReadDir(v.dir); err == nil {
		var b strings.Builder
		for _, e := range entradas {
			b.WriteString("  " + e.Name() + "\n")
		}
		t.Logf("archivos en %s:\n%s", v.dir, b.String())
	}

	// Lo que hacen vincular, desinstalar y el reinicio de la instalación de
	// usuario (stopRunning): «taskkill /F /IM» con el nombre de imagen del
	// ejecutable instalado. Tiene que bajar a los dos —al agente y al vigía,
	// que corre desde el «.old» de la primera actualización— y que no vuelva a
	// arrancar nada.
	out, err := ps(t, "taskkill /F /IM "+v.nom)
	t.Logf("taskkill /F /IM %s (%v):\n%s", v.nom, err, out)
	vence := time.Now().Add(15 * time.Second)
	for time.Now().Before(vence) {
		if lista, err := procesosE2E(t, v.nom); err == nil && len(lista) == 0 {
			break
		}
		time.Sleep(time.Second)
	}
	time.Sleep(3 * time.Second) // el vigía, si quedó vivo, levanta otro al segundo
	lista, err := procesosE2E(t, v.nom)
	if err != nil {
		t.Fatalf("no pude listar los procesos de %s: %v", v.nom, err)
	}
	if len(lista) != 0 {
		t.Fatalf("después de «taskkill /F /IM %s» quedan %d procesos; quiero ninguno:\n%s", v.nom, len(lista), textoProcesosE2E(lista))
	}
}

// rutaLargaE2E: el TEMP del runner viene con nombres cortos (RUNNER~1), y el
// .vbs tiene que nombrar al ejecutable con la misma ruta con que el agente se
// la va a encontrar (os.Executable). En su forma larga hay una sola manera de
// escribirla.
func rutaLargaE2E(t *testing.T, ruta string) string {
	t.Helper()
	p, err := windows.UTF16PtrFromString(ruta)
	if err != nil {
		return ruta
	}
	n, err := windows.GetLongPathName(p, nil, 0)
	if err != nil || n == 0 {
		t.Logf("no pude pasar %s a su forma larga: %v", ruta, err)
		return ruta
	}
	buf := make([]uint16, n)
	n, err = windows.GetLongPathName(p, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		t.Logf("no pude pasar %s a su forma larga: %v", ruta, err)
		return ruta
	}
	return windows.UTF16ToString(buf[:n])
}

// lanzadorViejoE2E es el .vbs que dejaba «instalar» antes del vigía (ver
// vbsLauncher en internal/svc): arranca «correr --log-archivo» a secas. Está
// escrito a mano porque svc depende de este paquete.
func lanzadorViejoE2E(exe, cfg string) string {
	vbs := func(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
	return strings.Join([]string{
		"' Lanzador de antes del vigia: arranca el agente sin ventana.",
		`Set sh = CreateObject("WScript.Shell")`,
		`sh.Environment("PROCESS").Item("MANDUCAR_IMPRESION_CONFIG") = ` + vbs(cfg),
		"sh.Run " + vbs(`"`+exe+`" correr --log-archivo`) + ", 0, False",
		"",
	}, "\r\n")
}

// Una instalación de antes del vigía se pasa sola: el agente que arranca con
// «correr» y encuentra su lanzador viejo en la clave Run lo reescribe para que
// arranque «vigilar», lo lanza y sale; en su lugar queda el vigía con su hijo.
//
// Toca la clave Run del usuario (HKCU), así que sólo corre en el CI, que es
// una máquina descartable: en la de alguien, dejaría su arranque cambiado.
func TestE2EWindowsVigiaMigracion(t *testing.T) {
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		t.Skip("toca HKCU\\Software\\Microsoft\\Windows\\CurrentVersion\\Run: sólo corre en GitHub Actions")
	}
	const (
		nombre  = "migrado.exe"
		version = "0.0.1-migrado"
	)
	dir := rutaLargaE2E(t, t.TempDir())
	exe := filepath.Join(dir, nombre)
	compilarAgenteE2E(t, exe, version)

	srv := nuevoServidorVigiaE2E(t)
	cfg := configurarE2E(t, dir, srv.web.URL)

	// Lo último que se registra corre primero: el diagnóstico con todo vivo, y
	// después se saca la clave Run (que no vuelva a lanzar nada) y se baja a
	// los procesos.
	t.Cleanup(func() {
		if k, err := registry.OpenKey(registry.CURRENT_USER, runKeyE2E, registry.SET_VALUE); err == nil {
			_ = k.DeleteValue(runValueE2E)
			k.Close()
		}
		matarInstalacionE2E(t, nombre)
	})
	t.Cleanup(func() { volcarDiagnosticoE2E(t, dir, nombre) })

	// La instalación vieja: el lanzador que corre «correr», anotado en Run
	// como lo anota «instalar».
	vbs := filepath.Join(dir, "manducar-impresion.vbs")
	if err := os.WriteFile(vbs, []byte(lanzadorViejoE2E(exe, cfg)), 0o644); err != nil {
		t.Fatal(err)
	}
	raizWin := os.Getenv("SystemRoot")
	if raizWin == "" {
		raizWin = `C:\Windows`
	}
	wscript := filepath.Join(raizWin, "System32", "wscript.exe")
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyE2E, registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	err = k.SetStringValue(runValueE2E, `"`+wscript+`" "`+vbs+`"`)
	k.Close()
	if err != nil {
		t.Fatal(err)
	}

	// El agente de antes del vigía: «correr», sin la marca de vigilado.
	salida, err := os.Create(filepath.Join(dir, "salida.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer salida.Close()
	viejo := exec.Command(exe, "correr")
	viejo.Env = entornoE2E(cfg)
	viejo.Stdout = salida
	viejo.Stderr = salida
	if err := viejo.Start(); err != nil {
		t.Fatal(err)
	}
	terminado := make(chan error, 1)
	go func() { terminado <- viejo.Wait() }()

	// 1. Sale enseguida y con 0: dejó el lugar al vigía.
	select {
	case err := <-terminado:
		if err != nil {
			t.Fatalf("el agente viejo terminó con error (quiero 0): %v", err)
		}
	case <-time.After(30 * time.Second):
		_ = viejo.Process.Kill()
		t.Fatal("el agente viejo no salió: no se pasó al vigía")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "salida.log")); !strings.Contains(string(b), "me paso al vigía") {
		t.Errorf("no quedó el rastro de la migración en su salida:\n%s", b)
	}

	// 2. El lanzador ahora arranca el vigía, y sigue clavándole la configuración.
	nuevo, err := os.ReadFile(vbs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(nuevo), " vigilar --log-archivo") || strings.Contains(string(nuevo), " correr") {
		t.Errorf("el .vbs no quedó arrancando «vigilar --log-archivo»:\n%s", nuevo)
	}
	if !strings.Contains(string(nuevo), "MANDUCAR_IMPRESION_CONFIG") {
		t.Errorf("el .vbs perdió la configuración:\n%s", nuevo)
	}

	// 3. Los latidos siguen llegando: es el hijo del vigía (el viejo salió sin
	// latir, antes de que arrancara el runner).
	esperar(t, 90*time.Second, "los latidos del agente vigilado", func() bool { return srv.latidosDe(version) >= 3 })

	// 4. Quedan exactamente dos procesos —el vigía y su agente—, y uno es hijo
	// del otro.
	afirmarProcesosE2E(t, nombre, 2)
	lista, err := procesosE2E(t, nombre)
	if err != nil {
		t.Fatal(err)
	}
	var vigia, agente *procesoE2E
	for i := range lista {
		switch {
		case strings.Contains(lista[i].comando, " vigilar"):
			vigia = &lista[i]
		case strings.Contains(lista[i].comando, " correr"):
			agente = &lista[i]
		}
	}
	if vigia == nil || agente == nil {
		t.Fatalf("quería un «vigilar» y un «correr»; hay:\n%s", textoProcesosE2E(lista))
	}
	if agente.ppid != vigia.pid {
		t.Errorf("el agente (pid %d) no es hijo del vigía (pid %d): su padre es %d", agente.pid, vigia.pid, agente.ppid)
	}
}
