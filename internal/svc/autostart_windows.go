//go:build windows

package svc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// En Windows no hay servicios «de usuario»: el Service Control Manager pide
// administrador para instalar cualquier servicio, y kardianos/service no
// tiene con qué hacerlo de otra forma. Sin administrador, lo que arranca solo
// al entrar es lo que esté anotado en la clave Run del usuario.
const runKeyAutostart = true

const (
	runKeyPath  = `Software\Microsoft\Windows\CurrentVersion\Run`
	runKeyValue = "ManducarImpresion"
	exeName     = "manducar-impresion.exe"
)

// launcherPath es el .vbs, al lado del ejecutable: los dos juntos, así sacar
// el agente es borrar una carpeta.
func launcherPath(exe string) string {
	return filepath.Join(filepath.Dir(exe), launcherName)
}

// wscriptPath: la ruta entera y no «wscript.exe» a secas, para no depender de
// lo que tenga en el PATH la computadora del local.
func wscriptPath() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "wscript.exe")
}

// autostartInstall deja el lanzador escrito, lo anota en la clave Run del
// usuario —HKCU, que se escribe sin permisos— y lo arranca ya.
func autostartInstall(exe, cfgPath string) error {
	vbs := launcherPath(exe)
	if err := os.WriteFile(vbs, []byte(vbsLauncher(exe, cfgPath, "correr", "--log-archivo")), 0o644); err != nil {
		return fmt.Errorf("escribir %s: %w", vbs, err)
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.SetStringValue(runKeyValue, runKeyCommand(wscriptPath(), vbs)); err != nil {
		return err
	}
	return startHidden(vbs)
}

// autostartUninstall saca primero lo que hace que vuelva —la clave y el
// lanzador— y recién después mata lo que esté corriendo: si algo falla en el
// medio, que quede sin arrancar y no al revés.
func autostartUninstall() error {
	// Primero se lee adónde apunta lo anotado: es la única fuente honesta de
	// dónde quedó el lanzador.
	anotado, habia := runValue()
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err == nil {
		if err := k.DeleteValue(runKeyValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
			k.Close()
			return err
		}
		k.Close()
	} else if !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	vbs := vbsFromRunValue(anotado)
	if vbs == "" {
		vbs = launcherPath(installPath(true)) // sin nada anotado, el de siempre
	}
	if err := os.Remove(vbs); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("borrar %s: %w", vbs, err)
	}
	stopRunning()
	if !habia {
		return errNotInstalled
	}
	return nil
}

// autostartRestart es lo que hace «vincular» cuando la computadora ya estaba
// instalada: se lo mata y se lo vuelve a lanzar, que es como toma el token
// nuevo.
func autostartRestart() error {
	anotado, ya := runValue()
	if !ya {
		return errNotInstalled
	}
	vbs := vbsFromRunValue(anotado)
	if vbs == "" {
		vbs = launcherPath(installPath(true))
	}
	stopRunning()
	return startHidden(vbs)
}

func autostartInstalled() (bool, error) {
	_, ya := runValue()
	return ya, nil
}

// runValue es lo anotado en la clave Run, y si estaba. Una clave que no se
// puede ni abrir se cuenta como «no está»: es lo mismo para el que pregunta,
// y no hay nada que hacer al respecto.
func runValue() (string, bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runKeyValue)
	if err != nil {
		return "", false
	}
	return v, true
}

// startHidden lanza el .vbs sin ventana y sin quedarse esperándolo. Va
// desprendido de la consola a propósito: el que corre `instalar` desde
// PowerShell cierra la ventana y el agente tiene que seguir imprimiendo.
func startHidden(vbs string) error {
	c := exec.Command(wscriptPath(), vbs)
	c.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
	if err := c.Start(); err != nil {
		return err
	}
	return c.Process.Release()
}

// stopRunning mata al agente que esté corriendo. El filtro por PID es para no
// matarse a sí mismo: el que corre `desinstalar` suele ser el mismo .exe
// bajado, con el mismo nombre. Si no había ninguno, taskkill devuelve error y
// no pasa nada.
//
// Es una muerte fea: `/F` no le da al agente el rato que sí le dan systemd y
// launchd para terminar de imprimir y de confirmar. En Windows a nivel usuario
// no hay gestor que mande una señal linda, y el runner no escucha ninguna
// (`correr` sale por Ctrl-C, y acá no hay consola). No se pierde nada igual:
// cada resultado va al outbox ANTES de reportarse, así que lo que quedó sin
// confirmar se reporta cuando el agente vuelve a arrancar.
func stopRunning() {
	c := exec.Command("taskkill", "/IM", exeName, "/F", "/FI", fmt.Sprintf("PID ne %d", os.Getpid()))
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = c.Run()
}

// checkPrivileges se planta antes de empezar: instalar el servicio del
// sistema a medias es peor que no empezar.
func checkPrivileges(system bool) error {
	elevada := windows.GetCurrentProcessToken().IsElevated()
	if system {
		if elevada {
			return nil
		}
		return errors.New("«--sistema» es el servicio del sistema, el de toda la computadora, y eso lo toca un administrador: cerrá esta ventana, abrí PowerShell con «Ejecutar como administrador» y repetí el comando. Sin --sistema no hace falta ningún permiso")
	}
	if elevada {
		// No se planta: en Windows el que abre PowerShell como administrador
		// suele ser el mismo usuario, y entonces la clave Run es la suya. Pero
		// si la terminal es de OTRA cuenta, el agente va a arrancar cuando
		// entre esa otra, no la del mostrador. Vale avisarlo.
		fmt.Fprintln(os.Stderr, "ojo: esta terminal es de administrador. El agente va a arrancar con el usuario de ESTA terminal; si el que atiende el mostrador es otro, cerrala y corré el comando en una terminal común de esa cuenta.")
	}
	return nil
}
