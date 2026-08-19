//go:build !windows

package update

import "os/exec"

// Fuera de Windows el reinicio es un exec() y nadie larga procesos hijos.
func hidden(*exec.Cmd) {}
