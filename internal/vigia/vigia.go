// Package vigia corre el agente como proceso hijo y lo vuelve a arrancar si se
// cae. Lo usa la instalación de usuario de Windows (el .vbs de la clave Run
// arranca «vigilar»), que no tiene un gestor de servicios que lo haga; en Mac,
// Linux y como servicio lo hacen launchd, systemd y el SCM.
package vigia

import (
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"time"

	"github.com/eesnaola/manducar-impresion/internal/memoria"
)

// CodigoActualizado: el hijo se actualizó y hay que arrancar el binario nuevo,
// que quedó en el mismo lugar (ver update.Restart).
const CodigoActualizado = 3

// EnvVigilado le dice al hijo que hay un vigía: no se relanza solo al
// actualizarse ni se pasa al vigía (ya está).
const EnvVigilado = "MANDUCAR_VIGILADO"

var (
	esperas = []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 15 * time.Second, time.Minute}
	// aguante: un hijo que corrió esto sin caerse vuelve la espera a 1 s.
	aguante = 10 * time.Minute
	// ventanaDeActualizacion: un hijo que salió con CodigoActualizado antes de
	// esto, justo después de otro que también lo hizo, es un bucle de
	// actualizaciones, no una actualización.
	ventanaDeActualizacion = 30 * time.Second
)

// Espera es cuánto esperar después de esa cantidad de caídas seguidas (o de
// actualizaciones seguidas y rápidas, ver Vigilar).
func Espera(fallos int) time.Duration {
	if fallos < 1 {
		return 0
	}
	if fallos > len(esperas) {
		return esperas[len(esperas)-1]
	}
	return esperas[fallos-1]
}

// Lanzador corre el hijo hasta que termina y devuelve su código de salida.
type Lanzador func(ctx context.Context) (codigo int, err error)

// Vigilar corre el hijo una y otra vez. Termina cuando ctx se cancela o el
// hijo sale con 0, que es que alguien lo mandó a parar. anotar recibe cada
// salida que no fue normal, con la hora en que pasó; dormir espera entre
// intentos (Dormir, o uno de mentira en los tests).
//
// La salida se anota recién después de la espera, justo antes de volver a
// arrancarlo. Un «taskkill /F» (vincular, desinstalar) puede matar al hijo un
// instante antes que al vigía: anotada enseguida, quedaría una caída de
// mentira para el próximo arranque; así, el vigía muere esperando y no anota
// nada.
func Vigilar(ctx context.Context, lanzar Lanzador, anotar func(codigo int, cuando time.Time), dormir func(context.Context, time.Duration) bool) error {
	fallos := 0
	// Actualizaciones seguidas y rápidas: si el binario nuevo vuelve a salir con
	// CodigoActualizado enseguida (un chequeo que siempre ve una versión más
	// nueva), el vigía no puede girar a toda velocidad. La primera es instantánea;
	// desde la segunda seguida se espera, sin anotarla como caída.
	vinoDeActualizar := false
	rapidas := 0
	for {
		inicio := time.Now()
		codigo, err := lanzar(ctx)
		salio := time.Now()
		if ctx.Err() != nil {
			return nil
		}
		switch {
		case err != nil:
			// No se pudo ni arrancar (el binario a medio reemplazar): se espera
			// como después de una caída, pero no se anota —el agente no corrió,
			// no hay nada de él que contar—; queda en el log del vigía.
		case codigo == 0:
			return nil
		case codigo == CodigoActualizado:
			fallos = 0
			if vinoDeActualizar && time.Since(inicio) < ventanaDeActualizacion {
				rapidas++
				d := Espera(rapidas)
				log.Printf("vigía: el agente se actualizó otra vez enseguida (%d seguidas); espero %s", rapidas+1, d)
				if !dormir(ctx, d) {
					return nil
				}
			} else {
				rapidas = 0
				log.Println("vigía: el agente se actualizó; arranco el binario nuevo")
			}
			vinoDeActualizar = true
			continue
		}
		vinoDeActualizar, rapidas = false, 0
		if salio.Sub(inicio) >= aguante {
			fallos = 0
		}
		fallos++
		d := Espera(fallos)
		if err != nil {
			log.Printf("vigía: no se pudo arrancar el agente (%v); pruebo de nuevo en %s", err, d)
		} else {
			log.Printf("vigía: el agente terminó con código %d; lo vuelvo a arrancar en %s", codigo, d)
		}
		if !dormir(ctx, d) {
			return nil
		}
		if err == nil {
			anotar(codigo, salio)
		}
	}
}

// Dormir espera d o hasta que ctx se cancele; dice si llegó a esperar todo.
func Dormir(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Proceso es el lanzador de verdad: el ejecutable con esos argumentos y la
// marca de vigilado. Lo que el hijo escriba en su salida normal va al log del
// vigía; su salida de error, a caidas (impresion.crash). Ahí escribe Go el
// motivo de una caída, y todo: también lo que escribe antes de darse por
// caído, como el «fatal error: …» de un error fatal o el encabezado de una
// excepción de Windows, que SetCrashOutput no copia.
//
// Cuando el hijo termina, mide la memoria —la de la computadora en ese momento
// y lo más que llegó a usar el hijo— y se la pasa a alTerminar, que la guarda
// para anotarla con la caída: medida después de la espera ya no diría cómo
// estaba la computadora cuando se cayó.
func Proceso(exe string, args []string, caidas string, alTerminar func(memoria.Medida)) Lanzador {
	return func(ctx context.Context) (int, error) {
		c := exec.CommandContext(ctx, exe, args...)
		c.Env = append(os.Environ(), EnvVigilado+"=1")
		c.Stdout = log.Writer()
		// Se abre en cada vuelta y para agregar al final: el agente que
		// arranca lo vacía después de contar la caída (lo trunca con este
		// abierto), y lo siguiente que escriba el hijo va al principio.
		if f, err := os.OpenFile(caidas, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err != nil {
			log.Println("vigía: no se pudo abrir el archivo de caídas (la salida de error del agente va al log):", err)
			c.Stderr = log.Writer()
		} else {
			defer f.Close()
			c.Stderr = f
		}
		ocultar(c)
		if err := c.Start(); err != nil {
			return 0, err
		}
		seguimiento := memoria.Seguir(c.Process.Pid)
		err := c.Wait()
		if m := seguimiento.Medir(); alTerminar != nil {
			alTerminar(m)
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), nil
		}
		return 0, err
	}
}
