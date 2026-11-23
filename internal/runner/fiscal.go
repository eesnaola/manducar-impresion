package runner

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/eesnaola/manducar-impresion/internal/api"
	"github.com/eesnaola/manducar-impresion/internal/fiscal"
)

// plazoFiscal: cuánto se le da a una consulta. Contesta al instante; el
// margen es para un equipo lento o una red de local.
const plazoFiscal = 30 * time.Second

// plazoDeEmision: cuánto se le da a un comprobante o a un cierre, que además
// se imprimen: un cierre Z es un papel largo, y por puerto serie va despacio.
const plazoDeEmision = 3 * time.Minute

// abrirFiscal abre el driver de un controlador. En una variable para que los
// tests del runner pongan un equipo de mentira.
var abrirFiscal = fiscal.Abrir

// Las acciones que dejan algo en el equipo y no se deshacen: van por el diario.
const (
	accionComprobante = "comprobante"
	accionCierre      = "cierre"
)

func noSeDeshace(accion string) bool {
	return accion == accionComprobante || accion == accionCierre
}

// detectarFiscal dice qué controladores fiscales puede manejar esta
// computadora. En una variable para que los tests no dependan de lo que haya
// instalado en la máquina que los corre.
var detectarFiscal = func() any { return fiscal.Detectar() }

// buscarFiscal busca controladores fiscales en la red de esta computadora.
// En una variable para que los tests no salgan a recorrer la red de la
// máquina que los corre.
var buscarFiscal = fiscal.Buscar

// plazoDeBusqueda: lo que se le da a una búsqueda. Lleva unos segundos; el
// tope es para que una red rara no la deje colgada.
const plazoDeBusqueda = 20 * time.Second

// busquedaFiscal es lo que vuelve de una búsqueda pedida desde el panel
// (POST /agente/fiscal/busqueda).
type busquedaFiscal struct {
	// ID: el de la búsqueda, como vino. El servidor descarta la respuesta de
	// una búsqueda que ya no es la que espera.
	ID          string              `json:"id"`
	Encontrados []fiscal.Encontrado `json:"encontrados"`
}

// pedidoFiscal es lo que el servidor quiere de un controlador fiscal.
type pedidoFiscal struct {
	ID          int                `json:"id"`
	Accion      string             `json:"accion"`
	Controlador fiscal.Controlador `json:"controlador"`
	// Comprobante: lo que hay que emitir, cuando la acción es «comprobante».
	Comprobante *fiscal.Comprobante `json:"comprobante,omitempty"`
	// Cierre: «Z» o «X», cuando la acción es «cierre».
	Cierre string `json:"cierre,omitempty"`
}

// resultadoFiscal es lo que vuelve al servidor
// (POST /agente/fiscal/pedidos/{id}/resultado).
type resultadoFiscal struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Incierto: se mandó a emitir y no se sabe si salió. El servidor no lo
	// reintenta solo: lo resuelve una persona mirando el papel.
	Incierto    bool            `json:"incierto,omitempty"`
	Equipo      *fiscal.Equipo  `json:"equipo,omitempty"`
	Comprobante *fiscal.Emitido `json:"comprobante,omitempty"`
	Cierre      *fiscal.Cierre  `json:"cierre,omitempty"`
}

// kickFiscal larga una vuelta de pedidos fiscales, si no hay una andando y
// el servidor sabe de fiscales. No se encola: el latido siguiente vuelve a
// avisar si quedó algo.
func (r *runner) kickFiscal(ctx context.Context) {
	if !r.fiscalSi.Load() || r.unpaired.Load() || r.updating.Load() {
		return
	}
	if !r.fiscalOcupado.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer r.fiscalOcupado.Store(false)
		r.atenderFiscales(ctx)
	}()
}

// atenderFiscales entrega lo que quedó sin entregar, trae los pedidos y los
// resuelve de a uno: un controlador fiscal no atiende dos conversaciones a la
// vez.
func (r *runner) atenderFiscales(ctx context.Context) {
	r.entregarFiscales(ctx)

	var sobre struct {
		Pedidos []pedidoFiscal `json:"pedidos"`
		// Buscar: el id de una búsqueda pedida desde el panel («Buscar» en
		// Agregar impresora fiscal); vacío si no hay ninguna.
		Buscar string `json:"buscar"`
	}
	if err := r.api().FetchFiscal(ctx, &sobre); err != nil {
		if errors.Is(err, api.ErrUnauthorized) {
			r.unpaired.Store(true)
		}
		if ctx.Err() == nil {
			r.fetchErr.Printf("buscar pedidos fiscales: %v", err)
		}
		return
	}
	for _, p := range sobre.Pedidos {
		if ctx.Err() != nil {
			return
		}
		res, anotado := r.atenderFiscal(ctx, p)
		if res.OK {
			log.Printf("✓ fiscal %d (%s en %s %s)", p.ID, p.Accion, p.Controlador.Marca, p.Controlador.Conexion)
		} else {
			log.Printf("✗ fiscal %d (%s en %s %s): %s", p.ID, p.Accion, p.Controlador.Marca, p.Controlador.Conexion, res.Error)
		}
		r.devolverFiscal(ctx, p.ID, res, anotado)
	}
	// Después de los pedidos: un comprobante no espera a una búsqueda.
	if sobre.Buscar != "" && ctx.Err() == nil {
		r.buscarFiscales(ctx, sobre.Buscar)
	}
}

// buscarFiscales busca controladores fiscales en la red y le lleva al
// servidor lo que encontró. No toca ningún equipo: sólo pregunta quién anda
// ahí. Si la respuesta no llega no se reintenta: el panel lo ve vencer y se
// vuelve a buscar.
func (r *runner) buscarFiscales(ctx context.Context, id string) {
	busqueda, cancelar := context.WithTimeout(ctx, plazoDeBusqueda)
	encontrados := buscarFiscal(busqueda)
	cancelar()
	if encontrados == nil {
		// Una lista vacía y no null: «no encontró nada» es una respuesta.
		encontrados = []fiscal.Encontrado{}
	}
	log.Printf("búsqueda de impresoras fiscales en la red: %d encontrada(s)", len(encontrados))
	if err := r.api().ReportFiscalSearch(ctx, busquedaFiscal{ID: id, Encontrados: encontrados}); err != nil && ctx.Err() == nil {
		log.Printf("no se pudo devolver la búsqueda de impresoras fiscales: %v", err)
	}
}

// atenderFiscal resuelve un pedido. Lo que no se deshace pasa por el diario:
// si ya está anotado no vuelve al equipo, y si no se puede anotar tampoco va.
// anotado dice si el resultado quedó en el diario.
func (r *runner) atenderFiscal(ctx context.Context, p pedidoFiscal) (res resultadoFiscal, anotado bool) {
	if !noSeDeshace(p.Accion) {
		return r.resolverFiscal(ctx, p), false
	}
	if r.diario == nil {
		return resultadoFiscal{Error: "esta computadora no tiene dónde anotar lo que emite: no se le pidió nada a la impresora fiscal"}, false
	}
	if asiento, ya := r.diario.buscar(p.ID); ya {
		if asiento.Resultado != nil {
			return *asiento.Resultado, true
		}
		// Se empezó y no quedó anotado cómo terminó: el agente se cerró en el medio.
		res = resultadoFiscal{Incierto: true, Error: "la computadora se apagó o se reinició mientras se lo pedía a la impresora fiscal, y no se sabe si salió. Mirá el papel antes de volver a emitirlo"}
		_ = r.diario.terminar(p.ID, res)
		return res, true
	}
	if err := r.diario.empezar(p.ID, p.Accion); err != nil {
		return resultadoFiscal{Error: fmt.Sprintf("no se pudo anotar el pedido en el disco de esa computadora (%v): no se le pidió nada a la impresora fiscal", err)}, false
	}
	res = r.resolverFiscal(ctx, p)
	if err := r.diario.terminar(p.ID, res); err != nil {
		log.Printf("no se pudo anotar cómo terminó el pedido fiscal %d: %v", p.ID, err)
	}
	return res, true
}

// devolverFiscal le lleva el resultado al servidor. Si no llega y está en el
// diario, sale en la próxima vuelta.
func (r *runner) devolverFiscal(ctx context.Context, id int, res resultadoFiscal, anotado bool) {
	err := r.api().ReportFiscal(ctx, id, res)
	if err != nil && !errors.Is(err, api.ErrUnknownJob) {
		if ctx.Err() == nil {
			log.Printf("no se pudo devolver el resultado del pedido fiscal %d: %v", id, err)
		}
		return
	}
	if anotado {
		if err := r.diario.entregado(id); err != nil {
			log.Printf("no se pudo anotar que el pedido fiscal %d ya se entregó: %v", id, err)
		}
	}
}

// entregarFiscales vuelve a mandar los resultados que el servidor todavía no
// recibió.
func (r *runner) entregarFiscales(ctx context.Context) {
	if r.diario == nil {
		return
	}
	for _, a := range r.diario.sinEntregar() {
		if ctx.Err() != nil {
			return
		}
		r.devolverFiscal(ctx, a.ID, *a.Resultado, true)
	}
}

// fiscalSinEntregar: ¿quedó algún resultado esperando que vuelva internet?
func (r *runner) fiscalSinEntregar() bool {
	return r.diario != nil && len(r.diario.sinEntregar()) > 0
}

// resolverFiscal hace lo que pide un pedido y arma su resultado. Nunca deja
// un pedido sin respuesta: lo que no se puede, vuelve con el motivo.
func (r *runner) resolverFiscal(ctx context.Context, p pedidoFiscal) resultadoFiscal {
	plazo := plazoFiscal
	if noSeDeshace(p.Accion) {
		plazo = plazoDeEmision
	}
	ctx, cancelar := context.WithTimeout(ctx, plazo)
	defer cancelar()

	switch p.Accion {
	case "info", accionComprobante, accionCierre:
	default:
		return resultadoFiscal{Error: fmt.Sprintf("este agente no sabe hacer «%s» con un controlador fiscal: hay que actualizarlo", p.Accion)}
	}
	driver, err := abrirFiscal(p.Controlador)
	if err != nil {
		return resultadoFiscal{Error: err.Error()}
	}
	switch p.Accion {
	case accionComprobante:
		if p.Comprobante == nil {
			return resultadoFiscal{Error: "el pedido no trae el comprobante a emitir"}
		}
		emitido, err := driver.Emitir(ctx, *p.Comprobante)
		if err != nil {
			return resultadoFiscal{Error: err.Error(), Incierto: errors.Is(err, fiscal.ErrIncierto)}
		}
		return resultadoFiscal{OK: true, Comprobante: &emitido}
	case accionCierre:
		cierre, err := driver.Cerrar(ctx, p.Cierre)
		if err != nil {
			return resultadoFiscal{Error: err.Error()}
		}
		return resultadoFiscal{OK: true, Cierre: &cierre}
	}
	equipo, err := driver.Info(ctx)
	if err != nil {
		return resultadoFiscal{Error: err.Error()}
	}
	return resultadoFiscal{OK: true, Equipo: &equipo}
}
