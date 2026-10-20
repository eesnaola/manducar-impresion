//go:build !windows

package memoria

// Seguimiento no sigue nada fuera de Windows: ahí no hay vigía.
type Seguimiento struct{}

func Seguir(int) *Seguimiento { return &Seguimiento{} }

func (*Seguimiento) Medir() Medida { return Medida{} }
