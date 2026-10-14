//go:build !windows

package printer

import (
	"context"
	"fmt"
)

// Fuera de Windows no hay aparato USB al que hablarle sin CUPS: sólo el
// socket:// de una cola que apunta a una térmica de red.
func abrirDirectoSistema(ctx context.Context, nombre string) (Directo, error) {
	if _, uri, ok := ippPrinterInfo(ctx, nombre); ok {
		if dir, ok := socketHostPort(uri); ok {
			return abrirTCP(ctx, dir)
		}
	}
	return nil, fmt.Errorf("%w: la cola %q no apunta a una impresora de red", ErrNoAplica, nombre)
}
