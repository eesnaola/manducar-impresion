// Package fiscal habla con los controladores fiscales de nueva tecnología
// (RG 3561, «segunda generación») de Hasar y de Epson.
//
// No es imprimir: a un controlador fiscal no se le manda un papel armado, se
// le dictan los renglones y él emite el comprobante, le pone el número y lo
// guarda en su memoria. Por eso esto no pasa por internal/printer.
//
// Cada marca va por el camino que su fabricante publica:
//
//   - Hasar, por red, con su interfaz HTTP: un POST con el comando en JSON a
//     http://IP/fiscal.json, con la clave de acceso por red. No hace falta
//     instalar nada en la computadora. Sale del paquete de distribución de
//     Hasar («CF 2G - Enviar comandos en XML o JSON» y el Manual de Comandos).
//     Y enchufada por USB —que se ve como un puerto serie—, con su «protocolo
//     empaquetado»: los mismos comandos, escritos de otra manera
//     (hasar_paquete.go y hasar_puerto.go).
//   - Epson, con su librería EpsonFiscalInterface.dll, que habla por USB,
//     puerto serie o red. Es para Windows, y tiene que estar instalada en esa
//     computadora: el agente la busca y dice si está.
//
// Detectar dice qué puede manejar esta computadora; el servidor lo usa para
// ofrecer en el panel sólo lo que acá se puede dar de alta. Buscar encuentra
// los equipos que hay en la red del local, para que el panel los sugiera.
package fiscal

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Las marcas, como las nombra el servidor.
const (
	MarcaHasar = "hasar"
	MarcaEpson = "epson"
)

// ErrNoDisponible: en esta computadora no se puede manejar esa marca (no es
// Windows, o falta la librería del fabricante). El texto dice por qué.
var ErrNoDisponible = errors.New("no disponible en esta computadora")

// Controlador es cómo llegar a un equipo, tal como lo cargaron en el panel.
type Controlador struct {
	Marca string `json:"marca"`
	// Conexion: en Hasar la dirección del equipo en la red («192.168.1.50»),
	// o «usb» si está enchufada a esta computadora (también vale un puerto
	// serie con nombre: «COM3», «/dev/ttyACM0»). En Epson «usb», un puerto
	// serie («COM3») o una dirección de red.
	Conexion string `json:"conexion"`
	// Velocidad del puerto serie, sólo en Epson; 0 es la de fábrica.
	Velocidad int `json:"velocidad,omitempty"`
	// Clave de acceso por red, sólo en Hasar; vacía es la de fábrica.
	Clave string `json:"clave,omitempty"`
}

// Equipo es lo que el controlador dice de sí mismo: sirve para saber que la
// conexión anda y que es el equipo de ese local.
type Equipo struct {
	Marca  string `json:"marca"`
	Modelo string `json:"modelo"`
	// Version del firmware fiscal, cuando el equipo la da aparte del modelo.
	Version      string `json:"version,omitempty"`
	PuntoDeVenta int    `json:"puntoDeVenta,omitempty"`
	CUIT         string `json:"cuit,omitempty"`
	RazonSocial  string `json:"razonSocial,omitempty"`
	// Registro: el código con el que el equipo está dado de alta ante ARCA.
	Registro string `json:"registro,omitempty"`
	// Responsabilidad ante el IVA del emisor, como la nombra el equipo.
	Responsabilidad string `json:"responsabilidad,omitempty"`
	// Estado: lo que el equipo avisa de sí mismo, si avisa algo.
	Estado []string `json:"estado,omitempty"`
}

// Driver es un controlador fiscal abierto.
type Driver interface {
	// Info le pregunta al equipo quién es. No imprime ni emite nada.
	Info(ctx context.Context) (Equipo, error)
	// Emitir le dicta un comprobante al equipo y devuelve el número que le
	// puso. Lo emitido queda en la memoria fiscal: no se deshace. Si no se
	// pudo saber si salió, el error es ErrIncierto.
	Emitir(ctx context.Context, c Comprobante) (Emitido, error)
	// Cerrar hace el cierre de jornada (CierreZ) o el informe sin cerrar (CierreX).
	Cerrar(ctx context.Context, tipo string) (Cierre, error)
}

// Abrir devuelve el driver de ese controlador.
func Abrir(c Controlador) (Driver, error) {
	switch strings.ToLower(strings.TrimSpace(c.Marca)) {
	case MarcaHasar:
		return nuevoHasar(c)
	case MarcaEpson:
		return nuevoEpson(c)
	}
	return nil, fmt.Errorf("no sé manejar un controlador fiscal de la marca %q", c.Marca)
}

// esEquipoDeRed: un nombre o una IP (la v6, entre corchetes), con su puerto
// si hace falta. Nada más.
var esEquipoDeRed = regexp.MustCompile(`^([A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?|\[[0-9A-Fa-f:.]+\])(:([0-9]{1,5}))?$`)

// direccionDeRed deja la dirección de un equipo como se usa —«192.168.1.50»,
// «caja.local:8080»—, la haya escrito el local como la haya escrito, o dice
// que eso no es una dirección.
//
// El servidor ya la valida al cargarla, pero es el agente el que le manda
// ahí los comandos y la clave de acceso: no confía en que llegue bien. Sólo
// un equipo y un puerto; ni un camino, ni un usuario, ni una consulta, que
// harían que el pedido fuera a parar a otro lado.
func direccionDeRed(escrita string) (string, error) {
	d := strings.TrimSpace(escrita)
	d = strings.TrimPrefix(strings.TrimPrefix(d, "http://"), "https://")
	d = strings.TrimRight(d, "/")
	if d == "" {
		return "", errors.New("falta la dirección de la impresora fiscal en la red (por ejemplo 192.168.1.50)")
	}
	partes := esEquipoDeRed.FindStringSubmatch(d)
	if partes == nil {
		return "", fmt.Errorf("%q no es la dirección de un equipo de la red: va su IP o su nombre, y el puerto si hace falta (por ejemplo 192.168.1.50)", recortar(d, 60))
	}
	if puerto := partes[4]; puerto != "" {
		if n, _ := strconv.Atoi(puerto); n < 1 || n > 65535 {
			return "", fmt.Errorf("en la dirección de la impresora fiscal, %s no es un puerto", puerto)
		}
	}
	return d, nil
}

// Capacidades es qué controladores fiscales puede manejar esta computadora.
type Capacidades struct {
	// Hasar va por red y no pide nada instalado: siempre se puede.
	Hasar bool `json:"hasar"`
	// HasarUSB: además se la puede dar de alta enchufada por USB, porque esta
	// computadora sabe encontrar su puerto.
	HasarUSB bool `json:"hasarUsb"`
	// Epson depende de la librería del fabricante.
	Epson LibreriaEpson `json:"epson"`
}

// LibreriaEpson dice si la librería de Epson está en esta computadora.
type LibreriaEpson struct {
	Disponible bool   `json:"disponible"`
	Version    string `json:"version,omitempty"`
	// Motivo: por qué no está disponible, en palabras para el panel.
	Motivo string `json:"motivo,omitempty"`
	// Puertos por los que se puede dar de alta un equipo: «usb» y los
	// puertos serie que tiene la computadora.
	Puertos []string `json:"puertos,omitempty"`
}

// Detectar mira qué se puede manejar desde acá. No le habla a ningún equipo.
func Detectar() Capacidades {
	return Capacidades{Hasar: true, HasarUSB: puedeHasarPorUSB(), Epson: detectarEpson()}
}
