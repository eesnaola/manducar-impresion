//go:build windows

package main

// Los cuadros de Windows: el Windows Script Host. Se escribe un .vbs de usar y
// tirar y se lo corre con wscript.exe —el host que no abre consola; cscript,
// el otro, abriría justo la ventana negra que estamos sacando—.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/windows"
)

func nuevosCuadros() (dialogs, error) {
	exe := wscriptExe()
	if _, err := os.Stat(exe); err != nil {
		return nil, fmt.Errorf("no encontré %s, que es lo que muestra los cuadros de diálogo: %w", exe, err)
	}
	return cuadrosWindows{wscript: exe}, nil
}

// soltarConsola suelta la consola que Windows le abrió al doble clic. Es lo
// que hace que no quede una ventana negra vacía atrás de los cuadros toda la
// noche. Se llama sólo cuando ya hay con qué preguntar; a partir de acá, lo
// que se escriba por la salida no lo lee nadie.
func soltarConsola() {
	// FreeConsole no está en golang.org/x/sys/windows, así que se la pide a
	// kernel32 a mano. Si falla, la consola queda: es feo, no es grave.
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("FreeConsole")
	_, _, _ = proc.Call()
}

// wscriptExe: la ruta entera y no «wscript.exe» a secas, para no depender de
// lo que tenga en el PATH la computadora del local.
func wscriptExe() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "wscript.exe")
}

type cuadrosWindows struct{ wscript string }

func (c cuadrosWindows) Ask(titulo, pregunta string) (string, bool, error) {
	r, err := c.correr(vbsPreguntar(titulo, pregunta), true)
	if err != nil {
		return "", false, err
	}
	// Vacío es «canceló»: InputBox contesta lo mismo si tocaron Cancelar que
	// si aceptaron sin escribir nada, y no hay con qué distinguirlos.
	return r, r != "", nil
}

func (c cuadrosWindows) Info(titulo, texto string) error {
	_, err := c.correr(vbsMensaje(titulo, texto, vbInformation), false)
	return err
}

func (c cuadrosWindows) Error(titulo, texto string) error {
	_, err := c.correr(vbsMensaje(titulo, texto, vbCritical), false)
	return err
}

func (c cuadrosWindows) Confirm(titulo, texto string) (bool, error) {
	r, err := c.correr(vbsConfirmar(titulo, texto), true)
	if err != nil {
		return false, err
	}
	n, err := strconv.Atoi(r)
	return err == nil && n == vbYes, nil
}

// correr escribe el .vbs, lo corre y devuelve lo que haya quedado en el
// archivo de respuesta. Los dos temporales se borran siempre, salga como
// salga. No se le esconde la ventana al proceso: la ventana ES el cuadro.
func (c cuadrosWindows) correr(script string, esperaRespuesta bool) (string, error) {
	vbs, err := os.CreateTemp("", "manducar-*.vbs")
	if err != nil {
		return "", fmt.Errorf("no se pudo preparar el cuadro de diálogo: %w", err)
	}
	defer os.Remove(vbs.Name())
	if _, err := vbs.Write(utf16LE(script)); err != nil {
		vbs.Close()
		return "", fmt.Errorf("no se pudo preparar el cuadro de diálogo: %w", err)
	}
	if err := vbs.Close(); err != nil {
		return "", fmt.Errorf("no se pudo preparar el cuadro de diálogo: %w", err)
	}

	args := []string{"//nologo", vbs.Name()}
	var respuesta string
	if esperaRespuesta {
		f, err := os.CreateTemp("", "manducar-*.txt")
		if err != nil {
			return "", fmt.Errorf("no se pudo preparar el cuadro de diálogo: %w", err)
		}
		respuesta = f.Name()
		f.Close()
		defer os.Remove(respuesta)
		args = append(args, respuesta)
	}
	// Run espera a que se cierre el cuadro, que es exactamente lo que hace
	// falta: hasta entonces no hay nada que leer.
	if err := exec.Command(c.wscript, args...).Run(); err != nil {
		return "", fmt.Errorf("no se pudo mostrar el cuadro de diálogo: %w", err)
	}
	if respuesta == "" {
		return "", nil
	}
	b, err := os.ReadFile(respuesta)
	if err != nil {
		return "", fmt.Errorf("no se pudo leer lo que contestaste: %w", err)
	}
	return textoDelArchivo(b), nil
}
