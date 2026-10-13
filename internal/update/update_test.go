package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/eesnaola/manducar-impresion/internal/vigia"
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

// Vigilado, el que se actualiza no se relanza solo: sale con el código del
// vigía, que arranca el binario nuevo. Si lanzara uno, quedarían dos.
func TestRestartVigiladoSaleConElCodigoDelVigia(t *testing.T) {
	if os.Getenv("UPDATE_HIJO") == "1" {
		Restart("/no/importa")
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestRestartVigiladoSaleConElCodigoDelVigia")
	cmd.Env = append(os.Environ(), "UPDATE_HIJO=1", vigia.EnvVigilado+"=1")
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != vigia.CodigoActualizado {
		t.Fatalf("salió con %v; quiero el código %d", err, vigia.CodigoActualizado)
	}
}

// servirBinario levanta un servidor que sirve body y devuelve su URL y su
// SHA-256, listos para applyTo.
func servirBinario(t *testing.T, body []byte) (url, sha string) {
	t.Helper()
	sum := sha256.Sum256(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	t.Cleanup(srv.Close)
	return srv.URL, hex.EncodeToString(sum[:])
}

// En Windows el vigía sigue corriendo desde el `.old` de la primera
// actualización (queda con el binario viejo hasta el próximo inicio de
// sesión), así que ese archivo está en uso y no se puede borrar. La segunda
// actualización no puede fallar por eso: guarda el anterior con otro nombre.
// Acá el `.old` que no se puede borrar es un directorio con algo adentro, que
// falla igual en todos los sistemas.
func TestApplyConUnOldQueNoSePuedeBorrarGuardaElAnteriorConOtroNombre(t *testing.T) {
	url, sha := servirBinario(t, []byte("nuevo binario"))
	target := filepath.Join(t.TempDir(), "agente")
	if err := os.WriteFile(target, []byte("viejo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target+".old", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target+".old", "adentro"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := applyTo(context.Background(), url, sha, target); err != nil {
		t.Fatalf("con el .old en uso tenía que actualizar igual: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "nuevo binario" {
		t.Fatalf("el binario quedó con %q", got)
	}
	guardados, err := filepath.Glob(target + ".old-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(guardados) != 1 {
		t.Fatalf("tenía que quedar un solo .old-*, hay %v", guardados)
	}
	anterior, _ := os.ReadFile(guardados[0])
	if string(anterior) != "viejo" {
		t.Fatalf("%s quedó con %q y tenía que tener el binario anterior", guardados[0], anterior)
	}
}

// Los `.old-*` de actualizaciones anteriores no se acumulan: cada
// actualización borra los que haya (los que siguen en uso, quedan).
func TestApplyBorraLosOldConSufijoQueQuedaronDeAntes(t *testing.T) {
	url, sha := servirBinario(t, []byte("nuevo binario"))
	target := filepath.Join(t.TempDir(), "agente")
	if err := os.WriteFile(target, []byte("viejo"), 0o755); err != nil {
		t.Fatal(err)
	}
	sobrante := target + ".old-123"
	if err := os.WriteFile(sobrante, []byte("de antes"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := applyTo(context.Background(), url, sha, target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sobrante); !os.IsNotExist(err) {
		t.Fatalf("el %s de la actualización anterior tenía que borrarse (stat: %v)", sobrante, err)
	}
	old, err := os.ReadFile(target + ".old")
	if err != nil || string(old) != "viejo" {
		t.Fatalf("el .old tenía que tener el binario anterior: %q, %v", old, err)
	}
}

// Si el binario bajado no es el que dijo el servidor, no se toca nada: ni el
// target ni lo que quedó al lado de la vez anterior.
func TestApplyConHashEquivocadoNoTocaLosOldDeAntes(t *testing.T) {
	url, _ := servirBinario(t, []byte("nuevo binario"))
	otro := sha256.Sum256([]byte("otro binario"))
	target := filepath.Join(t.TempDir(), "agente")
	for ruta, contenido := range map[string]string{target: "viejo", target + ".old": "de antes", target + ".old-123": "de mucho antes"} {
		if err := os.WriteFile(ruta, []byte(contenido), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if err := applyTo(context.Background(), url, hex.EncodeToString(otro[:]), target); err == nil {
		t.Fatal("con el hash equivocado no puede aplicar")
	}
	for ruta, quiero := range map[string]string{target: "viejo", target + ".old": "de antes", target + ".old-123": "de mucho antes"} {
		if got, _ := os.ReadFile(ruta); string(got) != quiero {
			t.Fatalf("%s quedó con %q y tenía que seguir con %q", ruta, got, quiero)
		}
	}
}
