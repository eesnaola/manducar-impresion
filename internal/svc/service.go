// Package svc instala, desinstala y corre el agente para que arranque solo.
//
// Hay dos modos. El de usuario es el default y no pide permisos: LaunchAgent
// en Mac, unidad de systemd `--user` en Linux y, en Windows —que no tiene
// servicios de usuario—, la clave Run del usuario con un lanzador .vbs. El
// del sistema es el de antes, con `--sistema`: launchd, systemd y el Service
// Control Manager, todos con sudo o administrador.
package svc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"time"

	"github.com/kardianos/service"

	"github.com/eesnaola/manducar-impresion/internal/config"
	"github.com/eesnaola/manducar-impresion/internal/runner"
)

// Variables y no constantes para que los tests no tarden minutos.
var (
	// stopWait: lo que se le da al bucle para terminar de imprimir y de
	// confirmar cuando el gestor de servicios manda parar. Windows corta a
	// los 20 o 30 segundos, así que no puede ser más.
	stopWait = 10 * time.Second
	// failWait: si el bucle se cae con error, se espera esto antes de salir
	// para que el gestor no lo reinicie mil veces por segundo.
	failWait = 5 * time.Second
	// logMaxBytes: pasado este tamaño el log se rota. La computadora del
	// local no está para que le llenemos el disco.
	logMaxBytes int64 = 10 << 20
	// run es runner.Run; los tests le ponen un bucle de mentira.
	run = runner.Run
	// Out es adónde van los avisos de este paquete —«quedó instalado en…»,
	// «la configuración se copió a…»—. Es la salida de siempre; el asistente
	// la apaga mientras instala, porque ahí el que cuenta lo que pasó es él y
	// no queremos decir dos veces lo mismo. Los errores no pasan por acá:
	// siguen yendo a la salida de error, que se ve igual.
	Out io.Writer = os.Stdout
)

type program struct {
	version string
	cancel  context.CancelFunc
	// done se cierra cuando el bucle terminó de verdad: Stop lo espera, así
	// lo que se estaba imprimiendo sale y los resultados se confirman antes
	// de que el gestor mate el proceso.
	done chan struct{}
}

func (p *program) Start(s service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan struct{})
	go func() {
		err := run(ctx, p.version)
		close(p.done)
		if err != nil {
			log.Println(err)
			// Salir con error deja que el gestor lo reinicie; el rato de
			// espera evita el reinicio en loop y el log dice por qué fue.
			time.Sleep(failWait)
			os.Exit(1)
		}
	}()
	return nil
}

func (p *program) Stop(s service.Service) error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.done == nil {
		return nil
	}
	select {
	case <-p.done:
	case <-time.After(stopWait):
		log.Println("no terminó de cerrar a tiempo: lo que quedó sin confirmar se reporta al volver")
	}
	return nil
}

// spec: que quede corriendo y que arranque solo, en los tres sistemas.
//   - systemd (Linux): `Restart=always` lo reinicia si se cae; `Install()`
//     hace `systemctl enable`, así que ya arranca solo sin ninguna opción
//     extra.
//   - launchd (Mac): `KeepAlive` lo reinicia si se cae; `RunAtLoad` es lo que
//     lo arranca (por default es false en kardianos/service).
//   - Windows: `OnFailure` configura la recovery action del SCM; el tipo de
//     arranque (`StartType`) ya es `automatic` por default en
//     kardianos/service, sin que haga falta pedirlo.
//
// executable es con qué ruta se anota el servicio; vacío significa «la del
// proceso de ahora», que es lo que quieren desinstalar y correr. user pide la
// instalación sin permisos: LaunchAgent del usuario, unidad `--user`.
func spec(executable string, user bool) *service.Config {
	c := &service.Config{
		Name:        "manducar-impresion",
		DisplayName: "Manducar — impresión",
		Description: "Imprime las comandas y tickets de Manducar en las impresoras de este local.",
		Arguments:   []string{"correr"},
		Executable:  executable,
		Option: service.KeyValue{
			"Restart":                "always",  // systemd
			"KeepAlive":              true,      // launchd: lo reinicia si se cae
			"RunAtLoad":              true,      // launchd: lo arranca al entrar
			"OnFailure":              "restart", // Windows: recovery actions
			"OnFailureDelayDuration": "5s",
		},
	}
	// La ruta de la configuración se le clava al servicio: instalado, el
	// agente no la vuelve a adivinar. Si no, alcanza con que el gestor le arme
	// otro entorno —otro HOME, otro XDG_CONFIG_HOME, o que quede dando vueltas
	// la configuración del otro modo— para que lea el archivo equivocado.
	// kardianos/service lo sabe poner en los tres: EnvironmentVariables del
	// plist, Environment= de systemd y el valor Environment del servicio en el
	// registro de Windows.
	if p, err := config.PathFor(!user); err == nil {
		c.EnvVars = map[string]string{config.EnvPath: p}
	}
	if !user {
		return c
	}
	// launchd lo pone en ~/Library/LaunchAgents y systemd en
	// ~/.config/systemd/user: ninguno de los dos necesita permisos.
	c.Option["UserService"] = true
	// En Mac, `service.Interactive()` no es confiable para un agente de
	// usuario —lo avisa el README de kardianos/service—, así que el log al
	// archivo se pide de prepo y no se depende de adivinar.
	c.Arguments = append(c.Arguments, "--log-archivo")
	// Y lo que igual escupa por consola queda al lado de la configuración, no
	// en /var/log —donde no puede escribir— ni tirado en el home.
	if dir, err := config.Dir(); err == nil {
		c.Option["LogDirectory"] = dir
	}
	if runtime.GOOS == "linux" {
		c.Option["SystemdScript"] = systemdUserScript
	}
	return c
}

// systemdUserScript es la plantilla de kardianos/service con dos cambios, los
// dos por ser unidad de usuario: `WantedBy=default.target` —en la sesión del
// usuario no hay `multi-user.target`, así que con el de la plantilla la
// unidad queda habilitada pero no arranca nunca— y un RestartSec corto, para
// que caerse no signifique dos minutos sin imprimir.
const systemdUserScript = `[Unit]
Description={{Description}}
ConditionFileIsExecutable={{Path | cmdEscape}}
{{range Dependencies}}{{.}}
{{end}}
[Service]
StartLimitInterval=5
StartLimitBurst=10
ExecStart={{Path | cmdEscape}}{{range Arguments}} {{. | cmd}}{{end}}
{{if ChRoot}}RootDirectory={{ChRoot | cmd}}
{{end}}{{if WorkingDirectory}}WorkingDirectory={{WorkingDirectory | cmdEscape}}
{{end}}{{if UserName}}User={{UserName}}
{{end}}{{if ReloadSignal}}ExecReload=/bin/kill -{{ReloadSignal}} "$MAINPID"
{{end}}{{if PIDFile}}PIDFile={{PIDFile | cmd}}
{{end}}{{if OutputFileSupport}}StandardOutput=file:{{LogDirectory}}/{{Name}}.out
StandardError=file:{{LogDirectory}}/{{Name}}.err
{{end}}{{if LimitNOFILE}}LimitNOFILE={{LimitNOFILE}}
{{end}}{{if Restart}}Restart={{Restart}}
{{end}}{{if SuccessExitStatus}}SuccessExitStatus={{SuccessExitStatus}}
{{end}}RestartSec=5

{{range EnvVars}}{{.}}
{{end}}[Install]
WantedBy=default.target
`

// installPath es adónde va a vivir el ejecutable: un lugar fijo, no la
// carpeta de Descargas. El agente se anota con esta ruta, así que el día que
// alguien limpie Descargas sigue arrancando.
//
// En modo sistema, en Windows va a Archivos de programa y no a ProgramData:
// el servicio corre como SYSTEM, y en ProgramData escribe cualquier usuario
// —ahí cualquiera podría reemplazar el ejecutable y hacerse SYSTEM—.
func installPath(user bool) string {
	if p := os.Getenv("MANDUCAR_IMPRESION_BIN"); p != "" {
		return p
	}
	if user {
		return userInstallPath()
	}
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("ProgramFiles")
		if base == "" {
			base = `C:\Program Files`
		}
		return filepath.Join(base, "Manducar", "manducar-impresion.exe")
	case "darwin":
		return "/Library/Application Support/Manducar/manducar-impresion"
	default:
		return "/usr/local/lib/manducar/manducar-impresion"
	}
}

// userInstallPath es lo mismo pero adentro del usuario. Tiene que ser una
// carpeta suya y escribible: el agente se actualiza solo reemplazando su
// propio archivo, y si no puede escribirlo se queda en la versión vieja.
func userInstallPath() string {
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("LocalAppData")
		if base == "" {
			if home, err := os.UserHomeDir(); err == nil {
				base = filepath.Join(home, "AppData", "Local")
			}
		}
		return filepath.Join(base, "Manducar", "manducar-impresion.exe")
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", "Manducar", "manducar-impresion")
	default:
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".local", "lib", "manducar", "manducar-impresion")
	}
}

// ensureInstalled deja el ejecutable en dest si todavía no está ahí, y
// devuelve esa ruta.
func ensureInstalled(dest string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	return copyTo(self, dest)
}

// copyTo copia src a dest salvo que ya sean el mismo archivo. Devuelve dest.
func copyTo(src, dest string) (string, error) {
	if a, err := os.Stat(src); err == nil {
		if b, err := os.Stat(dest); err == nil && os.SameFile(a, b) {
			return dest, nil // ya está en su lugar
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	// Se escribe al lado y se renombra: si algo se corta en el medio, no
	// queda medio ejecutable en el lugar del bueno.
	tmp := dest + ".nuevo"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	// Windows no deja pisar un .exe que está en uso: si no se puede borrar,
	// se lo corre al lado. El sufijo NO es `.old`: ése es el de selfupdate,
	// el que se renombra a mano cuando una actualización sale mal, y pisarlo
	// sería quedarse sin vuelta atrás.
	if _, err := os.Stat(dest); err == nil {
		if err := os.Remove(dest); err != nil {
			if err := os.Rename(dest, dest+".reemplazado"); err != nil {
				os.Remove(tmp)
				return "", err
			}
		}
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return dest, nil
}

// Install deja el agente arrancando solo. Por default, a nivel usuario y sin
// pedir permisos; con system, como servicio del sistema, que sí necesita sudo
// o administrador. Exige que la computadora esté vinculada: instalar sin
// configuración arranca y muere en loop sin decir por qué.
func Install(version string, system bool) int {
	// Los permisos primero: si falta sudo, mejor plantarse antes de copiar
	// nada que a mitad de camino.
	if err := checkPrivileges(system); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// Y el otro modo después: la configuración del sistema no la puede ni
	// leer un usuario común, así que preguntar por ella primero daría un
	// «vinculá la computadora» que no es lo que pasa.
	if !system && systemInstalled() {
		fmt.Fprintln(os.Stderr, "esta computadora ya tiene el agente instalado como servicio del sistema: sacalo primero («sudo manducar-impresion desinstalar --sistema», o como administrador en Windows) o volvé a instalarlo con --sistema. Dos agentes juntos imprimen todo dos veces.")
		return 1
	}
	if system && userInstalled() {
		fmt.Fprintln(os.Stderr, "esta computadora ya tiene un agente instalado en modo usuario: desinstalalo primero con «manducar-impresion desinstalar» —sin sudo, y con el usuario que lo instaló— y recién ahí instalá el del sistema. Dos agentes juntos imprimen todo dos veces.")
		return 1
	}
	if err := prepareConfig(system); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	dest := installPath(!system)

	// Windows sin administrador: no hay servicio de usuario que instalar, lo
	// que arranca solo es la clave Run.
	if !system && runKeyAutostart {
		if ya, err := autostartInstalled(); err == nil && ya {
			fmt.Fprintln(os.Stderr, "el agente ya está instalado; para actualizarlo a mano: manducar-impresion desinstalar && manducar-impresion instalar")
			return 1
		}
		exe, err := ensureInstalled(dest)
		if err != nil {
			fmt.Fprintln(os.Stderr, "no se pudo copiar el programa a su lugar definitivo:", err)
			return 1
		}
		// La ruta de la configuración va clavada en el lanzador, por lo mismo
		// que va en el servicio de Mac y Linux: instalado, el agente no la
		// adivina.
		cfgPath, err := config.PathFor(false)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := autostartInstall(exe, cfgPath); err != nil {
			fmt.Fprintln(os.Stderr, "no se pudo dejarlo arrancando solo:", err)
			// Lo que haya quedado a medio anotar se saca: si no, el próximo
			// `instalar` se niega diciendo que ya está.
			_ = autostartUninstall()
			return 1
		}
		announce(exe, false)
		return 0
	}

	// Se pregunta ANTES de copiar: si ya está instalado, `Install` va a
	// fallar igual, y para entonces ya le habríamos pisado el ejecutable al
	// que está corriendo.
	s, err := service.New(&program{version: version}, spec(dest, !system))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if st, err := s.Status(); err == nil && st != service.StatusUnknown {
		fmt.Fprintln(os.Stderr, "el agente ya está instalado; para actualizarlo a mano: manducar-impresion desinstalar && manducar-impresion instalar")
		return 1
	}
	exe, err := ensureInstalled(dest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "no se pudo copiar el programa a su lugar definitivo:", err, sudoHint(system))
		return 1
	}
	if err := s.Install(); err != nil {
		fmt.Fprintln(os.Stderr, "no se pudo instalar el agente:", err, sudoHint(system))
		return 1
	}
	if err := s.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "instalado, pero no arrancó:", err)
		// Y se lo saca: instalado y muerto es el peor de los dos mundos —el
		// próximo `instalar` se negaría diciendo que ya está, y no hay por
		// dónde salir sin desinstalar a mano—.
		if err := s.Uninstall(); err != nil {
			fmt.Fprintln(os.Stderr, "tampoco se pudo sacar lo que quedó a medio instalar:", err)
		}
		return 1
	}
	announce(exe, system)
	return 0
}

// announce cuenta dónde quedó todo: la ruta del programa y la del log son las
// dos cosas que después le vamos a pedir mirar.
func announce(exe string, system bool) {
	if system {
		fmt.Fprintf(Out, "Listo: el agente quedó instalado en %s, corriendo, y arranca solo con la computadora.\n", exe)
	} else {
		fmt.Fprintf(Out, "Listo: el agente quedó instalado en %s, corriendo, y arranca solo cada vez que entrás a esta computadora.\n", exe)
	}
	if dir, err := config.Dir(); err == nil {
		fmt.Fprintf(Out, "Lo que va haciendo queda en %s.\n", filepath.Join(dir, "impresion.log"))
	}
	fmt.Fprintln(Out, "Ya podés borrar el archivo que bajaste: el que manda es ése.")
}

// sudoHint es el empujón que necesita el que corrió `instalar --sistema` sin
// permisos. Sin `--sistema` no hace falta ninguno, así que no se le sugiere.
func sudoHint(system bool) string {
	if !system {
		return ""
	}
	if runtime.GOOS == "windows" {
		return "(--sistema instala el servicio para toda la computadora: abrí la terminal como administrador, o instalá sin --sistema, que no pide permisos)"
	}
	return "(--sistema instala el servicio para toda la computadora: corré el comando con sudo, o instalá sin --sistema, que no pide permisos)"
}

// prepareConfig se asegura de que la configuración esté donde la va a buscar
// el agente instalado, y le anota el modo. Vincular sin permisos y después
// instalar el servicio del sistema es lo esperable: en vez de mandarlo a
// vincular de nuevo, se copia lo que ya había.
func prepareConfig(system bool) error {
	want, err := config.PathFor(system)
	if err != nil {
		return err
	}
	otra, err := config.PathFor(!system)
	if err != nil {
		otra = ""
	}
	mode := config.ModeUser
	if system {
		mode = config.ModeSystem
	}
	return prepareConfigAt(want, otra, mode)
}

// prepareConfigAt es el laburo de prepareConfig con las rutas ya resueltas,
// para poder probarlo sin depender de en qué sistema corren los tests.
func prepareConfigAt(want, otra, mode string) error {
	sinVincular := func(err error) error {
		return fmt.Errorf("primero vinculá la computadora: %v", err)
	}
	system := mode == config.ModeSystem

	cfg, err := config.LoadFrom(want)
	if err == nil {
		if cfg.Mode == mode {
			return nil
		}
		cfg.Mode = mode
		return saveConfig(want, cfg, system)
	}
	if otra == "" || otra == want {
		return sinVincular(err)
	}
	cfg, otroErr := config.LoadFrom(otra)
	if otroErr != nil {
		return sinVincular(err)
	}
	fmt.Fprintf(Out, "La computadora ya estaba vinculada: copio la configuración a %s.\n", want)
	cfg.Mode = mode
	if err := saveConfig(want, cfg, system); err != nil {
		return err
	}
	// Y la que quedó atrás se corre del camino. Si se quedara ahí, entre las
	// dos gana siempre la del usuario —así se encontró la de las
	// instalaciones viejas—, y el agente del sistema estaría escribiendo en
	// una y `desinstalar` leyendo la otra.
	migrado := otra + ".migrado"
	_ = os.Remove(migrado)
	if err := os.Rename(otra, migrado); err != nil {
		fmt.Fprintf(os.Stderr, "ojo: no pude apartar la configuración vieja de %s (%v). Si queda ahí, borrala o renombrala a mano: el agente puede seguir leyéndola.\n", otra, err)
		return nil
	}
	fmt.Fprintf(Out, "La de antes quedó como %s.\n", migrado)
	return nil
}

func saveConfig(p string, cfg config.Config, system bool) error {
	if err := config.SaveTo(p, cfg); err != nil {
		return fmt.Errorf("no se pudo guardar la configuración en %s: %v %s", p, err, sudoHint(system))
	}
	return nil
}

// Las dos guardas de abajo son lo que evita que queden dos agentes con el
// mismo token en la misma computadora: cada comanda saldría dos veces por la
// misma impresora. Se preguntan en los dos sentidos.
//
// A ninguna se le pregunta por el gestor: en Mac, `launchctl list` contesta lo
// mismo para el agente del usuario que para el demonio del sistema, y la
// guarda se dispararía contra sí misma. Lo que no miente es el archivo de la
// unidad. En Windows sí, para el del sistema, porque ahí no hay servicios de
// usuario con qué confundirlo.
//
// Las rutas y el `exists` son variables para que los tests puedan probar las
// guardas sin instalar nada.
var (
	exists = func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	}

	systemUnitPaths = func() []string {
		return []string{
			"/Library/LaunchDaemons/manducar-impresion.plist", // launchd
			"/etc/systemd/system/manducar-impresion.service",  // systemd
			"/etc/init.d/manducar-impresion",                  // sysv, openrc
			"/etc/init/manducar-impresion.conf",               // upstart
		}
	}

	userUnitPaths = func() []string {
		home := invokingHome()
		if home == "" {
			return nil
		}
		return []string{
			filepath.Join(home, "Library", "LaunchAgents", "manducar-impresion.plist"),
			filepath.Join(home, ".config", "systemd", "user", "manducar-impresion.service"),
		}
	}
)

// Installed dice si el agente ya está anotado para arrancar solo en ese modo.
// Es la misma pregunta que se hacen las guardas de `instalar`; la usan
// `estado` y el asistente para saber si hay algo que reinstalar.
func Installed(system bool) bool {
	if system {
		return systemInstalled()
	}
	return userInstalled()
}

func systemInstalled() bool {
	if runtime.GOOS == "windows" {
		s, err := service.New(&program{}, spec("", false))
		if err != nil {
			return false
		}
		st, err := s.Status()
		return err == nil && st != service.StatusUnknown
	}
	return algunoExiste(systemUnitPaths())
}

// userInstalled es la misma pregunta al revés, la que se hace `instalar
// --sistema`. Sin ésta, instalar el servicio del sistema encima de un agente
// de usuario deja los dos corriendo con el mismo token.
func userInstalled() bool {
	if runKeyAutostart { // Windows: lo que arranca solo es la clave Run
		ya, err := autostartInstalled()
		return err == nil && ya
	}
	return algunoExiste(userUnitPaths())
}

func algunoExiste(rutas []string) bool {
	for _, p := range rutas {
		if exists(p) {
			return true
		}
	}
	return false
}

// invokingHome es el home del que llamó al comando, no el de root: `instalar
// --sistema` va con sudo, y ahí `os.UserHomeDir()` dice /root o /var/root. Si
// buscáramos el agente de usuario en ése, la guarda quedaría ciega justo
// cuando importa.
func invokingHome() string {
	if u := os.Getenv("SUDO_USER"); u != "" {
		if h := homeOf(user.Lookup, u); h != "" {
			return h
		}
	}
	if id := os.Getenv("SUDO_UID"); id != "" {
		if h := homeOf(user.LookupId, id); h != "" {
			return h
		}
	}
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return os.Getenv("HOME")
}

func homeOf(lookup func(string) (*user.User, error), quien string) string {
	u, err := lookup(quien)
	if err != nil || u == nil {
		return ""
	}
	return u.HomeDir
}

// Uninstall saca el agente. La configuración queda: si se vuelve a instalar,
// no hace falta vincular de nuevo. El ejecutable copiado también queda:
// borrarlo mientras corre es pelearse con el sistema para nada.
func Uninstall(system bool) int {
	if !system && runKeyAutostart {
		if err := autostartUninstall(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Fprintln(Out, "Listo: el agente ya no arranca solo. La configuración queda por si volvés a instalarlo.")
		return 0
	}
	if err := checkPrivileges(system); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	s, err := service.New(&program{}, spec("", !system))
	if err != nil {
		return 1
	}
	_ = s.Stop()
	if err := s.Uninstall(); err != nil {
		// El gestor contesta con la ruta del archivo que no encontró; eso no
		// le dice nada a nadie.
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, service.ErrNotInstalled) {
			fmt.Fprintln(os.Stderr, errNotInstalled)
			return 1
		}
		fmt.Fprintln(os.Stderr, err, sudoHint(system))
		return 1
	}
	fmt.Fprintln(Out, "Listo: el agente ya no arranca solo. La configuración queda por si volvés a instalarlo.")
	return 0
}

// Restart reinicia el agente si está instalado. Lo usa `vincular`: si no se
// lo reinicia, el que está corriendo sigue con el token viejo.
func Restart(system bool) error {
	if !system && runKeyAutostart {
		return autostartRestart()
	}
	s, err := service.New(&program{}, spec("", !system))
	if err != nil {
		return err
	}
	return s.Restart()
}

// RunAsService lo llama `correr` cuando no lo lanzó una persona sino el
// gestor de servicios. Como no hay terminal, el log va al archivo de al lado
// de la configuración: es lo que se mira cuando algo no sale.
func RunAsService(version string) int {
	s, err := service.New(&program{version: version}, spec("", config.InstalledMode() == config.ModeUser))
	if err != nil {
		return 1
	}
	if err := LogToFile(); err != nil {
		// Sin log no se deja de imprimir: el gestor se queda con lo que
		// escupa por consola.
		log.Println("no se pudo abrir el archivo de log:", err)
	}
	if err := s.Run(); err != nil {
		return 1
	}
	return 0
}

// LogToFile manda el log al archivo de siempre, con su rotación. Lo usan el
// servicio y `correr --log-archivo`, que es como lo lanza el .vbs de Windows:
// ahí tampoco hay dónde mostrar nada.
func LogToFile() error {
	dir, err := config.Dir()
	if err != nil {
		return err
	}
	f, err := openLog(dir)
	if err != nil {
		return err
	}
	log.SetOutput(f)
	return nil
}

// openLog abre (creando si hace falta) impresion.log al lado de dir, para
// agregar al final. Si el que había ya es grande, se lo guarda como
// `impresion.log.1` y se arranca uno nuevo: una vuelta atrás alcanza para
// ver qué pasó, y el disco no se llena.
func openLog(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, "impresion.log")
	if info, err := os.Stat(p); err == nil && info.Size() > logMaxBytes {
		_ = os.Remove(p + ".1")
		if err := os.Rename(p, p+".1"); err != nil {
			log.Println("no se pudo rotar el log:", err)
		}
	}
	return os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
}

var (
	// errNoAutostart es lo que devuelven los stubs de la clave Run en los
	// sistemas que sí tienen servicios de usuario: ahí nadie los llama.
	errNoAutostart = errors.New("en este sistema el arranque automático del usuario lo maneja el gestor de servicios")
	// errNotInstalled: sacar lo que no está no es un error del sistema, es
	// una aclaración.
	errNotInstalled = errors.New("el agente no estaba instalado en esta computadora")
)
