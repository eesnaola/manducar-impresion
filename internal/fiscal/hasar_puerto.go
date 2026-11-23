package fiscal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"regexp"
	"strings"
	"time"
)

// puertoSerie es un puerto serie abierto. Leer devuelve lo que haya llegado
// y, si en un rato corto no llegó nada, cero bytes y ningún error: así quien
// lee puede mirar el reloj entre un intento y otro.
type puertoSerie interface {
	io.ReadWriteCloser
}

// Las perillas del puerto, en variables para que los tests pongan un equipo
// de mentira del otro lado del cable.
var (
	// abrirPuertoSerie abre ese puerto («COM3», «/dev/ttyACM0»).
	abrirPuertoSerie = abrirSerieDelSistema
	// puertoDeHasarUSB dice en qué puerto quedó la Hasar enchufada por USB.
	puertoDeHasarUSB = func() (string, error) {
		if puertoHasarDePrueba != "" {
			return puertoHasarDePrueba, nil
		}
		return puertoHasarDelSistema()
	}
	// silencioDeHasar: cuánto se aguanta sin que llegue nada. Mientras
	// trabaja, el equipo avisa cada 400 ms: tres segundos callado es que no
	// está.
	silencioDeHasar = 3 * time.Second
	// secuenciaInicial: en qué número de secuencia arranca una conversación.
	secuenciaInicial = func() byte { return byte(0x20 + rand.Intn(0x60)) }
	// puertoHasarDePrueba: un puerto fijo para «la Hasar enchufada por USB»,
	// que se pone al compilar (-X) para probar contra el simulador, que
	// atiende en un pty. Vacío en el agente que se publica.
	puertoHasarDePrueba = ""
)

// intentosDeHasar: cuántas veces se manda un paquete que el equipo dice no
// haber entendido, o se pide una respuesta que llegó dañada.
const intentosDeHasar = 3

var esPuertoCOM = regexp.MustCompile(`(?i)^COM[0-9]{1,3}$`)

// puertoDeConexion dice si esa conexión es un puerto y no una dirección de
// red: «usb» (la que esté enchufada), un COM de Windows o un dispositivo.
func puertoDeConexion(conexion string) (puerto string, es bool) {
	c := strings.TrimSpace(conexion)
	switch {
	case strings.EqualFold(c, "usb"):
		return "usb", true
	case esPuertoCOM.MatchString(c):
		return strings.ToUpper(c), true
	case strings.HasPrefix(c, "/dev/"):
		return c, true
	}
	return "", false
}

// hasarPuerto le habla a una Hasar por su puerto serie —el USB se ve como
// uno— con el protocolo empaquetado. El puerto se abre con el primer comando
// de una operación y se suelta cuando termina: nadie más lo puede usar
// mientras tanto, y un equipo fiscal no atiende dos conversaciones.
type hasarPuerto struct {
	equipo    string
	conexion  string
	velocidad int
	puerto    puertoSerie
	sn        byte
}

func (p *hasarPuerto) comando(ctx context.Context, nombre string, campos any, salida any) error {
	armado, err := armarComando(nombre, campos)
	if err != nil {
		return err
	}
	if p.puerto == nil {
		if err := p.abrir(ctx); err != nil {
			return err
		}
	}
	respuesta, err := p.intercambiar(ctx, armado)
	if err != nil {
		return fmt.Errorf("no se pudo hablar con la impresora fiscal por %s: %w", p.equipo, err)
	}
	leido, err := leerRespuesta(nombre, respuesta)
	if err != nil {
		return err
	}
	if salida == nil {
		return nil
	}
	if err := json.Unmarshal(leido, salida); err != nil {
		return fmt.Errorf("no se entendió la respuesta de la impresora fiscal a «%s»: %w", nombre, err)
	}
	return nil
}

func (p *hasarPuerto) soltar() {
	if p.puerto != nil {
		_ = p.puerto.Close()
		p.puerto = nil
	}
}

// abrir abre el puerto y se presenta con una consulta de versión.
//
// La consulta no es de cortesía. El equipo, si le llega dos veces seguidas el
// mismo número de secuencia, repite la respuesta anterior sin hacer nada (es
// lo que permite reenviar un paquete). Una conversación nueva que arrancara
// justo en el número donde terminó la anterior recibiría, por su primer
// comando, la respuesta vieja: un cierre que no se hizo, dado por hecho. Con
// la consulta adelante, lo que se puede repetir es la consulta.
func (p *hasarPuerto) abrir(ctx context.Context) error {
	nombre := p.conexion
	if nombre == "usb" {
		encontrado, err := puertoDeHasarUSB()
		if err != nil {
			return fmt.Errorf("no encontré una impresora fiscal Hasar enchufada por USB a esta computadora: %w", err)
		}
		nombre = encontrado
	}
	puerto, err := abrirPuertoSerie(nombre, p.velocidad)
	if err != nil {
		return fmt.Errorf("no se pudo abrir %s para hablarle a la impresora fiscal: %w", p.equipo, err)
	}
	p.puerto, p.sn = puerto, secuenciaInicial()

	saludo, _ := armarComando("ConsultarVersion", nil)
	for intento := 0; ; intento++ {
		respuesta, err := p.intercambiar(ctx, saludo)
		if err == nil {
			_, err = leerRespuesta("ConsultarVersion", respuesta)
		}
		if err == nil {
			return nil
		}
		// La primera vez puede ser la respuesta de la conversación anterior,
		// repetida: con el número siguiente ya no.
		if intento == 0 && strings.Contains(err.Error(), "otro comando") {
			continue
		}
		p.soltar()
		return fmt.Errorf("en %s no contestó una impresora fiscal Hasar: %w", p.equipo, err)
	}
}

// intercambiar manda un comando y espera su respuesta, con el diálogo que
// pide el protocolo: el equipo acusa recibo, puede avisar que demora, y su
// respuesta también se acusa.
//
// Sólo se reenvía cuando el equipo dice que no entendió. Si se queda callado
// no se insiste: pudo haberlo hecho, y decidir qué pasó es de quien dictaba
// (para un cierre, preguntarle al equipo en qué quedó).
func (p *hasarPuerto) intercambiar(ctx context.Context, comando []byte) ([]byte, error) {
	p.sn = siguienteSecuencia(p.sn)
	paquete := empaquetar(p.sn, comando)
	for envio := 0; envio < intentosDeHasar; envio++ {
		if _, err := p.puerto.Write(paquete); err != nil {
			return nil, fmt.Errorf("no se pudo escribir en el puerto (%v)", err)
		}
		respuesta, reenviar, err := p.esperar(ctx)
		if err != nil {
			return nil, err
		}
		if !reenviar {
			return respuesta, nil
		}
	}
	return nil, fmt.Errorf("la impresora fiscal no entendió el comando después de %d intentos", intentosDeHasar)
}

// esperar lee hasta tener la respuesta al último comando. reenviar: el
// equipo pidió que se lo manden de nuevo.
func (p *hasarPuerto) esperar(ctx context.Context) (respuesta []byte, reenviar bool, err error) {
	var (
		paquete      []byte // el que se está recibiendo
		pideAtencion bool
		danados      int
		callado      = time.Now().Add(silencioDeHasar)
		uno          = make([]byte, 1)
	)
	for {
		if ctx.Err() != nil {
			if pideAtencion {
				return nil, false, errors.New("la impresora fiscal está esperando que alguien la atienda (¿falta papel, tiene la tapa abierta?) y se acabó el tiempo")
			}
			return nil, false, errors.New("la impresora fiscal no terminó a tiempo")
		}
		if time.Now().After(callado) {
			return nil, false, errors.New("la impresora fiscal no contestó")
		}
		n, err := p.puerto.Read(uno)
		if err != nil && n == 0 {
			return nil, false, fmt.Errorf("se cortó la comunicación con la impresora fiscal (%v)", err)
		}
		if n == 0 {
			continue
		}
		callado = time.Now().Add(silencioDeHasar)
		c := uno[0]

		if paquete == nil {
			switch c {
			case hasarNAK:
				return nil, true, nil
			case hasarDC4:
				pideAtencion = true
			case hasarDC2:
				pideAtencion = false
			case hasarSTX:
				paquete = []byte{c}
			}
			// El acuse y cualquier otra cosa suelta no cambian nada.
			continue
		}
		paquete = append(paquete, c)
		if len(paquete) < 7 || paquete[len(paquete)-5] != hasarETX {
			continue
		}
		sn, contenido, err := desempaquetar(paquete)
		paquete = nil
		if err != nil {
			danados++
			if danados >= intentosDeHasar {
				return nil, false, errors.New("la respuesta de la impresora fiscal llegó dañada una y otra vez: revisá el cable")
			}
			_, _ = p.puerto.Write([]byte{hasarNAK})
			continue
		}
		_, _ = p.puerto.Write([]byte{hasarACK})
		if sn != p.sn {
			continue // la respuesta atrasada de un comando anterior
		}
		return contenido, false, nil
	}
}
