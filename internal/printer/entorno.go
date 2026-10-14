package printer

import (
	"context"

	"github.com/eesnaola/manducar-impresion/internal/api"
)

// Entorno es lo que el diagnóstico cuenta de la impresora y de la
// computadora (spec, «Lo que devuelve»): por dónde va, con qué driver, cómo
// está configurada la cola y qué aparatos USB encontró.
type Entorno struct {
	Impresora string       `json:"impresora"`
	Tipo      string       `json:"tipo"`
	Direccion string       `json:"direccion,omitempty"`
	Puerto    string       `json:"puerto,omitempty"`
	Driver    string       `json:"driver,omitempty"`
	Cola      string       `json:"cola,omitempty"`
	USB       []AparatoUSB `json:"usb,omitempty"`
	Eleccion  string       `json:"eleccion,omitempty"`
	Nota      string       `json:"nota,omitempty"`
}

// AparatoUSB es una entrada de la clase de impresoras USB de Windows.
type AparatoUSB struct {
	Ruta   string `json:"ruta"`
	Puerto string `json:"puerto,omitempty"`
	Abre   bool   `json:"abre"`
}

// DescribirEntorno junta lo que se puede saber de la impresora sin mandarle nada.
func DescribirEntorno(ctx context.Context, t api.Target) Entorno {
	e := Entorno{Tipo: t.Kind}
	if t.Kind == "network" {
		e.Impresora = t.Address
		e.Direccion = t.Address
		return e
	}
	e.Impresora = t.SystemName
	describirSistema(ctx, t.SystemName, &e)
	return e
}
