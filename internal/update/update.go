// Package update baja el binario nuevo, verifica el sha256 que dijo el
// servidor y se reemplaza a sí mismo. El hash es la firma mientras no haya
// certificado de código.
package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/kardianos/service"
	"github.com/minio/selfupdate"

	"github.com/eesnaola/manducar-impresion/internal/vigia"
)

// Apply baja el binario de url, comprueba que su SHA-256 sea sha256hex y se
// reemplaza a sí mismo. Devuelve la ruta del ejecutable —resuelta ANTES de
// reemplazarlo— para que el que reinicie arranque el binario nuevo: en Linux,
// `os.Executable()` lee /proc/self/exe, y después del reemplazo ese enlace
// apunta al `.old`; volver a preguntar sería revivir la versión vieja y
// bajarse la nueva de nuevo, para siempre.
func Apply(ctx context.Context, url, sha256hex string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	// Si el ejecutable está detrás de un symlink, se reemplaza el archivo de
	// verdad y no el enlace.
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	return self, applyTo(ctx, url, sha256hex, self)
}

func applyTo(ctx context.Context, url, sha256hex, target string) error {
	want, err := hex.DecodeString(sha256hex)
	if err != nil || len(want) != sha256.Size {
		return errors.New("el servidor no mandó un sha256 válido")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	res, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("bajar %s: HTTP %d", url, res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 100<<20))
	if err != nil {
		return err
	}
	// selfupdate verifica el checksum antes de tocar nada; si no coincide,
	// devuelve error y el binario queda como estaba. El anterior queda al
	// lado como `.old`: si el nuevo no arranca, volver es renombrarlo. Con un
	// binario que no pasa la verificación no se toca nada de lo que hay al
	// lado: ni siquiera el `.old` de la vez anterior.
	oldPath := target + ".old"
	if sum := sha256.Sum256(body); bytes.Equal(sum[:], want) {
		oldPath = dondeGuardarElAnterior(target)
	}
	return selfupdate.Apply(bytes.NewReader(body), selfupdate.Options{
		TargetPath:  target,
		Checksum:    want,
		OldSavePath: oldPath,
	})
}

// dondeGuardarElAnterior dice dónde queda el binario que se va a reemplazar.
// Casi siempre es `target.old`. Pero en Windows, con el agente vigilado, el
// vigía sigue corriendo desde el `.old` de la primera actualización (queda
// con el binario viejo hasta el próximo inicio de sesión): ese archivo está
// en uso, no se puede borrar ni pisar, y selfupdate fallaría en cada vuelta
// —una sola actualización por sesión—. Entonces, si el `.old` no se puede
// sacar del medio, el anterior se guarda como `target.old-<nanosegundos>`.
// Los `.old-*` de actualizaciones pasadas se borran antes, a ver si ya se
// pueden: el que siga en uso se queda, no pasa nada.
func dondeGuardarElAnterior(target string) string {
	borrarOldsConSufijo(target)
	old := target + ".old"
	if err := os.Remove(old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Sprintf("%s.old-%d", target, time.Now().UnixNano())
	}
	return old
}

// borrarOldsConSufijo borra, sin quejarse, los `target.old-*` que dejaron
// actualizaciones anteriores. Se recorre la carpeta en vez de un glob porque
// la ruta puede traer corchetes o asteriscos (un nombre de usuario, una
// carpeta) y el glob los tomaría por patrón.
func borrarOldsConSufijo(target string) {
	dir, base := filepath.Dir(target), filepath.Base(target)
	entradas, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entradas {
		if strings.HasPrefix(e.Name(), base+".old-") {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// Restart vuelve a arrancar el agente con el binario nuevo que quedó en
// target. Como servicio, salir con 0 alcanza: el gestor de servicios lo
// levanta de nuevo (ver `spec()` en internal/svc, `Restart`/`KeepAlive`). En
// primer plano hay que arrancar el binario de vuelta a mano.
func Restart(target string) {
	// Vigilado, el vigía arranca el binario nuevo: lanzarlo acá dejaría dos
	// agentes peleándose los trabajos.
	if os.Getenv(vigia.EnvVigilado) != "" {
		os.Exit(vigia.CodigoActualizado)
	}
	if runtime.GOOS == "windows" {
		// Windows no tiene exec(): en primer plano se larga un hijo. Como
		// servicio NO: el SCM ya lo va a levantar, y el hijo quedaría
		// huérfano peleándole los trabajos al servicio.
		if service.Interactive() {
			c := exec.Command(target, os.Args[1:]...)
			hidden(c) // instalado a nivel usuario no hay consola: que no se abra una
			if err := c.Start(); err != nil {
				log.Println("no se pudo arrancar el binario nuevo:", err)
			}
		}
		os.Exit(0)
	}
	if err := syscall.Exec(target, os.Args, os.Environ()); err != nil {
		log.Println("no se pudo arrancar el binario nuevo:", err)
	}
	os.Exit(0)
}
