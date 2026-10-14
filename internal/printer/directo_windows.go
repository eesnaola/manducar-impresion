//go:build windows

package printer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

// abrirDirectoSistema: una cola de Windows tiene detrás un puerto TCP crudo
// (Standard TCP/IP) o un aparato USB; a ésos se les puede hablar sin la cola.
// Un puerto de archivo, uno de red compartida o un USB que no está en el
// registro no aplican.
func abrirDirectoSistema(ctx context.Context, nombre string) (Directo, error) {
	info, err := infoDelSpooler(nombre)
	if err != nil {
		return nil, fmt.Errorf("%w: no se pudo leer la cola de %q (%v)", ErrNoAplica, nombre, err)
	}
	if dir, ok := puertoTCPCrudo(info.puerto); ok {
		return abrirTCP(ctx, dir)
	}
	rutas := aparatosUSB(info.puerto)
	if len(rutas) == 0 {
		return nil, fmt.Errorf("%w: el puerto %s no tiene detrás un aparato USB ni un puerto TCP", ErrNoAplica, info.puerto)
	}
	h, ocupada, ok := abrirUSB(rutas)
	if !ok {
		if ocupada {
			return nil, errors.New("el aparato USB lo tiene abierto otro programa")
		}
		return nil, errors.New("el aparato USB no se deja abrir: ¿está apagada o desenchufada?")
	}
	return &usbDirecto{h: h}, nil
}

type usbDirecto struct{ h windows.Handle }

func (u *usbDirecto) Escribir(b []byte, plazo time.Duration) error {
	n, ok := usbOp(u.h, true, b, plazo)
	if !ok || n != len(b) {
		return fmt.Errorf("el aparato USB tomó %d de %d bytes en %s", n, len(b), plazo)
	}
	return nil
}

func (u *usbDirecto) Leer(plazo time.Duration) []byte { return usbLeer(u.h, plazo) }

func (u *usbDirecto) Close() error { return windows.CloseHandle(u.h) }
