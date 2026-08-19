//go:build windows

package update

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// hidden lanza el binario nuevo sin abrir ninguna ventana. Hace falta cuando
// el agente está instalado a nivel usuario: ahí lo arrancó un .vbs, el
// proceso no tiene consola, y un hijo sin esto se abre una consola negra
// nueva en la pantalla del local. Desprendido, además, para que no se muera
// con el proceso que lo largó.
func hidden(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW | windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
}
