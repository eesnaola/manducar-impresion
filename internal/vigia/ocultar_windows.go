//go:build windows

package vigia

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// ocultar: instalado a nivel usuario no hay consola; que el hijo no abra una.
func ocultar(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
