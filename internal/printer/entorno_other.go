//go:build !windows

package printer

import "context"

func describirSistema(ctx context.Context, nombre string, e *Entorno) {
	estado, uri, ok := ippPrinterInfo(ctx, nombre)
	if !ok {
		e.Nota = "CUPS no contestó por IPP"
		return
	}
	e.Puerto = uri
	e.Cola = "estado " + estado
	if dir, ok := socketHostPort(uri); ok {
		e.Direccion = dir
	}
}
