package main

// El asistente es el programa para el que no abre una terminal: baja el
// archivo, lo abre de un doble clic y contesta dos preguntas. Todo lo que
// hace de verdad —vincular, instalar— es lo mismo que hacen los comandos:
// acá sólo se pregunta, se valida lo que la persona escribió y se cuenta en
// castellano qué pasó.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"golang.org/x/term"

	"github.com/eesnaola/manducar-impresion/internal/config"
	"github.com/eesnaola/manducar-impresion/internal/svc"
)

// maxIntentos: tres veces el mismo dato mal escrito y mejor volver a empezar
// con el panel delante que seguir preguntando.
const maxIntentos = 3

// cmdAsistente es el asistente contra la terminal de verdad. Es lo que corre
// un doble clic y lo que corre `asistente`.
func cmdAsistente() int { return asistir(os.Stdin, os.Stdout) }

// asistir es el asistente entero, con la entrada y la salida por parámetro:
// así el test lo maneja con un par de cadenas.
func asistir(in io.Reader, out io.Writer) int {
	a := &asistente{in: bufio.NewReader(in), out: out}
	a.presentacion()
	e := estado()
	switch {
	case e.Err != nil:
		a.decir("No pude averiguar dónde guarda esta computadora su configuración: " + e.Err.Error())
		a.fallo = true
	case e.Vinculado:
		a.menu(e)
	default:
		a.desdeCero()
	}
	a.despedida()
	if a.fallo {
		return 1
	}
	return 0
}

type asistente struct {
	in  *bufio.Reader
	out io.Writer
	// fallo: algo no salió y la persona dejó de intentar. Sirve para el
	// código de salida, que es lo único que puede mirar un script.
	fallo bool
	// ultimo es el vínculo que se acaba de hacer, si se hizo alguno.
	ultimo pairInfo
}

func (a *asistente) decir(lineas ...string) {
	for _, l := range lineas {
		fmt.Fprintln(a.out, l)
	}
}

// preguntar escribe la pregunta y espera la respuesta. El false es que no hay
// más entrada —la ventana se cerró, o el programa corre con la entrada
// redirigida—: ahí el asistente termina en vez de preguntarle al vacío.
func (a *asistente) preguntar(pregunta string) (string, bool) {
	fmt.Fprintf(a.out, "%s ", pregunta)
	linea, err := a.in.ReadString('\n')
	if err != nil && strings.TrimSpace(linea) == "" {
		fmt.Fprintln(a.out)
		return "", false
	}
	return strings.TrimSpace(linea), true
}

// confirmar es la pregunta de sí o no. Por default sí: se la hace justo
// después de que algo salió mal, y ahí lo que se quiere es reintentar.
func (a *asistente) confirmar(pregunta string) bool {
	r, ok := a.preguntar(pregunta + " [S/n]")
	if !ok {
		return false
	}
	switch strings.ToLower(r) {
	case "n", "no":
		return false
	default:
		return true
	}
}

func (a *asistente) presentacion() {
	a.decir(
		"Manducar — impresión",
		"Este programa imprime las comandas y los tickets de tu local en las impresoras de esta computadora, aunque el navegador esté cerrado.",
		"",
	)
}

// despedida: en Windows el doble clic abre una ventana que se cierra sola
// apenas el programa termina. Si no la frenamos, todo lo que acabamos de
// escribir se ve un cuarto de segundo. Con la entrada redirigida —un script,
// el test— no hay a quién esperar.
func (a *asistente) despedida() {
	a.decir("", "Podés cerrar esta ventana.")
	if !esTerminal() {
		return
	}
	fmt.Fprint(a.out, "(apretá Enter para salir) ")
	_, _ = a.in.ReadString('\n')
}

// esTerminal dice si del otro lado hay una persona en una consola. Variable
// para que el test no dependa de con qué entrada lo corrieron.
var esTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// desdeCero es la primera vez: esta computadora no está vinculada a nada.
func (a *asistente) desdeCero() {
	a.decir(
		"Vamos a vincular esta computadora con tu local.",
		"Tené abierto el panel de Manducar en Configuraciones → Impresión → «Vincular una computadora»: ahí está el código.",
		"",
	)
	if !a.pasoVincular() {
		return
	}
	a.decir("")
	a.pasoInstalar()
}

// menu es lo que ve el que la abre de nuevo, con la computadora ya vinculada.
func (a *asistente) menu(e estadoInfo) {
	a.decir(
		fmt.Sprintf("Esta computadora ya está vinculada a «%s» (agente %d).", e.Local, e.AgentID),
		"",
		"  [1] Instalar o reinstalar para que arranque solo",
		"  [2] Volver a vincular con otro código",
		"  [3] Ver el estado",
		"  [Enter] salir",
		"",
	)
	op, ok := a.preguntar("¿Qué querés hacer?")
	if !ok {
		return
	}
	a.decir("")
	switch op {
	case "1":
		// El servicio del sistema se toca con permisos, y el asistente corre
		// sin ninguno: mejor decirlo que fallar a la mitad.
		if e.Modo == config.ModeSystem {
			a.decir("Acá el agente está instalado como servicio del sistema, y eso se toca con permisos de administrador: desde una terminal, «sudo manducar-impresion instalar --sistema» (o la terminal como administrador, en Windows).")
			return
		}
		a.pasoInstalar()
	case "2":
		if !a.pasoVincular() {
			return
		}
		if a.ultimo.Restarted {
			a.decir("", "El agente que ya estaba corriendo tomó el vínculo nuevo.")
			return
		}
		a.decir("")
		a.pasoInstalar()
	case "3":
		printEstado(a.out, estado())
	case "":
		// Enter: no toca nada y se va.
	default:
		a.decir(fmt.Sprintf("No entendí «%s»: era 1, 2, 3 o Enter. Abrí el programa de nuevo.", op))
	}
}

// pasoVincular pregunta el código y la dirección, y vincula. Si algo sale
// mal, lo cuenta y ofrece empezar de nuevo: el código vence a los diez
// minutos, así que reintentar es pedirle uno nuevo al panel, no repetir el
// mismo.
func (a *asistente) pasoVincular() bool {
	for {
		code, ok := a.pedirCodigo()
		if !ok {
			a.fallo = true
			return false
		}
		server, ok := a.pedirServidor()
		if !ok {
			a.fallo = true
			return false
		}
		a.decir("", "Vinculando…")
		info, err := pair(a.out, server, code, defaultName())
		if err == nil {
			a.ultimo = info
			return true
		}
		a.decir("")
		a.decir(explicarVinculo(err)...)
		if !a.confirmar("¿Probamos de nuevo?") {
			a.fallo = true
			return false
		}
		a.decir("")
	}
}

func (a *asistente) pedirCodigo() (string, bool) {
	for intento := 1; ; intento++ {
		linea, ok := a.preguntar("Pegá el código de seis dígitos que muestra el panel:")
		if !ok {
			return "", false
		}
		code, err := parseCode(linea)
		if err == nil {
			return code, true
		}
		a.decir(enCastellano(err))
		if intento == maxIntentos {
			a.decir("El código lo muestra el panel en Configuraciones → Impresión → «Vincular una computadora», y vence a los diez minutos. Abrí este programa de nuevo cuando lo tengas a mano.")
			return "", false
		}
	}
}

func (a *asistente) pedirServidor() (string, bool) {
	for intento := 1; ; intento++ {
		linea, ok := a.preguntar("Dirección de tu local en Manducar (la que ves arriba en el navegador, por ejemplo pizzeria.manduc.ar):")
		if !ok {
			return "", false
		}
		server, err := normalizeServer(linea)
		if err == nil {
			return server, true
		}
		a.decir(enCastellano(err))
		if intento == maxIntentos {
			a.decir("Es la dirección que ves arriba en el navegador cuando entrás a Manducar, la de tu local: pizzeria.manduc.ar, no manduc.ar a secas.")
			return "", false
		}
	}
}

// pasoInstalar deja el agente arrancando solo, y lo cuenta él: svc dice lo
// suyo por su cuenta y decir dos veces lo mismo confunde a cualquiera.
func (a *asistente) pasoInstalar() bool {
	for {
		a.decir("Dejando el agente instalado para que arranque solo…")
		if err := a.instalar(); err == nil {
			a.decir(
				"",
				"Listo: el agente arranca solo cada vez que entrás a esta computadora.",
				"Lo que va haciendo queda en "+estado().Log+".",
				"Ya podés borrar el archivo que bajaste: el que manda es el que quedó instalado.",
			)
			return true
		}
		a.decir("", "No se pudo dejar el agente arrancando solo; unas líneas más arriba está el detalle.")
		if !a.confirmar("¿Probamos de nuevo?") {
			a.fallo = true
			return false
		}
		a.decir("")
	}
}

// instalar es `instalar` sin permisos, con dos diferencias: los avisos de svc
// se callan —el que le habla a la persona es el asistente— y, si ya había un
// agente instalado, se lo saca primero. `instalar` a secas no pisa lo que
// hay, y hace bien —dos agentes imprimen todo dos veces—, pero el que abre el
// asistente y elige «reinstalar» quiere justamente eso.
func (a *asistente) instalar() error {
	previo := svc.Out
	svc.Out = io.Discard
	defer func() { svc.Out = previo }()
	if agentInstalled(false) {
		if svc.Uninstall(false) != 0 {
			return errors.New("no se pudo sacar el agente que ya estaba instalado")
		}
	}
	return installAgent()
}

// parseCode se queda con los seis dígitos del panel, como los pegue la
// persona: con un espacio en el medio, con un guión, con espacios de más.
func parseCode(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("no escribiste nada: el código son los seis números que muestra el panel")
	}
	limpio := strings.NewReplacer(" ", "", "-", "", ".", "").Replace(s)
	if len(limpio) != 6 || !soloDigitos(limpio) {
		return "", fmt.Errorf("«%s» no es un código de seis números: copiá el que muestra el panel, tal cual", s)
	}
	return limpio, nil
}

func soloDigitos(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// normalizeServer toma la dirección como la escribe alguien que no piensa en
// URLs —«pizzeria.manduc.ar», o lo que copió de la barra del navegador con
// todo lo que venía atrás— y la deja como la quiere el agente:
// https://pizzeria.manduc.ar. El https no se discute: por ahí viajan el
// código y el token. En la máquina de uno, que es donde se prueba, http
// alcanza; y si alguien escribió el esquema a mano se lo respeta y decide
// checkServer, que es el que ya sabía todo esto.
func normalizeServer(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("no escribiste nada: es la dirección que ves arriba en el navegador, por ejemplo pizzeria.manduc.ar")
	}
	if !strings.Contains(s, "://") {
		s = esquemaPara(s) + "://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("«%s» no parece una dirección: tiene que ser algo como pizzeria.manduc.ar", strings.TrimSpace(raw))
	}
	// De lo que haya pegado queda el local y nada más: el agente le pega
	// «/agente/…» atrás, así que una ruta de más rompería todas las llamadas.
	return checkServer(u.Scheme + "://" + strings.ToLower(u.Host))
}

// esquemaPara: https, salvo en la máquina de uno.
func esquemaPara(dir string) string {
	host := dir
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if esLocal(strings.ToLower(host)) {
		return "http"
	}
	return "https"
}

// explicarVinculo dice en castellano por qué no se pudo vincular. Los errores
// de red de Go son ilegibles para el que está en el mostrador, pero cuando
// después llama por teléfono son justo lo que hace falta: van abajo, entre
// paréntesis.
func explicarVinculo(err error) []string {
	detalle := "(detalle: " + err.Error() + ")"
	var red *url.Error
	var dns *net.DNSError
	switch {
	case errors.As(err, &dns):
		return []string{"No encontré esa dirección: revisá que sea la de tu local, la que ves arriba en el navegador.", detalle}
	case errors.As(err, &red) && red.Timeout():
		return []string{"El servidor de tu local no contestó a tiempo: probá de nuevo en un minuto.", detalle}
	case errors.As(err, &red):
		return []string{"No pude llegar a tu local: fijate que esta computadora tenga internet y que la dirección sea la correcta.", detalle}
	case errors.Is(err, errAlVincular):
		// Contestó el servidor y dijo que no: casi siempre es el código.
		return []string{enCastellano(err), "Puede que el código ya haya vencido —dura diez minutos—: pedile uno nuevo al panel y probá otra vez."}
	}
	return []string{enCastellano(err)}
}

// enCastellano deja el error como para mostrarlo solo, en su renglón: los de
// Go empiezan en minúscula, un cartel no.
func enCastellano(err error) string {
	r := []rune(err.Error())
	if len(r) == 0 {
		return ""
	}
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// estadoInfo es todo lo que se puede decir de esta computadora sin
// preguntarle nada al servidor.
type estadoInfo struct {
	Vinculado bool
	Local     string // el nombre del local; en una configuración vieja, el servidor
	Server    string
	AgentID   int
	Modo      string
	Config    string
	Log       string
	Instalado bool
	// Err es no haber podido ni averiguar dónde va la configuración.
	Err error
}

// estado mira la configuración y el arranque automático y arma el cuadro. Lo
// usan el comando `estado` y el asistente, que muestra lo mismo.
func estado() estadoInfo {
	p, err := config.Path()
	if err != nil {
		return estadoInfo{Err: err}
	}
	e := estadoInfo{
		Config: p,
		Log:    filepath.Join(filepath.Dir(p), "impresion.log"),
		Modo:   config.ModeFor(p),
	}
	if cfg, err := config.LoadFrom(p); err == nil {
		e.Vinculado = cfg.Token != ""
		e.Server = cfg.Server
		e.AgentID = cfg.AgentID
		if cfg.Mode != "" {
			e.Modo = cfg.Mode
		}
		if e.Local = cfg.Store; e.Local == "" {
			e.Local = hostDe(cfg.Server)
		}
	}
	e.Instalado = agentInstalled(e.Modo == config.ModeSystem)
	return e
}

func cmdEstado(w io.Writer) int {
	e := estado()
	printEstado(w, e)
	if e.Err != nil {
		return 1
	}
	return 0
}

func printEstado(w io.Writer, e estadoInfo) {
	if e.Err != nil {
		fmt.Fprintln(w, "No pude averiguar dónde guarda esta computadora su configuración:", e.Err)
		return
	}
	if e.Vinculado {
		fmt.Fprintf(w, "Local:          %s (agente %d)\n", e.Local, e.AgentID)
		fmt.Fprintf(w, "Servidor:       %s\n", e.Server)
	} else {
		fmt.Fprintln(w, "Esta computadora todavía no está vinculada a ningún local.")
	}
	fmt.Fprintf(w, "Modo:           %s\n", enPalabras(e.Modo))
	fmt.Fprintf(w, "Arranca solo:   %s\n", arrancaSolo(e.Instalado))
	fmt.Fprintf(w, "Configuración:  %s\n", e.Config)
	fmt.Fprintf(w, "Log:            %s\n", e.Log)
}

func enPalabras(modo string) string {
	if modo == config.ModeSystem {
		return "servicio del sistema (para todos los usuarios)"
	}
	return "tu usuario (sin permisos especiales)"
}

func arrancaSolo(instalado bool) string {
	if instalado {
		return "sí, está instalado"
	}
	return "no, todavía no lo instalaste"
}

// hostDe es el nombre que se muestra cuando la configuración es vieja y no
// tiene guardado el del local: el servidor dice bastante.
func hostDe(server string) string {
	if u, err := url.Parse(server); err == nil && u.Host != "" {
		return u.Host
	}
	return server
}
