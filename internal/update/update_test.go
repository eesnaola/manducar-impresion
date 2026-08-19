package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyRefusesAWrongHash(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("nuevo binario")) }))
	defer srv.Close()
	target := filepath.Join(t.TempDir(), "agente")
	_ = os.WriteFile(target, []byte("viejo"), 0o755)

	err := applyTo(context.Background(), srv.URL, "00", target)
	if err == nil {
		t.Fatal("con hash equivocado no puede aplicar")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "viejo" {
		t.Fatal("tocó el binario")
	}
}

func TestApplyReplacesWithTheRightHash(t *testing.T) {
	body := []byte("nuevo binario")
	sum := sha256.Sum256(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer srv.Close()
	target := filepath.Join(t.TempDir(), "agente")
	_ = os.WriteFile(target, []byte("viejo"), 0o755)

	if err := applyTo(context.Background(), srv.URL, hex.EncodeToString(sum[:]), target); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != string(body) {
		t.Fatalf("quedó %q", got)
	}
}

// El binario anterior queda al lado como `.old`: si el nuevo no arranca,
// volver es renombrarlo a mano (lo dice el spec). Y tiene que ser el `.old`
// del target que se resolvió antes de reemplazar, no el que dijera
// `os.Executable()` después —en Linux, después del reemplazo, ese es el
// `.old` y la vuelta siguiente se bajaría el binario encima de sí mismo—.
func TestApplyKeepsThePreviousBinaryAsOld(t *testing.T) {
	body := []byte("nuevo binario")
	sum := sha256.Sum256(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer srv.Close()
	target := filepath.Join(t.TempDir(), "agente")
	if err := os.WriteFile(target, []byte("viejo"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := applyTo(context.Background(), srv.URL, hex.EncodeToString(sum[:]), target); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(target + ".old")
	if err != nil {
		t.Fatalf("el binario anterior tenía que quedar en %s.old: %v", target, err)
	}
	if string(old) != "viejo" {
		t.Fatalf("el .old quedó con %q y tenía que tener el binario anterior", old)
	}
	nuevo, _ := os.ReadFile(target)
	if string(nuevo) != string(body) {
		t.Fatalf("el binario nuevo quedó con %q", nuevo)
	}
}
