// Package caida guarda el detalle de una caída del agente y lo convierte en el
// informe que se le cuenta al servidor al volver a arrancar.
//
// Todo pasa por impresion.crash, al lado de impresion.log. Con vigía (la
// instalación de usuario de Windows), la salida de error del agente va entera
// ahí —la abre el vigía— y el vigía agrega con qué código terminó el proceso
// (AnotarSalida). Sin vigía (Mac, Linux, el servicio), Go copia ahí el panic o
// el error fatal (Preparar). El agente que arranca después lo lee (Leer), lo
// manda, y lo vacía (Vaciar).
//
// Si una caída no llegó a contarse (sin red, o un agente que se cae en bucle)
// la siguiente se anota atrás de ella. Leer informa sólo la última, mira
// nada más el final del archivo, y Preparar lo achica si creció demasiado.
package caida

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eesnaola/manducar-impresion/internal/api"
	"github.com/eesnaola/manducar-impresion/internal/vigia"
)

// Archivo es donde queda la caída, al lado de impresion.log.
const Archivo = "impresion.crash"

const (
	archivoLog  = "impresion.log"
	marcaSalida = "=== el agente terminó con código "
	resumenMax  = 300
	detalleMax  = 16 << 10
	logMax      = 32 << 10
	lineasDeLog = 200

	// De impresion.crash se lee, y se conserva al compactar, sólo el final.
	colaMax        = 64 << 10
	compactarSobre = 1 << 20
)

// ErrNoEsCaida: impresion.crash tiene texto, pero ni el vigía anotó una
// salida ni Go dejó una caída. Es algo suelto que el agente escribió en la
// salida de error, y no se cuenta.
var ErrNoEsCaida = errors.New("en impresion.crash hay texto que no es una caída")

var (
	reSalida = regexp.MustCompile(`=== el agente terminó con código (-?\d+) (\S+)`)
	// Los panics anidados de un mismo volcado van con tabulación: lo que está
	// en la columna 0 es el encabezado de una caída.
	reCabecera = regexp.MustCompile(`(?m)^(?:panic:|fatal error:)`)
	// El renglón con que Go abre el volcado de cada goroutine: «goroutine 1
	// [running]:», o con gp, m y mp en el medio en las versiones nuevas.
	reGoroutine = regexp.MustCompile(`^goroutine \d+ .*\]:$`)
	// Lo que sólo escribe Go cuando se cae: el motivo, el encabezado de una
	// excepción de Windows, o el volcado.
	reFirma = regexp.MustCompile(`(?m)^(?:panic:|fatal error:|Exception 0x|runtime stack:|goroutine \d+ )`)
)

// Preparar le pide a Go que, si el proceso se cae, deje el detalle en
// impresion.crash además de en la salida de error, que con el agente
// escondido nadie ve. Se llama una vez al arrancar.
//
// Vigilado no hace falta: la salida de error ya va a impresion.crash, y
// entera. Es mejor que la copia: Go copia sólo desde que se da por caído, y
// el encabezado de un error fatal («fatal error: stack overflow») o de una
// excepción de Windows lo escribe antes; pedirla además dejaría la caída dos
// veces.
func Preparar(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Se achica si nadie lo vació en mucho tiempo. Si no se puede, se sigue
	// igual: un archivo grande sirve más que ninguna caída.
	_ = compactar(dir)
	if os.Getenv(vigia.EnvVigilado) != "" {
		return nil
	}

	f, err := os.OpenFile(filepath.Join(dir, Archivo), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	// SetCrashOutput duplica el descriptor: éste se puede cerrar.
	defer f.Close()
	return debug.SetCrashOutput(f, debug.CrashOptions{})
}

// AnotarSalida la usa el vigía cuando el hijo terminó mal.
func AnotarSalida(dir string, codigo int, cuando time.Time) error {
	f, err := os.OpenFile(filepath.Join(dir, Archivo), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s%d %s\n", marcaSalida, codigo, cuando.UTC().Format(time.RFC3339))
	return err
}

// Leer arma el informe de la última caída anotada; nil si no hay ninguna, y
// ErrNoEsCaida si lo que hay no es una caída.
func Leer(dir, version string) (*api.CrashReport, error) {
	p := filepath.Join(dir, Archivo)
	raw, err := colaDeArchivo(p, colaMax)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	if !reSalida.Match(raw) && !reFirma.Match(raw) {
		return nil, fmt.Errorf("%w: %s", ErrNoEsCaida, recortar(primerRenglon(string(raw)), resumenMax))
	}
	texto := ultimaCaida(string(raw))

	ocurrio := time.Now()
	if info, err := os.Stat(p); err == nil {
		ocurrio = info.ModTime()
	}
	var codigo *int
	if m := reSalida.FindAllStringSubmatch(texto, -1); len(m) > 0 {
		ultima := m[len(m)-1]
		if n, err := strconv.Atoi(ultima[1]); err == nil {
			codigo = &n
		}
		if t, err := time.Parse(time.RFC3339, ultima[2]); err == nil {
			ocurrio = t
		}
	}

	return &api.CrashReport{
		Version: version,
		Sistema: api.Platform(),
		Ocurrio: ocurrio.UTC().Format(time.RFC3339),
		Codigo:  codigo,
		Resumen: resumen(texto),
		// El principio: Go escribe primero el motivo y la goroutine que se
		// cayó, y atrás las demás.
		Detalle: principio(texto, detalleMax),
		Log:     colaDelLog(dir),
	}, nil
}

// Vaciar deja el archivo en cero. No se lo borra: el proceso que corre lo
// tiene abierto por Preparar, y en Windows un archivo abierto no se borra.
func Vaciar(dir string) error {
	err := os.Truncate(filepath.Join(dir, Archivo), 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// compactar deja el archivo en sus últimos 64 KB si pasó de 1 MB.
func compactar(dir string) error {
	p := filepath.Join(dir, Archivo)
	info, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() <= compactarSobre {
		return nil
	}
	cola, err := colaDeArchivo(p, colaMax)
	if err != nil {
		return err
	}
	return os.WriteFile(p, cola, 0o644)
}

// colaDeArchivo lee el final del archivo, hasta max bytes, empezando en el
// comienzo de un renglón (el primero, si el corte cayó a la mitad, se descarta).
func colaDeArchivo(p string, max int64) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	desde := int64(0)
	if info.Size() > max {
		// Un byte de más, para saber si el corte cayó justo en un salto de línea.
		desde = info.Size() - max - 1
	}
	buf := make([]byte, info.Size()-desde)
	n, err := f.ReadAt(buf, desde)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	buf = buf[:n]
	if desde > 0 {
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		} else {
			buf = buf[1:]
		}
	}
	return buf, nil
}

// ultimaCaida se queda con lo que corresponde a la última caída del texto.
//
// Con las marcas del vigía, es lo que sigue a la línea de la penúltima: lo que
// escribió el último hijo y su marca. Con una sola marca, todo. Sin ninguna
// (Mac, Linux, sin vigía), desde el último encabezado en la columna 0. Si
// tampoco hay, el texto entero.
func ultimaCaida(texto string) string {
	marcas := reSalida.FindAllStringIndex(texto, -1)
	switch {
	case len(marcas) >= 2:
		resto := texto[marcas[len(marcas)-2][1]:]
		if i := strings.IndexByte(resto, '\n'); i >= 0 {
			return resto[i+1:]
		}
		return resto
	case len(marcas) == 1:
		return texto
	}
	if cab := reCabecera.FindAllStringIndex(texto, -1); len(cab) > 0 {
		return texto[cab[len(cab)-1][0]:]
	}
	return texto
}

// resumen: la primera línea que dice qué pasó («panic: …», «fatal error: …»,
// o «Exception 0xc0000005 …», el encabezado de una excepción de Windows). Si no
// hay ninguna, el primer renglón que diga algo: no «runtime stack:», ni el que
// abre el volcado de una goroutine, ni el «[signal …]». Si Go no dejó nada, la
// salida que anotó el vigía, sin la fecha (ésa va en Ocurrio).
func resumen(texto string) string {
	var salida, primera string
	for _, l := range strings.Split(texto, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case l == "":
		case strings.HasPrefix(l, "panic:"), strings.HasPrefix(l, "fatal error:"), strings.HasPrefix(l, "Exception "):
			return recortar(l, resumenMax)
		case strings.HasPrefix(l, marcaSalida):
			if m := reSalida.FindStringSubmatch(l); m != nil && salida == "" {
				salida = strings.TrimPrefix(marcaSalida, "=== ") + m[1]
			}
		case l == "runtime stack:", strings.HasPrefix(l, "[signal "), reGoroutine.MatchString(l):
		case primera == "":
			primera = l
		}
	}
	if primera != "" {
		return recortar(primera, resumenMax)
	}
	return recortar(salida, resumenMax)
}

func primerRenglon(texto string) string {
	for _, l := range strings.Split(texto, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// colaDelLog: las últimas 200 líneas, y nunca más de 32 KB (se queda el final).
// Si impresion.log tiene menos (el agente lo rota a impresion.log.1 al arrancar,
// antes de que se lea la caída) se completa con el final del rotado.
func colaDelLog(dir string) string {
	lineas := lineasDe(filepath.Join(dir, archivoLog))
	if falta := lineasDeLog - len(lineas); falta > 0 {
		previas := lineasDe(filepath.Join(dir, archivoLog+".1"))
		if len(previas) > falta {
			previas = previas[len(previas)-falta:]
		}
		lineas = append(append([]string{}, previas...), lineas...)
	}
	if len(lineas) > lineasDeLog {
		lineas = lineas[len(lineas)-lineasDeLog:]
	}
	if len(lineas) == 0 {
		return ""
	}
	return final(strings.Join(lineas, "\n")+"\n", logMax)
}

// lineasDe devuelve las líneas del archivo; ninguna si no existe o está vacío.
func lineasDe(p string) []string {
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	texto := strings.TrimRight(string(raw), "\n")
	if texto == "" {
		return nil
	}
	return strings.Split(texto, "\n")
}

// principio se queda con los primeros max bytes de s, sin cortar a la mitad
// de un carácter de varios bytes.
func principio(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	// Se busca dónde empieza el último carácter: si no entró entero, afuera.
	for i := len(s) - 1; i >= 0 && i >= len(s)-utf8.UTFMax; i-- {
		if utf8.RuneStart(s[i]) {
			if !utf8.FullRuneInString(s[i:]) {
				s = s[:i]
			}
			break
		}
	}
	return s
}

// final se queda con los últimos max bytes de s, sin cortar a la mitad de un
// carácter de varios bytes.
func final(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[len(s)-max:]
	// El corte puede haber caído en medio de un carácter de varios bytes.
	for i := 0; i < utf8.UTFMax && len(s) > 0 && !utf8.RuneStart(s[0]); i++ {
		s = s[1:]
	}
	return s
}

func recortar(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
