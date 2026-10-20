//go:build !windows

package memoria

import (
	"os"
	"testing"
)

// Fuera de Windows no hay vigía y no se mide: la medida queda vacía y la caída
// sale sin la línea de memoria.
func TestFueraDeWindowsLaMedidaQuedaVacia(t *testing.T) {
	if m := Seguir(os.Getpid()).Medir(); !m.Vacia() {
		t.Errorf("medida = %+v, quiero vacía", m)
	}
}
