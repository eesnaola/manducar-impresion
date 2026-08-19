package svc

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/eesnaola/manducar-impresion/internal/config"
)

// openLog es lo único de este paquete que se puede probar sin instalar un
// servicio de verdad: que el archivo del log se cree al lado de la config y
// que una segunda apertura agregue en vez de pisar lo que ya había.
func TestOpenLogCreatesAndAppends(t *testing.T) {
	dir := t.TempDir()

	f, err := openLog(dir)
	if err != nil {
		t.Fatalf("abrir: %v", err)
	}
	if _, err := f.WriteString("primera línea\n"); err != nil {
		t.Fatalf("escribir: %v", err)
	}
	f.Close()

	f2, err := openLog(dir)
	if err != nil {
		t.Fatalf("reabrir: %v", err)
	}
	if _, err := f2.WriteString("segunda línea\n"); err != nil {
		t.Fatalf("escribir: %v", err)
	}
	f2.Close()

	got, err := os.ReadFile(filepath.Join(dir, "impresion.log"))
	if err != nil {
		t.Fatalf("leer: %v", err)
	}
	want := "primera línea\nsegunda línea\n"
	if string(got) != want {
		t.Fatalf("quedó %q, quería %q", got, want)
	}
}

// Pasado el tamaño, el log se rota: lo viejo queda en `impresion.log.1` y el
// nuevo arranca vacío. Sin esto, la computadora del local se queda sin disco.
func TestOpenLogRotatesWhenItGrows(t *testing.T) {
	dir := t.TempDir()
	viejo := logMaxBytes
	logMaxBytes = 16
	defer func() { logMaxBytes = viejo }()

	f, err := openLog(dir)
	if err != nil {
		t.Fatalf("abrir: %v", err)
	}
	if _, err := f.WriteString("una línea larga que pasa el tope\n"); err != nil {
		t.Fatalf("escribir: %v", err)
	}
	f.Close()

	f2, err := openLog(dir)
	if err != nil {
		t.Fatalf("reabrir: %v", err)
	}
	if _, err := f2.WriteString("después de rotar\n"); err != nil {
		t.Fatalf("escribir: %v", err)
	}
	f2.Close()

	nuevo, err := os.ReadFile(filepath.Join(dir, "impresion.log"))
	if err != nil {
		t.Fatalf("leer el nuevo: %v", err)
	}
	if string(nuevo) != "después de rotar\n" {
		t.Fatalf("el log nuevo tenía que arrancar vacío, quedó %q", nuevo)
	}
	rotado, err := os.ReadFile(filepath.Join(dir, "impresion.log.1"))
	if err != nil {
		t.Fatalf("el log viejo tenía que quedar en impresion.log.1: %v", err)
	}
	if string(rotado) != "una línea larga que pasa el tope\n" {
		t.Fatalf("el rotado quedó con %q", rotado)
	}
}

// Cuando el gestor manda parar, Stop espera a que el bucle termine: si no, el
// proceso se muere con la comanda a medio imprimir y el resultado sin
// confirmar. Y no espera para siempre: Windows corta a los 20 segundos.
func TestStopWaitsForTheLoopToDrain(t *testing.T) {
	viejoRun, viejoStop := run, stopWait
	defer func() { run, stopWait = viejoRun, viejoStop }()

	drenó := make(chan struct{})
	run = func(ctx context.Context, version string) error {
		<-ctx.Done()
		time.Sleep(100 * time.Millisecond) // lo que tarda en cerrar
		close(drenó)
		return nil
	}

	p := &program{version: "test"}
	if err := p.Start(nil); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-drenó:
	default:
		t.Fatal("Stop volvió antes de que el bucle terminara de cerrar")
	}
}

func TestStopDoesNotHangOnALoopThatNeverEnds(t *testing.T) {
	viejoRun, viejoStop := run, stopWait
	colgado := make(chan struct{})
	p := &program{version: "test"}
	// El orden importa: primero se suelta el bucle y se lo espera terminar, y
	// recién ahí se devuelven las variables del paquete. Al revés, la
	// goroutine seguiría viva leyendo `run` mientras se lo escribe.
	defer func() {
		close(colgado)
		<-p.done
		run, stopWait = viejoRun, viejoStop
	}()
	run = func(ctx context.Context, version string) error {
		<-colgado
		return nil
	}
	stopWait = 100 * time.Millisecond

	if err := p.Start(nil); err != nil {
		t.Fatal(err)
	}
	arranque := time.Now()
	if err := p.Stop(nil); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(arranque); d > time.Second {
		t.Fatalf("Stop tardó %v: tenía que rendirse al plazo", d)
	}
}

// El ejecutable se copia a un lugar fijo y el servicio se anota con esa ruta:
// si quedara anotada la carpeta de Descargas, el día que alguien la limpie el
// agente no arranca más.
func TestCopyToPutsTheBinaryInItsPlace(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "bajado", "manducar-impresion")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("el binario"), 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "fijo", "manducar-impresion")

	got, err := copyTo(src, dest)
	if err != nil {
		t.Fatal(err)
	}
	if got != dest {
		t.Fatalf("devolvió %q y tenía que devolver %q", got, dest)
	}
	data, err := os.ReadFile(dest)
	if err != nil || string(data) != "el binario" {
		t.Fatalf("no quedó copiado: %q, %v", data, err)
	}
	info, _ := os.Stat(dest)
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("tiene que quedar ejecutable, quedó %v", info.Mode().Perm())
	}

	// Copiar sobre sí mismo no lo rompe: es lo que pasa al reinstalar.
	if _, err := copyTo(dest, dest); err != nil {
		t.Fatalf("copiar sobre sí mismo: %v", err)
	}
	data, _ = os.ReadFile(dest)
	if string(data) != "el binario" {
		t.Fatalf("se pisó a sí mismo: %q", data)
	}

	// Y una versión nueva reemplaza a la que había.
	if err := os.WriteFile(src, []byte("el binario nuevo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := copyTo(src, dest); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(dest)
	if string(data) != "el binario nuevo" {
		t.Fatalf("no se actualizó: %q", data)
	}
}

// La instalación sin permisos le pide al gestor un servicio del usuario —el
// LaunchAgent de Mac, la unidad `--user` de systemd— y le manda el log al
// archivo de siempre. La del sistema, ninguna de las dos cosas.
func TestSpecAsksForAUserServiceWhenItRunsWithoutPermissions(t *testing.T) {
	c := spec("/donde/sea/manducar-impresion", true)
	if v, _ := c.Option["UserService"].(bool); !v {
		t.Fatalf("faltó UserService: %+v", c.Option)
	}
	if !tieneArgumento(c.Arguments, "--log-archivo") {
		t.Fatalf("el servicio de usuario tiene que mandar el log al archivo: %v", c.Arguments)
	}
	if v, _ := c.Option["KeepAlive"].(bool); !v {
		t.Fatal("KeepAlive es lo que lo levanta si se cae")
	}
	if v, _ := c.Option["RunAtLoad"].(bool); !v {
		t.Fatal("RunAtLoad es lo que lo arranca al entrar")
	}
	// La ruta de la configuración va clavada: instalado, el agente no la
	// vuelve a adivinar con el entorno que le arme el gestor.
	if c.EnvVars[config.EnvPath] == "" {
		t.Fatalf("faltó clavarle la ruta de la configuración: %+v", c.EnvVars)
	}

	c = spec("", false)
	if _, hay := c.Option["UserService"]; hay {
		t.Fatal("el servicio del sistema no es de usuario")
	}
	if tieneArgumento(c.Arguments, "--log-archivo") {
		t.Fatal("como servicio del sistema el log al archivo lo pone RunAsService, no la línea de comandos")
	}
}

// La plantilla de kardianos/service pide `WantedBy=multi-user.target`, que en
// la sesión del usuario no existe: la unidad quedaría habilitada y no
// arrancaría nunca. Este es el renglón que hace que arranque.
func TestSystemdUserScriptIsWantedByTheUsersTarget(t *testing.T) {
	if !strings.Contains(systemdUserScript, "WantedBy=default.target") {
		t.Fatal("la unidad de usuario tiene que colgar de default.target")
	}
	if strings.Contains(systemdUserScript, "multi-user.target") {
		t.Fatal("multi-user.target no existe en la sesión del usuario")
	}
	if !strings.Contains(systemdUserScript, "Restart={{Restart}}") {
		t.Fatal("sin Restart no se levanta solo cuando se cae")
	}
}

func tieneArgumento(args []string, a string) bool {
	for _, x := range args {
		if x == a {
			return true
		}
	}
	return false
}

// Las dos guardas, en los dos sentidos: dos agentes con el mismo token en la
// misma computadora sacan cada comanda dos veces por la misma impresora.
func TestTheGuardsSeeTheOtherModeBothWays(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("en Windows las guardas preguntan por el SCM y por la clave Run, no por archivos")
	}
	viejoExists, viejoUser, viejoSystem := exists, userUnitPaths, systemUnitPaths
	t.Cleanup(func() { exists, userUnitPaths, systemUnitPaths = viejoExists, viejoUser, viejoSystem })

	const delUsuario, delSistema = "/casa/agente.plist", "/sistema/demonio.plist"
	userUnitPaths = func() []string { return []string{delUsuario} }
	systemUnitPaths = func() []string { return []string{delSistema} }

	exists = func(string) bool { return false }
	if userInstalled() || systemInstalled() {
		t.Fatal("sin nada instalado, ninguna de las dos tiene que ver nada")
	}

	exists = func(p string) bool { return p == delUsuario }
	if !userInstalled() {
		t.Fatal("no vio el agente del usuario: `instalar --sistema` dejaría los dos corriendo")
	}
	if systemInstalled() {
		t.Fatal("el agente del usuario no es un servicio del sistema")
	}

	exists = func(p string) bool { return p == delSistema }
	if !systemInstalled() {
		t.Fatal("no vio el servicio del sistema")
	}
	if userInstalled() {
		t.Fatal("el servicio del sistema no es un agente de usuario")
	}
}

// `instalar --sistema` va con sudo, y ahí el home es el de root: si buscáramos
// el agente del usuario ahí, la guarda quedaría ciega justo cuando importa.
func TestInvokingHomeIsTheHomeOfWhoeverRanTheCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sudo no existe en Windows")
	}
	yo, err := user.Current()
	if err != nil {
		t.Skip("sin usuario que mirar:", err)
	}
	t.Setenv("SUDO_USER", "")
	t.Setenv("SUDO_UID", "")
	propio, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	if got := invokingHome(); got != propio {
		t.Fatalf("sin sudo es el home de siempre: %q, quería %q", got, propio)
	}

	// Con sudo, el home sale del usuario que llamó, no del efectivo.
	t.Setenv("HOME", "/root")
	t.Setenv("SUDO_USER", yo.Username)
	if got := invokingHome(); got != yo.HomeDir {
		t.Fatalf("por SUDO_USER tenía que dar %q, dio %q", yo.HomeDir, got)
	}
	t.Setenv("SUDO_USER", "")
	t.Setenv("SUDO_UID", yo.Uid)
	if got := invokingHome(); got != yo.HomeDir {
		t.Fatalf("por SUDO_UID tenía que dar %q, dio %q", yo.HomeDir, got)
	}
}

// Vincular sin sudo y después instalar el servicio del sistema es el camino
// esperable: la configuración se copia sola. Y la que queda atrás se aparta,
// porque entre las dos gana siempre la del usuario: si se quedara ahí, el
// agente del sistema escribiría en una y `desinstalar` leería la otra.
func TestPrepareConfigAtCopiesTheConfigAndMovesTheOldOneOut(t *testing.T) {
	dir := t.TempDir()
	usuario := filepath.Join(dir, "usuario", "impresion.json")
	sistema := filepath.Join(dir, "sistema", "impresion.json")
	if err := config.SaveTo(usuario, config.Config{Server: "https://x", Token: "t", AgentID: 3, Mode: config.ModeUser}); err != nil {
		t.Fatal(err)
	}

	if err := prepareConfigAt(sistema, usuario, config.ModeSystem); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadFrom(sistema)
	if err != nil {
		t.Fatalf("tenía que quedar copiada en la del sistema: %v", err)
	}
	if cfg.Token != "t" || cfg.AgentID != 3 {
		t.Fatalf("se copió mal: %+v", cfg)
	}
	if cfg.Mode != config.ModeSystem {
		t.Fatalf("el modo tenía que quedar en «system», quedó %q", cfg.Mode)
	}
	if _, err := os.Stat(usuario); !os.IsNotExist(err) {
		t.Fatal("la del usuario tenía que quedar apartada, sigue ahí")
	}
	if _, err := os.Stat(usuario + ".migrado"); err != nil {
		t.Fatalf("la vieja tenía que quedar como .migrado: %v", err)
	}
}

// Si la configuración ya está donde va y con el modo puesto, no se la toca.
func TestPrepareConfigAtLeavesAGoodConfigAlone(t *testing.T) {
	dir := t.TempDir()
	quiere := filepath.Join(dir, "sistema", "impresion.json")
	otra := filepath.Join(dir, "usuario", "impresion.json")
	if err := config.SaveTo(quiere, config.Config{Server: "https://x", Token: "t", Mode: config.ModeSystem}); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveTo(otra, config.Config{Server: "https://x", Token: "otro", Mode: config.ModeUser}); err != nil {
		t.Fatal(err)
	}
	if err := prepareConfigAt(quiere, otra, config.ModeSystem); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.LoadFrom(quiere)
	if cfg.Token != "t" {
		t.Fatalf("se pisó con la otra: %+v", cfg)
	}
	if _, err := os.Stat(otra); err != nil {
		t.Fatal("no había nada que migrar: la otra no se toca")
	}
}

// El modo se corrige en el archivo que ya está: es lo que hace que
// `desinstalar` sepa después a qué gestor hablarle.
func TestPrepareConfigAtFixesTheModeInPlace(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "impresion.json")
	if err := config.SaveTo(p, config.Config{Server: "https://x", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := prepareConfigAt(p, "", config.ModeUser); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.LoadFrom(p)
	if cfg.Mode != config.ModeUser {
		t.Fatalf("quedó %q", cfg.Mode)
	}
}

// Y sin ninguna configuración, lo que corresponde es mandar a vincular.
func TestPrepareConfigAtWithoutAnyConfigSaysToPairFirst(t *testing.T) {
	dir := t.TempDir()
	err := prepareConfigAt(filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json"), config.ModeUser)
	if err == nil {
		t.Fatal("tenía que quejarse")
	}
	if !strings.Contains(err.Error(), "vinculá") {
		t.Fatalf("dijo %q", err)
	}
}
