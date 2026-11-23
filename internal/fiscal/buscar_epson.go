package fiscal

import (
	"regexp"
	"strings"
)

// Epson no tiene un buscador como el de Hasar, y por USB no hay dirección
// que buscar. Lo que sí publica alcanza para saber, sin hablarle al equipo, si
// hay una fiscal enchufada:
//
//   - los identificadores USB de sus impresoras: fabricante 1208 (04B8) y
//     producto 513 o 514 (0201, 0202), en EpsonFiscalDriverSpec («USB and
//     serial»). Son los de cualquier Epson de tickets, fiscal o no;
//   - que una fiscal lleva de número de serie USB su código de registro
//     (EpsonFiscalInterfaceSpec, ConfigurarPuerto: «usb:EPSONN1234567890»,
//     desde el firmware Neptuno): seis letras y diez dígitos, la forma que la
//     RG 3561 le da al registro de todo controlador fiscal. Una Epson de
//     tickets común tiene otra serie, y así se las distingue.
//
// Una fiscal con firmware anterior, que no informe el registro, no aparece:
// queda como hasta ahora, eligiendo «USB» a mano.
var (
	// usbEnchufados: los identificadores de los dispositivos USB que están
	// enchufados ahora, como los da el sistema (`USB\VID_04B8&PID_0202\SERIE`).
	usbEnchufados = usbDelSistema
	// libreriaDeEpson: si esta computadora puede manejar una Epson.
	libreriaDeEpson = detectarEpson
)

var (
	usbDeEpson     = regexp.MustCompile(`^USB\\VID_04B8&PID_020[12]\\([^\\&]+)$`)
	registroFiscal = regexp.MustCompile(`^[A-Z]{6}[0-9]{10}$`)
)

// registroDeEpsonFiscal dice si ese dispositivo USB es una impresora fiscal
// Epson, y con qué código de registro.
func registroDeEpsonFiscal(idDeUSB string) (registro string, esFiscal bool) {
	partes := usbDeEpson.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(idDeUSB)))
	if partes == nil || !registroFiscal.MatchString(partes[1]) {
		return "", false
	}
	return partes[1], true
}

// buscarEpson mira si hay una impresora fiscal Epson enchufada por USB. No
// le habla: sólo lee la lista de lo enchufado. Quién es lo contesta ella
// después, cuando se la carga y el panel le pregunta.
func buscarEpson() []Encontrado {
	var registros []string
	for _, id := range usbEnchufados() {
		if registro, es := registroDeEpsonFiscal(id); es {
			registros = append(registros, registro)
		}
	}
	if len(registros) == 0 {
		return nil
	}
	// Por USB la librería se conecta «a la que está enchufada» (usb:usb), así
	// que hay una sola sugerencia. Con una sola enchufada se sabe cuál es.
	encontrado := Encontrado{Marca: MarcaEpson, Conexion: "usb"}
	if len(registros) == 1 {
		encontrado.Equipo = &Equipo{Marca: MarcaEpson, Registro: registros[0]}
	}
	if libreria := libreriaDeEpson(); !libreria.Disponible {
		encontrado.Aviso = "Está enchufada por USB, pero esta computadora todavía no la puede manejar. " + libreria.Motivo
	}
	return []Encontrado{encontrado}
}
