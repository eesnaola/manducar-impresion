package fiscal

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// El «protocolo empaquetado» de las Hasar 2G: lo que hablan por el puerto
// serie —el USB se ve como uno— y por TCP en el puerto 20000. La interfaz
// HTTP (hasar.go) existe sólo por red; por USB es esto o nada.
//
// Son los mismos comandos con los mismos datos, escritos de otra manera: en
// vez de un objeto con campos con nombre, un texto con los campos en un orden
// fijo, separados por FS, y las enumeraciones por su código en vez de por su
// identificador:
//
//	{"AbrirDocumento": {"CodigoComprobante": "TiqueFacturaB"}}   →   ESC 40 FS 82
//
// Por eso acá no hay otra lógica de emisión: se traduce cada comando de su
// forma con nombres a la empaquetada y cada respuesta al revés, y lo que
// dicta el comprobante (hasar_emitir.go) es lo mismo por los dos caminos.
//
// Todo sale del «Quick Manual de Comandos» de Hasar (Rev007), que trae cada
// comando en los tres formatos: el orden de los campos, los códigos de las
// tablas de codificación y los bits de los estados. Los tests comparan contra
// los ejemplos de ese manual.

// Los caracteres de la trama.
const (
	hasarSTX = 0x02 // comienzo del paquete
	hasarETX = 0x03 // fin del paquete
	hasarACK = 0x06 // «entendí»
	hasarDC2 = 0x12 // «esperá, sigo trabajando»
	hasarDC4 = 0x14 // «esperá, necesito que alguien me atienda»
	hasarNAK = 0x15 // «no entendí, mandalo de nuevo»
	hasarESC = 0x1B // abre el comando; con él el equipo avisa mientras demora
	hasarFS  = 0x1C // separa los campos
)

// comandoEmpaquetado es un comando en su forma empaquetada: su identificador,
// sus campos en orden y los de su respuesta, después de los dos estados.
type comandoEmpaquetado struct {
	id      byte
	entrada []string
	salida  []string
}

// Los comandos que usa el agente. Los nombres de los campos son los de la
// interfaz HTTP, en el orden en que el manual numera los empaquetados.
var comandosEmpaquetados = map[string]comandoEmpaquetado{
	"ConsultarVersion":             {id: 0x7F, salida: []string{"Version", "Marca", "NombreProducto", "VersionMotor", "FechaFirmware", "VersionProtocolo"}},
	"ConsultarDatosInicializacion": {id: 0x73, salida: []string{"CUIT", "RazonSocial", "Registro", "NumeroPos", "FechaInicioActividades", "IngBrutos", "ResponsabilidadIVA", "FechaInicioActividadesCompleta"}},
	"ConsultarEstado": {
		id: 0x2A, entrada: []string{"CodigoComprobante"},
		salida: []string{"EstadoAuxiliar", "EstadoInterno", "ComprobanteEnCurso", "CodigoComprobante", "NumeroUltimoComprobante", "CantidadEmitidos", "CantidadCancelados"},
	},
	"CargarDocumentoAsociado": {id: 0x93, entrada: []string{"NumeroLinea", "CodigoComprobante", "NumeroPos", "NumeroComprobante", "FechaComprobante"}},
	"CargarDatosCliente":      {id: 0x62, entrada: []string{"RazonSocial", "NumeroDocumento", "ResponsabilidadIVA", "TipoDocumento", "Domicilio"}},
	"AbrirDocumento":          {id: 0x40, entrada: []string{"CodigoComprobante"}, salida: []string{"NumeroComprobante", "IndiceAuditoria"}},
	"ImprimirItem": {
		id: 0x42,
		entrada: []string{
			"Descripcion", "Cantidad", "PrecioUnitario", "CondicionIVA", "AlicuotaIVA", "OperacionMonto", "TipoImpuestoInterno",
			"MagnitudImpuestoInterno", "ModoDisplay", "ModoBaseTotal", "UnidadReferencia", "CodigoProducto", "CodigoInterno", "UnidadMedida",
		},
		salida: []string{"IndiceAuditoria"},
	},
	"ImprimirAjuste": {
		id: 0x54, entrada: []string{"Descripcion", "Monto", "ModoDisplay", "ModoBaseTotal", "CodigoProducto", "Operacion"},
		salida: []string{"IndiceAuditoria", "CantidadIVAs"},
	},
	"ConsultarSubtotal": {
		id: 0x43, entrada: []string{"Impresion", "ModoDisplay"},
		salida: []string{"CantidadItems", "Subtotal", "MontoIVA", "MontoPagado", "MontoOtrosTributos", "MontoImpInternos", "MontoBase", "AjusteRedondeo"},
	},
	"ImprimirPago": {
		id: 0x44, entrada: []string{"Descripcion", "Monto", "Operacion", "ModoDisplay", "DescripcionAdicional", "CodigoFormaPago", "Cuotas", "Cupones", "Referencia"},
		salida: []string{"Saldo"},
	},
	"CerrarDocumento": {id: 0x45, entrada: []string{"Copias", "DireccionEMail"}, salida: []string{"NumeroComprobante", "CantidadDePaginas", "IndiceAuditoria"}},
	"Cancelar":        {id: 0x98},
	// La respuesta de CerrarJornadaFiscal depende de qué reporte fue: ver salidaDeCierre.
	"CerrarJornadaFiscal": {id: 0x39, entrada: []string{"Reporte"}},
}

// Lo que contesta CerrarJornadaFiscal, según el reporte que dice su primer campo.
var salidaDeCierre = map[string][]string{
	"X": {
		"Reporte", "Numero", "FechaInicio", "HoraInicio", "FechaCierre", "HoraCierre", "DF_Total", "DF_TotalIVA", "DF_TotalTributos",
		"DF_CantidadEmitidos", "NC_Total", "NC_TotalIVA", "NC_TotalTributos", "NC_CantidadEmitidos", "DNFH_CantidadEmitidos",
	},
	"Z": {
		"Reporte", "Numero", "Fecha", "DF_Total", "DF_TotalGravado", "DF_TotalNoGravado", "DF_TotalExento", "DF_TotalIVA", "DF_TotalTributos",
		"DF_CantidadEmitidos", "DF_CantidadCancelados", "NC_Total", "NC_TotalGravado", "NC_TotalNoGravado", "NC_TotalExento", "NC_TotalIVA",
		"NC_TotalTributos", "NC_CantidadEmitidos", "NC_CantidadCancelados", "DNFH_Total", "DNFH_CantidadEmitidos",
	},
}

// Los comprobantes («documentos soportados»): el código es el de ARCA.
var documentosDeHasar = map[string]string{
	"ReciboA": "4", "ReciboB": "9", "ReciboC": "15", "ReciboM": "54", "InformeDiarioDeCierre": "80",
	"TiqueFacturaA": "81", "TiqueFacturaB": "82", "Tique": "83", "TiqueNotaCredito": "110", "TiqueFacturaC": "111",
	"TiqueNotaCreditoA": "112", "TiqueNotaCreditoB": "113", "TiqueNotaCreditoC": "114",
	"TiqueNotaDebitoA": "115", "TiqueNotaDebitoB": "116", "TiqueNotaDebitoC": "117",
	"TiqueFacturaM": "118", "TiqueNotaCreditoM": "119", "TiqueNotaDebitoM": "120",
	"RemitoR": "91", "RemitoX": "901", "ReciboX": "902", "PresupuestoX": "903",
}

// valoresDeHasar: de cada campo que es una enumeración, el código empaquetado
// de cada identificador (las «tablas de codificación» del manual).
var valoresDeHasar = map[string]map[string]string{
	"CodigoComprobante":  documentosDeHasar,
	"ComprobanteEnCurso": documentosDeHasar,
	"ResponsabilidadIVA": {
		"ResponsableInscripto": "I", "ResponsableExento": "E", "NoResponsable": "A", "ConsumidorFinal": "C", "NoCategorizado": "T",
		"Monotributo": "M", "MonotributoSocial": "S", "Eventual": "V", "EventualSocial": "W",
	},
	// «Sin identificar» es un espacio.
	"TipoDocumento":       {"TipoCUIT": "C", "TipoCUIL": "L", "TipoLE": "0", "TipoLC": "1", "TipoDNI": "2", "TipoPasaporte": "3", "TipoCI": "4", "TipoNinguno": " "},
	"CondicionIVA":        {"NoGravado": "1", "Exento": "2", "Gravado": "7"},
	"OperacionMonto":      {"ModoSumaMonto": "M", "ModoRestaMonto": "m"},
	"TipoImpuestoInterno": {"IIVariableKIVA": "0", "IIVariablePorcentual": "%", "IIFijoKIVA": "+", "IIFijoMonto": "$"},
	"ModoDisplay":         {"DisplayNo": "0", "DisplaySi": "1", "DisplayRep": "2"},
	"ModoBaseTotal":       {"ModoPrecioBase": "B", "ModoPrecioTotal": "T"},
	"Impresion":           {"ImprimeSubtotal": "P", "NoImprimeSubtotal": "N"},
	"Reporte":             {"ReporteX": "X", "ReporteZ": "Z"},
	"CodigoFormaPago": {
		"Cambio": "0", "CartaDeCreditoDocumentario": "1", "CartaDeCreditoSimple": "2", "Cheque": "3", "ChequeCancelatorios": "4",
		"CreditoDocumentario": "5", "CuentaCorriente": "6", "Deposito": "7", "Efectivo": "8", "EndosoDeCheque": "9", "FacturaDeCredito": "10",
		"GarantiaBancaria": "11", "Giro": "12", "LetraDeCambio": "13", "MedioDePagoDeComercioExterior": "14", "OrdenDePagoDocumentaria": "15",
		"OrdenDePagoSimple": "16", "PagoContraReembolso": "17", "RemesaDocumentaria": "18", "RemesaSimple": "19", "TarjetaDeCredito": "20",
		"TarjetaDeDebito": "21", "Ticket": "22", "TransferenciaBancaria": "23", "TransferenciaNoBancaria": "24", "OtrosMediosPago": "99",
	},
	"EstadoInterno": {
		"Desconocido": "0", "NoInicializado": "1", "InicioJornadaFiscal": "2", "EnJornadaFiscal": "3", "VendiendoItems": "4",
		"ImprimiendoTextoFiscal": "5", "Pagando": "6", "IngresandoOtrosTributos": "7", "RealizandoOperacionAjuste": "8",
		"RealizandoOperacionGlobalIVA": "9", "RealizandoOperacionAnticipo": "10", "ImprimiendoLineasRecibo": "11", "ImprimiendoTextoNoFiscal": "12",
		"CintaAuditoriaCasiLlena": "13", "CintaAuditoriaLlena": "14", "ControladorFiscalEsperandoBaja": "15", "ControladorFiscalDadoDeBaja": "16",
		"ControladorFiscalBloqueado": "17",
	},
}

// La misma palabra «Operacion» es otra enumeración en cada comando.
var operacionesDeHasar = map[string]map[string]string{
	"ImprimirAjuste": {"AjusteNeg": "D", "AjustePos": "R", "BonificacionGeneral": "B"},
	"ImprimirPago":   {"Pagar": "T", "Anular": "R"},
}

// Los campos de una respuesta cuyo código se devuelve con su identificador,
// como lo hace la interfaz HTTP. La responsabilidad del emisor no: por HTTP
// también viene con su letra.
var seNombran = map[string]bool{"ComprobanteEnCurso": true, "CodigoComprobante": true, "EstadoInterno": true, "Reporte": true}

// Los bits de los tres estados, con el identificador que les da el manual.
var (
	bitsDeImpresora = map[int]string{0: "ImpresoraOcupada", 2: "ErrorImpresora", 3: "ImpresoraOffLine", 4: "FaltaPapelJournal", 5: "FaltaPapelReceipt", 8: "TapaAbierta", 14: "CajonAbierto", 15: "OrLogico"}
	bitsFiscales    = map[int]string{
		0: "ErrorMemoriaFiscal", 1: "ErrorMemoriaTrabajo", 2: "ErrorMemoriaAuditoria", 3: "ErrorGeneral", 4: "ErrorParametro", 5: "ErrorEstado",
		6: "ErrorAritmetico", 7: "MemoriaFiscalLlena", 8: "MemoriaFiscalCasiLlena", 9: "MemoriaFiscalInicializada", 12: "DocumentoFiscalAbierto",
		13: "DocumentoAbierto", 15: "ErrorEjecucion",
	}
	bitsAuxiliares = map[int]string{
		0: "MemoriaAuditoriaLlena", 1: "MemoriaAuditoriaCasiLlena", 2: "DatosClienteAlmacenados", 3: "CodigoBarrasAlmacenado", 4: "ModoEntrenamiento",
		5: "UltimoComprobanteFueCancelado", 6: "CajeroActivo",
	}
)

// Por el puerto el equipo no dice con palabras por qué rechazó un comando:
// prende bits del estado fiscal. Estos son los que quieren decir «no lo hice»,
// con lo que dice el manual de cada uno.
var rechazosFiscales = []struct {
	bit  int
	dice string
}{
	{4, "tiene un error en un parámetro"},
	{5, "no es válido para el estado en que está la impresora fiscal"},
	{6, "da un error aritmético"},
	{15, "dio un error de ejecución"},
	{3, "dio un error general"},
	{0, "hay un error en la memoria fiscal"},
	{1, "hay un error en la memoria de trabajo"},
	{2, "hay un error en la memoria de auditoría"},
}

// Y lo que la impresora puede estar pidiendo, para decirlo junto al rechazo.
var pedidosDeImpresora = []struct {
	bit  int
	dice string
}{
	{5, "falta papel"},
	{4, "falta papel testigo"},
	{8, "la tapa está abierta"},
	{3, "la impresora está fuera de línea"},
	{2, "la impresora da error"},
}

// armarComando escribe un comando en su forma empaquetada, sin la trama:
// ESC, el identificador y cada campo en su lugar. campos es lo mismo que se
// le manda a la interfaz HTTP (una estructura o un mapa con los nombres).
func armarComando(nombre string, campos any) ([]byte, error) {
	comando, ok := comandosEmpaquetados[nombre]
	if !ok {
		return nil, fmt.Errorf("no sé mandar «%s» por el puerto de la impresora fiscal", nombre)
	}
	valores := map[string]string{}
	if campos != nil {
		crudo, err := json.Marshal(campos)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(crudo, &valores); err != nil {
			return nil, fmt.Errorf("los campos de «%s» tienen que ser textos: %w", nombre, err)
		}
	}
	lugares := map[string]bool{}
	for _, campo := range comando.entrada {
		lugares[campo] = true
	}
	for campo := range valores {
		if !lugares[campo] {
			// Mandarlo igual correría los demás de lugar.
			return nil, fmt.Errorf("«%s» no tiene un campo «%s» en el protocolo del puerto", nombre, campo)
		}
	}

	partes := make([]string, len(comando.entrada))
	ultimo := -1
	for i, campo := range comando.entrada {
		valor, vino := valores[campo]
		if !vino {
			continue
		}
		partes[i] = enCodigo(nombre, campo, valor)
		if partes[i] != "" {
			ultimo = i
		}
	}
	// Los campos vacíos del final no se mandan, como en los ejemplos del manual.
	armado := []byte{hasarESC, comando.id}
	for _, parte := range partes[:ultimo+1] {
		armado = append(armado, hasarFS)
		armado = append(armado, aCP437(parte)...)
	}
	return armado, nil
}

// enCodigo traduce el identificador de una enumeración a su código. Lo que
// no es un identificador conocido va como vino: ya es un código, o un texto.
func enCodigo(comando, campo, valor string) string {
	tabla := valoresDeHasar[campo]
	if campo == "Operacion" {
		tabla = operacionesDeHasar[comando]
	}
	if codigo, es := tabla[valor]; es {
		return codigo
	}
	return valor
}

// enNombre es la vuelta de enCodigo, para los campos de una respuesta que la
// interfaz HTTP devuelve con su identificador.
func enNombre(campo, codigo string) string {
	if !seNombran[campo] {
		return codigo
	}
	for nombre, c := range valoresDeHasar[campo] {
		if c == codigo {
			return nombre
		}
	}
	return codigo
}

// leerRespuesta interpreta la respuesta empaquetada de un comando y la
// devuelve como la daría la interfaz HTTP: un objeto con «Estado» y los
// campos con su nombre. Si el equipo rechazó el comando, el error lo dice.
func leerRespuesta(nombre string, respuesta []byte) (json.RawMessage, error) {
	comando, ok := comandosEmpaquetados[nombre]
	if !ok {
		return nil, fmt.Errorf("no sé leer la respuesta de «%s» por el puerto de la impresora fiscal", nombre)
	}
	if len(respuesta) < 2 || respuesta[0] != hasarESC {
		return nil, fmt.Errorf("la impresora fiscal contestó algo que no es una respuesta a «%s»", nombre)
	}
	if respuesta[1] != comando.id {
		return nil, fmt.Errorf("la impresora fiscal contestó a otro comando (%02X) y no a «%s» (%02X)", respuesta[1], nombre, comando.id)
	}
	var partes []string
	if resto := respuesta[2:]; len(resto) > 0 {
		if resto[0] != hasarFS {
			return nil, fmt.Errorf("la respuesta de la impresora fiscal a «%s» vino mal armada", nombre)
		}
		for _, parte := range strings.Split(string(resto[1:]), string(rune(hasarFS))) {
			partes = append(partes, deCP437([]byte(parte)))
		}
	}
	if len(partes) < 2 {
		return nil, fmt.Errorf("la respuesta de la impresora fiscal a «%s» vino sin sus estados", nombre)
	}
	impresora, errImpresora := strconv.ParseUint(strings.TrimSpace(partes[0]), 16, 16)
	fiscal, errFiscal := strconv.ParseUint(strings.TrimSpace(partes[1]), 16, 16)
	if errImpresora != nil || errFiscal != nil {
		return nil, fmt.Errorf("la respuesta de la impresora fiscal a «%s» vino con los estados mal escritos", nombre)
	}

	if motivos := rechazo(impresora, fiscal); len(motivos) > 0 {
		return nil, fmt.Errorf("la impresora fiscal no aceptó «%s»: %s", nombre, strings.Join(motivos, " · "))
	}

	salida := comando.salida
	datos := partes[2:]
	if nombre == "CerrarJornadaFiscal" && len(datos) > 0 {
		salida = salidaDeCierre[strings.TrimSpace(datos[0])]
	}
	objeto := map[string]any{"Estado": map[string]any{"Impresora": nombresDe(impresora, bitsDeImpresora), "Fiscal": nombresDe(fiscal, bitsFiscales)}}
	for i, campo := range salida {
		if i >= len(datos) {
			break // un protocolo anterior manda menos campos al final
		}
		if campo == "EstadoAuxiliar" {
			auxiliar, _ := strconv.ParseUint(strings.TrimSpace(datos[i]), 16, 16)
			objeto[campo] = nombresDe(auxiliar, bitsAuxiliares)
			continue
		}
		objeto[campo] = enNombre(campo, datos[i])
	}
	return json.Marshal(objeto)
}

// rechazo dice, mirando los estados, por qué el equipo no hizo lo que se le
// pidió. Vacío si lo hizo.
func rechazo(impresora, fiscal uint64) []string {
	var motivos []string
	for _, r := range rechazosFiscales {
		if fiscal&(1<<r.bit) != 0 {
			motivos = append(motivos, r.dice)
		}
	}
	if len(motivos) == 0 {
		return nil
	}
	for _, p := range pedidosDeImpresora {
		if impresora&(1<<p.bit) != 0 {
			motivos = append(motivos, p.dice)
		}
	}
	return motivos
}

// nombresDe da los identificadores de los bits prendidos, en orden de bit.
func nombresDe(estado uint64, bits map[int]string) []string {
	nombres := []string{}
	orden := make([]int, 0, len(bits))
	for bit := range bits {
		orden = append(orden, bit)
	}
	sort.Ints(orden)
	for _, bit := range orden {
		if estado&(1<<bit) != 0 {
			nombres = append(nombres, bits[bit])
		}
	}
	return nombres
}

// empaquetar le pone la trama a un comando: STX, el número de secuencia, el
// comando, ETX y la suma de todo lo anterior en cuatro dígitos hexadecimales.
func empaquetar(sn byte, comando []byte) []byte {
	paquete := make([]byte, 0, len(comando)+7)
	paquete = append(paquete, hasarSTX, sn)
	paquete = append(paquete, comando...)
	paquete = append(paquete, hasarETX)
	return append(paquete, fmt.Sprintf("%04X", sumaDe(paquete))...)
}

// desempaquetar valida la trama de un paquete entero y devuelve su número de
// secuencia y lo que lleva adentro.
func desempaquetar(paquete []byte) (sn byte, contenido []byte, err error) {
	n := len(paquete)
	if n < 7 || paquete[0] != hasarSTX || paquete[n-5] != hasarETX {
		return 0, nil, errors.New("el paquete de la impresora fiscal vino mal armado")
	}
	suma, err := strconv.ParseUint(string(paquete[n-4:]), 16, 16)
	if err != nil || uint16(suma) != sumaDe(paquete[:n-4]) {
		return 0, nil, errors.New("el paquete de la impresora fiscal llegó dañado (no da la suma de comprobación)")
	}
	sn = paquete[1]
	if sn < 0x20 || sn > 0x7F {
		return 0, nil, errors.New("el paquete de la impresora fiscal trae un número de secuencia que no existe")
	}
	return sn, paquete[2 : n-5], nil
}

func sumaDe(b []byte) uint16 {
	var suma uint16
	for _, c := range b {
		suma += uint16(c)
	}
	return suma
}

// siguienteSecuencia: va de 20 a 7F hexadecimal y da la vuelta.
func siguienteSecuencia(sn byte) byte {
	if sn < 0x20 || sn >= 0x7F {
		return 0x20
	}
	return sn + 1
}

// cp437 son las letras que el equipo tiene fuera del ASCII («DOS CP-437») y
// que aparecen en un nombre o en una dirección en castellano. Las mayúsculas
// acentuadas que la tabla no tiene van sin el acento.
var cp437 = map[rune]byte{
	'á': 0xA0, 'é': 0x82, 'í': 0xA1, 'ó': 0xA2, 'ú': 0xA3, 'ñ': 0xA4, 'Ñ': 0xA5, 'ü': 0x81, 'Ü': 0x9A, 'É': 0x90,
	'¿': 0xA8, '¡': 0xAD, 'º': 0xA7, 'ª': 0xA6, '°': 0xF8, '«': 0xAE, '»': 0xAF, 'ç': 0x87, 'Ç': 0x80,
	'Á': 'A', 'Í': 'I', 'Ó': 'O', 'Ú': 'U', '–': '-', '—': '-', '’': '\'', '“': '"', '”': '"',
}

// aCP437 pasa un texto a la codificación del equipo. Un carácter de control
// adentro de un campo rompería la trama: va un espacio. Lo que la tabla no
// tiene, un signo de pregunta.
func aCP437(texto string) []byte {
	salida := make([]byte, 0, len(texto))
	for _, letra := range texto {
		switch {
		case letra < 0x20 || letra == 0x7F:
			salida = append(salida, ' ')
		case letra < 0x7F:
			salida = append(salida, byte(letra))
		default:
			if b, hay := cp437[letra]; hay {
				salida = append(salida, b)
			} else {
				salida = append(salida, '?')
			}
		}
	}
	return salida
}

// deCP437 es la vuelta: lo que el equipo manda, a texto.
func deCP437(b []byte) string {
	var texto strings.Builder
	for _, c := range b {
		if c < 0x80 {
			texto.WriteByte(c)
			continue
		}
		letra := '?'
		for r, codigo := range cp437 {
			if codigo == c && r > 0x7F {
				letra = r
				break
			}
		}
		texto.WriteRune(letra)
	}
	return texto.String()
}
