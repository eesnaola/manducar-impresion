//go:build darwin

package fiscal

import (
	"errors"

	"golang.org/x/sys/unix"
)

const (
	leerTermios  = unix.TIOCGETA
	ponerTermios = unix.TIOCSETA
	// hasarUSBEnEsteSistema: en una Mac el agente abre un puerto serie, pero
	// no sabe averiguar cuál es el de la Hasar: ahí va por red.
	hasarUSBEnEsteSistema = false
)

func ponerVelocidad(modo *unix.Termios, baudios int) error {
	if baudios == 0 {
		baudios = velocidadDeHasar
	}
	modo.Ispeed, modo.Ospeed = uint64(baudios), uint64(baudios)
	return nil
}

func puertoHasarDelSistema() (string, error) {
	return "", errors.New("desde una Mac la Hasar no se encuentra sola por USB: conectala por red")
}
