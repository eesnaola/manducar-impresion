package fiscal

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// libEpson es la librería de Epson («Epson Fiscal Interface») vista desde
// acá: funciones por nombre, con argumentos enteros (int) y de texto
// (string), como en su especificación. La de verdad llama a la DLL
// (epson_windows.go); los tests usan una de mentira, y así la secuencia de
// cada operación se prueba en cualquier sistema.
type libEpson interface {
	// llamar invoca una función que no devuelve datos.
	llamar(nombre string, args ...any) error
	// texto invoca una función que devuelve un texto: los args van antes del
	// texto de salida y su largo, que agrega quien implementa.
	texto(nombre string, args ...any) (string, error)
	// entero invoca una función que devuelve un entero por referencia.
	entero(nombre string, args ...any) (int, error)
	// version es para ConsultarVersionEquipo y ConsultarVersionDll, que
	// devuelven un texto y dos enteros.
	version(nombre string) (texto string, mayor, menor int, err error)
}

// La librería de Epson («Epson Fiscal Interface», EpsonFiscalInterface.dll) la
// publica Epson Argentina para desarrolladores; su especificación
// (EpsonFiscalInterfaceSpec.pdf) es de donde salen los nombres y los
// parámetros de acá. Todas las funciones son stdcall, reciben enteros y
// cadenas ANSI terminadas en cero, y devuelven un entero: 0 es «sin error».
const dllEpson = "EpsonFiscalInterface.dll"

// candidatasEpsonEn dice dónde se busca la librería, de lo más explícito a lo
// más buscado: la que indicaron a mano, la que está al lado del agente, y las
// que haya bajo las carpetas de programas instalados (raices).
//
// La lista es corta a propósito. La librería no viene firmada: lo único que
// dice que es la de Epson es dónde está, y cargar una es ejecutar su código.
// Sólo se la busca donde no escribe cualquiera; las carpetas públicas, la del
// usuario y la raíz del disco quedan afuera. Una ruta indicada a mano tiene
// que ser entera: una relativa se resolvería contra la carpeta desde la que
// arrancó el agente.
func candidatasEpsonEn(indicada, delAgente string, raices []string) []string {
	var rutas []string
	if filepath.IsAbs(indicada) {
		rutas = append(rutas, filepath.Clean(indicada))
	}
	if delAgente != "" {
		rutas = append(rutas, filepath.Join(delAgente, dllEpson))
	}
	for _, raiz := range raices {
		if raiz != "" {
			rutas = append(rutas, buscarEpsonEn(raiz)...)
		}
	}
	return rutas
}

// buscarEpsonEn baja hasta seis niveles desde raiz, sólo por carpetas que
// nombren a Epson o a lo fiscal: el instalador de Epson no registra la
// librería en el sistema, deja una carpeta con sus ejemplos y la de 64 bits
// está adentro. Recorrer el disco entero en cada latido no es opción.
func buscarEpsonEn(raiz string) []string {
	var halladas []string
	var bajar func(dir string, nivel int, adentro bool)
	bajar = func(dir string, nivel int, adentro bool) {
		entradas, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entradas {
			nombre := strings.ToLower(e.Name())
			if !e.IsDir() {
				if adentro && nombre == strings.ToLower(dllEpson) {
					halladas = append(halladas, filepath.Join(dir, e.Name()))
				}
				continue
			}
			esDeEpson := strings.Contains(nombre, "epson") || strings.Contains(nombre, "fiscal")
			if nivel < 6 && (adentro || esDeEpson) {
				bajar(filepath.Join(dir, e.Name()), nivel+1, adentro || esDeEpson)
			}
		}
	}
	bajar(raiz, 0, false)
	sort.Strings(halladas)
	return halladas
}

// La librería guarda el vínculo con el equipo en su propio estado: no admite
// dos conversaciones a la vez.
var turnoEpson sync.Mutex

type epson struct {
	c   Controlador
	lib libEpson
}

// Las consultas de estado de la librería (ConsultarEstado).
const (
	epsonDocumentoEnCurso = 1003
	epsonModo             = 1015
	epsonModoEntrenar     = 2
)

var (
	epsonDocumentos   = map[string]int{"ninguno": 0, "dni": 1, "cuil": 2, "cuit": 3, "ci": 4, "pasaporte": 5}
	epsonResponsables = map[string]int{
		"responsable_inscripto": 1, "no_responsable": 3, "monotributo": 4, "consumidor_final": 5,
		"exento": 6, "no_categorizado": 7, "monotributo_social": 8,
	}
	epsonMedios = map[string]int{"efectivo": 8, "credito": 20, "debito": 21, "transferencia": 23, "cuenta_corriente": 6, "otro": 99}
	// Las tasas con identificador propio; las demás van como «-tasa×100».
	epsonTasas = map[string]int{"0.00": 3, "10.50": 4, "21.00": 5, "27.00": 6}
)

// codigoAFIP es el comprobante como lo numera ARCA, que es como lo nombra la
// librería al consultar («Tipos de comprobantes»).
func codigoAFIP(tipo, letra string) int {
	switch tipo {
	case TipoFactura:
		return map[string]int{"A": 81, "B": 82, "C": 111, "M": 118}[letra]
	case TipoNotaDeCredito:
		if letra == "" {
			return 110
		}
		return map[string]int{"A": 112, "B": 113, "C": 114, "M": 119}[letra]
	}
	return 83
}

// nombreAFIP dice en castellano qué comprobante es ese código.
func nombreAFIP(codigo int) string {
	nombres := map[int]string{
		83: "tique", 81: "factura A", 82: "factura B", 111: "factura C", 118: "factura M",
		110: "nota de crédito", 112: "nota de crédito A", 113: "nota de crédito B", 114: "nota de crédito C", 119: "nota de crédito M",
	}
	if n, ok := nombres[codigo]; ok {
		return n
	}
	return fmt.Sprintf("comprobante %d", codigo)
}

// puertoEpson pasa la conexión cargada en el panel a como la nombra la
// librería (ConfigurarPuerto).
func puertoEpson(conexion string) (string, error) {
	c := strings.TrimSpace(conexion)
	switch {
	case c == "":
		return "", errors.New("falta por dónde está conectada la impresora fiscal: USB, un puerto COM o su dirección de red")
	case strings.EqualFold(c, "usb"):
		return "usb:usb", nil
	case strings.HasPrefix(strings.ToUpper(c), "COM"):
		return "serial:" + strings.ToUpper(c), nil
	}
	// Lo demás es una dirección de red (desde el firmware Jano): un equipo y
	// su puerto, y nada más, que es a donde la librería le va a hablar.
	direccion, err := direccionDeRed(c)
	if err != nil {
		return "", err
	}
	return "lan:" + direccion, nil
}

// tasaEpson es el identificador de tasa de IVA de un renglón.
func tasaEpson(it Item) int {
	switch it.Condicion {
	case "exento":
		return 1
	case "no_gravado":
		return 2
	}
	if it.IVA == "" {
		return 0
	}
	tasa := decimal(it.IVA)
	if id, ok := epsonTasas[tasa.FloatString(2)]; ok {
		return id
	}
	centesimas := new(big.Rat).Mul(tasa, big.NewRat(100, 1))
	n, _ := strconv.Atoi(centesimas.FloatString(0))
	return -n
}

// aANSI pasa un texto a la tabla de Windows (1252), que es lo que recibe la
// librería. Lo que no existe en esa tabla va como «?».
func aANSI(s string) []byte {
	salida := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r < 0x80, r >= 0xA0 && r <= 0xFF:
			salida = append(salida, byte(r))
		case r == '€':
			salida = append(salida, 0x80)
		default:
			salida = append(salida, '?')
		}
	}
	return salida
}

// deANSI lee un texto que devolvió la librería: bytes de la tabla de Windows
// hasta el primer cero.
func deANSI(b []byte) string {
	var salida strings.Builder
	for _, c := range b {
		switch {
		case c == 0:
			return strings.TrimSpace(salida.String())
		case c < 0x80, c >= 0xA0:
			salida.WriteRune(rune(c))
		case c == 0x80:
			salida.WriteRune('€')
		default:
			salida.WriteRune('?')
		}
	}
	return strings.TrimSpace(salida.String())
}

// numero lee un entero como lo devuelve la librería («00000345»).
func numero(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// conectar deja el vínculo abierto; quien llama lo cierra con desconectar.
func (e *epson) conectar() error {
	puerto, err := puertoEpson(e.c.Conexion)
	if err != nil {
		return err
	}
	if strings.HasPrefix(puerto, "serial:") && e.c.Velocidad > 0 {
		if err := e.lib.llamar("ConfigurarVelocidad", e.c.Velocidad); err != nil {
			return err
		}
	}
	if err := e.lib.llamar("ConfigurarPuerto", puerto); err != nil {
		return err
	}
	if err := e.lib.llamar("Conectar"); err != nil {
		return fmt.Errorf("no se pudo conectar con la impresora fiscal Epson por %s: %w", e.c.Conexion, err)
	}
	return nil
}

func (e *epson) desconectar() { _ = e.lib.llamar("Desconectar") }

// abrir toma el turno de la librería y conecta. Devuelve cómo soltar todo.
func (e *epson) abrir(ctx context.Context) (func(), error) {
	turnoEpson.Lock()
	if err := ctx.Err(); err != nil {
		turnoEpson.Unlock()
		return nil, err
	}
	if err := e.conectar(); err != nil {
		turnoEpson.Unlock()
		return nil, err
	}
	return func() { e.desconectar(); turnoEpson.Unlock() }, nil
}

// Info: el modelo, el punto de venta y en qué modo está. No imprime nada.
func (e *epson) Info(ctx context.Context) (Equipo, error) {
	soltar, err := e.abrir(ctx)
	if err != nil {
		return Equipo{}, err
	}
	defer soltar()

	modelo, mayor, menor, err := e.lib.version("ConsultarVersionEquipo")
	if err != nil {
		return Equipo{}, err
	}
	equipo := Equipo{Marca: MarcaEpson, Modelo: modelo, Version: fmt.Sprintf("%d.%02d", mayor, menor)}
	// Lo demás suma, pero que falte no quita que el equipo contestó.
	if pv, err := e.lib.texto("ConsultarNumeroPuntoDeVenta"); err == nil {
		equipo.PuntoDeVenta = numero(pv)
	}
	if r, err := e.lib.texto("ConsultarTipoResponsabilidadAnteElIVA"); err == nil {
		equipo.Responsabilidad = r
	}
	if modo, err := e.lib.entero("ConsultarEstado", epsonModo); err == nil && modo == epsonModoEntrenar {
		equipo.Estado = append(equipo.Estado, "Modo entrenamiento: lo que emite no tiene valor fiscal")
	}
	return equipo, nil
}

// Emitir le dicta el comprobante a la librería en el orden de su
// especificación: asociado, cliente, abrir, renglones, ajustes, pagos, cerrar.
func (e *epson) Emitir(ctx context.Context, c Comprobante) (Emitido, error) {
	if err := c.Validar(); err != nil {
		return Emitido{}, err
	}
	soltar, err := e.abrir(ctx)
	if err != nil {
		return Emitido{}, err
	}
	defer soltar()

	esperado := codigoAFIP(c.Tipo, c.Letra)
	pv, err := e.lib.texto("ConsultarNumeroPuntoDeVenta")
	if err != nil {
		return Emitido{}, err
	}
	emitido := Emitido{Tipo: c.Tipo, Letra: c.Letra, PuntoDeVenta: numero(pv)}

	// Un comprobante que quedó abierto de un intento anterior se cancela.
	enCurso, err := e.lib.entero("ConsultarEstado", epsonDocumentoEnCurso)
	if err != nil {
		return Emitido{}, err
	}
	if enCurso != 0 {
		if err := e.lib.llamar("Cancelar"); err != nil {
			return Emitido{}, fmt.Errorf("la impresora fiscal tiene un comprobante abierto de antes y no se pudo cancelar: %w", err)
		}
	}
	antes, err := e.lib.texto("ConsultarNumeroComprobanteUltimo", strconv.Itoa(esperado))
	if err != nil {
		return Emitido{}, err
	}

	if a := c.Asociado; a != nil {
		asociado := fmt.Sprintf("%03d-%05d-%08d", codigoAFIP(a.Tipo, a.Letra), a.PuntoDeVenta, a.Numero)
		if err := e.lib.llamar("CargarComprobanteAsociado", asociado); err != nil {
			return Emitido{}, err
		}
	}
	if cl := c.Cliente; cl != nil {
		documento := cl.Documento
		if cl.TipoDocumento != "pasaporte" {
			documento = soloDigitos(documento)
		}
		err := e.lib.llamar("CargarDatosCliente", recortar(cl.Nombre, largoDatoCliente), "", recortar(cl.Domicilio, largoDatoCliente), "", "",
			epsonDocumentos[cl.TipoDocumento], documento, epsonResponsables[cl.Responsabilidad])
		if err != nil {
			return Emitido{}, err
		}
	}

	abrir := map[string]int{TipoTique: 1, TipoFactura: 2, TipoNotaDeCredito: 3}[c.Tipo]
	if err := e.lib.llamar("AbrirComprobante", abrir); err != nil {
		return Emitido{}, e.cancelarPor(err)
	}
	// La letra la decide el equipo, con su responsabilidad y la del cliente.
	abierto, err := e.lib.texto("ConsultarTipoComprobanteActual")
	if err != nil {
		return Emitido{}, e.cancelarPor(err)
	}
	if numero(abierto) != esperado {
		return Emitido{}, e.cancelarPor(fmt.Errorf("la impresora fiscal abrió una %s y se esperaba una %s: revisá la condición frente al IVA del local y la del cliente", nombreAFIP(numero(abierto)), nombreAFIP(esperado)))
	}
	actual, err := e.lib.texto("ConsultarNumeroComprobanteActual")
	if err != nil {
		return Emitido{}, e.cancelarPor(err)
	}
	emitido.Numero = numero(actual)

	if err := e.dictar(c); err != nil {
		return Emitido{}, e.cancelarPor(err)
	}

	errCierre := e.lib.llamar("CerrarComprobante")
	if errCierre == nil {
		return emitido, nil
	}
	// Se mandó a cerrar y volvió un error: sólo el equipo sabe en qué quedó.
	incierto := fmt.Errorf("%w: se mandó a cerrar el %s nº %d y la impresora fiscal no contestó (%v). Mirá si salió el papel antes de volver a emitirlo", ErrIncierto, nombreDe(c), emitido.Numero, errCierre)
	enCurso, err = e.lib.entero("ConsultarEstado", epsonDocumentoEnCurso)
	if err != nil {
		return Emitido{}, incierto
	}
	if enCurso != 0 {
		return Emitido{}, e.cancelarPor(errCierre)
	}
	despues, err := e.lib.texto("ConsultarNumeroComprobanteUltimo", strconv.Itoa(esperado))
	if err != nil {
		return Emitido{}, incierto
	}
	if numero(despues) == emitido.Numero && emitido.Numero > numero(antes) {
		return emitido, nil
	}
	return Emitido{}, fmt.Errorf("no se emitió el comprobante: %w", errCierre)
}

// dictar manda los renglones, los ajustes y los pagos del comprobante abierto.
func (e *epson) dictar(c Comprobante) error {
	// En los comprobantes que discriminan IVA (A y M) Epson recibe el precio
	// neto y suma el impuesto él; en los demás, el precio final.
	discrimina := c.Letra == "A" || c.Letra == "M"
	for _, it := range c.Items {
		precio := it.Precio
		if discrimina && it.IVA != "" && it.Condicion != "exento" && it.Condicion != "no_gravado" {
			divisor := new(big.Rat).Add(big.NewRat(1, 1), new(big.Rat).Quo(decimal(it.IVA), big.NewRat(100, 1)))
			precio = new(big.Rat).Quo(decimal(it.Precio), divisor).FloatString(4)
		}
		codigo := it.Codigo
		if codigo == "" {
			codigo = "1"
		}
		// 200: agregar ítem de venta. Sin impuestos internos. 1: código interno. 7: unidad.
		if err := e.lib.llamar("ImprimirItem", 200, recortar(it.Descripcion, largoDescripcionItem), it.Cantidad, precio, tasaEpson(it), 0, "", 1, codigo, "", 7); err != nil {
			return err
		}
	}
	for _, a := range c.Ajustes {
		modificador := 400 // descuento
		if a.Recargo {
			modificador = 401 // ajuste
		}
		// Tasa 0: se prorratea entre todas las tasas del comprobante.
		if err := e.lib.llamar("CargarAjuste", modificador, recortar(a.Descripcion, largoDescripcionItem), a.Monto, 0, ""); err != nil {
			return err
		}
	}
	if c.Total != "" {
		bruto, err := e.lib.texto("ConsultarSubTotalBrutoComprobanteActual")
		if err != nil {
			return err
		}
		bruto = strings.TrimSpace(bruto)
		if !importe.MatchString(bruto) || !coincide(decimal(bruto), decimal(c.Total)) {
			return fmt.Errorf("la impresora fiscal sumó %s y la venta es de %s", bruto, c.Total)
		}
	}
	for _, p := range c.Pagos {
		// 200: agregar. Sin cuotas ni cupones.
		if err := e.lib.llamar("CargarPago", 200, epsonMedios[p.Medio], 0, p.Monto, "", recortar(p.Descripcion, largoDescripcionPago), "", ""); err != nil {
			return err
		}
	}
	return nil
}

// cancelarPor cancela lo que esté abierto después de ese error.
func (e *epson) cancelarPor(causa error) error {
	if err := e.lib.llamar("Cancelar"); err != nil {
		return fmt.Errorf("no se emitió el comprobante: %w. Además no se pudo cancelar en la impresora fiscal (se cancela solo antes del próximo)", causa)
	}
	return fmt.Errorf("no se emitió el comprobante: %w", causa)
}

// Cerrar imprime el cierre Z o el X. La librería no devuelve sus totales.
func (e *epson) Cerrar(ctx context.Context, tipo string) (Cierre, error) {
	funcion := map[string]string{CierreZ: "ImprimirCierreZ", CierreX: "ImprimirCierreX"}[tipo]
	if funcion == "" {
		return Cierre{}, fmt.Errorf("no sé hacer un cierre %q: es Z o X", tipo)
	}
	soltar, err := e.abrir(ctx)
	if err != nil {
		return Cierre{}, err
	}
	defer soltar()
	if err := e.lib.llamar(funcion); err != nil {
		return Cierre{}, err
	}
	return Cierre{Tipo: tipo}, nil
}
