package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/kardianos/service"

	"github.com/eesnaola/manducar-impresion/internal/api"
	"github.com/eesnaola/manducar-impresion/internal/config"
	"github.com/eesnaola/manducar-impresion/internal/printer"
	"github.com/eesnaola/manducar-impresion/internal/runner"
	"github.com/eesnaola/manducar-impresion/internal/svc"
)

// Version la pone el release: -ldflags "-X main.Version=1.2.3".
var Version = "dev"

func main() {
	if len(os.Args) < 2 {
		// Sin argumentos es lo que hace un doble clic: se abrió una ventana y
		// hay alguien mirándola, no una línea de comandos que alguien sepa
		// escribir. Ahí va el asistente, no la lista de comandos.
		os.Exit(cmdAsistente())
	}
	// `--sistema` vale para vincular, instalar y desinstalar, y puede venir en
	// cualquier lugar de la línea: se lo saca acá, una vez, antes de que cada
	// comando parsee lo suyo.
	args, sistema := takeFlag(os.Args[2:], "sistema")
	config.UseSystem(sistema)
	switch os.Args[1] {
	case "asistente":
		os.Exit(cmdAsistente())
	case "vincular":
		os.Exit(cmdPair(args, sistema))
	case "correr":
		os.Exit(cmdRun(args))
	case "instalar":
		if sistema {
			os.Exit(svc.Install(Version, true))
		}
		if err := installUser(); err != nil {
			// Qué falló ya salió por la salida de error, con su detalle.
			os.Exit(1)
		}
	case "desinstalar":
		// Sin `--sistema` no hace falta acordarse de cómo se instaló: lo
		// anotó `instalar` en la configuración.
		os.Exit(svc.Uninstall(sistema || config.InstalledMode() == config.ModeSystem))
	case "estado":
		os.Exit(cmdEstado(os.Stdout))
	case "version":
		fmt.Println(Version)
	case "ayuda", "-h", "-help", "--help", "--ayuda":
		usage()
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `manducar-impresion — el agente de impresión de Manducar

Abrilo sin nada y te va guiando; los comandos son para el que prefiere la
terminal.

  asistente                                      te va preguntando y hace todo (es lo que pasa al abrirlo)
  vincular CODIGO --servidor https://TULOCAL.manduc.ar   vincula esta computadora con el código del panel
  instalar                                       deja el agente arrancando solo cada vez que entrás
  desinstalar                                    saca el agente
  estado                                         a qué local está vinculada, dónde están la configuración y el log
  correr                                         corre en primer plano (para probar y ver el log)
  version

Opciones:
  --sistema        en vincular, instalar y desinstalar: como servicio del sistema,
                   para todos los usuarios de la computadora. Necesita sudo (Mac y
                   Linux) o una terminal como administrador (Windows). Sin esto,
                   que es lo normal, no hace falta ningún permiso.
  --log-archivo    en correr: en vez de mostrar el log, lo escribe en
                   impresion.log al lado de la configuración.`)
}

// takeFlag saca una opción sin valor de la línea de comandos y dice si estaba.
// Se hace a mano porque `flag` se planta en el primer argumento suelto, y el
// código de vinculación es justamente eso.
func takeFlag(args []string, name string) ([]string, bool) {
	quedan := make([]string, 0, len(args))
	var estaba bool
	for _, a := range args {
		if a == "--"+name || a == "-"+name {
			estaba = true
			continue
		}
		quedan = append(quedan, a)
	}
	return quedan, estaba
}

// parsePairArgs saca de la línea de comandos el código, el servidor y el
// nombre, en el orden en que vengan. flag.Parse se planta en el primer
// argumento suelto, y el panel imprime «vincular 123456 --servidor URL»: hay
// que dar vuelta el bucle a mano para que las dos formas anden.
func parsePairArgs(args []string) (code, server, name string, err error) {
	fs := flag.NewFlagSet("vincular", flag.ContinueOnError)
	// El subdominio del local, no el dominio raíz: el código de vinculación
	// vale sólo en su local.
	srv := fs.String("servidor", "", "la URL de tu local en Manducar, p. ej. https://pizzeria.manduc.ar")
	nombre := fs.String("nombre", defaultName(), "cómo llamar a esta computadora en el panel")

	var sueltos []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return "", "", "", err
		}
		if fs.NArg() == 0 {
			break
		}
		sueltos = append(sueltos, fs.Arg(0))
		rest = fs.Args()[1:]
	}

	switch {
	case len(sueltos) == 0:
		return "", "", "", errors.New("falta el código de seis dígitos que muestra el panel")
	case len(sueltos) > 1:
		return "", "", "", fmt.Errorf("sobra «%s»: se vincula con un solo código", sueltos[1])
	case *srv == "":
		return "", "", "", errors.New("falta --servidor: la URL de tu local, p. ej. https://pizzeria.manduc.ar (la muestra el panel junto al código)")
	}
	server, err = checkServer(*srv)
	if err != nil {
		return "", "", "", err
	}
	return sueltos[0], server, *nombre, nil
}

// checkServer se planta antes de mandar el token a cualquier lado: una URL
// mal escrita da un error raro recién en el POST, y un `http://` suelto
// manda el código y el token en claro por la red del local.
func checkServer(raw string) (string, error) {
	// Se limpia una vez, acá: lo que se valida y lo que se devuelve tienen
	// que ser la misma cadena.
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("--servidor no es una URL: %s", raw)
	}
	if u.Host == "" || u.Scheme == "" {
		return "", fmt.Errorf("--servidor tiene que ser la URL entera de tu local, con https:// adelante, p. ej. https://pizzeria.manduc.ar (vino «%s»)", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("--servidor tiene que empezar con https://, no con «%s://»", u.Scheme)
	}
	if u.Scheme == "http" && !esLocal(u.Hostname()) {
		return "", fmt.Errorf("con http:// el código y el token viajan en claro por la red: poné https://%s", u.Host)
	}
	return raw, nil
}

// esLocal: en la máquina de uno, http alcanza —es donde se prueba—.
func esLocal(host string) bool {
	switch {
	case host == "localhost", strings.HasSuffix(host, ".localhost"):
		return true
	case host == "127.0.0.1", host == "::1":
		return true
	}
	return false
}

func cmdPair(args []string, sistema bool) int {
	code, server, name, err := parsePairArgs(args)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, err)
		}
		return 2
	}
	info, err := pair(os.Stdout, server, code, name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, os.ErrPermission) {
			if sistema {
				fmt.Fprintln(os.Stderr, "con --sistema la configuración va a una carpeta del sistema, que escribe sólo un administrador: corré el comando con sudo (o abrí la terminal como administrador). Sin --sistema el agente guarda todo en tu carpeta de usuario y no necesita permisos.")
			} else {
				fmt.Fprintln(os.Stderr, "no tenés permiso para escribir ahí: revisá los permisos de esa carpeta, o definí MANDUCAR_IMPRESION_CONFIG con una ruta tuya antes de vincular y de correr.")
			}
		}
		return 1
	}
	if info.Restarted {
		fmt.Println("El agente se reinició con el vínculo nuevo.")
	} else {
		fmt.Println("Ahora corré «manducar-impresion instalar».")
		fmt.Println("Si el agente ya estaba instalado, reinicialo: manducar-impresion desinstalar && manducar-impresion instalar")
	}
	return 0
}

// pairInfo es lo que quedó del vínculo: con qué local, con qué número de
// agente, en qué modo, y si el agente que ya estaba corriendo tomó el token
// nuevo.
type pairInfo struct {
	Store     string
	AgentID   int
	Mode      string
	Restarted bool
}

// pair vincula esta computadora con el local: le pide el token al servidor,
// lo guarda donde el agente lo va a buscar y reinicia el que estuviera
// corriendo. Es lo que hacen `vincular` y el asistente, y tiene que ser
// exactamente lo mismo en los dos: la única diferencia es de dónde salieron
// el código y el servidor. Los avisos van a out; los errores vuelven para que
// el que llamó decida cómo contarlos.
func pair(out io.Writer, server, code, name string) (pairInfo, error) {
	// La ruta se resuelve antes de hablar con el servidor: si la computadora
	// ya estaba vinculada como servicio del sistema, volver a vincularla
	// tiene que reescribir ese archivo y no dejar uno nuevo en el usuario, que
	// el servicio no va a leer nunca. Salvo que ese archivo no sea nuestro:
	// ahí se escribe el del usuario, que es la única forma de salir del modo
	// sistema sin ser administrador.
	path, cambió, err := config.WritePath()
	if err != nil {
		return pairInfo{}, err
	}
	client := api.New(server, "", Version)
	res, err := client.Pair(context.Background(), code, name, listPrinters())
	if err != nil {
		return pairInfo{}, fmt.Errorf("%w: %w", errAlVincular, err)
	}
	modo := config.ModeFor(path)
	if cambió {
		fmt.Fprintf(out, "La configuración del sistema no es tuya —la escribió un administrador—: guardo la de tu usuario, en %s.\n", path)
		fmt.Fprintln(out, "Si el agente estaba instalado como servicio del sistema, sacalo con «sudo manducar-impresion desinstalar --sistema» y volvé a correr «manducar-impresion instalar».")
	}
	cfg := config.Config{Server: server, Token: res.Token, AgentID: res.AgentID, Store: res.Store.Name, Mercure: config.Mercure(res.Mercure), Mode: modo}
	if err := config.SaveTo(path, cfg); err != nil {
		return pairInfo{}, fmt.Errorf("no se pudo guardar la configuración en %s: %w", path, err)
	}
	fmt.Fprintf(out, "Listo: esta computadora quedó vinculada a «%s».\n", res.Store.Name)
	// Si ya estaba instalado, el que corre tiene el token viejo: se lo
	// reinicia para que tome el nuevo. Si no está instalado —que es lo normal
	// la primera vez— esto falla y no pasa nada.
	return pairInfo{
		Store:     res.Store.Name,
		AgentID:   res.AgentID,
		Mode:      modo,
		Restarted: restartAgent(modo == config.ModeSystem) == nil,
	}, nil
}

// installUser deja el agente arrancando solo en la carpeta del usuario: la
// instalación que no pide permisos, la que corre el 99% de los locales. Es la
// misma para `instalar` y para el asistente. Lo que salió mal lo cuenta svc
// por la salida de error, con su detalle; acá vuelve sólo el «no se pudo»,
// para que el asistente pueda ofrecer reintentar.
func installUser() error {
	if svc.Install(Version, false) != 0 {
		return errNoSeInstalo
	}
	return nil
}

var (
	errNoSeInstalo = errors.New("no se pudo dejar el agente arrancando solo")
	// errAlVincular marca los errores que vinieron de hablar con el servidor:
	// el asistente los cuenta distinto, porque ahí lo que suele estar mal es
	// el código —vence a los diez minutos— o la dirección del local.
	errAlVincular = errors.New("no se pudo vincular")
)

// Lo que toca la computadora de verdad, en variables: así el test del
// asistente no le pregunta a CUPS por las impresoras, no reinicia servicios y
// no instala nada.
var (
	listPrinters   = printer.ListSystem
	restartAgent   = svc.Restart
	installAgent   = installUser
	agentInstalled = svc.Installed
)

func cmdRun(args []string) int {
	// Lanzado por el gestor de servicios (systemd, launchd, el SCM de
	// Windows), no hay terminal: el log va a archivo y el ciclo de vida lo
	// maneja el gestor.
	if !service.Interactive() {
		return svc.RunAsService(Version)
	}
	// En Windows sin administrador no hay gestor: al agente lo lanza un .vbs
	// que le pasa --log-archivo. Para el sistema eso es «interactivo», pero
	// no hay ninguna ventana donde mirar el log.
	if wantsFileLog(args, os.Getenv("MANDUCAR_IMPRESION_LOG")) {
		if err := svc.LogToFile(); err != nil {
			// No se deja de imprimir por no poder escribir el log: se sigue,
			// y lo que haya para decir sale por la salida de error.
			log.SetOutput(os.Stderr)
			log.Println("no se pudo abrir el archivo de log:", err)
		}
	} else {
		log.SetOutput(os.Stdout)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runner.Run(ctx, Version); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// wantsFileLog: por la opción o por la variable de entorno, que es lo que se
// puede poner en un acceso directo cuando la línea de comandos no se toca.
func wantsFileLog(args []string, env string) bool {
	if _, hay := takeFlag(args, "log-archivo"); hay {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "", "0", "no", "false":
		return false
	default:
		return true
	}
}

func defaultName() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "Computadora"
	}
	return h
}
