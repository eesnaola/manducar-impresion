//go:build !windows

package fiscal

import (
	"errors"
	"testing"
)

// Fuera de Windows el panel no tiene que ofrecer Epson, y tiene que poder
// decir por qué.
func TestFueraDeWindowsEpsonNoEstaDisponibleYDicePorQue(t *testing.T) {
	lib := Detectar().Epson
	if lib.Disponible || lib.Motivo == "" {
		t.Errorf("quiero no disponible y con motivo, vino %+v", lib)
	}
	if _, err := Abrir(Controlador{Marca: "epson", Conexion: "usb"}); !errors.Is(err, ErrNoDisponible) {
		t.Errorf("abrir una Epson acá tenía que dar ErrNoDisponible, vino: %v", err)
	}
}
