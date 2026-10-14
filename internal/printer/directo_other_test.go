//go:build !windows

package printer

import (
	"context"
	"errors"
	"testing"

	"github.com/eesnaola/manducar-impresion/internal/api"
)

// En Linux y Mac una cola sin socket:// detrás (o sin CUPS, como en el CI)
// no tiene vía directa: no aplica.
func TestDirectoEnUnaColaSinRedNoAplica(t *testing.T) {
	_, err := AbrirDirecto(context.Background(), api.Target{Kind: "system", SystemName: "NoExisteTal"})

	if !errors.Is(err, ErrNoAplica) {
		t.Fatalf("err = %v; quería ErrNoAplica", err)
	}
}
