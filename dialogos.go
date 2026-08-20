package main

// Los cuadros de diálogo del sistema: lo que ve el que abre el programa de un
// doble clic, donde no hay ninguna terminal en la que escribir.
//
// Adentro no hay ninguna biblioteca de ventanas: en Windows se le pide el
// cuadro al Windows Script Host —un .vbs de cuatro renglones que corre
// wscript, el host que no abre consola— y en Mac a osascript, que los dos
// vienen con el sistema. Es feo y es lo que no hay que instalar.

import (
	"fmt"
	"runtime"
	"strings"
)

// dialogs es lo poco que hace falta pedirle al sistema para que el asistente
// entero funcione: preguntar un dato, contar algo, contar algo que salió mal
// y preguntar sí o no. Cada sistema lo contesta a su manera; el asistente no
// se entera de cuál le tocó.
type dialogs interface {
	// Ask pregunta un dato. El false es que la persona canceló.
	Ask(title, prompt string) (value string, ok bool, err error)
	Info(title, text string) error
	Error(title, text string) error
	Confirm(title, text string) (bool, error)
}

// tituloCuadro va en la barra de todas las ventanas: es lo único que le dice
// a la persona de qué programa es el cuadro que le apareció.
const tituloCuadro = "Manducar — impresión"

// uiDeCuadros devuelve por dónde hablar si esto lo abrió un doble clic, y nil
// si no: ahí manda la consola de siempre. comando es con qué lo llamaron.
func uiDeCuadros(comando string) ui {
	if !abiertoDeDobleClic(comando, esTerminal(), runtime.GOOS) {
		return nil
	}
	d, err := nuevosCuadros()
	if err != nil {
		// Sin cuadros no se abandona: queda la consola, que en el peor caso es
		// una ventana negra que se ve fea pero deja vincular igual.
		return nil
	}
	// Recién ahora, con los cuadros conseguidos, se suelta la consola que
	// Windows abrió al hacer doble clic: si la soltáramos antes y después no
	// hubiera con qué preguntar, nos quedaríamos sin las dos cosas.
	soltarConsola()
	return &cuadros{d: d}
}

// abiertoDeDobleClic: no hay forma de preguntarle al sistema «¿esto lo abrió
// una persona desde el escritorio?», así que se lo deduce. Del otro lado de la
// entrada no hay una terminal —el doble clic no le da ninguna— y no le pasaron
// ningún comando, porque para escribir un comando hace falta justamente la
// terminal que no hay. Sólo vale donde hay cuadros que mostrar: en Linux el
// programa se abre desde una terminal, y ahí el asistente ya se entiende.
func abiertoDeDobleClic(comando string, terminal bool, sistema string) bool {
	if terminal {
		return false
	}
	switch comando {
	case "", "asistente":
	default:
		return false
	}
	switch sistema {
	case "windows", "darwin":
		return true
	}
	return false
}

// cuadros es la ui contra las ventanas del sistema. La diferencia con la
// consola no es de idioma sino de costo: un renglón de más no molesta a nadie,
// una ventana de más hay que cerrarla. Por eso la narración se pierde y sólo
// se abre una ventana cuando hay algo que decidir o algo que enterarse.
type cuadros struct{ d dialogs }

func (c *cuadros) decir(...string) {}

func (c *cuadros) avisar(lineas ...string) { c.mostrar(c.d.Info, lineas) }
func (c *cuadros) fallar(lineas ...string) { c.mostrar(c.d.Error, lineas) }

func (c *cuadros) mostrar(cuadro func(string, string) error, lineas []string) {
	texto := parrafo(lineas)
	if texto == "" {
		return
	}
	_ = cuadro(tituloCuadro, texto)
}

func (c *cuadros) preguntar(pregunta string) (string, bool) {
	v, ok, err := c.d.Ask(tituloCuadro, pregunta)
	if err != nil {
		c.fallar(enCastellano(err))
		return "", false
	}
	return v, ok
}

func (c *cuadros) reintentar(motivo []string) bool {
	si, err := c.d.Confirm(tituloCuadro, parrafo(append(motivo, "", "¿Probamos de nuevo?")))
	if err != nil {
		c.fallar(enCastellano(err))
		return false
	}
	return si
}

// elegir: un cuadro no sabe hacer un menú de cuatro opciones, y tampoco hace
// falta. Al que ya tiene la computadora vinculada y vuelve a abrir el programa
// le pasa una sola cosa: le dieron un código nuevo. Se le pregunta eso, y el
// que dice que no se va sabiendo cómo quedó todo.
func (c *cuadros) elegir(e estadoInfo) string {
	si, err := c.d.Confirm(tituloCuadro, fmt.Sprintf(
		"Esta computadora ya está vinculada a «%s».\n\n¿Querés volver a vincularla con otro código?", e.Local))
	if err != nil {
		c.fallar(enCastellano(err))
		return ""
	}
	if si {
		return "2" // volver a vincular
	}
	return "3" // ver el estado
}

func (c *cuadros) listo(local, _ string) {
	c.avisar(
		fmt.Sprintf("Listo: «%s» quedó vinculada y el programa va a arrancar solo cada vez que entrés a esta computadora.", local),
		"",
		"Ya podés cerrar esto.",
	)
}

// cerrar: el último cuadro ya se cerró de un clic; no hay ninguna ventana que
// frenar.
func (c *cuadros) cerrar() {}

// parrafo arma el texto de una ventana con los renglones que en la terminal
// irían sueltos. Los vacíos del principio y del final no valen nada adentro de
// un cuadro, y dos seguidos tampoco: lo que en la pantalla es aire, acá es
// una ventana más alta y nada más.
func parrafo(lineas []string) string {
	var quedan []string
	for _, l := range lineas {
		l = strings.TrimRight(l, " \t")
		if l == "" && (len(quedan) == 0 || quedan[len(quedan)-1] == "") {
			continue
		}
		quedan = append(quedan, l)
	}
	for len(quedan) > 0 && quedan[len(quedan)-1] == "" {
		quedan = quedan[:len(quedan)-1]
	}
	return strings.Join(quedan, "\n")
}
