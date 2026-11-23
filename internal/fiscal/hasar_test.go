package fiscal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// hasarFalsa hace de impresora fiscal Hasar 2G: atiende /fiscal.json como lo
// describe el Manual de Comandos, con la clave de acceso por red. Las
// respuestas son las de los ejemplos del manual.
type hasarFalsa struct {
	mu       sync.Mutex
	clave    string
	pedidos  []string
	cuerpos  []string // el JSON de cada comando, en el orden en que llegó
	cabezas  []http.Header
	contesta map[string]string // respuestas fijas; lo demás lo contesta el estado de abajo
	falla    map[string]string // comando → lo que dice el equipo al rechazarlo
	corta    map[string]bool   // comando → lo hace, pero la respuesta no llega
	apagada  bool              // no atiende más (se cayó la red)
	// apagarAlCortar: el corte no es de una respuesta sola, es la red que se cae.
	apagarAlCortar bool
	// Lo justo del estado de un equipo de verdad para emitir comprobantes.
	enCurso    string         // comprobante abierto; "" si no hay ninguno
	subtotal   float64        // lo que lleva sumado el comprobante abierto
	ultimo     map[string]int // último número emitido de cada comprobante
	cancelados int
	web        *httptest.Server
}

func nuevaHasarFalsa(t *testing.T) *hasarFalsa {
	t.Helper()
	h := &hasarFalsa{clave: "9999", falla: map[string]string{}, corta: map[string]bool{}, ultimo: map[string]int{}, contesta: map[string]string{
		"ConsultarVersion": `{"ConsultarVersion":{"Estado":{"Impresora":[],"Fiscal":[]},
			"Version":"Hasar SMH/PT-250F V: 02.00","Marca":"Hasar","NombreProducto":"D6",
			"VersionMotor":"6.00A","FechaFirmware":"29/01/2015 15:26:08","VersionProtocolo":"1"}}`,
		"ConsultarDatosInicializacion": `{"ConsultarDatosInicializacion":{"Estado":{"Impresora":[],"Fiscal":["MemoriaFiscalInicializada"]},
			"CUIT":"30123456781","RazonSocial":"KROKANTISSIMO SA","Registro":"HHAASS1234567890",
			"NumeroPos":"99998","FechaInicioActividades":"150101","IngBrutos":"IIBB-123456789012345",
			"ResponsabilidadIVA":"I","FechaInicioActividadesCompleta":"20150101"}}`,
	}}
	h.web = httptest.NewServer(http.HandlerFunc(h.atender))
	t.Cleanup(h.web.Close)
	return h
}

func (h *hasarFalsa) direccion() string { return strings.TrimPrefix(h.web.URL, "http://") }

func (h *hasarFalsa) atender(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.apagada {
		if conexion, _, err := w.(http.Hijacker).Hijack(); err == nil {
			conexion.Close()
		}
		return
	}
	if r.URL.Path != "/fiscal.json" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if usuario, clave, ok := r.BasicAuth(); !ok || usuario != "" || clave != h.clave {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	cuerpo, _ := io.ReadAll(r.Body)
	var comando map[string]json.RawMessage
	if json.Unmarshal(cuerpo, &comando) != nil || len(comando) != 1 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	for nombre, campos := range comando {
		h.pedidos = append(h.pedidos, nombre)
		h.cuerpos = append(h.cuerpos, string(cuerpo))
		h.cabezas = append(h.cabezas, r.Header.Clone())
		respuesta := h.responder(nombre, campos)
		if h.corta[nombre] {
			// El equipo hizo lo que se le pidió y la respuesta se perdió en el camino.
			h.apagada = h.apagarAlCortar
			if conexion, _, err := w.(http.Hijacker).Hijack(); err == nil {
				conexion.Close()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, respuesta)
	}
}

// responder hace lo que haría el equipo con ese comando y arma su respuesta.
func (h *hasarFalsa) responder(nombre string, campos json.RawMessage) string {
	if fija, ok := h.contesta[nombre]; ok {
		return fija
	}
	if motivo, ok := h.falla[nombre]; ok {
		return `{"Error":{"Identificador":"ERROR_ESTADO","Descripcion":` + strconv.Quote(motivo) + `,"Contexto":` + strconv.Quote(nombre) + `}}`
	}
	var c struct{ CodigoComprobante string }
	_ = json.Unmarshal(campos, &c)
	const estado = `"Estado":{"Impresora":[],"Fiscal":[]}`
	switch nombre {
	case "ConsultarEstado":
		enCurso := h.enCurso
		if enCurso == "" {
			enCurso = "NoDocumento"
		}
		return fmt.Sprintf(`{"ConsultarEstado":{%s,"EstadoAuxiliar":[],"EstadoInterno":"EnJornadaFiscal","ComprobanteEnCurso":%q,"CodigoComprobante":%q,"NumeroUltimoComprobante":"%d","CantidadEmitidos":"%d","CantidadCancelados":"%d"}}`,
			estado, enCurso, c.CodigoComprobante, h.ultimo[c.CodigoComprobante], h.ultimo[c.CodigoComprobante], h.cancelados)
	case "AbrirDocumento":
		h.enCurso, h.subtotal = c.CodigoComprobante, 0
		return fmt.Sprintf(`{"AbrirDocumento":{%s,"NumeroComprobante":"%08d","IndiceAuditoria":"1"}}`, estado, h.ultimo[c.CodigoComprobante]+1)
	case "CerrarDocumento":
		h.ultimo[h.enCurso]++
		numero := h.ultimo[h.enCurso]
		h.enCurso = ""
		return fmt.Sprintf(`{"CerrarDocumento":{%s,"NumeroComprobante":"%08d","CantidadDePaginas":"1","IndiceAuditoria":"1"}}`, estado, numero)
	case "Cancelar":
		if h.enCurso != "" {
			h.cancelados++
		}
		h.enCurso = ""
	case "ImprimirItem", "ImprimirAjuste":
		var r struct{ Cantidad, PrecioUnitario, Monto, Operacion string }
		_ = json.Unmarshal(campos, &r)
		cantidad, _ := strconv.ParseFloat(r.Cantidad, 64)
		precio, _ := strconv.ParseFloat(r.PrecioUnitario, 64)
		monto, _ := strconv.ParseFloat(r.Monto, 64)
		if r.Operacion == "AjusteNeg" {
			monto = -monto
		}
		h.subtotal += cantidad*precio + monto
	case "ConsultarSubtotal":
		return fmt.Sprintf(`{"ConsultarSubtotal":{%s,"CantidadItems":"1.00","Subtotal":"%.2f","MontoIVA":"0.00","MontoPagado":"0.00","MontoOtrosTributos":"0.00","MontoImpInternos":"0.00","MontoBase":"0.00","AjusteRedondeo":"0.00"}}`, estado, h.subtotal)
	case "ImprimirPago":
		return fmt.Sprintf(`{"ImprimirPago":{%s,"Saldo":"0.00"}}`, estado)
	case "CerrarJornadaFiscal":
		var j struct{ Reporte string }
		_ = json.Unmarshal(campos, &j)
		if j.Reporte == "ReporteX" {
			return fmt.Sprintf(`{"CerrarJornadaFiscal":{%s,"Reporte":"ReporteX","Numero":"12","FechaInicio":"150130","HoraInicio":"090000","FechaCierre":"150130","HoraCierre":"160000","DF_Total":"780.00","DF_TotalIVA":"0.00","DF_TotalTributos":"75.00","DF_CantidadEmitidos":"3","NC_Total":"0.00","NC_TotalIVA":"0.00","NC_TotalTributos":"0.00","NC_CantidadEmitidos":"0","DNFH_CantidadEmitidos":"1"}}`, estado)
		}
		return fmt.Sprintf(`{"CerrarJornadaFiscal":{%s,"Reporte":"ReporteZ","Numero":"40","Fecha":"150130","DF_Total":"780.00","DF_TotalGravado":"705.00","DF_TotalNoGravado":"0.00","DF_TotalExento":"0.00","DF_TotalIVA":"0.00","DF_TotalTributos":"75.00","DF_CantidadEmitidos":"3","DF_CantidadCancelados":"0","NC_Total":"0.00","NC_TotalGravado":"0.00","NC_TotalNoGravado":"0.00","NC_TotalExento":"0.00","NC_TotalIVA":"0.00","NC_TotalTributos":"0.00","NC_CantidadEmitidos":"0","NC_CantidadCancelados":"0","DNFH_Total":"20.00","DNFH_CantidadEmitidos":"1"}}`, estado)
	}
	return fmt.Sprintf(`{%q:{%s}}`, nombre, estado)
}

func TestInfoDeUnaHasarJuntaElModeloYLosDatosDelEmisor(t *testing.T) {
	falsa := nuevaHasarFalsa(t)
	d, err := Abrir(Controlador{Marca: "hasar", Conexion: falsa.direccion()})
	if err != nil {
		t.Fatal(err)
	}

	equipo, err := d.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	quiero := Equipo{
		Marca: "hasar", Modelo: "Hasar SMH/PT-250F V: 02.00", PuntoDeVenta: 99998,
		CUIT: "30123456781", RazonSocial: "KROKANTISSIMO SA", Registro: "HHAASS1234567890",
		Responsabilidad: "I", Estado: []string{"MemoriaFiscalInicializada"},
	}
	if a, b := mustJSON(t, equipo), mustJSON(t, quiero); a != b {
		t.Errorf("el equipo salió\n %s\nquiero\n %s", a, b)
	}
	if got := strings.Join(falsa.pedidos, ","); got != "ConsultarVersion,ConsultarDatosInicializacion" {
		t.Errorf("le mandó %q: Info son dos consultas que no imprimen nada", got)
	}
	if ct := falsa.cabezas[0].Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type %q: Hasar pide application/json para /fiscal.json", ct)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLaHasarAceptaLaDireccionComoLaEscribaElLocal(t *testing.T) {
	falsa := nuevaHasarFalsa(t)
	for _, escrita := range []string{falsa.direccion(), "http://" + falsa.direccion(), " " + falsa.direccion() + "/ "} {
		d, err := Abrir(Controlador{Marca: "Hasar", Conexion: escrita})
		if err != nil {
			t.Fatalf("%q: %v", escrita, err)
		}
		if _, err := d.Info(context.Background()); err != nil {
			t.Errorf("%q: %v", escrita, err)
		}
	}
	if _, err := Abrir(Controlador{Marca: "hasar", Conexion: "  "}); err == nil {
		t.Error("sin dirección no hay a quién hablarle: tenía que fallar al abrir")
	}
}

// La clave de acceso por red sale de fábrica en 9999 y el local la puede
// cambiar: si no es ésa, el mensaje tiene que mandar a mirar la clave.
func TestConLaClaveEquivocadaElMensajeNombraLaClave(t *testing.T) {
	falsa := nuevaHasarFalsa(t)
	falsa.clave = "4321"

	d, _ := Abrir(Controlador{Marca: "hasar", Conexion: falsa.direccion()})
	_, err := d.Info(context.Background())
	if err == nil || !strings.Contains(err.Error(), "clave") {
		t.Fatalf("quiero un error que nombre la clave, vino: %v", err)
	}

	d, _ = Abrir(Controlador{Marca: "hasar", Conexion: falsa.direccion(), Clave: "4321"})
	if _, err := d.Info(context.Background()); err != nil {
		t.Errorf("con la clave que cargó el local tenía que andar: %v", err)
	}
}

func TestUnErrorDelEquipoLlegaConLoQueElEquipoDijo(t *testing.T) {
	falsa := nuevaHasarFalsa(t)
	falsa.contesta["ConsultarVersion"] = `{"Error":{"Identificador":"FISMEM_CHANGED","Descripcion":"La memoria fiscal fue cambiada","Contexto":"Identificador de memoria fiscal erroneo"}}`

	d, _ := Abrir(Controlador{Marca: "hasar", Conexion: falsa.direccion()})
	_, err := d.Info(context.Background())
	if err == nil || !strings.Contains(err.Error(), "La memoria fiscal fue cambiada") {
		t.Fatalf("quiero la descripción que dio el equipo, vino: %v", err)
	}
}

func TestLoQueContestaYNoEsUnaHasarSeDiceAsi(t *testing.T) {
	otra := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "<html>router</html>")
	}))
	defer otra.Close()

	d, _ := Abrir(Controlador{Marca: "hasar", Conexion: strings.TrimPrefix(otra.URL, "http://")})
	_, err := d.Info(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no es una impresora fiscal Hasar") {
		t.Fatalf("vino: %v", err)
	}
}

func TestSiNoHayNadieEnEsaDireccionLoDiceEnCastellano(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	direccion := l.Addr().String()
	l.Close()

	d, _ := Abrir(Controlador{Marca: "hasar", Conexion: direccion})
	_, err = d.Info(context.Background())
	if err == nil || !strings.Contains(err.Error(), "rechazó la conexión") || strings.Contains(err.Error(), "dial tcp") {
		t.Fatalf("quiero el motivo en castellano y sin el error crudo, vino: %v", err)
	}
	// El 9100 es el puerto de las térmicas: una fiscal Hasar atiende por
	// HTTP, y mandar al local a revisar ese puerto es mandarlo a otro lado.
	if strings.Contains(err.Error(), "9100") || !strings.Contains(err.Error(), "impresora fiscal?") {
		t.Fatalf("el consejo tiene que ser el de una fiscal, vino: %v", err)
	}
}

func TestUnaMarcaQueNoSeManejaSeDice(t *testing.T) {
	if _, err := Abrir(Controlador{Marca: "ncr", Conexion: "x"}); err == nil || !strings.Contains(err.Error(), "ncr") {
		t.Fatalf("vino: %v", err)
	}
}

func TestHasarSePuedeSiempre(t *testing.T) {
	if !Detectar().Hasar {
		t.Error("Hasar va por red y no pide nada instalado: se puede en cualquier computadora")
	}
}
