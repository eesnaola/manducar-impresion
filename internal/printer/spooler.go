package printer

// Lo que comparten CUPS y el spooler de Windows: entregar el papel al sistema
// no es imprimirlo. Acá vive la espera —¿ya salió de la cola del sistema?— y
// sus perillas; cada sistema pone su manera de preguntar.

import (
	"context"
	"time"
)

// Cuánto se espera a que el spooler saque el papel antes de avisar que quedó
// trabado, y cada cuánto se le pregunta. Diez segundos alcanzan de sobra para
// una térmica; una impresora que a los diez segundos sigue con el trabajo en
// la cola está apagada, trabada o pausada.
const (
	spoolWait = 10 * time.Second
	spoolPoll = time.Second
)

// sigueEnCola pregunta cada tanto si el trabajo sigue en la cola del sistema,
// hasta que salga o se acabe la espera. Devuelve si quedó trabado; un error de
// la consulta corta la verificación (mejor confiar que inventar un problema).
func sigueEnCola(ctx context.Context, total, cada time.Duration, consulta func() (bool, error)) (bool, error) {
	vence := time.Now().Add(total)
	for {
		enCola, err := consulta()
		if err != nil {
			return false, err
		}
		if !enCola {
			return false, nil
		}
		if time.Now().After(vence) {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return true, nil
		case <-time.After(cada):
		}
	}
}
