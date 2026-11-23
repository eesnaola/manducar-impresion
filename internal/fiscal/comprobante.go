package fiscal

import (
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

// Los comprobantes que se le pueden pedir a un controlador fiscal, como los
// nombra el servidor.
const (
	// TipoTique es el tique a consumidor final, sin letra y sin cliente.
	TipoTique = "tique"
	// TipoFactura es la tique-factura: lleva letra (A, B, C o M).
	TipoFactura = "factura"
	// TipoNotaDeCredito anula, en todo o en parte, un comprobante anterior:
	// con letra si anula una factura, sin letra si anula un tique.
	TipoNotaDeCredito = "nota_credito"
)

// Los cierres de jornada.
const (
	// CierreZ es el Informe Diario de Cierre: cierra la jornada fiscal.
	CierreZ = "Z"
	// CierreX es el Detalle de Ventas: informa sin cerrar (cambio de turno).
	CierreX = "X"
)

// ErrIncierto: se mandó a cerrar el comprobante y no se pudo saber si el
// equipo lo emitió. No se reintenta solo: emitirlo de nuevo puede ser
// duplicarlo, y lo que un controlador fiscal emite no se borra.
var ErrIncierto = errors.New("no se sabe si el comprobante salió")

// Comprobante es lo que el servidor manda a emitir. Los importes viajan como
// texto con punto decimal («8500.00»): son plata, y no pasan por un float.
type Comprobante struct {
	Tipo  string `json:"tipo"`
	Letra string `json:"letra,omitempty"`
	// CUIT del emisor, como lo conoce el servidor. Si viene, el equipo tiene
	// que ser de ese contribuyente o no se le dicta nada.
	CUIT     string    `json:"cuit,omitempty"`
	Cliente  *Cliente  `json:"cliente,omitempty"`
	Asociado *Asociado `json:"asociado,omitempty"`
	Items    []Item    `json:"items"`
	Ajustes  []Ajuste  `json:"ajustes,omitempty"`
	Pagos    []Pago    `json:"pagos,omitempty"`
	// Total de la venta, como lo cobró el local. Si viene, antes de cerrar se
	// le pide el subtotal al equipo: si no coincide, el comprobante se cancela
	// en vez de emitirse por otro importe.
	Total string `json:"total,omitempty"`
}

// Cliente es el receptor, cuando el comprobante lo identifica.
type Cliente struct {
	Nombre    string `json:"nombre"`
	Documento string `json:"documento"`
	// TipoDocumento: cuit, cuil, dni, pasaporte, ci o ninguno.
	TipoDocumento string `json:"tipoDocumento"`
	// Responsabilidad ante el IVA: responsable_inscripto, exento,
	// consumidor_final, monotributo, monotributo_social, no_responsable o
	// no_categorizado.
	Responsabilidad string `json:"responsabilidad"`
	Domicilio       string `json:"domicilio,omitempty"`
}

// Asociado es el comprobante que una nota de crédito anula.
type Asociado struct {
	Tipo         string `json:"tipo"`
	Letra        string `json:"letra,omitempty"`
	PuntoDeVenta int    `json:"puntoDeVenta"`
	Numero       int    `json:"numero"`
	// Fecha de aquel comprobante, AAAA-MM-DD.
	Fecha string `json:"fecha"`
}

// Item es un renglón. El precio es el final de una unidad, con el IVA adentro.
type Item struct {
	Descripcion string `json:"descripcion"`
	Cantidad    string `json:"cantidad"`
	Precio      string `json:"precio"`
	// IVA: la alícuota («21.00»). Vacía: el equipo usa la suya por defecto
	// (un monotributista no discrimina IVA).
	IVA string `json:"iva,omitempty"`
	// Condicion: gravado (por defecto), exento o no_gravado.
	Condicion string `json:"condicion,omitempty"`
	// Codigo del producto en el local. Epson lo pide en cada renglón.
	Codigo string `json:"codigo,omitempty"`
}

// Ajuste es un descuento o un recargo sobre el total del comprobante.
type Ajuste struct {
	Descripcion string `json:"descripcion"`
	Monto       string `json:"monto"`
	Recargo     bool   `json:"recargo,omitempty"`
}

// Pago es un medio con el que se pagó.
type Pago struct {
	Descripcion string `json:"descripcion"`
	Monto       string `json:"monto"`
	// Medio: efectivo, credito, debito, transferencia, cuenta_corriente u otro.
	Medio string `json:"medio"`
}

// Emitido es el comprobante que el equipo emitió: el número es el suyo.
type Emitido struct {
	Tipo         string `json:"tipo"`
	Letra        string `json:"letra,omitempty"`
	PuntoDeVenta int    `json:"puntoDeVenta"`
	Numero       int    `json:"numero"`
}

// Cierre es lo que el equipo informa al hacer un cierre Z o X.
type Cierre struct {
	Tipo   string `json:"tipo"`
	Numero int    `json:"numero"`
	// Fecha de la jornada que cerró, AAAA-MM-DD.
	Fecha string `json:"fecha,omitempty"`
	// Total de los comprobantes fiscales, sin las notas de crédito.
	Total               string `json:"total,omitempty"`
	Comprobantes        int    `json:"comprobantes"`
	TotalNotasDeCredito string `json:"totalNotasDeCredito,omitempty"`
	NotasDeCredito      int    `json:"notasDeCredito"`
}

var (
	importe  = regexp.MustCompile(`^\d{1,10}(\.\d{1,4})?$`)
	alicuota = regexp.MustCompile(`^\d{1,2}(\.\d{1,2})?$`)
	fechaISO = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})$`)
)

// Los medios de pago y las condiciones que se aceptan, para rechazar acá lo
// que el equipo no va a entender: una vez abierto el comprobante, un error es
// un comprobante cancelado en la cinta.
var (
	mediosDePago = map[string]bool{"efectivo": true, "credito": true, "debito": true, "transferencia": true, "cuenta_corriente": true, "otro": true}
	condiciones  = map[string]bool{"": true, "gravado": true, "exento": true, "no_gravado": true}
	documentos   = map[string]bool{"cuit": true, "cuil": true, "dni": true, "pasaporte": true, "ci": true, "ninguno": true}
	responsables = map[string]bool{"responsable_inscripto": true, "exento": true, "consumidor_final": true, "monotributo": true, "monotributo_social": true, "no_responsable": true, "no_categorizado": true}
	letras       = map[string]bool{"A": true, "B": true, "C": true, "M": true}
)

// Validar dice qué le falta o le sobra al comprobante antes de tocar el equipo.
func (c Comprobante) Validar() error {
	switch c.Tipo {
	case TipoTique:
		if c.Letra != "" {
			return errors.New("un tique no lleva letra")
		}
	case TipoFactura:
		if !letras[c.Letra] {
			return fmt.Errorf("una factura lleva letra A, B, C o M (vino %q)", c.Letra)
		}
	case TipoNotaDeCredito:
		if c.Letra != "" && !letras[c.Letra] {
			return fmt.Errorf("la letra de la nota de crédito no es A, B, C ni M (vino %q)", c.Letra)
		}
		a := c.Asociado
		if a == nil {
			return errors.New("una nota de crédito tiene que decir qué comprobante anula")
		}
		if (a.Tipo != TipoTique && a.Tipo != TipoFactura) || a.Numero <= 0 || a.PuntoDeVenta <= 0 || !fechaISO.MatchString(a.Fecha) {
			return errors.New("el comprobante que anula la nota de crédito está incompleto (tipo, punto de venta, número y fecha)")
		}
		if (a.Tipo == TipoFactura) != (c.Letra != "") || (a.Tipo == TipoFactura && a.Letra != c.Letra) {
			return errors.New("la nota de crédito lleva la letra del comprobante que anula")
		}
	default:
		return fmt.Errorf("no sé emitir un comprobante de tipo %q", c.Tipo)
	}
	// El comprobante asociado es el que anula una nota de crédito. En
	// cualquier otro no quiere decir nada, y no se manda al equipo algo que
	// no se revisó.
	if c.Tipo != TipoNotaDeCredito && c.Asociado != nil {
		return errors.New("sólo una nota de crédito lleva un comprobante asociado")
	}
	// Con letra es una factura (o su nota de crédito), y una factura dice a
	// quién: sin cliente lo que corresponde es un tique.
	if c.Letra != "" && c.Cliente == nil {
		return fmt.Errorf("un comprobante %s tiene que identificar al cliente", c.Letra)
	}
	if c.Total != "" && !importe.MatchString(c.Total) {
		return errors.New("el total del comprobante no es un importe")
	}
	if cl := c.Cliente; cl != nil {
		if strings.TrimSpace(cl.Nombre) == "" || !documentos[cl.TipoDocumento] || !responsables[cl.Responsabilidad] {
			return errors.New("los datos del cliente están incompletos (nombre, tipo de documento y responsabilidad ante el IVA)")
		}
	}
	if len(c.Items) == 0 {
		return errors.New("el comprobante no tiene renglones")
	}
	for i, it := range c.Items {
		if strings.TrimSpace(it.Descripcion) == "" || !importe.MatchString(it.Cantidad) || !importe.MatchString(it.Precio) {
			return fmt.Errorf("el renglón %d está mal armado (descripción, cantidad y precio)", i+1)
		}
		if (it.IVA != "" && !alicuota.MatchString(it.IVA)) || !condiciones[it.Condicion] {
			return fmt.Errorf("el IVA del renglón %d está mal armado", i+1)
		}
	}
	for i, a := range c.Ajustes {
		if strings.TrimSpace(a.Descripcion) == "" || !importe.MatchString(a.Monto) {
			return fmt.Errorf("el ajuste %d está mal armado (descripción y monto)", i+1)
		}
	}
	for i, p := range c.Pagos {
		if strings.TrimSpace(p.Descripcion) == "" || !importe.MatchString(p.Monto) || !mediosDePago[p.Medio] {
			return fmt.Errorf("el pago %d está mal armado (descripción, monto y medio)", i+1)
		}
	}
	return nil
}

// soloDigitos deja los números de un CUIT o un documento escrito con guiones.
func soloDigitos(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

// decimal lee un importe ya validado («8500.00»).
func decimal(s string) *big.Rat {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return new(big.Rat)
	}
	return r
}

// unCentavo es lo que se tolera de diferencia entre dos totales: redondeos.
var unCentavo = big.NewRat(1, 100)

// coincide dice si dos importes son el mismo, centavo más o menos.
func coincide(a, b *big.Rat) bool {
	d := new(big.Rat).Sub(a, b)
	return d.Abs(d).Cmp(unCentavo) <= 0
}

// recortar deja el texto en a lo sumo n caracteres, sin partir una letra.
func recortar(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n]))
	}
	return s
}
