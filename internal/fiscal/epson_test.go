package fiscal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// epsonFalsa hace de librería de Epson con una impresora conectada: anota
// cada llamada como la escribiría un programador y lleva lo justo del estado
// del equipo para emitir. Los códigos y los identificadores son los de la
// especificación de EpsonFiscalInterface 3.1.0.
type epsonFalsa struct {
	llamadas     []string
	falla        map[string]string // función → lo que dice la librería al fallar
	corta        map[string]bool   // la hace, pero vuelve con error (se perdió la respuesta)
	mudaAlCortar bool              // después del corte no contesta nada más
	muda         bool
	emisor       string // "I" responsable inscripto, "M" monotributista
	puntoDeVenta int
	modo         int // 1015: 2 entrenamiento, 3 fiscal
	enCurso      int // 1003: 0 nada, 1 tique, 2 tique factura, 3 tique nota de crédito con letra
	codigo       int // código AFIP del comprobante abierto
	numero       int
	ultimo       map[int]int
	cliente      int // id_responsabilidad_iva cargado; 0 si no hay
	bruto        float64
}

func nuevaEpsonFalsa() *epsonFalsa {
	return &epsonFalsa{falla: map[string]string{}, corta: map[string]bool{}, emisor: "I", puntoDeVenta: 7, modo: 3, ultimo: map[int]int{}}
}

func (f *epsonFalsa) anotar(nombre string, args []any) error {
	partes := make([]string, len(args))
	for i, a := range args {
		if s, ok := a.(string); ok {
			partes[i] = strconv.Quote(s)
		} else {
			partes[i] = fmt.Sprint(a)
		}
	}
	f.llamadas = append(f.llamadas, nombre+"("+strings.Join(partes, ", ")+")")
	if f.muda {
		return errors.New(nombre + ": Error de comunicación (0x02000001)")
	}
	if motivo, ok := f.falla[nombre]; ok {
		return errors.New(nombre + ": " + motivo)
	}
	return nil
}

func (f *epsonFalsa) cortar(nombre string) error {
	if !f.corta[nombre] {
		return nil
	}
	f.muda = f.mudaAlCortar
	return errors.New(nombre + ": Error de comunicación (0x02000001)")
}

func (f *epsonFalsa) llamar(nombre string, args ...any) error {
	if err := f.anotar(nombre, args); err != nil {
		return err
	}
	conLetraA := f.cliente == 1 || f.cliente == 4
	switch nombre {
	case "Cancelar":
		f.enCurso, f.cliente = 0, 0
	case "CargarDatosCliente":
		f.cliente = args[7].(int)
	case "AbrirComprobante":
		f.enCurso = args[0].(int)
		switch {
		case f.enCurso == 1:
			f.codigo = 83
		case f.enCurso == 2 && f.emisor == "M":
			f.codigo = 111
		case f.enCurso == 2 && conLetraA:
			f.codigo = 81
		case f.enCurso == 2:
			f.codigo = 82
		case f.cliente == 0:
			f.codigo, f.enCurso = 110, 1 // la nota de crédito de un tique
		case f.emisor == "M":
			f.codigo = 114
		case conLetraA:
			f.codigo = 112
		default:
			f.codigo = 113
		}
		f.numero, f.bruto = f.ultimo[f.codigo]+1, 0
	case "ImprimirItem":
		cantidad, _ := strconv.ParseFloat(args[2].(string), 64)
		precio, _ := strconv.ParseFloat(args[3].(string), 64)
		if f.codigo == 81 || f.codigo == 112 {
			// En una «A» el precio que recibe es el neto: el IVA lo suma el equipo.
			tasas := map[int]float64{4: 10.5, 5: 21, 6: 27}
			precio *= 1 + tasas[args[4].(int)]/100
		}
		f.bruto += cantidad * precio
	case "CargarAjuste":
		monto, _ := strconv.ParseFloat(args[2].(string), 64)
		if args[0].(int) == 400 {
			monto = -monto
		}
		f.bruto += monto
	case "CerrarComprobante":
		f.ultimo[f.codigo] = f.numero
		f.enCurso, f.cliente = 0, 0
	}
	return f.cortar(nombre)
}

func (f *epsonFalsa) texto(nombre string, args ...any) (string, error) {
	if err := f.anotar(nombre, args); err != nil {
		return "", err
	}
	switch nombre {
	case "ConsultarNumeroPuntoDeVenta":
		return fmt.Sprintf("%05d", f.puntoDeVenta), nil
	case "ConsultarTipoResponsabilidadAnteElIVA":
		return f.emisor, nil
	case "ConsultarNumeroComprobanteUltimo":
		codigo, _ := strconv.Atoi(args[0].(string))
		return fmt.Sprintf("%08d", f.ultimo[codigo]), nil
	case "ConsultarNumeroComprobanteActual":
		return fmt.Sprintf("%08d", f.numero), nil
	case "ConsultarTipoComprobanteActual":
		return strconv.Itoa(f.codigo), nil
	case "ConsultarSubTotalBrutoComprobanteActual":
		return fmt.Sprintf("%.2f", f.bruto), nil
	}
	return "", nil
}

func (f *epsonFalsa) entero(nombre string, args ...any) (int, error) {
	if err := f.anotar(nombre, args); err != nil {
		return 0, err
	}
	switch args[0].(int) {
	case 1003:
		return f.enCurso, nil
	case 1015:
		return f.modo, nil
	}
	return 0, nil
}

func (f *epsonFalsa) version(nombre string) (string, int, int, error) {
	if err := f.anotar(nombre, nil); err != nil {
		return "", 0, 0, err
	}
	return "TM-T900FA", 3, 1, nil
}

func (f *epsonFalsa) llamo(prefijo string) string {
	for _, l := range f.llamadas {
		if strings.HasPrefix(l, prefijo+"(") {
			return l
		}
	}
	return ""
}

// nombres devuelve sólo los nombres de las funciones llamadas, en orden.
func (f *epsonFalsa) nombres() string {
	n := make([]string, len(f.llamadas))
	for i, l := range f.llamadas {
		n[i] = l[:strings.Index(l, "(")]
	}
	return strings.Join(n, ",")
}

func epsonCon(f *epsonFalsa, conexion string, velocidad int) Driver {
	return &epson{c: Controlador{Marca: "epson", Conexion: conexion, Velocidad: velocidad}, lib: f}
}

func TestInfoDeUnaEpsonDiceElModeloElPuntoDeVentaYElModo(t *testing.T) {
	f := nuevaEpsonFalsa()
	f.modo = 2

	equipo, err := epsonCon(f, "COM3", 9600).Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	quiero := Equipo{Marca: "epson", Modelo: "TM-T900FA", Version: "3.01", PuntoDeVenta: 7, Responsabilidad: "I", Estado: []string{"Modo entrenamiento: lo que emite no tiene valor fiscal"}}
	if a, b := mustJSON(t, equipo), mustJSON(t, quiero); a != b {
		t.Errorf("el equipo salió\n %s\nquiero\n %s", a, b)
	}
	if got := strings.Join(f.llamadas[:3], " "); got != `ConfigurarVelocidad(9600) ConfigurarPuerto("serial:COM3") Conectar()` {
		t.Errorf("la conexión fue: %s", got)
	}
	if ultima := f.llamadas[len(f.llamadas)-1]; ultima != "Desconectar()" {
		t.Errorf("tiene que soltar el puerto al terminar; lo último fue %s", ultima)
	}
}

func TestLaConexionDeEpsonSeNombraComoLaPideLaLibreria(t *testing.T) {
	for escrita, quiero := range map[string]string{"usb": "usb:usb", "USB": "usb:usb", "com4": "serial:COM4", "192.168.1.60": "lan:192.168.1.60"} {
		if got, err := puertoEpson(escrita); err != nil || got != quiero {
			t.Errorf("%q → %q (%v), quiero %q", escrita, got, err, quiero)
		}
	}
	if _, err := puertoEpson(" "); err == nil {
		t.Error("sin conexión no hay por dónde hablarle")
	}
}

func TestEpsonEmiteUnTiqueYDevuelveElNumero(t *testing.T) {
	f := nuevaEpsonFalsa()
	f.ultimo[83] = 344

	emitido, err := epsonCon(f, "usb", 0).Emitir(context.Background(), tiqueDePrueba())
	if err != nil {
		t.Fatal(err)
	}

	if quiero := (Emitido{Tipo: TipoTique, PuntoDeVenta: 7, Numero: 345}); emitido != quiero {
		t.Errorf("emitido %+v, quiero %+v", emitido, quiero)
	}
	quiero := "ConfigurarPuerto,Conectar,ConsultarNumeroPuntoDeVenta,ConsultarEstado,ConsultarNumeroComprobanteUltimo," +
		"AbrirComprobante,ConsultarTipoComprobanteActual,ConsultarNumeroComprobanteActual," +
		"ImprimirItem,ImprimirItem,CargarPago,CerrarComprobante,Desconectar"
	if got := f.nombres(); got != quiero {
		t.Errorf("la secuencia fue\n %s\nquiero\n %s", got, quiero)
	}
	if got, quiero := f.llamo("ImprimirItem"), `ImprimirItem(200, "Pizza muzzarella", "2", "8500.00", 5, 0, "", 1, "1", "", 7)`; got != quiero {
		t.Errorf("el renglón fue\n %s\nquiero\n %s", got, quiero)
	}
	if got, quiero := f.llamo("CargarPago"), `CargarPago(200, 8, 0, "20000.00", "", "Efectivo", "", "")`; got != quiero {
		t.Errorf("el pago fue\n %s\nquiero\n %s", got, quiero)
	}
	if got := f.llamo("AbrirComprobante"); got != "AbrirComprobante(1)" {
		t.Errorf("abrió con %s", got)
	}
}

// En una «A» Epson recibe el precio sin IVA y lo suma él. El total que le da
// se compara con el de la venta antes de cerrar.
func TestEpsonEnUnaFacturaAMandaElNetoYControlaElTotal(t *testing.T) {
	f := nuevaEpsonFalsa()
	c := tiqueDePrueba()
	c.Tipo, c.Letra, c.Total = TipoFactura, "A", "20000.00"
	c.Cliente = &Cliente{Nombre: "ELSIS SRL", Documento: "30-22222222-6", TipoDocumento: "cuit", Responsabilidad: "responsable_inscripto", Domicilio: "Av. Roca 852"}

	emitido, err := epsonCon(f, "usb", 0).Emitir(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}

	if emitido.Letra != "A" || emitido.Numero != 1 {
		t.Errorf("emitido %+v", emitido)
	}
	if got, quiero := f.llamo("CargarDatosCliente"), `CargarDatosCliente("ELSIS SRL", "", "Av. Roca 852", "", "", 3, "30222222226", 1)`; got != quiero {
		t.Errorf("el cliente fue\n %s\nquiero\n %s", got, quiero)
	}
	if got := f.llamo("AbrirComprobante"); got != "AbrirComprobante(2)" {
		t.Errorf("abrió con %s", got)
	}
	if got, quiero := f.llamo("ImprimirItem"), `ImprimirItem(200, "Pizza muzzarella", "2", "7024.7934", 5, 0, "", 1, "1", "", 7)`; got != quiero {
		t.Errorf("el renglón fue\n %s\nquiero\n %s", got, quiero)
	}
	if got := f.nombres(); !strings.Contains(got, "ImprimirItem,ConsultarSubTotalBrutoComprobanteActual,CargarPago") {
		t.Errorf("el total se controla antes de los pagos; la secuencia fue %s", got)
	}
}

// La letra la decide el equipo con su responsabilidad y la del cliente. Si
// abre otra que la que esperaba el servidor, no se sigue.
func TestEpsonSiElEquipoAbreOtraLetraSeCancela(t *testing.T) {
	f := nuevaEpsonFalsa()
	f.emisor = "M" // el equipo es de un monotributista: abre una C
	c := tiqueDePrueba()
	c.Tipo, c.Letra = TipoFactura, "B"
	c.Cliente = &Cliente{Nombre: "Juana Pérez", Documento: "30111222", TipoDocumento: "dni", Responsabilidad: "consumidor_final"}

	_, err := epsonCon(f, "usb", 0).Emitir(context.Background(), c)

	if err == nil || !strings.Contains(err.Error(), "no se emitió") || !strings.Contains(err.Error(), "factura C") {
		t.Fatalf("quiero que diga que abrió una factura C y que no se emitió, vino: %v", err)
	}
	if got := f.nombres(); !strings.HasSuffix(got, "ConsultarTipoComprobanteActual,Cancelar,Desconectar") {
		t.Errorf("la secuencia fue %s", got)
	}
}

func TestEpsonUnaNotaDeCreditoNombraElComprobanteQueAnula(t *testing.T) {
	f := nuevaEpsonFalsa()
	c := tiqueDePrueba()
	c.Tipo, c.Letra = TipoNotaDeCredito, "B"
	c.Cliente = &Cliente{Nombre: "Juana Pérez", Documento: "30111222", TipoDocumento: "dni", Responsabilidad: "consumidor_final"}
	c.Asociado = &Asociado{Tipo: TipoFactura, Letra: "B", PuntoDeVenta: 7, Numero: 345, Fecha: "2026-11-20"}

	emitido, err := epsonCon(f, "usb", 0).Emitir(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}

	if emitido.Tipo != TipoNotaDeCredito || emitido.Letra != "B" || emitido.Numero != 1 {
		t.Errorf("emitido %+v", emitido)
	}
	if got := f.llamo("CargarComprobanteAsociado"); got != `CargarComprobanteAsociado("082-00007-00000345")` {
		t.Errorf("el asociado fue %s", got)
	}
	if got := f.llamo("AbrirComprobante"); got != "AbrirComprobante(3)" {
		t.Errorf("abrió con %s", got)
	}

	// La de un tique: sin cliente, y el asociado es el tique (83).
	g := nuevaEpsonFalsa()
	c.Letra, c.Cliente = "", nil
	c.Asociado = &Asociado{Tipo: TipoTique, PuntoDeVenta: 7, Numero: 12, Fecha: "2026-11-20"}
	if _, err := epsonCon(g, "usb", 0).Emitir(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if got := g.llamo("CargarComprobanteAsociado"); got != `CargarComprobanteAsociado("083-00007-00000012")` {
		t.Errorf("el asociado fue %s", got)
	}
}

func TestEpsonLosAjustesVanConSuSigno(t *testing.T) {
	f := nuevaEpsonFalsa()
	c := tiqueDePrueba()
	c.Ajustes = []Ajuste{{Descripcion: "Descuento efectivo", Monto: "2000.00"}, {Descripcion: "Servicio de mesa", Monto: "500.00", Recargo: true}}
	c.Total = "18500.00"

	if _, err := epsonCon(f, "usb", 0).Emitir(context.Background(), c); err != nil {
		t.Fatal(err)
	}

	if got, quiero := f.llamo("CargarAjuste"), `CargarAjuste(400, "Descuento efectivo", "2000.00", 0, "")`; got != quiero {
		t.Errorf("el descuento fue %s", got)
	}
	if !strings.Contains(strings.Join(f.llamadas, " "), `CargarAjuste(401, "Servicio de mesa", "500.00", 0, "")`) {
		t.Errorf("falta el recargo: %v", f.llamadas)
	}
}

func TestEpsonSiUnRenglonFallaElComprobanteSeCancela(t *testing.T) {
	f := nuevaEpsonFalsa()
	f.falla["CargarPago"] = "Falta papel (0x09000003)"

	_, err := epsonCon(f, "usb", 0).Emitir(context.Background(), tiqueDePrueba())

	if err == nil || !strings.Contains(err.Error(), "Falta papel") || !strings.Contains(err.Error(), "no se emitió") || errors.Is(err, ErrIncierto) {
		t.Fatalf("vino: %v", err)
	}
	if got := f.nombres(); !strings.HasSuffix(got, "CargarPago,Cancelar,Desconectar") {
		t.Errorf("la secuencia fue %s", got)
	}
	if f.enCurso != 0 || f.ultimo[83] != 0 {
		t.Errorf("quedó abierto %d, último %d", f.enCurso, f.ultimo[83])
	}
}

func TestEpsonSiTotalNoCoincideSeCancela(t *testing.T) {
	f := nuevaEpsonFalsa()
	c := tiqueDePrueba()
	c.Total = "19000.00"

	_, err := epsonCon(f, "usb", 0).Emitir(context.Background(), c)

	if err == nil || !strings.Contains(err.Error(), "20000.00") || !strings.Contains(err.Error(), "19000.00") {
		t.Fatalf("quiero los dos totales en el error, vino: %v", err)
	}
	if f.ultimo[83] != 0 {
		t.Error("no tenía que cerrarse")
	}
}

func TestEpsonSiQuedoUnComprobanteAbiertoDeAntesLoCancela(t *testing.T) {
	f := nuevaEpsonFalsa()
	f.enCurso = 2

	if _, err := epsonCon(f, "usb", 0).Emitir(context.Background(), tiqueDePrueba()); err != nil {
		t.Fatal(err)
	}

	if got := f.nombres(); !strings.Contains(got, "ConsultarEstado,Cancelar,ConsultarNumeroComprobanteUltimo,AbrirComprobante") {
		t.Errorf("la secuencia fue %s", got)
	}
}

func TestEpsonSiSePierdeLaRespuestaDelCierreSeLePreguntaAlEquipo(t *testing.T) {
	f := nuevaEpsonFalsa()
	f.ultimo[83] = 9
	f.corta["CerrarComprobante"] = true

	emitido, err := epsonCon(f, "usb", 0).Emitir(context.Background(), tiqueDePrueba())

	if err != nil || emitido.Numero != 10 {
		t.Fatalf("el equipo lo emitió: quiero el número 10 y sin error, vino %+v, %v", emitido, err)
	}

	g := nuevaEpsonFalsa()
	g.ultimo[83] = 9
	g.corta["CerrarComprobante"], g.mudaAlCortar = true, true
	_, err = epsonCon(g, "usb", 0).Emitir(context.Background(), tiqueDePrueba())
	if !errors.Is(err, ErrIncierto) || !strings.Contains(err.Error(), "10") {
		t.Fatalf("no se puede saber si salió: quiero ErrIncierto con el número 10, vino %v", err)
	}
}

func TestEpsonCierreZYX(t *testing.T) {
	f := nuevaEpsonFalsa()

	z, err := epsonCon(f, "usb", 0).Cerrar(context.Background(), CierreZ)
	if err != nil || z.Tipo != CierreZ {
		t.Fatalf("cierre %+v, %v", z, err)
	}
	if _, err := epsonCon(f, "usb", 0).Cerrar(context.Background(), CierreX); err != nil {
		t.Fatal(err)
	}
	if got := f.nombres(); got != "ConfigurarPuerto,Conectar,ImprimirCierreZ,Desconectar,ConfigurarPuerto,Conectar,ImprimirCierreX,Desconectar" {
		t.Errorf("la secuencia fue %s", got)
	}
	if _, err := epsonCon(f, "usb", 0).Cerrar(context.Background(), "W"); err == nil {
		t.Error("un cierre que no es Z ni X no se manda")
	}
}

func TestLasTasasDeIvaDeEpson(t *testing.T) {
	casos := []struct {
		item   Item
		quiero int
	}{
		{Item{IVA: "21.00"}, 5}, {Item{IVA: "21"}, 5}, {Item{IVA: "10.50"}, 4}, {Item{IVA: "27.00"}, 6},
		{Item{IVA: "0.00"}, 3}, {Item{Condicion: "exento"}, 1}, {Item{Condicion: "no_gravado"}, 2},
		{Item{}, 0}, {Item{IVA: "5.00"}, -500}, {Item{IVA: "2.50"}, -250},
	}
	for _, c := range casos {
		if got := tasaEpson(c.item); got != c.quiero {
			t.Errorf("%+v → %d, quiero %d", c.item, got, c.quiero)
		}
	}
}

// La librería recibe texto ANSI: lo que no existe en esa tabla no puede
// viajar, y no puede romper el renglón.
func TestElTextoParaEpsonVaEnAnsi(t *testing.T) {
	if got := string(aANSI("Ñoquis añejos")); got != "\xd1oquis a\xf1ejos" {
		t.Errorf("salió %q", got)
	}
	if got := string(aANSI("Café ☕ 2€")); got != "Caf\xe9 ? 2\x80" {
		t.Errorf("salió %q", got)
	}
	// Y de vuelta: lo que contesta la librería llega en esa tabla, hasta el primer cero.
	if got := deANSI([]byte("  Falta papel en la estaci\xf3n \x00basura")); got != "Falta papel en la estación" {
		t.Errorf("salió %q", got)
	}
}

// La librería de Epson no viene firmada: lo único que dice que es la de Epson
// es dónde está. Por eso sólo se la busca donde no escribe cualquiera —la
// carpeta del propio agente y las de programas instalados— o donde la indicó
// a mano quien configuró la computadora. Buscarla también en las carpetas
// públicas o en la raíz del disco era cargar la primera que alguien dejara
// ahí con ese nombre.
func TestLaLibreriaDeEpsonSoloSeBuscaEnLugaresDeConfianza(t *testing.T) {
	programas, ajena, delAgente, aMano := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	crear := func(partes ...string) string {
		t.Helper()
		ruta := filepath.Join(append(partes, dllEpson)...)
		if err := os.MkdirAll(filepath.Dir(ruta), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ruta, []byte("MZ"), 0o644); err != nil {
			t.Fatal(err)
		}
		return ruta
	}
	instalada := crear(programas, "EPSON", "EpsonFiscalInterface", "x64")
	alLado := crear(delAgente)
	indicada := crear(aMano)
	crear(ajena, "Epson Fiscal")  // plantada donde escribe cualquiera
	crear(programas, "Otra cosa") // no nombra a Epson ni a lo fiscal: ni se recorre

	if got, quiero := candidatasEpsonEn("", delAgente, []string{programas}), []string{alLado, instalada}; !iguales(got, quiero) {
		t.Errorf("candidatas\n %v\nquiero\n %v", got, quiero)
	}
	// La indicada a mano va primero.
	if got, quiero := candidatasEpsonEn(indicada, delAgente, []string{programas}), []string{indicada, alLado, instalada}; !iguales(got, quiero) {
		t.Errorf("con la indicada\n %v\nquiero\n %v", got, quiero)
	}
	// Una ruta relativa no se toma: se resolvería contra la carpeta desde la que arrancó el agente.
	if got, quiero := candidatasEpsonEn(dllEpson, delAgente, nil), []string{alLado}; !iguales(got, quiero) {
		t.Errorf("con una relativa\n %v\nquiero\n %v", got, quiero)
	}
}

func iguales(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
