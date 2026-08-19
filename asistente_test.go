package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eesnaola/manducar-impresion/internal/config"
)

// El código lo copia una persona de una pantalla: viene con espacios, con
// guiones, o viene mal.
func TestParseCode(t *testing.T) {
	casos := []struct {
		nombre  string
		escrito string
		queda   string
	}{
		{"tal cual", "123456", "123456"},
		{"con espacios alrededor", "  123456  ", "123456"},
		{"partido al medio", "123 456", "123456"},
		{"con guión", "12-34-56", "123456"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			code, err := parseCode(c.escrito)
			if err != nil {
				t.Fatalf("«%s»: %v", c.escrito, err)
			}
			if code != c.queda {
				t.Fatalf("quedó %q y tenía que quedar %q", code, c.queda)
			}
		})
	}
}

func TestParseCodeRechazaLoQueNoEsUnCodigo(t *testing.T) {
	casos := []struct {
		nombre  string
		escrito string
		dice    string
	}{
		{"vacío", "   ", "no escribiste nada"},
		{"corto", "12345", "seis números"},
		{"largo", "1234567", "seis números"},
		{"con letras", "12345a", "seis números"},
		{"el nombre del local", "pizzeria", "seis números"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if _, err := parseCode(c.escrito); err == nil || !contains(err.Error(), c.dice) {
				t.Fatalf("«%s» dio %v y tenía que decir %q", c.escrito, err, c.dice)
			}
		})
	}
}

// La dirección la escribe alguien que no piensa en URLs: la copia del
// navegador, o la escribe de memoria sin el https.
func TestNormalizeServer(t *testing.T) {
	casos := []struct {
		nombre  string
		escrito string
		queda   string
	}{
		{"a secas", "pizzeria.manduc.ar", "https://pizzeria.manduc.ar"},
		{"con espacios", "  pizzeria.manduc.ar ", "https://pizzeria.manduc.ar"},
		{"con https", "https://pizzeria.manduc.ar", "https://pizzeria.manduc.ar"},
		{"con la barra del final", "https://pizzeria.manduc.ar/", "https://pizzeria.manduc.ar"},
		{"copiado con todo el camino", "https://pizzeria.manduc.ar/panel/impresion", "https://pizzeria.manduc.ar"},
		{"en mayúsculas", "Pizzeria.Manduc.Ar", "https://pizzeria.manduc.ar"},
		{"en la máquina de uno", "localhost:8080", "http://localhost:8080"},
		{"el local de desarrollo", "pizzeria.manducar.localhost:8080", "http://pizzeria.manducar.localhost:8080"},
		{"por IP local", "127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"con http escrito a mano, en la máquina de uno", "http://localhost:8080", "http://localhost:8080"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			server, err := normalizeServer(c.escrito)
			if err != nil {
				t.Fatalf("«%s»: %v", c.escrito, err)
			}
			if server != c.queda {
				t.Fatalf("quedó %q y tenía que quedar %q", server, c.queda)
			}
		})
	}
}

func TestNormalizeServerRechazaLoQueNoSirve(t *testing.T) {
	casos := []struct {
		nombre  string
		escrito string
		dice    string
	}{
		{"vacío", "  ", "no escribiste nada"},
		// El asistente no puede mandar el código en claro por la red del
		// local aunque se lo pidan: acá manda checkServer, el mismo que en
		// `vincular`.
		{"http a la calle", "http://pizzeria.manduc.ar", "viajan en claro"},
		{"otro protocolo", "ftp://pizzeria.manduc.ar", "tiene que empezar con https://"},
		{"no es una dirección", "no sé, la de la pizzería", "no parece una dirección"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if _, err := normalizeServer(c.escrito); err == nil || !contains(err.Error(), c.dice) {
				t.Fatalf("«%s» dio %v y tenía que decir %q", c.escrito, err, c.dice)
			}
		})
	}
}

// preparar deja el asistente en un banco de pruebas: la configuración en una
// carpeta de usar y tirar, y nada que toque la computadora de verdad —ni
// CUPS, ni el gestor de servicios, ni una instalación—.
func preparar(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "impresion.json")
	t.Setenv(config.EnvPath, p)

	list, restart, install, installed, terminal := listPrinters, restartAgent, installAgent, agentInstalled, esTerminal
	t.Cleanup(func() {
		listPrinters, restartAgent, installAgent, agentInstalled, esTerminal = list, restart, install, installed, terminal
	})
	listPrinters = func() []string { return nil }
	restartAgent = func(bool) error { return errors.New("el agente no está instalado") }
	installAgent = func() error { return nil }
	agentInstalled = func(bool) bool { return false }
	esTerminal = func() bool { return false }
	return p
}

// servidorDeMentira contesta la vinculación como el de verdad.
func servidorDeMentira(t *testing.T, anotar func(code, name string)) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agente/vincular" {
			http.NotFound(w, r)
			return
		}
		var body struct{ Code, Name string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if anotar != nil {
			anotar(body.Code, body.Name)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":   "tok-123",
			"agentId": 7,
			"store":   map[string]string{"slug": "pizzeria", "name": "Pizzería Demo"},
			"mercure": map[string]string{"url": "https://hub/.well-known/mercure", "jwt": "jwt", "topic": "t"},
		})
	}))
	t.Cleanup(s.Close)
	return s
}

// El camino de todos los días: se abre el programa, se pega el código, se
// escribe la dirección, y la computadora queda vinculada e instalada.
func TestAsistenteVinculaEInstalaSinQueNadieEscribaUnComando(t *testing.T) {
	p := preparar(t)
	var pedido struct{ code, name string }
	srv := servidorDeMentira(t, func(code, name string) { pedido.code, pedido.name = code, name })
	instalado := false
	installAgent = func() error { instalado = true; return nil }

	var out strings.Builder
	if code := asistir(strings.NewReader("123456\n"+srv.URL+"\n"), &out); code != 0 {
		t.Fatalf("salió con %d:\n%s", code, out.String())
	}
	texto := out.String()
	for _, dice := range []string{
		"Manducar — impresión",
		"Pegá el código de seis dígitos que muestra el panel:",
		"Dirección de tu local en Manducar",
		"Listo: esta computadora quedó vinculada a «Pizzería Demo».",
		"arranca solo cada vez que entrás",
		"impresion.log",
		"Podés cerrar esta ventana.",
	} {
		if !contains(texto, dice) {
			t.Errorf("no dijo %q:\n%s", dice, texto)
		}
	}
	if pedido.code != "123456" {
		t.Errorf("al servidor le mandó el código %q", pedido.code)
	}
	if pedido.name == "" {
		t.Error("la computadora tiene que ir con nombre")
	}
	if !instalado {
		t.Error("no lo dejó instalado")
	}
	cfg, err := config.LoadFrom(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "tok-123" || cfg.AgentID != 7 || cfg.Store != "Pizzería Demo" {
		t.Fatalf("quedó guardado %+v", cfg)
	}
	if cfg.Server != srv.URL {
		t.Fatalf("servidor %q y tenía que ser %q", cfg.Server, srv.URL)
	}
}

// El código mal escrito no puede ser el final del asunto: se lo vuelve a
// pedir.
func TestAsistenteVuelveAPedirElCodigoMalEscrito(t *testing.T) {
	preparar(t)
	var pedido string
	srv := servidorDeMentira(t, func(code, _ string) { pedido = code })

	var out strings.Builder
	if code := asistir(strings.NewReader("hola\n12345\n123456\n"+srv.URL+"\n"), &out); code != 0 {
		t.Fatalf("salió con %d:\n%s", code, out.String())
	}
	if pedido != "123456" {
		t.Fatalf("al servidor le llegó %q", pedido)
	}
	if !contains(out.String(), "no es un código de seis números") {
		t.Fatalf("no se quejó del código:\n%s", out.String())
	}
}

// Tres veces mal y se deja de preguntar, diciendo dónde estaba el código.
func TestAsistenteSeRindeDespuesDeTresIntentos(t *testing.T) {
	preparar(t)
	llamaron := false
	servidorDeMentira(t, func(string, string) { llamaron = true })

	var out strings.Builder
	if code := asistir(strings.NewReader("uno\ndos\ntres\n"), &out); code != 1 {
		t.Fatalf("salió con %d y tenía que salir con 1:\n%s", code, out.String())
	}
	if llamaron {
		t.Fatal("no tenía que hablar con el servidor")
	}
	if !contains(out.String(), "Vincular una computadora") {
		t.Fatalf("no dijo dónde está el código:\n%s", out.String())
	}
}

// Abierto de nuevo, con la computadora ya vinculada, lo primero que dice es a
// qué local.
func TestAsistenteYaVinculadaMuestraElLocalYElEstado(t *testing.T) {
	p := preparar(t)
	if err := config.SaveTo(p, config.Config{
		Server: "https://pizzeria.manduc.ar", Token: "tok", AgentID: 7,
		Store: "Pizzería Demo", Mode: config.ModeUser,
	}); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if code := asistir(strings.NewReader("3\n"), &out); code != 0 {
		t.Fatalf("salió con %d:\n%s", code, out.String())
	}
	texto := out.String()
	for _, dice := range []string{
		"Esta computadora ya está vinculada a «Pizzería Demo» (agente 7).",
		"[1] Instalar o reinstalar",
		"[2] Volver a vincular",
		"https://pizzeria.manduc.ar",
		p,
		filepath.Join(filepath.Dir(p), "impresion.log"),
		"no, todavía no lo instalaste",
	} {
		if !contains(texto, dice) {
			t.Errorf("no dijo %q:\n%s", dice, texto)
		}
	}
}

// La opción 1 instala; si ya había un agente instalado, primero lo saca.
func TestAsistenteInstalaDesdeElMenu(t *testing.T) {
	p := preparar(t)
	if err := config.SaveTo(p, config.Config{
		Server: "https://pizzeria.manduc.ar", Token: "tok", AgentID: 7,
		Store: "Pizzería Demo", Mode: config.ModeUser,
	}); err != nil {
		t.Fatal(err)
	}
	instalado := false
	installAgent = func() error { instalado = true; return nil }

	var out strings.Builder
	if code := asistir(strings.NewReader("1\n"), &out); code != 0 {
		t.Fatalf("salió con %d:\n%s", code, out.String())
	}
	if !instalado {
		t.Fatalf("no instaló nada:\n%s", out.String())
	}
	if !contains(out.String(), "arranca solo cada vez que entrás") {
		t.Fatalf("no contó cómo quedó:\n%s", out.String())
	}
}

// Si la instalación falla, se lo cuenta y ofrece probar de nuevo; el que dice
// que no se va con código de error.
func TestAsistenteOfreceReintentarLaInstalacion(t *testing.T) {
	preparar(t)
	veces := 0
	installAgent = func() error { veces++; return errNoSeInstalo }
	srv := servidorDeMentira(t, nil)

	var out strings.Builder
	// código, dirección, «sí» al primer reintento, «no» al segundo.
	code := asistir(strings.NewReader("123456\n"+srv.URL+"\ns\nn\n"), &out)
	if code != 1 {
		t.Fatalf("salió con %d y tenía que salir con 1:\n%s", code, out.String())
	}
	if veces != 2 {
		t.Fatalf("probó %d veces y tenía que probar 2:\n%s", veces, out.String())
	}
	if !contains(out.String(), "¿Probamos de nuevo?") {
		t.Fatalf("no ofreció reintentar:\n%s", out.String())
	}
}

// En una consola de verdad —la que abre el doble clic en Windows— la ventana
// se cierra sola al terminar: hay que frenarla o no se lee nada.
func TestAsistenteEsperaElEnterFinalSoloEnUnaTerminal(t *testing.T) {
	preparar(t)
	esTerminal = func() bool { return true }
	var out strings.Builder
	asistir(strings.NewReader(""), &out)
	if !contains(out.String(), "apretá Enter para salir") {
		t.Fatalf("no esperó a que lo lean:\n%s", out.String())
	}

	esTerminal = func() bool { return false }
	var otro strings.Builder
	asistir(strings.NewReader(""), &otro)
	if contains(otro.String(), "apretá Enter para salir") {
		t.Fatalf("con la entrada redirigida no hay a quién esperar:\n%s", otro.String())
	}
}

// Un error de red no se le puede tirar por la cabeza al que está en el
// mostrador: primero la frase, y el texto de Go abajo, para el que después
// tenga que mirarlo.
func TestAsistenteCuentaElErrorDeRedEnCastellano(t *testing.T) {
	preparar(t)
	srv := servidorDeMentira(t, nil)
	dir := srv.URL
	srv.Close() // el puerto queda cerrado: es lo que se ve sin internet

	var out strings.Builder
	if code := asistir(strings.NewReader("123456\n"+dir+"\nn\n"), &out); code != 1 {
		t.Fatalf("salió con %d y tenía que salir con 1:\n%s", code, out.String())
	}
	texto := out.String()
	if !contains(texto, "No pude llegar a tu local") {
		t.Errorf("no lo dijo en castellano:\n%s", texto)
	}
	if !contains(texto, "(detalle:") {
		t.Errorf("no dejó el detalle técnico:\n%s", texto)
	}
}
