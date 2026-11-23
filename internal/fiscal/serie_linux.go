//go:build linux

package fiscal

import (
	"fmt"

	"golang.org/x/sys/unix"
)

const (
	leerTermios  = unix.TCGETS
	ponerTermios = unix.TCSETS
	// hasarUSBEnEsteSistema: acá el agente encuentra solo a la Hasar enchufada por USB.
	hasarUSBEnEsteSistema = true
)

var baudiosDeLinux = map[int]uint32{
	1200: unix.B1200, 2400: unix.B2400, 4800: unix.B4800, 9600: unix.B9600, 19200: unix.B19200,
	38400: unix.B38400, 57600: unix.B57600, 115200: unix.B115200,
}

func ponerVelocidad(modo *unix.Termios, baudios int) error {
	if baudios == 0 {
		baudios = velocidadDeHasar
	}
	codigo, hay := baudiosDeLinux[baudios]
	if !hay {
		return fmt.Errorf("%d no es una velocidad de puerto serie que se maneje", baudios)
	}
	modo.Cflag |= codigo
	modo.Ispeed, modo.Ospeed = codigo, codigo
	return nil
}

// puertoHasarDelSistema busca, entre los puertos serie que salen de un USB,
// el del dispositivo con los identificadores de la Hasar.
func puertoHasarDelSistema() (string, error) {
	return puertoHasarEnSysfs("/sys/class/tty")
}
