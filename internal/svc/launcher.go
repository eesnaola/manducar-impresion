package svc

import (
	"strings"

	"github.com/eesnaola/manducar-impresion/internal/config"
)

// Lo que hace falta para que el agente arranque solo en Windows sin
// administrador. Las funciones de acá son puras a propósito —arman texto y
// nada más—, así se prueban en cualquier sistema; lo que toca el registro y
// lanza procesos vive en autostart_windows.go.

// launcherName es el .vbs que queda al lado del ejecutable.
const launcherName = "manducar-impresion.vbs"

// vbsLauncher arma el lanzador: unos renglones de VBScript que corren el
// agente sin ventana. Es la única forma sencilla de que la clave Run no le
// abra una consola negra en la cara al que entra a la computadora: el 0 es
// «ventana escondida» y el False es «no esperes a que termine».
//
// cfgPath, si viene, se le clava al agente por variable de entorno. Es el
// equivalente de lo que en Mac y Linux hacen las EnvVars del servicio:
// instalado, el agente no vuelve a adivinar dónde está su configuración.
// «PROCESS» es el entorno de este proceso, que es justamente el que hereda lo
// que se lance desde acá.
func vbsLauncher(exe, cfgPath string, args ...string) string {
	lineas := []string{
		"' Arranca el agente de impresión de Manducar sin ventana.",
		"' Lo escribe «manducar-impresion instalar»; no hace falta tocarlo.",
		`Set sh = CreateObject("WScript.Shell")`,
	}
	if cfgPath != "" {
		lineas = append(lineas, `sh.Environment("PROCESS").Item(`+VBSString(config.EnvPath)+`) = `+VBSString(cfgPath))
	}
	lineas = append(lineas,
		"sh.Run "+VBSString(windowsCommandLine(exe, args...))+", 0, False",
		// CRLF y un renglón al final: es un archivo de Windows y lo puede
		// llegar a abrir el Bloc de notas.
		"")
	return strings.Join(lineas, "\r\n")
}

// vbsFromRunValue saca la ruta del lanzador de lo que está anotado en la clave
// Run. Se lee de ahí y no se vuelve a calcular: si el ejecutable se instaló en
// otra carpeta —una versión anterior, un MANDUCAR_IMPRESION_BIN—, calcularla
// de nuevo nos haría borrar un archivo que no es y dejar el de verdad
// arrancando para siempre.
func vbsFromRunValue(v string) string {
	// Lo normal: «"…\wscript.exe" "…\manducar-impresion.vbs"».
	for _, s := range quotedSegments(v) {
		if esVbs(s) {
			return s
		}
	}
	// Y por si alguien lo editó a mano y le sacó las comillas.
	for _, campo := range strings.Fields(v) {
		if esVbs(campo) {
			return campo
		}
	}
	return ""
}

func esVbs(s string) bool { return strings.HasSuffix(strings.ToLower(s), ".vbs") }

// quotedSegments devuelve lo que haya entre comillas: partiendo por comilla,
// son los pedazos impares.
func quotedSegments(s string) []string {
	partes := strings.Split(s, `"`)
	var out []string
	for i := 1; i < len(partes); i += 2 {
		out = append(out, partes[i])
	}
	return out
}

// VBSString mete una cadena adentro de un literal de VBScript, donde la
// comilla se escapa duplicándola. Los `\` de las rutas de Windows no se tocan:
// en VBScript no escapan nada. Está exportada porque el asistente arma sus
// propios .vbs —los cuadros de diálogo del doble clic— y el escapado tiene que
// ser uno solo: dos copias de esto es una que algún día se olvida.
func VBSString(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// windowsCommandLine arma la línea de comandos: el ejecutable entre comillas
// y los argumentos atrás.
func windowsCommandLine(exe string, args ...string) string {
	partes := make([]string, 0, len(args)+1)
	partes = append(partes, quotePath(exe))
	for _, a := range args {
		partes = append(partes, quoteWindowsArg(a))
	}
	return strings.Join(partes, " ")
}

// quotePath entrecomilla siempre, tenga espacios o no: una ruta de Windows
// sin comillas es el bug de siempre —«C:\Archivos de programa\…» se parte al
// medio y arranca «C:\Archivos.exe»—, y no vale la pena depender de dónde
// haya quedado instalado para estar a salvo.
func quotePath(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// quoteWindowsArg entrecomilla lo que lo necesite. Windows no deja poner `"`
// en el nombre de un archivo, así que esto nunca trae una; igual se la escapa,
// que sale gratis.
func quoteWindowsArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"") {
		return s
	}
	return quotePath(s)
}

// runKeyCommand es lo que se anota en la clave Run del usuario: wscript
// abriendo el lanzador. wscript es la versión sin consola del Windows Script
// Host —cscript, la otra, abre una ventana—.
func runKeyCommand(wscript, vbs string) string {
	return quotePath(wscript) + " " + quotePath(vbs)
}
