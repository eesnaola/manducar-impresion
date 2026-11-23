//go:build !windows

package fiscal

import "fmt"

// Fuera de Windows no hay librería de Epson que cargar: la que publica Epson
// para Linux pide cgo, y el agente se compila sin él.
func nuevoEpson(Controlador) (Driver, error) {
	return nil, fmt.Errorf("%w: las impresoras fiscales Epson se manejan con la librería de Epson, que el agente usa sólo en Windows", ErrNoDisponible)
}

func detectarEpson() LibreriaEpson {
	return LibreriaEpson{Motivo: "Las impresoras fiscales Epson se manejan desde una computadora con Windows."}
}
