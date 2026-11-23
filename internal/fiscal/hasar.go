package fiscal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eesnaola/manducar-impresion/internal/printer"
)

// claveHasarDeFabrica es la clave de acceso por red con la que salen los
// equipos; la misma que pide su interfaz web.
const claveHasarDeFabrica = "9999"

// plazoHasar: lo que se espera cada respuesta. Una consulta contesta al
// instante; el margen es para una red de local.
const plazoHasar = 10 * time.Second

// hasar es una impresora fiscal Hasar 2G. Lo que se le dicta es lo mismo
// llegue por donde llegue; cómo viaja cada comando lo sabe su transporte.
type hasar struct {
	// equipo: cómo nombrarla en un mensaje («192.168.1.50», «el puerto USB»).
	equipo string
	hablar transporteHasar
}

// transporteHasar es un camino hasta el equipo: la interfaz HTTP, por red, o
// el protocolo empaquetado, por el puerto serie o el USB.
type transporteHasar interface {
	// comando manda un comando fiscal con sus campos por nombre y deja en
	// salida los datos de la respuesta.
	comando(ctx context.Context, nombre string, campos any, salida any) error
	// soltar deja libre el equipo al terminar una operación.
	soltar()
}

func (h *hasar) comando(ctx context.Context, nombre string, campos any, salida any) error {
	return h.hablar.comando(ctx, nombre, campos, salida)
}

// hasarHTTP le habla por su interfaz HTTP: cada comando es un POST a
// /fiscal.json con un objeto {"Comando": {campos}}, y la respuesta trae el
// mismo comando con sus datos.
type hasarHTTP struct {
	equipo  string
	url     string
	clave   string
	cliente *http.Client
}

func (h *hasarHTTP) soltar() {}

func nuevoHasar(c Controlador) (*hasar, error) {
	if puerto, es := puertoDeConexion(c.Conexion); es {
		equipo := "el puerto " + puerto
		if puerto == "usb" {
			equipo = "el puerto USB"
		}
		return &hasar{equipo: equipo, hablar: &hasarPuerto{equipo: equipo, conexion: puerto, velocidad: c.Velocidad}}, nil
	}
	equipo, err := direccionDeRed(c.Conexion)
	if err != nil {
		return nil, err
	}
	clave := strings.TrimSpace(c.Clave)
	if clave == "" {
		clave = claveHasarDeFabrica
	}
	return &hasar{equipo: equipo, hablar: &hasarHTTP{
		equipo: equipo,
		url:    "http://" + equipo + "/fiscal.json",
		clave:  clave,
		// Sin proxy: el equipo está en la red del local, y un proxy del
		// sistema lo mandaría a buscar afuera (Hasar lo pide con --noproxy).
		cliente: &http.Client{Timeout: plazoHasar, Transport: &http.Transport{Proxy: nil}},
	}}, nil
}

// comando manda un comando fiscal y deja en salida sus datos de respuesta.
func (h *hasarHTTP) comando(ctx context.Context, nombre string, campos any, salida any) error {
	if campos == nil {
		campos = struct{}{}
	}
	cuerpo, err := json.Marshal(map[string]any{nombre: campos})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(cuerpo))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// Sin usuario: sólo la clave, como en los ejemplos de Hasar (curl -u:9999).
	req.SetBasicAuth("", h.clave)

	res, err := h.cliente.Do(req)
	if err != nil {
		return fmt.Errorf("no se pudo hablar con la impresora fiscal en %s: %s", h.equipo, motivoDeConexion(err))
	}
	defer res.Body.Close()
	datos, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))

	switch {
	case res.StatusCode == http.StatusUnauthorized, res.StatusCode == http.StatusForbidden:
		return &claveRechazada{equipo: h.equipo}
	case res.StatusCode != http.StatusOK:
		return fmt.Errorf("la impresora fiscal en %s contestó HTTP %d: ¿es una Hasar de segunda generación?", h.equipo, res.StatusCode)
	}

	var sobre map[string]json.RawMessage
	if err := json.Unmarshal(datos, &sobre); err != nil {
		return fmt.Errorf("en %s contestó algo que no es una impresora fiscal Hasar (no es JSON)", h.equipo)
	}
	respuesta, ok := sobre[nombre]
	if !ok {
		return fmt.Errorf("la impresora fiscal no aceptó «%s»: %s", nombre, errorDeHasar(sobre))
	}
	if salida == nil {
		return nil
	}
	if err := json.Unmarshal(respuesta, salida); err != nil {
		return fmt.Errorf("no se entendió la respuesta de la impresora fiscal a «%s»: %w", nombre, err)
	}
	return nil
}

// claveRechazada: el equipo contestó que la clave de acceso por red no es
// ésa. Es un tipo aparte porque la búsqueda lo distingue: quien contesta eso
// puede ser una Hasar con la clave cambiada.
type claveRechazada struct{ equipo string }

func (e *claveRechazada) Error() string {
	return fmt.Sprintf("la impresora fiscal en %s no aceptó la clave de acceso por red: revisá la que tiene cargada (de fábrica es %s)", e.equipo, claveHasarDeFabrica)
}

// errorDeHasar saca el motivo de una respuesta que no es la del comando. El
// equipo contesta un registro con la descripción del error y su contexto; no
// se da por sabido en qué nivel viene, así que se lo busca.
func errorDeHasar(sobre map[string]json.RawMessage) string {
	var partes []string
	var buscar func(m map[string]json.RawMessage)
	buscar = func(m map[string]json.RawMessage) {
		for _, clave := range []string{"Descripcion", "Contexto", "Identificador"} {
			var texto string
			if crudo, ok := m[clave]; ok && json.Unmarshal(crudo, &texto) == nil && texto != "" {
				partes = append(partes, texto)
			}
		}
		if len(partes) > 0 {
			return
		}
		for _, crudo := range m {
			var adentro map[string]json.RawMessage
			if json.Unmarshal(crudo, &adentro) == nil {
				buscar(adentro)
			}
		}
	}
	buscar(sobre)
	if len(partes) == 0 {
		return "contestó otra cosa, sin decir por qué"
	}
	return strings.Join(partes, " · ")
}

// motivoDeConexion dice en castellano por qué no se llegó al equipo. Los
// motivos de red son los mismos que los de una térmica; el de la conexión
// rechazada no, porque aquél manda a revisar el puerto 9100 y una fiscal
// atiende por HTTP: si en esa dirección hay alguien que rechaza, lo más
// probable es que la dirección sea de otro aparato.
func motivoDeConexion(err error) string {
	if strings.Contains(err.Error(), "connection refused") {
		return "rechazó la conexión (¿esa dirección es la de la impresora fiscal?)"
	}
	return printer.MotivoDeRed(err)
}

// estadoHasar son las dos listas de avisos que trae toda respuesta.
type estadoHasar struct {
	Impresora []string `json:"Impresora"`
	Fiscal    []string `json:"Fiscal"`
}

func (e estadoHasar) avisos() []string {
	return append(append([]string{}, e.Impresora...), e.Fiscal...)
}

// numeroSuelto acepta un número venga como número o como texto: el manual lo
// muestra de las dos formas según el campo.
type numeroSuelto int

func (n *numeroSuelto) UnmarshalJSON(b []byte) error {
	texto := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if texto == "" || texto == "null" {
		*n = 0
		return nil
	}
	v, err := strconv.Atoi(strings.TrimSpace(texto))
	if err != nil {
		return fmt.Errorf("%q no es un número", texto)
	}
	*n = numeroSuelto(v)
	return nil
}

// Info junta lo que el equipo dice de sí mismo con dos consultas que no
// imprimen nada: ConsultarVersion y ConsultarDatosInicializacion.
func (h *hasar) Info(ctx context.Context) (Equipo, error) {
	defer h.hablar.soltar()
	equipo, err := h.modelo(ctx)
	if err != nil {
		return Equipo{}, err
	}
	if err := h.inicializacion(ctx, &equipo); err != nil {
		return Equipo{}, err
	}
	return equipo, nil
}

// modelo le pregunta al equipo qué modelo es (ConsultarVersion). Es lo
// primero que contesta cualquier Hasar 2G, inicializada o no.
func (h *hasar) modelo(ctx context.Context) (Equipo, error) {
	var version struct {
		Estado  estadoHasar `json:"Estado"`
		Version string      `json:"Version"`
		Marca   string      `json:"Marca"`
	}
	if err := h.comando(ctx, "ConsultarVersion", nil, &version); err != nil {
		return Equipo{}, err
	}
	return Equipo{Marca: MarcaHasar, Modelo: strings.TrimSpace(version.Version)}, nil
}

// inicializacion completa en equipo de qué contribuyente es y con qué punto
// de venta (ConsultarDatosInicializacion).
func (h *hasar) inicializacion(ctx context.Context, equipo *Equipo) error {
	var datos struct {
		Estado             estadoHasar  `json:"Estado"`
		CUIT               string       `json:"CUIT"`
		RazonSocial        string       `json:"RazonSocial"`
		Registro           string       `json:"Registro"`
		NumeroPos          numeroSuelto `json:"NumeroPos"`
		ResponsabilidadIVA string       `json:"ResponsabilidadIVA"`
	}
	if err := h.comando(ctx, "ConsultarDatosInicializacion", nil, &datos); err != nil {
		return err
	}
	equipo.PuntoDeVenta = int(datos.NumeroPos)
	equipo.CUIT = strings.TrimSpace(datos.CUIT)
	equipo.RazonSocial = strings.TrimSpace(datos.RazonSocial)
	equipo.Registro = strings.TrimSpace(datos.Registro)
	equipo.Responsabilidad = strings.TrimSpace(datos.ResponsabilidadIVA)
	equipo.Estado = datos.Estado.avisos()
	return nil
}
