package outbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eesnaola/manducar-impresion/internal/api"
)

func TestAddPersistsAndFlushRemovesAccepted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	o, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = o.Add(Entry{JobID: 1, Result: api.Result{OK: true, WroteSomething: true}, At: time.Now()})
	_ = o.Add(Entry{JobID: 2, Result: api.Result{OK: false, Error: "x"}, At: time.Now()})
	_ = o.Add(Entry{JobID: 3, Result: api.Result{OK: true, WroteSomething: true}, At: time.Now()})

	// Se reabre: lo que se agregó tiene que estar en disco.
	o2, _ := Open(path)
	if len(o2.Pending()) != 3 {
		t.Fatalf("pendientes tras reabrir: %d", len(o2.Pending()))
	}

	o2.Flush(context.Background(), func(_ context.Context, id int, _ api.Result) error {
		switch id {
		case 1:
			return nil
		case 2:
			return errors.New("red caída")
		default:
			return api.ErrUnknownJob
		}
	})
	p := o2.Pending()
	if len(p) != 1 || p[0].JobID != 2 {
		t.Fatalf("tenía que quedar sólo el 2: %+v", p)
	}
}

func TestOpenWithCorruptFileStartsEmptyAndUsable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	if err := os.WriteFile(path, []byte("esto no es json"), 0o600); err != nil {
		t.Fatal(err)
	}

	o, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Pending()) != 0 {
		t.Fatalf("un outbox corrupto tenía que arrancar vacío: %+v", o.Pending())
	}

	if err := o.Add(Entry{JobID: 9, Result: api.Result{OK: true, WroteSomething: true}, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if p := o.Pending(); len(p) != 1 || p[0].JobID != 9 {
		t.Fatalf("tenía que quedar el agregado tras el arranque vacío: %+v", p)
	}
}
