//go:build !windows

package vigia

import "os/exec"

func ocultar(*exec.Cmd) {}
