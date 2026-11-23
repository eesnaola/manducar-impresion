package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eesnaola/manducar-impresion/internal/config"
)

// hasarDePrueba hace de impresora fiscal Hasar 2G para los tests del runner:
// contesta las dos consultas de «¿quién sos?».
func hasarDePrueba(t *testing.T) string {
	t.Helper()
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cuerpo, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(string(cuerpo), "ConsultarVersion"):
			_, _ = io.WriteString(w, `{"ConsultarVersion":{"Estado":{"Impresora":[],"Fiscal":[]},"Version":"Hasar SMH/PT-250F V: 02.00","Marca":"Hasar"}}`)
		case strings.Contains(string(cuerpo), "ConsultarDatosInicializacion"):
			_, _ = io.WriteString(w, `{"ConsultarDatosInicializacion":{"Estado":{"Impresora":[],"Fiscal":[]},"CUIT":"30123456781","RazonSocial":"PIZZERIA SA","Registro":"HHAASS1234567890","NumeroPos":"7","ResponsabilidadIVA":"I"}}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(web.Close)
	return strings.TrimPrefix(web.URL, "http://")
}

// servidorFiscal es el Manducar de mentira para lo fiscal: dice en el latido
// cuántos pedidos hay, los entrega una vez y guarda lo que le contestan.
type servidorFiscal struct {
	mu         sync.Mutex
	web        *httptest.Server
	conFiscal  bool // ¿el servidor sabe de fiscales? (los viejos no)
	pedidos    string
	entregados bool
	latidos    []map[string]any
	pedidas    int
	resultados map[int]map[string]any
	// busquedas: lo que volvió de cada «Buscar» del panel.
	busquedas []map[string]any
}

func nuevoServidorFiscal(t *testing.T, pedidos string) *servidorFiscal {
	t.Helper()
	s := &servidorFiscal{conFiscal: true, pedidos: pedidos, resultados: map[int]map[string]any{}}
	s.web = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case r.URL.Path == "/agente/latido":
			var cuerpo map[string]any
			_ = json.NewDecoder(r.Body).Decode(&cuerpo)
			s.latidos = append(s.latidos, cuerpo)
			fiscal := ""
			if s.conFiscal {
				pendientes := 1
				if s.entregados {
					pendientes = 0
				}
				fiscal = fmt.Sprintf(`,"fiscalPending":%d`, pendientes)
			}
			fmt.Fprintf(w, `{"mercure":{"url":"http://127.0.0.1:1/.well-known/mercure","jwt":"j","topic":"t"},"agent":null,"jobsPending":0,"heartbeatSeconds":30%s}`, fiscal)
		case r.URL.Path == "/agente/fiscal/pedidos":
			s.pedidas++
			if s.entregados {
				_, _ = io.WriteString(w, `{"pedidos":[]}`)
				return
			}
			s.entregados = true
			_, _ = io.WriteString(w, s.pedidos)
		case strings.HasPrefix(r.URL.Path, "/agente/fiscal/pedidos/") && strings.HasSuffix(r.URL.Path, "/resultado"):
			var id int
			fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/agente/fiscal/pedidos/"), "%d", &id)
			var cuerpo map[string]any
			_ = json.NewDecoder(r.Body).Decode(&cuerpo)
			s.resultados[id] = cuerpo
			_, _ = io.WriteString(w, `{"ok":true}`)
		case r.URL.Path == "/agente/fiscal/busqueda":
			var cuerpo map[string]any
			_ = json.NewDecoder(r.Body).Decode(&cuerpo)
			s.busquedas = append(s.busquedas, cuerpo)
			_, _ = io.WriteString(w, `{"ok":true}`)
		case r.URL.Path == "/agente/trabajos":
			_, _ = io.WriteString(w, `{"jobs":[]}`)
		default:
			_, _ = io.WriteString(w, `{"ok":true}`)
		}
	}))
	t.Cleanup(s.web.Close)
	return s
}

func (s *servidorFiscal) resultado(id int) (map[string]any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.resultados[id]
	return r, ok
}

func correrContra(t *testing.T, servidor string) {
	t.Helper()
	soltarArchivoDeCaidas(t)
	t.Setenv("MANDUCAR_IMPRESION_CONFIG", filepath.Join(t.TempDir(), "impresion.json"))
	if err := config.Save(config.Config{Server: servidor, Token: "tok", AgentID: 1}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	fin := make(chan struct{})
	go func() { _ = Run(ctx, "test"); close(fin) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-fin:
		case <-time.After(3 * time.Second):
		}
	})
}

// «Probar conexión» en el panel: el servidor le pide al agente que le
// pregunte al controlador fiscal quién es, y el agente contesta con lo que
// dijo el equipo.
func TestUnPedidoFiscalSeAtiendeYSeContestaConLoQueDijoElEquipo(t *testing.T) {
	equipo := hasarDePrueba(t)
	srv := nuevoServidorFiscal(t, fmt.Sprintf(`{"pedidos":[{"id":9,"accion":"info","controlador":{"marca":"hasar","conexion":"%s"}}]}`, equipo))
	correrContra(t, srv.web.URL)

	waitFor(t, 4*time.Second, "el resultado del pedido fiscal 9", func() bool { _, ok := srv.resultado(9); return ok })
	res, _ := srv.resultado(9)
	if res["ok"] != true {
		t.Fatalf("el equipo contestó y el agente dijo que no: %v", res)
	}
	dicho, _ := res["equipo"].(map[string]any)
	if dicho["modelo"] != "Hasar SMH/PT-250F V: 02.00" || dicho["puntoDeVenta"] != float64(7) || dicho["cuit"] != "30123456781" {
		t.Errorf("no volvió lo que dijo el equipo: %v", dicho)
	}
}

// El panel ofrece «impresora fiscal» según lo que esa computadora puede
// manejar: el agente lo cuenta en el latido, junto con las impresoras.
func TestElLatidoCuentaQueControladoresFiscalesPuedeManejar(t *testing.T) {
	srv := nuevoServidorFiscal(t, `{"pedidos":[]}`)
	correrContra(t, srv.web.URL)

	waitFor(t, 4*time.Second, "un latido con lo fiscal", func() bool {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return len(srv.latidos) > 0
	})
	srv.mu.Lock()
	fiscal, _ := srv.latidos[0]["fiscal"].(map[string]any)
	srv.mu.Unlock()
	if fiscal["hasar"] != true {
		t.Errorf("el primer latido tenía que decir que Hasar se puede: %v", fiscal)
	}
	if _, ok := fiscal["epson"].(map[string]any); !ok {
		t.Errorf("el latido tenía que decir cómo está la librería de Epson: %v", fiscal)
	}
}

// Un servidor más nuevo puede pedir algo que este agente todavía no sabe: se
// contesta que no, con el motivo, en vez de dejar el pedido colgado.
func TestUnPedidoFiscalQueNoSeEntiendeSeContestaConElMotivo(t *testing.T) {
	srv := nuevoServidorFiscal(t, `{"pedidos":[{"id":3,"accion":"bailar","controlador":{"marca":"hasar","conexion":"127.0.0.1:1"}}]}`)
	correrContra(t, srv.web.URL)

	waitFor(t, 4*time.Second, "el resultado del pedido 3", func() bool { _, ok := srv.resultado(3); return ok })
	res, _ := srv.resultado(3)
	if res["ok"] != false || !strings.Contains(fmt.Sprint(res["error"]), "bailar") {
		t.Errorf("quiero un no con el motivo, vino: %v", res)
	}
}

func TestSiElControladorNoContestaElPedidoVuelveConElMotivo(t *testing.T) {
	srv := nuevoServidorFiscal(t, `{"pedidos":[{"id":4,"accion":"info","controlador":{"marca":"hasar","conexion":"127.0.0.1:1"}}]}`)
	correrContra(t, srv.web.URL)

	waitFor(t, 4*time.Second, "el resultado del pedido 4", func() bool { _, ok := srv.resultado(4); return ok })
	res, _ := srv.resultado(4)
	if res["ok"] != false || !strings.Contains(fmt.Sprint(res["error"]), "impresora fiscal") {
		t.Errorf("quiero un no que nombre a la impresora fiscal, vino: %v", res)
	}
}

// Un servidor de antes de lo fiscal no tiene esas rutas: el agente no le
// pregunta por pedidos fiscales.
func TestAUnServidorQueNoSabeDeFiscalesNoSeLePreguntaPorPedidos(t *testing.T) {
	srv := nuevoServidorFiscal(t, `{"pedidos":[]}`)
	srv.conFiscal = false
	correrContra(t, srv.web.URL)

	waitFor(t, 4*time.Second, "un latido", func() bool {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return len(srv.latidos) > 0
	})
	time.Sleep(300 * time.Millisecond)
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.pedidas != 0 {
		t.Errorf("le preguntó %d veces por pedidos fiscales a un servidor que no dijo tenerlos", srv.pedidas)
	}
}
