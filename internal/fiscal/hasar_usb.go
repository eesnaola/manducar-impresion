package fiscal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Por USB una Hasar 2G se presenta como un puerto serie. Sus identificadores
// salen del driver que Hasar distribuye para Windows (gserial.inf, en «USB
// GADGET» del paquete de distribución): fabricante 03EB, producto 2310. Con
// eso el agente sabe cuál de los puertos de la computadora es el suyo, sin
// probar a hablarle a los demás, que pueden ser una balanza o una comandera.
const (
	fabricanteHasarUSB = "03EB"
	productoHasarUSB   = "2310"
	// velocidadDeHasar: la de fábrica del puerto serie. Por USB da lo mismo.
	velocidadDeHasar = 9600
)

var errNoHayHasarUSB = errors.New("no hay ninguna enchufada, o la computadora no la reconoció (puede faltar el driver USB de Hasar)")

// esLaHasarPorUSB: ¿esos identificadores, como los da el sistema, son los de la Hasar?
func esLaHasarPorUSB(fabricante, producto string) bool {
	return strings.EqualFold(fabricante, fabricanteHasarUSB) && strings.EqualFold(producto, productoHasarUSB)
}

// Windows nombra lo enchufado como USB\VID_03EB&PID_2310\serie; cuando el
// dispositivo tiene varias funciones, el puerto es una de ellas (&MI_00).
var idDeUSB = regexp.MustCompile(`^USB\\VID_([0-9A-F]{4})&PID_([0-9A-F]{4})(&|\\|$)`)

func esIDDeHasarUSB(id string) bool {
	partes := idDeUSB.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(id)))
	return partes != nil && esLaHasarPorUSB(partes[1], partes[2])
}

// puertoHasarEnSysfs busca la Hasar entre los puertos serie de Linux. clase
// es donde el sistema los lista (/sys/class/tty): de cada uno que sale de un
// USB, «device» lleva a la interfaz y su carpeta de arriba es el dispositivo,
// con sus identificadores.
func puertoHasarEnSysfs(clase string) (string, error) {
	var puertos []string
	for _, patron := range []string{"ttyACM*", "ttyUSB*"} {
		encontrados, _ := filepath.Glob(filepath.Join(clase, patron))
		puertos = append(puertos, encontrados...)
	}
	sort.Strings(puertos)
	for _, puerto := range puertos {
		interfaz, err := filepath.EvalSymlinks(filepath.Join(puerto, "device"))
		if err != nil {
			continue
		}
		dispositivo := filepath.Dir(interfaz)
		fabricante, _ := os.ReadFile(filepath.Join(dispositivo, "idVendor"))
		producto, _ := os.ReadFile(filepath.Join(dispositivo, "idProduct"))
		if esLaHasarPorUSB(strings.TrimSpace(string(fabricante)), strings.TrimSpace(string(producto))) {
			return "/dev/" + filepath.Base(puerto), nil
		}
	}
	return "", errNoHayHasarUSB
}

// puedeHasarPorUSB: ¿desde esta computadora se puede dar de alta una Hasar
// enchufada por USB?
func puedeHasarPorUSB() bool {
	return hasarUSBEnEsteSistema || puertoHasarDePrueba != ""
}

// buscarHasarUSB mira si hay una Hasar enchufada por USB y, si la hay, le
// pregunta quién es. A diferencia de los puertos serie en general —donde
// puede haber una balanza o una comandera—, a éste se le puede hablar: ya se
// sabe que es la Hasar.
func buscarHasarUSB(ctx context.Context) []Encontrado {
	if _, err := puertoDeHasarUSB(); err != nil {
		return nil
	}
	encontrado := Encontrado{Marca: MarcaHasar, Conexion: "usb"}
	h, err := nuevoHasar(Controlador{Marca: MarcaHasar, Conexion: "usb"})
	if err != nil {
		return nil
	}
	ctx, cancelar := context.WithTimeout(ctx, 2*plazoDeIdentificar)
	defer cancelar()
	equipo, err := h.Info(ctx)
	if err != nil {
		encontrado.Aviso = "Está enchufada por USB pero no dijo quién es: " + err.Error()
		return []Encontrado{encontrado}
	}
	encontrado.Equipo = &equipo
	return []Encontrado{encontrado}
}
