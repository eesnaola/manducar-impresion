//go:build linux && linuxe2e

package main

// El E2E de Linux: el BINARIO de verdad —vincular, instalar, imprimir,
// caerse, actualizarse y desinstalar— contra un servidor de mentira, en el
// runner de GitHub Actions (linux-e2e.yml), que es un Linux amd64 con systemd.
// Es la única prueba que ve andar en Linux lo que se programó sin una PC con
// Linux a mano: la unidad de systemd del usuario, la cola de CUPS y el
// reemplazo del ejecutable al actualizarse.
//
// En un contenedor no se puede: sin systemd de PID 1 no hay `systemctl
// --user`, y un amd64 emulado con QEMU en una Mac se cae en el runtime de Go
// apenas espera a un proceso hijo (le da direcciones de 48 bits, que en un
// amd64 real no existen).
//
// Corre sólo con -tags linuxe2e. Lo que se le dice por variables de entorno:
//
//	MANDUCAR_E2E_AGENTE        el binario a probar (si falta, compila el del repo)
//	MANDUCAR_E2E_AGENTE_NUEVO  el binario al que se actualiza (ídem)
//	MANDUCAR_E2E_SYSTEMD=1     instala de verdad, como unidad de systemd del
//	                           usuario: OJO, usa el HOME de quien lo corre. Sin
//	                           esto corre «correr» en primer plano, con un HOME
//	                           de prueba, y saltea lo que es de systemd.
//	MANDUCAR_E2E_COLA          una cola cruda de CUPS que apunta a
//	MANDUCAR_E2E_COLA_PUERTO   socket://127.0.0.1:ese-puerto; sin las dos, la
//	                           impresión por CUPS se saltea.
//
// En GitHub Actions nada se saltea: lo que falta, falla.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/eesnaola/manducar-impresion/internal/api"
)

const (
	codigoE2E = "12345678"
	tokenE2E  = "token-e2e-linux"
	unidadE2E = "manducar-impresion"
)

// servidorLinuxE2E es el Manducar de mentira: entrega el token al vincular,
// cuenta los latidos por versión, reparte los trabajos armados, guarda sus
// resultados y —cuando se le pide— anuncia una versión nueva y la sirve.
type servidorLinuxE2E struct {
	mu         sync.Mutex
	web        *httptest.Server
	vinculo    map[string]any
	latidos    map[string]int
	sinToken   int
	impresoras []string
	pendientes []map[string]any
	resultados map[int][]api.Result
	nueva      *api.Release
	binNuevo   []byte
}

func nuevoServidorLinuxE2E(t *testing.T) *servidorLinuxE2E {
	t.Helper()
	s := &servidorLinuxE2E{latidos: map[string]int{}, resultados: map[int][]api.Result{}}
	s.web = httptest.NewServer(http.HandlerFunc(s.atender))
	t.Cleanup(s.web.Close)
	return s
}

func (s *servidorLinuxE2E) atender(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Mercure a un puerto cerrado: los trabajos se entregan por el latido.
	mercure := map[string]string{"url": "http://127.0.0.1:1/.well-known/mercure", "jwt": "j", "topic": "t"}
	if r.URL.Path != "/agente/vincular" && strings.HasPrefix(r.URL.Path, "/agente/") && r.Header.Get("Authorization") != "Bearer "+tokenE2E {
		s.sinToken++
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.URL.Path == "/agente/vincular":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["code"] != codigoE2E {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"código inválido o vencido"}`))
			return
		}
		s.vinculo = body
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token": tokenE2E, "agentId": 7,
			"store":   map[string]string{"slug": "e2e", "name": "Local E2E"},
			"mercure": mercure,
		})
	case r.URL.Path == "/agente/latido":
		var body struct {
			Version        string   `json:"version"`
			SystemPrinters []string `json:"systemPrinters"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.latidos[body.Version]++
		if body.SystemPrinters != nil {
			s.impresoras = body.SystemPrinters
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"mercure": mercure, "agent": s.nueva,
			"jobsPending": len(s.pendientes), "heartbeatSeconds": 5,
		})
	case r.URL.Path == "/agente/trabajos":
		jobs := s.pendientes
		if jobs == nil {
			jobs = []map[string]any{}
		}
		s.pendientes = nil
		_ = json.NewEncoder(w).Encode(map[string]any{"jobs": jobs})
	case strings.HasPrefix(r.URL.Path, "/agente/trabajos/") && strings.HasSuffix(r.URL.Path, "/resultado"):
		id, _ := strconv.Atoi(strings.Split(strings.TrimPrefix(r.URL.Path, "/agente/trabajos/"), "/")[0])
		var res api.Result
		_ = json.NewDecoder(r.Body).Decode(&res)
		s.resultados[id] = append(s.resultados[id], res)
		_, _ = w.Write([]byte(`{"ok":true}`))
	case r.URL.Path == "/descarga/nuevo":
		_, _ = w.Write(s.binNuevo)
	default:
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
}

func (s *servidorLinuxE2E) armar(id int, destino map[string]string, payload []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendientes = append(s.pendientes, map[string]any{
		"id": id, "kind": "TEST", "printer": destino,
		"payload": base64.StdEncoding.EncodeToString(payload),
	})
}

func (s *servidorLinuxE2E) resultado(id int) (api.Result, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs := s.resultados[id]
	if len(rs) == 0 {
		return api.Result{}, false
	}
	return rs[len(rs)-1], true
}

func (s *servidorLinuxE2E) latidosDe(version string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latidos[version]
}

func (s *servidorLinuxE2E) latidosTotales() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.latidos {
		n += c
	}
	return n
}

func (s *servidorLinuxE2E) pedidosSinToken() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sinToken
}

func (s *servidorLinuxE2E) anunciar(version string, bin []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.binNuevo = bin
	s.nueva = &api.Release{Version: version, SHA256: fmt.Sprintf("%x", sha256.Sum256(bin)), URL: s.web.URL + "/descarga/nuevo"}
}

// impresoraE2E hace de térmica: escucha en un puerto y junta lo que le
// manden. Es a lo que le habla el agente por red, y lo que tiene detrás la
// cola cruda de CUPS.
type impresoraE2E struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	addr string
}

func escucharImpresoraE2E(t *testing.T, addr string) *impresoraE2E {
	t.Helper()
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("no se pudo escuchar en %s: %v", addr, err)
	}
	t.Cleanup(func() { l.Close() })
	p := &impresoraE2E{addr: l.Addr().String()}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				data, _ := io.ReadAll(c)
				p.mu.Lock()
				p.buf.Write(data)
				p.mu.Unlock()
			}()
		}
	}()
	return p
}

func (p *impresoraE2E) recibio(payload []byte) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return bytes.Contains(p.buf.Bytes(), payload)
}

func (p *impresoraE2E) veces(payload []byte) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return bytes.Count(p.buf.Bytes(), payload)
}

func esperarE2E(t *testing.T, plazo time.Duration, que string, ok func() bool) {
	t.Helper()
	limite := time.Now().Add(plazo)
	for time.Now().Before(limite) {
		if ok() {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("pasaron %s y no pasó: %s", plazo, que)
}

// exigirOSaltear: en GitHub Actions lo que falta es una falla —ahí el
// workflow lo arma todo, y un salteo silencioso sería una prueba que no
// prueba—; en la máquina de uno se saltea.
func exigirOSaltear(t *testing.T, motivo string) {
	t.Helper()
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		t.Fatal(motivo)
	}
	t.Skip(motivo)
}

// binarioE2E devuelve el ejecutable de esa variable de entorno; si no hay,
// compila el del repositorio con esa versión, como lo hace el release.
func binarioE2E(t *testing.T, variable, version string) string {
	t.Helper()
	if p := os.Getenv(variable); p != "" {
		return p
	}
	destino := filepath.Join(t.TempDir(), "manducar-impresion-"+version)
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w -X main.Version="+version, "-o", destino, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", version, err, out)
	}
	return destino
}

func versionDeE2E(t *testing.T, exe string) string {
	t.Helper()
	out, err := exec.Command(exe, "version").Output()
	if err != nil {
		t.Fatalf("%s version: %v", exe, err)
	}
	return strings.TrimSpace(string(out))
}

func copiarE2E(t *testing.T, origen, destino string) {
	t.Helper()
	data, err := os.ReadFile(origen)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destino, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

func systemctlE2E(args ...string) (string, error) {
	out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func pidPrincipalE2E() int {
	out, _ := systemctlE2E("show", unidadE2E, "--property=MainPID", "--value")
	n, _ := strconv.Atoi(out)
	return n
}

func colaDeArchivoLinuxE2E(ruta string, tope int) string {
	data, err := os.ReadFile(ruta)
	if err != nil {
		return "(" + err.Error() + ")"
	}
	if len(data) > tope {
		data = data[len(data)-tope:]
	}
	return string(data)
}

func TestE2ELinux(t *testing.T) {
	conSystemd := os.Getenv("MANDUCAR_E2E_SYSTEMD") == "1"
	if !conSystemd && os.Getenv("GITHUB_ACTIONS") == "true" {
		t.Fatal("en GitHub Actions el E2E de Linux corre con MANDUCAR_E2E_SYSTEMD=1")
	}
	cola, puertoCola := os.Getenv("MANDUCAR_E2E_COLA"), os.Getenv("MANDUCAR_E2E_COLA_PUERTO")

	viejo := binarioE2E(t, "MANDUCAR_E2E_AGENTE", "0.0.1-e2e")
	nuevo := binarioE2E(t, "MANDUCAR_E2E_AGENTE_NUEVO", "0.0.2-e2e")
	v1, v2 := versionDeE2E(t, viejo), versionDeE2E(t, nuevo)
	if v1 == v2 {
		t.Fatalf("los dos binarios dicen la misma versión (%s): no hay a qué actualizarse", v1)
	}
	binNuevo, err := os.ReadFile(nuevo)
	if err != nil {
		t.Fatal(err)
	}

	// Dónde vive todo. Con systemd, en el HOME de verdad: el gestor del
	// usuario lee las unidades de ahí y de ningún otro lado. Sin systemd, en
	// una carpeta de la prueba.
	home := os.Getenv("HOME")
	entorno := os.Environ()
	if !conSystemd {
		home = t.TempDir()
		entorno = append(entorno, "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"))
	}
	cfg := filepath.Join(home, ".config", "manducar", "impresion.json")
	instalado := filepath.Join(home, ".local", "lib", "manducar", "manducar-impresion")
	unidad := filepath.Join(home, ".config", "systemd", "user", unidadE2E+".service")

	// El que se baja el local: una copia en su carpeta de descargas.
	descargas := t.TempDir()
	agente := filepath.Join(descargas, "manducar-impresion")
	copiarE2E(t, viejo, agente)

	correr := func(args ...string) (string, error) {
		cmd := exec.Command(agente, args...)
		cmd.Env = entorno
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	srv := nuevoServidorLinuxE2E(t)
	red := escucharImpresoraE2E(t, "127.0.0.1:0")
	var porCola *impresoraE2E
	if cola != "" && puertoCola != "" {
		porCola = escucharImpresoraE2E(t, "127.0.0.1:"+puertoCola)
	}

	var salidaDelHijo bytes.Buffer
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		t.Logf("--- impresion.log:\n%s", colaDeArchivoLinuxE2E(filepath.Join(filepath.Dir(cfg), "impresion.log"), 4000))
		if conSystemd {
			for _, args := range [][]string{{"status", unidadE2E, "--no-pager", "--lines=30"}, {"cat", unidadE2E, "--no-pager"}} {
				out, _ := systemctlE2E(args...)
				t.Logf("--- systemctl --user %s:\n%s", strings.Join(args, " "), out)
			}
		} else {
			t.Logf("--- salida de «correr»:\n%s", salidaDelHijo.String())
		}
	})

	// ── 1. Vincular ───────────────────────────────────────────────────────
	if out, err := correr("vincular", "00000000", "--servidor", srv.web.URL); err == nil {
		t.Fatalf("vincular con un código que no existe tenía que fallar:\n%s", out)
	}
	if out, err := correr("vincular", codigoE2E, "--servidor", srv.web.URL, "--nombre", "Caja E2E"); err != nil {
		t.Fatalf("vincular: %v\n%s", err, out)
	}
	if got, want := srv.vinculo["platform"], "linux-"+runtime.GOARCH; got != want {
		t.Errorf("vincular dijo plataforma %v, quiero %s", got, want)
	}
	if got := srv.vinculo["version"]; got != v1 {
		t.Errorf("vincular dijo versión %v, quiero %s", got, v1)
	}
	if st, err := os.Stat(cfg); err != nil {
		t.Fatalf("vincular no dejó la configuración en %s: %v", cfg, err)
	} else if st.Mode().Perm() != 0o600 {
		t.Errorf("la configuración guarda el token y quedó con permisos %o: quiero 600", st.Mode().Perm())
	}
	if porCola != nil && !strings.Contains(fmt.Sprint(srv.vinculo["systemPrinters"]), cola) {
		t.Errorf("vincular no contó la cola de CUPS %q entre las impresoras del sistema: %v", cola, srv.vinculo["systemPrinters"])
	}

	// ── 2. Que quede corriendo ────────────────────────────────────────────
	var hijo *exec.Cmd
	if conSystemd {
		t.Cleanup(func() {
			// Si la prueba se cortó a mitad de camino, que no quede instalado.
			_, _ = correr("desinstalar")
		})
		if out, err := correr("instalar"); err != nil {
			t.Fatalf("instalar: %v\n%s", err, out)
		}
		if _, err := os.Stat(unidad); err != nil {
			t.Fatalf("instalar no dejó la unidad en %s: %v", unidad, err)
		}
		if out, _ := systemctlE2E("is-enabled", unidadE2E); out != "enabled" {
			t.Errorf("la unidad quedó %q: quiero «enabled», que es lo que la arranca al entrar", out)
		}
		esperarE2E(t, 15*time.Second, "que systemd diga que la unidad está activa", func() bool {
			out, _ := systemctlE2E("is-active", unidadE2E)
			return out == "active"
		})
		if got := versionDeE2E(t, instalado); got != v1 {
			t.Errorf("el ejecutable instalado en %s dice %s, quiero %s", instalado, got, v1)
		}
	} else {
		// Sin systemd: en primer plano, desde una copia que se pueda pisar
		// (al actualizarse reemplaza su propio archivo).
		if err := os.MkdirAll(filepath.Dir(instalado), 0o755); err != nil {
			t.Fatal(err)
		}
		copiarE2E(t, viejo, instalado)
		hijo = exec.Command(instalado, "correr")
		hijo.Env = entorno
		hijo.Stdout, hijo.Stderr = &salidaDelHijo, &salidaDelHijo
		if err := hijo.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = hijo.Process.Kill() })
	}
	esperarE2E(t, 30*time.Second, "el primer latido, con el token que guardó vincular", func() bool { return srv.latidosDe(v1) > 0 })
	if n := srv.pedidosSinToken(); n > 0 {
		t.Errorf("%d pedidos llegaron sin el token que entregó vincular", n)
	}

	// ── 3. Imprimir por red ───────────────────────────────────────────────
	imprimir := func(id int, destino map[string]string, en *impresoraE2E, payload []byte) {
		t.Helper()
		srv.armar(id, destino, payload)
		esperarE2E(t, 40*time.Second, fmt.Sprintf("que el trabajo %d llegue a la impresora", id), func() bool { return en.recibio(payload) })
		esperarE2E(t, 30*time.Second, fmt.Sprintf("el resultado del trabajo %d", id), func() bool { _, ok := srv.resultado(id); return ok })
		if res, _ := srv.resultado(id); !res.OK {
			t.Fatalf("el trabajo %d llegó a la impresora pero el agente dijo que no: %+v", id, res)
		}
		if n := en.veces(payload); n != 1 {
			t.Errorf("el trabajo %d salió %d veces: quiero una", id, n)
		}
	}
	porRed := map[string]string{"kind": "network", "address": red.addr}
	imprimir(1, porRed, red, []byte("E2E Linux: ñandú por red\n\x1dV\x00"))

	// ── 4. Imprimir por CUPS ──────────────────────────────────────────────
	if porCola == nil {
		t.Run("cups", func(t *testing.T) {
			exigirOSaltear(t, "falta MANDUCAR_E2E_COLA y MANDUCAR_E2E_COLA_PUERTO: la impresión por CUPS no se prueba")
		})
	} else {
		esperarE2E(t, 20*time.Second, "que un latido cuente la cola de CUPS", func() bool {
			srv.mu.Lock()
			defer srv.mu.Unlock()
			return strings.Contains(strings.Join(srv.impresoras, "\n"), cola)
		})
		// Los bytes tienen que llegar tal cual: `lp -o raw` no los toca.
		imprimir(2, map[string]string{"kind": "system", "systemName": cola}, porCola, []byte("E2E Linux: jam\xf3n por CUPS\n\x1b@\x1dV\x00"))
	}

	// ── 5. Si se cae, systemd lo levanta ──────────────────────────────────
	if conSystemd {
		antes := pidPrincipalE2E()
		if antes == 0 {
			t.Fatal("systemd no sabe el PID del agente")
		}
		latidos := srv.latidosTotales()
		if err := syscall.Kill(antes, syscall.SIGKILL); err != nil {
			t.Fatalf("no se pudo matar al agente (pid %d): %v", antes, err)
		}
		esperarE2E(t, 40*time.Second, "que systemd lo levante con otro PID (Restart=always)", func() bool {
			pid := pidPrincipalE2E()
			return pid != 0 && pid != antes
		})
		esperarE2E(t, 40*time.Second, "que el agente levantado vuelva a latir", func() bool { return srv.latidosTotales() > latidos })
	}

	// ── 6. Actualizarse solo ──────────────────────────────────────────────
	srv.anunciar(v2, binNuevo)
	esperarE2E(t, 90*time.Second, "un latido de la versión nueva ("+v2+")", func() bool { return srv.latidosDe(v2) > 0 })
	if got := versionDeE2E(t, instalado); got != v2 {
		t.Errorf("después de actualizarse, %s dice %s: quiero %s", instalado, got, v2)
	}
	if _, err := os.Stat(instalado + ".old"); err != nil {
		t.Errorf("la versión anterior no quedó al lado como .old (es la vuelta atrás): %v", err)
	}
	if conSystemd {
		esperarE2E(t, 20*time.Second, "que la unidad siga activa después de actualizarse", func() bool {
			out, _ := systemctlE2E("is-active", unidadE2E)
			return out == "active"
		})
	}
	// Y la versión nueva imprime.
	imprimir(3, porRed, red, []byte("E2E Linux: después de actualizarse\n\x1dV\x00"))

	// ── 7. Sacarlo ────────────────────────────────────────────────────────
	if conSystemd {
		if out, err := correr("desinstalar"); err != nil {
			t.Fatalf("desinstalar: %v\n%s", err, out)
		}
		if _, err := os.Stat(unidad); !os.IsNotExist(err) {
			t.Errorf("desinstalar dejó la unidad en %s (err: %v)", unidad, err)
		}
		if out, _ := systemctlE2E("is-active", unidadE2E); out == "active" {
			t.Errorf("después de desinstalar la unidad sigue activa")
		}
	} else {
		if err := hijo.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		fin := make(chan error, 1)
		go func() { fin <- hijo.Wait() }()
		select {
		case <-fin:
		case <-time.After(15 * time.Second):
			t.Fatal("el agente no terminó a los 15 segundos de un SIGTERM")
		}
	}
	// Sacado, no late más: dos vueltas de latido sin ninguno nuevo.
	latidos := srv.latidosTotales()
	time.Sleep(12 * time.Second)
	if n := srv.latidosTotales(); n != latidos {
		t.Errorf("después de sacarlo siguió latiendo (%d latidos más): quedó un agente corriendo", n-latidos)
	}
	if _, err := os.Stat(cfg); err != nil {
		t.Errorf("sacarlo no tiene que borrar la configuración (queda por si se reinstala): %v", err)
	}
}
