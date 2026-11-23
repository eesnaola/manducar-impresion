package fiscal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func tiqueDePrueba() Comprobante {
	return Comprobante{
		Tipo: TipoTique,
		Items: []Item{
			{Descripcion: "Pizza muzzarella", Cantidad: "2", Precio: "8500.00", IVA: "21.00"},
			{Descripcion: "Gaseosa 1,5 l", Cantidad: "1", Precio: "3000.00", IVA: "21.00"},
		},
		Pagos: []Pago{{Descripcion: "Efectivo", Monto: "20000.00", Medio: "efectivo"}},
	}
}

func abrirFalsa(t *testing.T, falsa *hasarFalsa) Driver {
	t.Helper()
	d, err := Abrir(Controlador{Marca: "hasar", Conexion: falsa.direccion()})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// campos devuelve lo que se le mandó al equipo en el enésimo comando con ese nombre.
func (h *hasarFalsa) campos(t *testing.T, nombre string, enesimo int) map[string]string {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	visto := 0
	for i, p := range h.pedidos {
		if p != nombre {
			continue
		}
		if visto == enesimo {
			var sobre map[string]map[string]string
			if err := json.Unmarshal([]byte(h.cuerpos[i]), &sobre); err != nil {
				t.Fatalf("%s no llegó como un objeto de textos: %v\n%s", nombre, err, h.cuerpos[i])
			}
			return sobre[nombre]
		}
		visto++
	}
	t.Fatalf("no le llegó %s nº %d; llegó: %s", nombre, enesimo+1, strings.Join(h.pedidos, ","))
	return nil
}

func (h *hasarFalsa) secuencia() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.pedidos, ",")
}

func igual(t *testing.T, que string, vino, quiero map[string]string) {
	t.Helper()
	if a, b := mustJSON(t, vino), mustJSON(t, quiero); a != b {
		t.Errorf("%s salió\n %s\nquiero\n %s", que, a, b)
	}
}

// Emitir es dictarle el comprobante al equipo, renglón por renglón, en el
// orden que pide Hasar; el número lo pone el equipo al cerrar.
func TestEmitirUnTiqueDictaLosRenglonesYDevuelveElNumero(t *testing.T) {
	falsa := nuevaHasarFalsa(t)
	falsa.ultimo["Tique"] = 344

	emitido, err := abrirFalsa(t, falsa).Emitir(context.Background(), tiqueDePrueba())
	if err != nil {
		t.Fatal(err)
	}

	if quiero := (Emitido{Tipo: TipoTique, PuntoDeVenta: 99998, Numero: 345}); emitido != quiero {
		t.Errorf("emitido %+v, quiero %+v", emitido, quiero)
	}
	if got, quiero := falsa.secuencia(), "ConsultarDatosInicializacion,ConsultarEstado,AbrirDocumento,ImprimirItem,ImprimirItem,ImprimirPago,CerrarDocumento"; got != quiero {
		t.Errorf("la secuencia fue\n %s\nquiero\n %s", got, quiero)
	}
	igual(t, "AbrirDocumento", falsa.campos(t, "AbrirDocumento", 0), map[string]string{"CodigoComprobante": "Tique"})
	// Los precios de Manducar son finales: el IVA va adentro (ModoPrecioTotal).
	igual(t, "el primer renglón", falsa.campos(t, "ImprimirItem", 0), map[string]string{
		"Descripcion": "Pizza muzzarella", "Cantidad": "2", "PrecioUnitario": "8500.00",
		"CondicionIVA": "Gravado", "AlicuotaIVA": "21.00", "OperacionMonto": "ModoSumaMonto",
		"TipoImpuestoInterno": "IIVariableKIVA", "MagnitudImpuestoInterno": "0.00",
		"ModoDisplay": "DisplayNo", "ModoBaseTotal": "ModoPrecioTotal", "UnidadReferencia": "1",
	})
	igual(t, "el pago", falsa.campos(t, "ImprimirPago", 0), map[string]string{
		"Descripcion": "Efectivo", "Monto": "20000.00", "Operacion": "Pagar",
		"ModoDisplay": "DisplayNo", "CodigoFormaPago": "Efectivo",
	})
}

func TestUnaFacturaACargaAlClienteAntesDeAbrir(t *testing.T) {
	falsa := nuevaHasarFalsa(t)
	c := tiqueDePrueba()
	c.Tipo, c.Letra = TipoFactura, "A"
	c.Cliente = &Cliente{Nombre: "ELSIS SRL", Documento: "30-22222222-6", TipoDocumento: "cuit", Responsabilidad: "responsable_inscripto", Domicilio: "Av. Roca 852"}
	c.Pagos = []Pago{{Descripcion: "Visa crédito", Monto: "20000.00", Medio: "credito"}}

	emitido, err := abrirFalsa(t, falsa).Emitir(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}

	if emitido.Tipo != TipoFactura || emitido.Letra != "A" || emitido.Numero != 1 {
		t.Errorf("emitido %+v", emitido)
	}
	if got := falsa.secuencia(); !strings.Contains(got, "ConsultarEstado,CargarDatosCliente,AbrirDocumento,") {
		t.Errorf("el cliente se carga antes de abrir; la secuencia fue %s", got)
	}
	igual(t, "el cliente", falsa.campos(t, "CargarDatosCliente", 0), map[string]string{
		"RazonSocial": "ELSIS SRL", "NumeroDocumento": "30222222226",
		"ResponsabilidadIVA": "ResponsableInscripto", "TipoDocumento": "TipoCUIT", "Domicilio": "Av. Roca 852",
	})
	igual(t, "AbrirDocumento", falsa.campos(t, "AbrirDocumento", 0), map[string]string{"CodigoComprobante": "TiqueFacturaA"})
	if medio := falsa.campos(t, "ImprimirPago", 0)["CodigoFormaPago"]; medio != "TarjetaDeCredito" {
		t.Errorf("el medio de pago salió %q", medio)
	}
}

func TestUnaNotaDeCreditoNombraElComprobanteQueAnula(t *testing.T) {
	falsa := nuevaHasarFalsa(t)
	c := tiqueDePrueba()
	c.Tipo, c.Letra = TipoNotaDeCredito, "B"
	c.Cliente = &Cliente{Nombre: "Juana Pérez", Documento: "30111222", TipoDocumento: "dni", Responsabilidad: "consumidor_final"}
	c.Asociado = &Asociado{Tipo: TipoFactura, Letra: "B", PuntoDeVenta: 99998, Numero: 345, Fecha: "2026-11-20"}

	emitido, err := abrirFalsa(t, falsa).Emitir(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}

	if emitido.Tipo != TipoNotaDeCredito || emitido.Letra != "B" {
		t.Errorf("emitido %+v", emitido)
	}
	if got := falsa.secuencia(); !strings.Contains(got, "ConsultarEstado,CargarDocumentoAsociado,CargarDatosCliente,AbrirDocumento,") {
		t.Errorf("el comprobante asociado se carga antes de abrir; la secuencia fue %s", got)
	}
	igual(t, "el asociado", falsa.campos(t, "CargarDocumentoAsociado", 0), map[string]string{
		"NumeroLinea": "1", "CodigoComprobante": "TiqueFacturaB", "NumeroPos": "99998",
		"NumeroComprobante": "00000345", "FechaComprobante": "261120",
	})
	igual(t, "AbrirDocumento", falsa.campos(t, "AbrirDocumento", 0), map[string]string{"CodigoComprobante": "TiqueNotaCreditoB"})

	// La de un tique (sin letra) es la «tique nota de crédito» a secas.
	otra := nuevaHasarFalsa(t)
	c.Letra, c.Cliente = "", nil
	c.Asociado = &Asociado{Tipo: TipoTique, PuntoDeVenta: 99998, Numero: 12, Fecha: "2026-11-20"}
	if _, err := abrirFalsa(t, otra).Emitir(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if abrio := otra.campos(t, "AbrirDocumento", 0)["CodigoComprobante"]; abrio != "TiqueNotaCredito" {
		t.Errorf("abrió %q", abrio)
	}
	if asociado := otra.campos(t, "CargarDocumentoAsociado", 0)["CodigoComprobante"]; asociado != "Tique" {
		t.Errorf("asoció %q", asociado)
	}
}

func TestUnDescuentoVaComoAjusteDespuesDeLosRenglones(t *testing.T) {
	falsa := nuevaHasarFalsa(t)
	c := tiqueDePrueba()
	c.Ajustes = []Ajuste{{Descripcion: "Descuento efectivo", Monto: "2000.00"}, {Descripcion: "Servicio de mesa", Monto: "500.00", Recargo: true}}

	if _, err := abrirFalsa(t, falsa).Emitir(context.Background(), c); err != nil {
		t.Fatal(err)
	}

	if got := falsa.secuencia(); !strings.Contains(got, "ImprimirItem,ImprimirAjuste,ImprimirAjuste,ImprimirPago") {
		t.Errorf("los ajustes van entre los renglones y los pagos; la secuencia fue %s", got)
	}
	igual(t, "el descuento", falsa.campos(t, "ImprimirAjuste", 0), map[string]string{
		"Descripcion": "Descuento efectivo", "Monto": "2000.00", "ModoDisplay": "DisplayNo",
		"ModoBaseTotal": "ModoPrecioTotal", "Operacion": "AjusteNeg",
	})
	if op := falsa.campos(t, "ImprimirAjuste", 1)["Operacion"]; op != "AjustePos" {
		t.Errorf("el recargo salió %q", op)
	}
}

// El equipo corta la descripción en 34 caracteres: se la recorta acá para que
// no rechace el renglón, sin partir una letra con tilde por la mitad.
func TestLaDescripcionSeRecortaAlLargoQueAceptaElEquipo(t *testing.T) {
	falsa := nuevaHasarFalsa(t)
	c := tiqueDePrueba()
	c.Items = []Item{{Descripcion: "Milanesa napolitana con papas fritas y ensalada mixta de estación", Cantidad: "1", Precio: "100.00", IVA: "21.00"}}

	if _, err := abrirFalsa(t, falsa).Emitir(context.Background(), c); err != nil {
		t.Fatal(err)
	}

	descripcion := falsa.campos(t, "ImprimirItem", 0)["Descripcion"]
	if largo := len([]rune(descripcion)); largo != 34 || !strings.HasPrefix("Milanesa napolitana con papas fritas y ensalada mixta de estación", descripcion) {
		t.Errorf("salió %q (%d caracteres)", descripcion, largo)
	}
}

// Si el equipo rechaza algo con el comprobante abierto, se cancela: no puede
// quedar un comprobante a medias esperando al próximo.
func TestSiUnRenglonFallaElComprobanteSeCancela(t *testing.T) {
	falsa := nuevaHasarFalsa(t)
	falsa.falla["ImprimirPago"] = "Falta papel"

	_, err := abrirFalsa(t, falsa).Emitir(context.Background(), tiqueDePrueba())

	if err == nil || !strings.Contains(err.Error(), "Falta papel") || !strings.Contains(err.Error(), "no se emitió") {
		t.Fatalf("quiero el motivo del equipo y que diga que no se emitió, vino: %v", err)
	}
	if errors.Is(err, ErrIncierto) {
		t.Error("se sabe que no salió: no es incierto")
	}
	if got := falsa.secuencia(); !strings.HasSuffix(got, "ImprimirPago,Cancelar") {
		t.Errorf("después del rechazo se cancela; la secuencia fue %s", got)
	}
	if falsa.enCurso != "" || falsa.ultimo["Tique"] != 0 {
		t.Errorf("quedó abierto %q, último %d", falsa.enCurso, falsa.ultimo["Tique"])
	}
}

func TestSiQuedoUnComprobanteAbiertoDeAntesLoCancelaYEmite(t *testing.T) {
	falsa := nuevaHasarFalsa(t)
	falsa.enCurso = "TiqueFacturaB"

	emitido, err := abrirFalsa(t, falsa).Emitir(context.Background(), tiqueDePrueba())
	if err != nil {
		t.Fatal(err)
	}

	if emitido.Numero != 1 {
		t.Errorf("emitido %+v", emitido)
	}
	if got := falsa.secuencia(); !strings.HasPrefix(got, "ConsultarDatosInicializacion,ConsultarEstado,Cancelar,AbrirDocumento,") {
		t.Errorf("la secuencia fue %s", got)
	}
}

// Lo peor que puede pasar: se mandó a cerrar y la respuesta no llegó. El
// comprobante puede haber salido o no. Se le pregunta al equipo.
func TestSiSePierdeLaRespuestaDelCierreSeLePreguntaAlEquipoSiSalio(t *testing.T) {
	falsa := nuevaHasarFalsa(t)
	falsa.ultimo["Tique"] = 9
	falsa.corta["CerrarDocumento"] = true

	emitido, err := abrirFalsa(t, falsa).Emitir(context.Background(), tiqueDePrueba())

	if err != nil {
		t.Fatalf("el equipo lo emitió y lo dice su estado: no tenía que fallar, vino %v", err)
	}
	if emitido.Numero != 10 {
		t.Errorf("emitido %+v, quiero el número 10", emitido)
	}
	if got := falsa.secuencia(); !strings.HasSuffix(got, "CerrarDocumento,ConsultarEstado") {
		t.Errorf("la secuencia fue %s", got)
	}
}

func TestSiNoSePuedeSaberSiSalioElErrorEsIncierto(t *testing.T) {
	falsa := nuevaHasarFalsa(t)
	falsa.ultimo["Tique"] = 9
	// Se cae la red justo al cerrar: ni la respuesta ni la consulta de después llegan.
	falsa.corta["CerrarDocumento"] = true
	falsa.apagarAlCortar = true

	_, err := abrirFalsa(t, falsa).Emitir(context.Background(), tiqueDePrueba())

	if !errors.Is(err, ErrIncierto) {
		t.Fatalf("no se sabe si salió: quiero ErrIncierto, vino %v", err)
	}
	if !strings.Contains(err.Error(), "10") {
		t.Errorf("el error tiene que decir qué número iba a ser (10), vino: %v", err)
	}
}

// Un equipo de otro contribuyente en esa dirección: no se le dicta nada.
func TestSiElEquipoEsDeOtroCuitNoSeEmite(t *testing.T) {
	falsa := nuevaHasarFalsa(t)
	c := tiqueDePrueba()
	c.CUIT = "20-11111111-2"

	_, err := abrirFalsa(t, falsa).Emitir(context.Background(), c)

	if err == nil || !strings.Contains(err.Error(), "30123456781") || !strings.Contains(err.Error(), "20111111112") {
		t.Fatalf("quiero los dos CUIT en el error, vino: %v", err)
	}
	if got := falsa.secuencia(); got != "ConsultarDatosInicializacion" {
		t.Errorf("no tenía que abrir nada; la secuencia fue %s", got)
	}

	c.CUIT = "30-12345678-1"
	if _, err := abrirFalsa(t, falsa).Emitir(context.Background(), c); err != nil {
		t.Errorf("con el CUIT del equipo tenía que emitir: %v", err)
	}
}

func TestUnComprobanteMalArmadoNoLlegaAlEquipo(t *testing.T) {
	falsa := nuevaHasarFalsa(t)
	casos := map[string]func(*Comprobante){
		"sin renglones":            func(c *Comprobante) { c.Items = nil },
		"precio que no es número":  func(c *Comprobante) { c.Items[0].Precio = "ocho mil" },
		"cantidad negativa":        func(c *Comprobante) { c.Items[0].Cantidad = "-1" },
		"tipo desconocido":         func(c *Comprobante) { c.Tipo = "remito" },
		"factura sin letra":        func(c *Comprobante) { c.Tipo = TipoFactura },
		"factura A sin cliente":    func(c *Comprobante) { c.Tipo, c.Letra = TipoFactura, "A" },
		"factura B sin cliente":    func(c *Comprobante) { c.Tipo, c.Letra = TipoFactura, "B" },
		"total que no es importe":  func(c *Comprobante) { c.Total = "veinte mil" },
		"nota de crédito sin nada": func(c *Comprobante) { c.Tipo = TipoNotaDeCredito },
		"medio de pago inventado":  func(c *Comprobante) { c.Pagos[0].Medio = "trueque" },
	}
	for nombre, romper := range casos {
		c := tiqueDePrueba()
		romper(&c)
		if _, err := abrirFalsa(t, falsa).Emitir(context.Background(), c); err == nil {
			t.Errorf("%s: tenía que rechazarlo", nombre)
		}
	}
	if got := falsa.secuencia(); got != "" {
		t.Errorf("nada de eso tenía que llegarle al equipo; llegó %s", got)
	}
}

// El total lo calcula el equipo con lo que se le dictó. Si no da lo que cobró
// el local, no se cierra: un comprobante por otro importe no se arregla.
func TestSiElEquipoSumaOtroTotalElComprobanteSeCancela(t *testing.T) {
	falsa := nuevaHasarFalsa(t)
	c := tiqueDePrueba()
	c.Total = "19000.00" // los renglones suman 20000

	_, err := abrirFalsa(t, falsa).Emitir(context.Background(), c)

	if err == nil || !strings.Contains(err.Error(), "20000.00") || !strings.Contains(err.Error(), "19000.00") {
		t.Fatalf("quiero los dos totales en el error, vino: %v", err)
	}
	if got := falsa.secuencia(); !strings.HasSuffix(got, "ImprimirItem,ConsultarSubtotal,Cancelar") {
		t.Errorf("se consulta antes de los pagos y se cancela; la secuencia fue %s", got)
	}

	c.Total = "20000.00"
	if _, err := abrirFalsa(t, falsa).Emitir(context.Background(), c); err != nil {
		t.Errorf("con el total que da tenía que emitir: %v", err)
	}
	igual(t, "ConsultarSubtotal", falsa.campos(t, "ConsultarSubtotal", 1), map[string]string{"Impresion": "NoImprimeSubtotal", "ModoDisplay": "DisplayNo"})
}

func TestElCierreZDevuelveElNumeroYLosTotales(t *testing.T) {
	falsa := nuevaHasarFalsa(t)

	cierre, err := abrirFalsa(t, falsa).Cerrar(context.Background(), CierreZ)
	if err != nil {
		t.Fatal(err)
	}

	if quiero := (Cierre{Tipo: CierreZ, Numero: 40, Fecha: "2015-01-30", Total: "780.00", Comprobantes: 3, TotalNotasDeCredito: "0.00", NotasDeCredito: 0}); cierre != quiero {
		t.Errorf("cierre %+v\nquiero %+v", cierre, quiero)
	}
	igual(t, "CerrarJornadaFiscal", falsa.campos(t, "CerrarJornadaFiscal", 0), map[string]string{"Reporte": "ReporteZ"})

	x, err := abrirFalsa(t, falsa).Cerrar(context.Background(), CierreX)
	if err != nil {
		t.Fatal(err)
	}
	if x.Tipo != CierreX || x.Numero != 12 || x.Total != "780.00" || x.Fecha != "2015-01-30" {
		t.Errorf("cierre X %+v", x)
	}
	if _, err := abrirFalsa(t, falsa).Cerrar(context.Background(), "W"); err == nil {
		t.Error("un cierre que no es Z ni X no se manda")
	}
}
