package main

// El asistente es el programa para el que no abre una terminal: baja el
// archivo, lo abre de un doble clic y pega el código que le muestra el panel.
// Todo lo que hace de verdad —vincular, instalar— es lo mismo que hacen los
// comandos: acá sólo se pregunta el código, se lo valida y se cuenta en
// castellano qué pasó.
//
// Los pasos son unos solos; por dónde se habla, no. En una terminal se
// pregunta y se contesta en la terminal; abierto de un doble clic no hay
// ninguna, así que se pregunta y se cuenta con los cuadros de diálogo del
// sistema (dialogos.go). Eso es la interfaz `ui` de acá abajo: la consola es
// una implementación, los cuadros son la otra, y el asistente —el que vincula
// e instala— no sabe cuál le tocó.

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
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

// dondeEstaElCodigo es lo último que se dice cuando se deja de preguntar: sin
// esto, el que abrió el programa antes de tener el código se queda sin saber
// adónde ir a buscarlo.
const dondeEstaElCodigo = "El código lo muestra el panel en Configuraciones → Impresión → «Vincular una computadora», y vence a los diez minutos. Abrí este programa de nuevo cuando lo tengas a mano."

// cmdAsistente es el asistente: lo que corre un doble clic y lo que corre
// `asistente`. comando es con qué lo llamaron —vacío si no le pasaron nada—,
// que es la mitad de la pregunta de si hay alguien mirando una terminal o no.
func cmdAsistente(args []string, comando string) int {
	servidor, err := wizardServer(args)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, err)
		}
		return 2
	}
	if u := uiDeCuadros(comando); u != nil {
		return asistirCon(u, servidor)
	}
	return asistir(os.Stdin, os.Stdout, servidor)
}

// wizardServer decide contra qué servidor vincula el asistente: el que se le
// haya dado con --servidor —para probar contra un local de desarrollo—, si no
// el de la variable MANDUCAR_IMPRESION_SERVIDOR —lo mismo, sin tocar la línea
// de comandos—, y si no hay ninguno de los dos, el de Manducar. Los códigos
// valen para toda la plataforma: por eso el asistente ya no pregunta la
// dirección del local.
func wizardServer(args []string) (string, error) {
	fs := flag.NewFlagSet("asistente", flag.ContinueOnError)
	srv := fs.String("servidor", "", "para pruebas: contra qué servidor vincula el asistente, si no es "+defaultServer)
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if *srv != "" {
		return normalizeServer(*srv)
	}
	if env := strings.TrimSpace(os.Getenv(envServidor)); env != "" {
		return normalizeServer(env)
	}
	return defaultServer, nil
}

// ui es por dónde habla el asistente. Los pasos son los mismos de los dos
// lados; lo que cambia es cuánto se puede contar sin hacerle cerrar una
// ventana a nadie, y por eso hay más de un verbo para «decir algo».
type ui interface {
	// decir es la narración: en una terminal son renglones que se leen al
	// pasar. En cuadros de diálogo no existe —cada renglón sería una ventana
	// más que cerrar—, así que ahí se pierde a propósito. Nada de lo que va
	// por acá es imprescindible.
	decir(lineas ...string)
	// avisar es lo que sí hay que leer: un renglón en la terminal, una
	// ventana en los cuadros.
	avisar(lineas ...string)
	// fallar es avisar de algo que salió mal. En la terminal es lo mismo que
	// avisar; en un cuadro cambia el ícono, que es lo único que distingue una
	// buena noticia de una mala cuando no hay contexto alrededor.
	fallar(lineas ...string)
	// preguntar pide un dato. El false es que no hay más entrada —la ventana
	// se cerró, la persona canceló, el programa corre con la entrada
	// redirigida—: ahí el asistente termina en vez de preguntarle al vacío.
	preguntar(pregunta string) (string, bool)
	// reintentar cuenta por qué algo no salió y pregunta si se prueba de
	// nuevo. Las dos cosas van juntas porque en un cuadro son una sola
	// ventana: el motivo arriba y los botones abajo.
	reintentar(motivo []string) bool
	// elegir es qué hacer con una computadora que ya está vinculada.
	// Devuelve la opción del menú: "1" instalar, "2" volver a vincular, "3"
	// ver el estado, "" salir sin tocar nada. En la terminal entra el menú
	// entero; en un cuadro, que sólo sabe preguntar sí o no, entra la única
	// pregunta que le importa al que lo abrió.
	elegir(e estadoInfo) string
	// listo es cómo se cuenta que quedó todo hecho.
	listo(local, log string)
	// cerrar es lo último, y en la consola es lo que evita que la ventana se
	// cierre antes de que alguien alcance a leerla.
	cerrar()
}

// asistir es el asistente por la terminal, con la entrada, la salida y el
// servidor contra el que vincula por parámetro: así el test lo maneja con un
// par de cadenas y un httptest.Server.
func asistir(in io.Reader, out io.Writer, servidor string) int {
	return asistirCon(&consola{in: bufio.NewReader(in), out: out}, servidor)
}

// asistirCon son los pasos, sin importar por dónde se hable.
func asistirCon(u ui, servidor string) int {
	a := &asistente{u: u, servidor: servidor}
	a.u.decir(
		"Manducar — impresión",
		"Este programa imprime las comandas y los tickets de tu local en las impresoras de esta computadora, aunque el navegador esté cerrado.",
		"",
	)
	e := estado()
	switch {
	case e.Err != nil:
		a.u.fallar("No pude averiguar dónde guarda esta computadora su configuración: " + e.Err.Error())
		a.fallo = true
	case e.Vinculado:
		a.menu(e)
	default:
		a.desdeCero()
	}
	a.u.cerrar()
	if a.fallo {
		return 1
	}
	return 0
}

type asistente struct {
	u ui
	// servidor es contra qué servidor vincula: manduc.ar salvo que se haya
	// pedido otro con --servidor o con la variable de entorno. La persona no
	// lo elige: el asistente ya no pregunta la dirección del local.
	servidor string
	// fallo: algo no salió y la persona dejó de intentar. Sirve para el
	// código de salida, que es lo único que puede mirar un script.
	fallo bool
	// ultimo es el vínculo que se acaba de hacer, si se hizo alguno.
	ultimo pairInfo
}

// consola es la ui de siempre: la terminal.
type consola struct {
	in  *bufio.Reader
	out io.Writer
}

func (c *consola) decir(lineas ...string) {
	for _, l := range lineas {
		fmt.Fprintln(c.out, l)
	}
}

// En la terminal no hay diferencia entre contar algo al pasar y contar algo
// que hay que leer: es el mismo renglón. La diferencia la hacen los cuadros.
func (c *consola) avisar(lineas ...string) { c.decir(lineas...) }
func (c *consola) fallar(lineas ...string) { c.decir(lineas...) }

func (c *consola) preguntar(pregunta string) (string, bool) {
	fmt.Fprintf(c.out, "%s ", pregunta)
	linea, err := c.in.ReadString('\n')
	if err != nil && strings.TrimSpace(linea) == "" {
		fmt.Fprintln(c.out)
		return "", false
	}
	return strings.TrimSpace(linea), true
}

// confirmar es la pregunta de sí o no. Por default sí: se la hace justo
// después de que algo salió mal, y ahí lo que se quiere es reintentar.
func (c *consola) confirmar(pregunta string) bool {
	r, ok := c.preguntar(pregunta + " [S/n]")
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

func (c *consola) reintentar(motivo []string) bool {
	c.decir(motivo...)
	return c.confirmar("¿Probamos de nuevo?")
}

func (c *consola) elegir(e estadoInfo) string {
	c.decir(
		fmt.Sprintf("Esta computadora ya está vinculada a «%s» (agente %d).", e.Local, e.AgentID),
		"",
		"  [1] Instalar o reinstalar para que arranque solo",
		"  [2] Volver a vincular con otro código",
		"  [3] Ver el estado",
		"  [Enter] salir",
		"",
	)
	op, ok := c.preguntar("¿Qué querés hacer?")
	if !ok {
		return ""
	}
	c.decir("")
	return op
}

func (c *consola) listo(_, log string) {
	c.decir(
		"",
		"Listo: el agente arranca solo cada vez que entrás a esta computadora.",
		"Lo que va haciendo queda en "+log+".",
		"Ya podés borrar el archivo que bajaste: el que manda es el que quedó instalado.",
	)
}

// cerrar: en Windows el doble clic abre una ventana que se cierra sola apenas
// el programa termina. Si no la frenamos, todo lo que acabamos de escribir se
// ve un cuarto de segundo. Con la entrada redirigida —un script, el test— no
// hay a quién esperar.
func (c *consola) cerrar() {
	c.decir("", "Podés cerrar esta ventana.")
	if !esTerminal() {
		return
	}
	fmt.Fprint(c.out, "(apretá Enter para salir) ")
	_, _ = c.in.ReadString('\n')
}

// esTerminal dice si del otro lado hay una persona en una consola. Variable
// para que el test no dependa de con qué entrada lo corrieron.
var esTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// desdeCero es la primera vez: esta computadora no está vinculada a nada.
func (a *asistente) desdeCero() {
	a.u.decir(
		"Vamos a vincular esta computadora con tu local.",
		"Tené abierto el panel de Manducar en Configuraciones → Impresión → «Vincular una computadora»: ahí está el código.",
		"",
	)
	if !a.pasoVincular() {
		return
	}
	a.u.decir("")
	a.pasoInstalar()
}

// menu es lo que ve el que la abre de nuevo, con la computadora ya vinculada.
func (a *asistente) menu(e estadoInfo) {
	switch op := a.u.elegir(e); op {
	case "1":
		// El servicio del sistema se toca con permisos, y el asistente corre
		// sin ninguno: mejor decirlo que fallar a la mitad.
		if e.Modo == config.ModeSystem {
			a.u.avisar("Acá el agente está instalado como servicio del sistema, y eso se toca con permisos de administrador: desde una terminal, «sudo manducar-impresion instalar --sistema» (o la terminal como administrador, en Windows).")
			return
		}
		a.pasoInstalar()
	case "2":
		if !a.pasoVincular() {
			return
		}
		if a.ultimo.Restarted {
			a.u.avisar("", "El agente que ya estaba corriendo tomó el vínculo nuevo.")
			return
		}
		a.u.decir("")
		a.pasoInstalar()
	case "3":
		a.u.avisar(estadoLineas(estado())...)
	case "":
		// Enter: no toca nada y se va.
	default:
		a.u.avisar(fmt.Sprintf("No entendí «%s»: era 1, 2, 3 o Enter. Abrí el programa de nuevo.", op))
	}
}

// pasoVincular pregunta el código y vincula contra a.servidor. Si algo sale
// mal, lo cuenta y ofrece empezar de nuevo: el código vence a los diez
// minutos, así que reintentar es pedirle uno nuevo al panel, no repetir el
// mismo. A la tercera se deja de ofrecer: el que vincula mal tres veces no
// tiene el código a mano, y lo que le sirve es saber dónde está.
func (a *asistente) pasoVincular() bool {
	for intento := 1; ; intento++ {
		code, ok := a.pedirCodigo()
		if !ok {
			a.fallo = true
			return false
		}
		a.u.decir("", "Vinculando…")
		w := &renglones{u: a.u}
		info, err := pair(w, a.servidor, code, defaultName())
		w.cerrar()
		if err == nil {
			a.ultimo = info
			return true
		}
		if intento == maxIntentos {
			a.u.avisar(append(explicarVinculo(err), dondeEstaElCodigo)...)
			a.fallo = true
			return false
		}
		if !a.u.reintentar(append([]string{""}, explicarVinculo(err)...)) {
			a.fallo = true
			return false
		}
		a.u.decir("")
	}
}

func (a *asistente) pedirCodigo() (string, bool) {
	for intento := 1; ; intento++ {
		linea, ok := a.u.preguntar("Pegá el código que muestra el panel:")
		if !ok {
			return "", false
		}
		code, err := parseCode(linea)
		if err == nil {
			return code, true
		}
		if intento == maxIntentos {
			a.u.avisar(enCastellano(err), dondeEstaElCodigo)
			return "", false
		}
		a.u.avisar(enCastellano(err))
	}
}

// pasoInstalar deja el agente arrancando solo, y lo cuenta él: svc dice lo
// suyo por su cuenta y decir dos veces lo mismo confunde a cualquiera.
func (a *asistente) pasoInstalar() bool {
	for {
		a.u.decir("Dejando el agente instalado para que arranque solo…")
		if err := a.instalar(); err == nil {
			a.u.listo(a.local(), estado().Log)
			return true
		}
		if !a.u.reintentar([]string{"", "No se pudo dejar el agente arrancando solo en esta computadora."}) {
			a.fallo = true
			return false
		}
		a.u.decir("")
	}
}

// local es a qué local quedó atada esta computadora: el del vínculo que se
// acaba de hacer, o el que ya estaba guardado si no se vinculó nada.
func (a *asistente) local() string {
	if a.ultimo.Store != "" {
		return a.ultimo.Store
	}
	return estado().Local
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

// renglones es adónde escribe `pair` sus avisos. Son narración —«listo, quedó
// vinculada a…»—, así que van por decir: renglones en la terminal, nada en un
// cuadro. Se junta hasta el fin de renglón porque la ui habla de a renglones
// enteros y un io.Writer puede venir cortado por cualquier lado.
type renglones struct {
	u     ui
	resto []byte
}

func (r *renglones) Write(p []byte) (int, error) {
	r.resto = append(r.resto, p...)
	for {
		i := bytes.IndexByte(r.resto, '\n')
		if i < 0 {
			return len(p), nil
		}
		r.u.decir(string(r.resto[:i]))
		r.resto = r.resto[i+1:]
	}
}

// cerrar suelta lo que haya quedado sin su fin de renglón.
func (r *renglones) cerrar() {
	if len(r.resto) > 0 {
		r.u.decir(string(r.resto))
		r.resto = nil
	}
}

// parseCode se queda con los ocho dígitos del panel, como los pegue la
// persona: el panel los muestra en dos grupos («4286 2135»), y pueden llegar
// con un guión, con espacios de más, o de cualquier otra forma en que se
// copien ocho números.
func parseCode(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("no escribiste nada: el código son los ocho números que muestra el panel")
	}
	limpio := strings.NewReplacer(" ", "", "-", "", ".", "").Replace(s)
	if len(limpio) != 8 || !soloDigitos(limpio) {
		return "", fmt.Errorf("«%s» no es un código de ocho números: copiá el que muestra el panel, tal cual", s)
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

// normalizeServer toma la dirección como la escribiría alguien que no piensa
// en URLs —«pizzeria.manduc.ar», o lo que copió de la barra del navegador con
// todo lo que venía atrás— y la deja como la quiere el agente:
// https://pizzeria.manduc.ar. La usa wizardServer para --servidor y para la
// variable de entorno, que en pruebas se escriben así de sueltos. El https no
// se discute: por ahí viajan el código y el token. En la máquina de uno, que
// es donde se prueba, http alcanza; y si alguien escribió el esquema a mano
// se lo respeta y decide checkServer, que es el que ya sabía todo esto.
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
	for _, l := range estadoLineas(e) {
		fmt.Fprintln(w, l)
	}
}

// estadoLineas es el cuadro de estado, renglón por renglón: lo escribe
// `estado` por la salida, y el asistente lo muestra como está —en la terminal,
// o adentro de una ventana— sin volver a armarlo.
func estadoLineas(e estadoInfo) []string {
	if e.Err != nil {
		return []string{"No pude averiguar dónde guarda esta computadora su configuración: " + e.Err.Error()}
	}
	var l []string
	if e.Vinculado {
		l = append(l,
			fmt.Sprintf("Local:          %s (agente %d)", e.Local, e.AgentID),
			fmt.Sprintf("Servidor:       %s", e.Server),
		)
	} else {
		l = append(l, "Esta computadora todavía no está vinculada a ningún local.")
	}
	return append(l,
		fmt.Sprintf("Modo:           %s", enPalabras(e.Modo)),
		fmt.Sprintf("Arranca solo:   %s", arrancaSolo(e.Instalado)),
		fmt.Sprintf("Configuración:  %s", e.Config),
		fmt.Sprintf("Log:            %s", e.Log),
	)
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
