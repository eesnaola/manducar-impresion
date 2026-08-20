//go:build darwin

package main

// Los cuadros de Mac: osascript, que viene con el sistema. El AppleScript se
// le pasa renglón por renglón con -e, que es como está probado que anda.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func nuevosCuadros() (dialogs, error) {
	exe, err := exec.LookPath("osascript")
	if err != nil {
		return nil, fmt.Errorf("no encontré osascript, que es lo que muestra los cuadros de diálogo: %w", err)
	}
	return cuadrosMac{osascript: exe, icono: iconoDelBundle()}, nil
}

// iconoDelBundle: la placa de la marca, si estamos corriendo desde adentro de
// «Manducar Impresión.app» (…/Contents/MacOS/el-binario → …/Contents/
// Resources/manducar.icns). Con el binario suelto no hay ícono propio y los
// cuadros usan el «note» del sistema.
func iconoDelBundle() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	icns := filepath.Join(filepath.Dir(filepath.Dir(exe)), "Resources", "manducar.icns")
	if _, err := os.Stat(icns); err != nil {
		return ""
	}
	return icns
}

// soltarConsola: en Mac el doble clic abre la Terminal, y ésa es de la
// persona; no hay ninguna consola nuestra que soltar.
func soltarConsola() {}

type cuadrosMac struct {
	osascript string
	icono     string // ruta al .icns del bundle, o "" (usa «note»)
}

// iconoNormal es el de los cuadros que no son un error.
func (c cuadrosMac) iconoNormal() string {
	if c.icono != "" {
		return c.icono
	}
	return "note"
}

func (c cuadrosMac) Ask(titulo, pregunta string) (string, bool, error) {
	salida, cancelo, err := c.correr(osaPreguntar(titulo, pregunta, c.iconoNormal()))
	if err != nil || cancelo {
		return "", false, err
	}
	// Acá sí se puede saber si aceptaron con el campo vacío —en Windows no—, y
	// eso no es cancelar: es que se apuraron. Vuelve como respuesta vacía y el
	// asistente le explica que el código son ocho números.
	v, ok := textoDevuelto(salida)
	return v, ok, nil
}

func (c cuadrosMac) Info(titulo, texto string) error {
	_, _, err := c.correr(osaMensaje(titulo, texto, c.iconoNormal()))
	return err
}

func (c cuadrosMac) Error(titulo, texto string) error {
	_, _, err := c.correr(osaMensaje(titulo, texto, "stop"))
	return err
}

func (c cuadrosMac) Confirm(titulo, texto string) (bool, error) {
	salida, cancelo, err := c.correr(osaConfirmar(titulo, texto, c.iconoNormal()))
	if err != nil || cancelo {
		return false, err
	}
	return botonDevuelto(salida) == botonSi, nil
}

// correr larga el osascript y separa las tres cosas que pueden pasar:
// contestó, la persona canceló —que para AppleScript es un error, el -128— o
// se rompió de verdad.
func (c cuadrosMac) correr(script string) (salida string, cancelo bool, err error) {
	args := make([]string, 0, 2*strings.Count(script, "\n")+2)
	for _, l := range strings.Split(script, "\n") {
		args = append(args, "-e", l)
	}
	cmd := exec.Command(c.osascript, args...)
	var afuera, errores strings.Builder
	cmd.Stdout = &afuera
	cmd.Stderr = &errores
	if err := cmd.Run(); err != nil {
		if esCancelacion(errores.String()) {
			return "", true, nil
		}
		var salioMal *exec.ExitError
		if msg := mensajeDeOsascript(errores.String()); errors.As(err, &salioMal) && msg != "" {
			return "", false, errors.New(msg)
		}
		return "", false, err
	}
	// Y por si alguna vez el botón de cancelar dejara de contar como error:
	// tocarlo tampoco es contestar.
	if botonDevuelto(afuera.String()) == botonCancelar {
		return "", true, nil
	}
	return afuera.String(), false, nil
}
