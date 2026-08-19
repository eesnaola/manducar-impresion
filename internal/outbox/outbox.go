// Package outbox guarda los resultados que todavía no se pudieron confirmar
// al servidor. Un resultado se agrega ANTES de intentar reportarlo, así una
// caída del proceso entre imprimir y confirmar no deja al servidor con un
// «enviado» eterno: al volver, se reporta.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/eesnaola/manducar-impresion/internal/api"
)

type Entry struct {
	JobID  int        `json:"jobId"`
	Result api.Result `json:"result"`
	At     time.Time  `json:"at"`
}

type Outbox struct {
	mu      sync.Mutex
	path    string
	entries []Entry
}

func Open(path string) (*Outbox, error) {
	o := &Outbox{path: path}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &o.entries); err != nil {
			// Un outbox roto no puede frenar la impresión: se arranca vacío.
			o.entries = nil
		}
	}
	return o, nil
}

func (o *Outbox) Add(e Entry) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.entries = append(o.entries, e)
	return o.save()
}

func (o *Outbox) Pending() []Entry {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]Entry(nil), o.entries...)
}

// Flush reporta en orden. Lo que el servidor aceptó —o dijo no conocer— se
// saca; lo que falla (por red u otro motivo) queda para la próxima, sin
// frenar el intento con las entradas siguientes.
func (o *Outbox) Flush(ctx context.Context, report func(ctx context.Context, jobID int, r api.Result) error) {
	for _, e := range o.Pending() {
		err := report(ctx, e.JobID, e.Result)
		if err == nil || errors.Is(err, api.ErrUnknownJob) {
			o.remove(e.JobID)
		}
	}
}

func (o *Outbox) remove(jobID int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	kept := o.entries[:0]
	for _, e := range o.entries {
		if e.JobID != jobID {
			kept = append(kept, e)
		}
	}
	o.entries = kept
	_ = o.save()
}

func (o *Outbox) save() error {
	raw, _ := json.Marshal(o.entries)
	tmp := o.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, o.path)
}
