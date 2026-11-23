package fiscal

import (
	"context"
	"errors"
	"fmt"
	"strconv"
)

// Los comandos van con sus campos en el orden del Manual de Comandos y todos
// como texto, igual que en los ejemplos de Hasar. Las enumeraciones se
// mandan por su identificador («se puede especificar el valor o el
// identificador, indistintamente»).

type hasarCliente struct {
	RazonSocial        string `json:"RazonSocial"`
	NumeroDocumento    string `json:"NumeroDocumento"`
	ResponsabilidadIVA string `json:"ResponsabilidadIVA"`
	TipoDocumento      string `json:"TipoDocumento"`
	Domicilio          string `json:"Domicilio,omitempty"`
}

type hasarAsociado struct {
	NumeroLinea       string `json:"NumeroLinea"`
	CodigoComprobante string `json:"CodigoComprobante"`
	NumeroPos         string `json:"NumeroPos"`
	NumeroComprobante string `json:"NumeroComprobante"`
	FechaComprobante  string `json:"FechaComprobante"`
}

type hasarCodigo struct {
	CodigoComprobante string `json:"CodigoComprobante"`
}

type hasarItem struct {
	Descripcion             string `json:"Descripcion"`
	Cantidad                string `json:"Cantidad"`
	PrecioUnitario          string `json:"PrecioUnitario"`
	CondicionIVA            string `json:"CondicionIVA,omitempty"`
	AlicuotaIVA             string `json:"AlicuotaIVA,omitempty"`
	OperacionMonto          string `json:"OperacionMonto"`
	TipoImpuestoInterno     string `json:"TipoImpuestoInterno"`
	MagnitudImpuestoInterno string `json:"MagnitudImpuestoInterno"`
	ModoDisplay             string `json:"ModoDisplay"`
	ModoBaseTotal           string `json:"ModoBaseTotal"`
	UnidadReferencia        string `json:"UnidadReferencia"`
}

type hasarAjuste struct {
	Descripcion   string `json:"Descripcion"`
	Monto         string `json:"Monto"`
	ModoDisplay   string `json:"ModoDisplay"`
	ModoBaseTotal string `json:"ModoBaseTotal"`
	Operacion     string `json:"Operacion"`
}

type hasarPago struct {
	Descripcion     string `json:"Descripcion"`
	Monto           string `json:"Monto"`
	Operacion       string `json:"Operacion"`
	ModoDisplay     string `json:"ModoDisplay"`
	CodigoFormaPago string `json:"CodigoFormaPago"`
}

// Los largos que acepta el equipo para cada texto.
const (
	largoDescripcionItem = 34
	largoDescripcionPago = 64
	largoDatoCliente     = 50
)

const sinDocumento = "NoDocumento"

var (
	hasarDocumentos = map[string]string{
		"cuit": "TipoCUIT", "cuil": "TipoCUIL", "dni": "TipoDNI",
		"pasaporte": "TipoPasaporte", "ci": "TipoCI", "ninguno": "TipoNinguno",
	}
	hasarResponsables = map[string]string{
		"responsable_inscripto": "ResponsableInscripto", "exento": "ResponsableExento",
		"consumidor_final": "ConsumidorFinal", "monotributo": "Monotributo",
		"monotributo_social": "MonotributoSocial", "no_responsable": "NoResponsable",
		"no_categorizado": "NoCategorizado",
	}
	hasarCondiciones = map[string]string{"gravado": "Gravado", "exento": "Exento", "no_gravado": "NoGravado"}
	hasarMedios      = map[string]string{
		"efectivo": "Efectivo", "credito": "TarjetaDeCredito", "debito": "TarjetaDeDebito",
		"transferencia": "TransferenciaBancaria", "cuenta_corriente": "CuentaCorriente", "otro": "OtrosMediosPago",
	}
)

// codigoHasar es el comprobante como lo nombra Hasar («documentos soportados»).
func codigoHasar(tipo, letra string) string {
	switch tipo {
	case TipoFactura:
		return "TiqueFactura" + letra
	case TipoNotaDeCredito:
		return "TiqueNotaCredito" + letra
	}
	return "Tique"
}

// estadoDeHasar es lo que ConsultarEstado dice de un tipo de comprobante.
type estadoDeHasar struct {
	ComprobanteEnCurso      string       `json:"ComprobanteEnCurso"`
	NumeroUltimoComprobante numeroSuelto `json:"NumeroUltimoComprobante"`
}

func (e estadoDeHasar) abierto() bool {
	return e.ComprobanteEnCurso != "" && e.ComprobanteEnCurso != sinDocumento && e.ComprobanteEnCurso != "0"
}

func (h *hasar) estado(ctx context.Context, codigo string) (estadoDeHasar, error) {
	var e estadoDeHasar
	err := h.comando(ctx, "ConsultarEstado", hasarCodigo{codigo}, &e)
	return e, err
}

// Emitir le dicta el comprobante al equipo en el orden que pide Hasar:
// asociado, cliente, abrir, renglones, ajustes, pagos y cerrar.
func (h *hasar) Emitir(ctx context.Context, c Comprobante) (Emitido, error) {
	defer h.hablar.soltar()
	if err := c.Validar(); err != nil {
		return Emitido{}, err
	}
	if c.Letra == "A" && c.Cliente != nil && (c.Cliente.Responsabilidad == "monotributo" || c.Cliente.Responsabilidad == "monotributo_social") {
		// Hasar pide abrirla como a un responsable inscripto y sumarle las dos
		// leyendas de la RG 5003; eso todavía no está hecho.
		return Emitido{}, errors.New("por la impresora fiscal todavía no se puede emitir una factura A a un monotributista")
	}
	codigo := codigoHasar(c.Tipo, c.Letra)

	// De quién es el equipo, antes de dictarle nada.
	var datos struct {
		CUIT      string       `json:"CUIT"`
		NumeroPos numeroSuelto `json:"NumeroPos"`
	}
	if err := h.comando(ctx, "ConsultarDatosInicializacion", nil, &datos); err != nil {
		return Emitido{}, err
	}
	if esperado, tiene := soloDigitos(c.CUIT), soloDigitos(datos.CUIT); esperado != "" && esperado != tiene {
		return Emitido{}, fmt.Errorf("la impresora fiscal en %s es del CUIT %s y el local factura con el %s: no se emitió nada", h.equipo, tiene, esperado)
	}
	emitido := Emitido{Tipo: c.Tipo, Letra: c.Letra, PuntoDeVenta: int(datos.NumeroPos)}

	// Un comprobante que quedó abierto de un intento anterior se cancela: el
	// equipo no abre otro encima.
	antes, err := h.estado(ctx, codigo)
	if err != nil {
		return Emitido{}, err
	}
	if antes.abierto() {
		if err := h.comando(ctx, "Cancelar", nil, nil); err != nil {
			return Emitido{}, fmt.Errorf("la impresora fiscal tiene un comprobante abierto de antes y no se pudo cancelar: %w", err)
		}
	}

	if a := c.Asociado; a != nil {
		fecha := fechaISO.FindStringSubmatch(a.Fecha)
		if fecha == nil {
			// Validar ya lo frena; acá se vuelve a mirar porque lo que sigue lo da por bueno.
			return Emitido{}, errors.New("la fecha del comprobante asociado no es una fecha (AAAA-MM-DD)")
		}
		err := h.comando(ctx, "CargarDocumentoAsociado", hasarAsociado{
			NumeroLinea: "1", CodigoComprobante: codigoHasar(a.Tipo, a.Letra),
			NumeroPos: strconv.Itoa(a.PuntoDeVenta), NumeroComprobante: fmt.Sprintf("%08d", a.Numero),
			FechaComprobante: fecha[1][2:] + fecha[2] + fecha[3],
		}, nil)
		if err != nil {
			return Emitido{}, err
		}
	}
	if cl := c.Cliente; cl != nil {
		documento := cl.Documento
		if cl.TipoDocumento != "pasaporte" {
			documento = soloDigitos(documento)
		}
		err := h.comando(ctx, "CargarDatosCliente", hasarCliente{
			RazonSocial: recortar(cl.Nombre, largoDatoCliente), NumeroDocumento: documento,
			ResponsabilidadIVA: hasarResponsables[cl.Responsabilidad], TipoDocumento: hasarDocumentos[cl.TipoDocumento],
			Domicilio: recortar(cl.Domicilio, largoDatoCliente),
		}, nil)
		if err != nil {
			return Emitido{}, err
		}
	}

	var abierto struct {
		NumeroComprobante numeroSuelto `json:"NumeroComprobante"`
	}
	if err := h.comando(ctx, "AbrirDocumento", hasarCodigo{codigo}, &abierto); err != nil {
		// Si la respuesta se perdió, puede haber quedado abierto.
		return Emitido{}, h.cancelarPor(ctx, err)
	}
	numero := int(abierto.NumeroComprobante)

	if err := h.dictar(ctx, c); err != nil {
		return Emitido{}, h.cancelarPor(ctx, err)
	}

	var cerrado struct {
		NumeroComprobante numeroSuelto `json:"NumeroComprobante"`
	}
	errCierre := h.comando(ctx, "CerrarDocumento", struct{}{}, &cerrado)
	if errCierre == nil {
		emitido.Numero = int(cerrado.NumeroComprobante)
		return emitido, nil
	}

	// Se mandó a cerrar y no vino un «cerrado». Que el error diga «rechazado»
	// no alcanza para darlo por no emitido: se le pregunta al equipo en qué
	// quedó, que es el único que lo sabe.
	despues, err := h.estado(ctx, codigo)
	switch {
	case err != nil:
		return Emitido{}, fmt.Errorf("%w: se mandó a cerrar el %s nº %d y la impresora fiscal no contestó (%v). Mirá si salió el papel antes de volver a emitirlo", ErrIncierto, nombreDe(c), numero, errCierre)
	case despues.abierto():
		return Emitido{}, h.cancelarPor(ctx, errCierre)
	case int(despues.NumeroUltimoComprobante) == numero && numero > int(antes.NumeroUltimoComprobante):
		emitido.Numero = numero
		return emitido, nil
	}
	return Emitido{}, fmt.Errorf("no se emitió el comprobante: %w", errCierre)
}

// dictar manda los renglones, los ajustes y los pagos del comprobante abierto.
func (h *hasar) dictar(ctx context.Context, c Comprobante) error {
	for _, it := range c.Items {
		item := hasarItem{
			Descripcion: recortar(it.Descripcion, largoDescripcionItem), Cantidad: it.Cantidad, PrecioUnitario: it.Precio,
			OperacionMonto: "ModoSumaMonto", TipoImpuestoInterno: "IIVariableKIVA", MagnitudImpuestoInterno: "0.00",
			ModoDisplay: "DisplayNo", ModoBaseTotal: "ModoPrecioTotal", UnidadReferencia: "1",
		}
		if it.IVA != "" || it.Condicion != "" {
			condicion := it.Condicion
			if condicion == "" {
				condicion = "gravado"
			}
			item.CondicionIVA, item.AlicuotaIVA = hasarCondiciones[condicion], it.IVA
		}
		if err := h.comando(ctx, "ImprimirItem", item, nil); err != nil {
			return err
		}
	}
	for _, a := range c.Ajustes {
		operacion := "AjusteNeg"
		if a.Recargo {
			operacion = "AjustePos"
		}
		err := h.comando(ctx, "ImprimirAjuste", hasarAjuste{
			Descripcion: recortar(a.Descripcion, largoDescripcionItem), Monto: a.Monto,
			ModoDisplay: "DisplayNo", ModoBaseTotal: "ModoPrecioTotal", Operacion: operacion,
		}, nil)
		if err != nil {
			return err
		}
	}
	if c.Total != "" {
		// Lo que el equipo lleva sumado tiene que ser lo que cobró el local.
		var sub struct {
			Subtotal string `json:"Subtotal"`
		}
		err := h.comando(ctx, "ConsultarSubtotal", struct {
			Impresion   string `json:"Impresion"`
			ModoDisplay string `json:"ModoDisplay"`
		}{"NoImprimeSubtotal", "DisplayNo"}, &sub)
		if err != nil {
			return err
		}
		if !importe.MatchString(sub.Subtotal) || !coincide(decimal(sub.Subtotal), decimal(c.Total)) {
			return fmt.Errorf("la impresora fiscal sumó %s y la venta es de %s", sub.Subtotal, c.Total)
		}
	}
	for _, p := range c.Pagos {
		err := h.comando(ctx, "ImprimirPago", hasarPago{
			Descripcion: recortar(p.Descripcion, largoDescripcionPago), Monto: p.Monto,
			Operacion: "Pagar", ModoDisplay: "DisplayNo", CodigoFormaPago: hasarMedios[p.Medio],
		}, nil)
		if err != nil {
			return err
		}
	}
	return nil
}

// cancelarPor cancela el comprobante abierto después de ese error, y dice las
// dos cosas: por qué no se emitió y en qué quedó el equipo.
func (h *hasar) cancelarPor(ctx context.Context, causa error) error {
	if err := h.comando(ctx, "Cancelar", nil, nil); err != nil {
		return fmt.Errorf("no se emitió el comprobante: %w. Además no se pudo cancelar en la impresora fiscal (se cancela solo antes del próximo)", causa)
	}
	return fmt.Errorf("no se emitió el comprobante: %w", causa)
}

func nombreDe(c Comprobante) string {
	switch c.Tipo {
	case TipoFactura:
		return "factura " + c.Letra
	case TipoNotaDeCredito:
		if c.Letra == "" {
			return "nota de crédito"
		}
		return "nota de crédito " + c.Letra
	}
	return "tique"
}

// Cerrar hace el cierre Z (Informe Diario de Cierre) o el X (Detalle de Ventas).
func (h *hasar) Cerrar(ctx context.Context, tipo string) (Cierre, error) {
	defer h.hablar.soltar()
	reporte := map[string]string{CierreZ: "ReporteZ", CierreX: "ReporteX"}[tipo]
	if reporte == "" {
		return Cierre{}, fmt.Errorf("no sé hacer un cierre %q: es Z o X", tipo)
	}
	var r struct {
		Numero       numeroSuelto `json:"Numero"`
		Fecha        string       `json:"Fecha"`       // la del cierre Z
		FechaCierre  string       `json:"FechaCierre"` // la del cierre X
		Total        string       `json:"DF_Total"`
		Emitidos     numeroSuelto `json:"DF_CantidadEmitidos"`
		TotalNC      string       `json:"NC_Total"`
		EmitidosDeNC numeroSuelto `json:"NC_CantidadEmitidos"`
	}
	err := h.comando(ctx, "CerrarJornadaFiscal", struct {
		Reporte string `json:"Reporte"`
	}{reporte}, &r)
	if err != nil {
		return Cierre{}, err
	}
	fecha := r.Fecha
	if fecha == "" {
		fecha = r.FechaCierre
	}
	return Cierre{
		Tipo: tipo, Numero: int(r.Numero), Fecha: fechaDeHasar(fecha),
		Total: r.Total, Comprobantes: int(r.Emitidos),
		TotalNotasDeCredito: r.TotalNC, NotasDeCredito: int(r.EmitidosDeNC),
	}, nil
}

// fechaDeHasar pasa «150130» (AAMMDD) a «2015-01-30». Lo que no tenga esa
// forma se deja como vino.
func fechaDeHasar(s string) string {
	if len(s) != 6 || soloDigitos(s) != s {
		return s
	}
	return "20" + s[0:2] + "-" + s[2:4] + "-" + s[4:6]
}
