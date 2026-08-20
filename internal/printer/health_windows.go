//go:build windows

package printer

// El pulso a una impresora del sistema, del lado Windows. El spooler no
// cuenta mucho sin pelearse con los drivers, así que acá el semáforo es
// modesto: existe o no existe, y si su cola está en pausa.

import (
	"context"
)

// La librería no expone GetPrinter (donde vive PRINTER_STATUS_PAUSED), así
// que el semáforo acá es modesto de verdad: existe → OK, no existe → MISSING.
func probeSystem(ctx context.Context, name string) Health {
	if name == "" {
		return HealthMissing
	}
	names, err := listSystemCtx(ctx)
	if err != nil {
		return HealthSilent
	}
	for _, n := range names {
		if n == name {
			return HealthOK
		}
	}
	return HealthMissing
}
