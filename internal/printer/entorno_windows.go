//go:build windows

package printer

import (
	"context"
	"fmt"
	"strings"
)

func describirSistema(_ context.Context, nombre string, e *Entorno) {
	info, err := infoDelSpooler(nombre)
	if err != nil {
		e.Nota = "no se pudo leer la cola: " + err.Error()
		return
	}
	e.Puerto = info.puerto
	e.Cola = fmt.Sprintf("RAW=%s directa=%s bidireccional=%s pausada=%s sin conexión=%s",
		siNo(info.atributos&printerAttributeRawOnly != 0), siNo(info.atributos&printerAttributeDirect != 0),
		siNo(info.atributos&printerAttributeEnableBidi != 0), siNo(info.pausada), siNo(info.sinConexion))
	e.Driver = info.driver
	if info.procesador != "" || info.tipoDeDatos != "" {
		e.Cola += fmt.Sprintf(" procesador=%s tipo=%s", info.procesador, info.tipoDeDatos)
	}
	if dir, ok := puertoTCPCrudo(info.puerto); ok {
		e.Direccion = dir
		e.Eleccion = "puerto TCP"
		return
	}
	// Los aparatos USB, sólo si la cola está en un puerto USB: en uno de
	// archivo o de red compartida no hay elección que contar.
	if !strings.HasPrefix(strings.ToUpper(primerPuerto(info.puerto)), "USB") {
		return
	}
	aparatos := listarAparatosUSB()
	for _, a := range aparatos {
		u := AparatoUSB{Ruta: a.ruta, Abre: existeUSB(a.ruta)}
		if a.conPuerto {
			u.Puerto = nombrePuerto(a.base, a.numero)
		}
		e.USB = append(e.USB, u)
	}
	_, e.Eleccion = elegirConMotivo(aparatos, primerPuerto(info.puerto), existeUSB)
}

func siNo(b bool) string {
	if b {
		return "sí"
	}
	return "no"
}
