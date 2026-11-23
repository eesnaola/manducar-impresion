package runner

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"sync"
	"time"
)

// El diario fiscal es lo que hace que un comprobante no se emita dos veces.
//
// Lo que un controlador fiscal emite queda en su memoria y no se borra. El
// servidor vuelve a entregar un pedido hasta que le llega el resultado, y ese
// resultado se puede perder (se corta internet, se reinicia la computadora).
// Por eso, antes de tocar el equipo, el pedido se anota acá, en el disco; y al
// terminar, cómo terminó. Un pedido que ya está anotado no vuelve al equipo:
// se contesta con lo anotado.

// diasDeDiario: cuánto se guarda un asiento ya entregado, por si el servidor
// insiste con un pedido viejo.
const diasDeDiario = 7

// topeDeDiario: más que esto no se guarda, pase lo que pase.
const topeDeDiario = 500

type asientoFiscal struct {
	ID       int       `json:"id"`
	Accion   string    `json:"accion"`
	Empezado time.Time `json:"empezado"`
	// Resultado: nil mientras no se sabe cómo terminó (o si el agente se
	// cerró en el medio).
	Resultado *resultadoFiscal `json:"resultado,omitempty"`
	// Entregado: el servidor ya recibió el resultado.
	Entregado bool `json:"entregado,omitempty"`
}

type diarioFiscal struct {
	mu       sync.Mutex
	path     string
	asientos []asientoFiscal
}

func abrirDiarioFiscal(path string) (*diarioFiscal, error) {
	d := &diarioFiscal{path: path}
	crudo, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(crudo) > 0 {
		if err := json.Unmarshal(crudo, &d.asientos); err != nil {
			// Se escribe entero y se renombra, así que no debería romperse
			// solo. Si pasa, se lo aparta y se sigue: dejar al local sin
			// poder facturar es peor que el riesgo de un pedido repetido.
			apartado := path + ".roto"
			_ = os.Rename(path, apartado)
			log.Printf("el diario fiscal no se pudo leer (%v): quedó apartado en %s y se empieza uno nuevo", err, apartado)
			d.asientos = nil
		}
	}
	return d, nil
}

func (d *diarioFiscal) buscar(id int) (asientoFiscal, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, a := range d.asientos {
		if a.ID == id {
			return a, true
		}
	}
	return asientoFiscal{}, false
}

// empezar anota que se le va a pedir eso al equipo. Si no se puede anotar,
// no se le pide.
func (d *diarioFiscal) empezar(id int, accion string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.asientos = append(d.asientos, asientoFiscal{ID: id, Accion: accion, Empezado: time.Now()})
	if err := d.guardar(); err != nil {
		d.asientos = d.asientos[:len(d.asientos)-1]
		return err
	}
	return nil
}

func (d *diarioFiscal) cambiar(id int, cambio func(*asientoFiscal)) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.asientos {
		if d.asientos[i].ID == id {
			cambio(&d.asientos[i])
		}
	}
	return d.guardar()
}

// terminar anota cómo terminó.
func (d *diarioFiscal) terminar(id int, res resultadoFiscal) error {
	return d.cambiar(id, func(a *asientoFiscal) { a.Resultado = &res })
}

// entregado anota que el servidor ya tiene el resultado.
func (d *diarioFiscal) entregado(id int) error {
	return d.cambiar(id, func(a *asientoFiscal) { a.Entregado = true })
}

// sinEntregar son los que terminaron y el servidor todavía no sabe cómo.
func (d *diarioFiscal) sinEntregar() []asientoFiscal {
	d.mu.Lock()
	defer d.mu.Unlock()
	var faltan []asientoFiscal
	for _, a := range d.asientos {
		if a.Resultado != nil && !a.Entregado {
			faltan = append(faltan, a)
		}
	}
	return faltan
}

// guardar escribe el diario entero y lo renombra, para que un corte no lo
// deje por la mitad. Los entregados viejos se van.
func (d *diarioFiscal) guardar() error {
	viejo := time.Now().AddDate(0, 0, -diasDeDiario)
	quedan := d.asientos[:0]
	for _, a := range d.asientos {
		if !a.Entregado || a.Empezado.After(viejo) {
			quedan = append(quedan, a)
		}
	}
	if sobran := len(quedan) - topeDeDiario; sobran > 0 {
		quedan = quedan[sobran:]
	}
	d.asientos = quedan
	crudo, err := json.Marshal(d.asientos)
	if err != nil {
		return err
	}
	tmp := d.path + ".tmp"
	if err := os.WriteFile(tmp, crudo, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, d.path)
}
