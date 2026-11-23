//go:build linux || darwin

package fiscal

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// serieUnix es un puerto serie abierto en Linux o en una Mac (el USB de una
// Hasar aparece como /dev/ttyACM0).
type serieUnix struct{ fd int }

// abrirSerieDelSistema abre el puerto sin que el sistema toque lo que pasa
// por él (ni ecos, ni saltos de línea, ni control de flujo: son bytes), a 8
// bits sin paridad, y de modo que leer espere una décima de segundo y
// devuelva lo que haya.
func abrirSerieDelSistema(nombre string, velocidad int) (puertoSerie, error) {
	// Sin bloquear al abrir: un puerto sin nadie del otro lado espera para siempre.
	fd, err := unix.Open(nombre, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", nombre, err)
	}
	modo, err := unix.IoctlGetTermios(fd, leerTermios)
	if err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("%s no es un puerto serie: %w", nombre, err)
	}
	modo.Iflag, modo.Oflag, modo.Lflag = 0, 0, 0
	modo.Cflag = unix.CS8 | unix.CREAD | unix.CLOCAL
	modo.Cc[unix.VMIN], modo.Cc[unix.VTIME] = 0, 1
	if err := ponerVelocidad(modo, velocidad); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if err := unix.IoctlSetTermios(fd, ponerTermios, modo); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("no se pudo configurar %s: %w", nombre, err)
	}
	// Ya abierto, que leer espere lo que dice el puerto y no vuelva al instante.
	if err := unix.SetNonblock(fd, false); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return &serieUnix{fd: fd}, nil
}

func (s *serieUnix) Read(b []byte) (int, error) {
	n, err := unix.Read(s.fd, b)
	if n < 0 {
		n = 0
	}
	if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
		return n, nil
	}
	return n, err
}

func (s *serieUnix) Write(b []byte) (int, error) {
	escrito := 0
	for escrito < len(b) {
		n, err := unix.Write(s.fd, b[escrito:])
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
			continue
		}
		if err != nil {
			return escrito, err
		}
		escrito += n
	}
	return escrito, nil
}

func (s *serieUnix) Close() error { return unix.Close(s.fd) }
