package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eesnaola/manducar-impresion/internal/config"
	"github.com/eesnaola/manducar-impresion/internal/fiscal"
)

// equipoFalso hace de controlador fiscal para los tests del runner: lo que
// importa acá es cuántas veces se le pide emitir, no cómo se le habla (eso
// está en internal/fiscal).
type equipoFalso struct {
	mu        sync.Mutex
	emitidos  []fiscal.Comprobante
	cierres   []string
	errEmitir error
}

func (e *equipoFalso) Info(context.Context) (fiscal.Equipo, error) { return fiscal.Equipo{}, nil }

func (e *equipoFalso) Emitir(_ context.Context, c fiscal.Comprobante) (fiscal.Emitido, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitidos = append(e.emitidos, c)
	if e.errEmitir != nil {
		return fiscal.Emitido{}, e.errEmitir
	}
	return fiscal.Emitido{Tipo: c.Tipo, Letra: c.Letra, PuntoDeVenta: 7, Numero: 344 + len(e.emitidos)}, nil
}

func (e *equipoFalso) Cerrar(_ context.Context, tipo string) (fiscal.Cierre, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cierres = append(e.cierres, tipo)
	return fiscal.Cierre{Tipo: tipo, Numero: 40, Fecha: "2026-11-22", Total: "780.00", Comprobantes: 3}, nil
}

func (e *equipoFalso) veces() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.emitidos)
}

func conEquipoFalso(t *testing.T) *equipoFalso {
	t.Helper()
	equipo := &equipoFalso{}
	anterior := abrirFiscal
	abrirFiscal = func(fiscal.Controlador) (fiscal.Driver, error) { return equipo, nil }
	t.Cleanup(func() { abrirFiscal = anterior })
	return equipo
}

// servidorQueInsiste es un Manducar que, como el de verdad, vuelve a entregar
// un pedido fiscal hasta que le llega su resultado. rechazos: cuántas veces
// contesta 500 al resultado antes de aceptarlo (internet que se corta).
type servidorQueInsiste struct {
	mu         sync.Mutex
	web        *httptest.Server
	pedidos    string
	rechazos   int
	entregas   int
	resultados []map[string]any
}

func nuevoServidorQueInsiste(t *testing.T, pedidos string) *servidorQueInsiste {
	t.Helper()
	s := &servidorQueInsiste{pedidos: pedidos}
	s.web = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case r.URL.Path == "/agente/latido":
			pendientes := 1
			if len(s.resultados) > 0 {
				pendientes = 0
			}
			// Un latido corto, para que el test no espere medio minuto a la segunda vuelta.
			fmt.Fprintf(w, `{"mercure":{"url":"http://127.0.0.1:1/.well-known/mercure","jwt":"j","topic":"t"},"agent":null,"jobsPending":0,"heartbeatSeconds":5,"fiscalPending":%d}`, pendientes)
		case r.URL.Path == "/agente/fiscal/pedidos":
			if len(s.resultados) > 0 {
				_, _ = io.WriteString(w, `{"pedidos":[]}`)
				return
			}
			s.entregas++
			_, _ = io.WriteString(w, s.pedidos)
		case strings.HasSuffix(r.URL.Path, "/resultado") && strings.HasPrefix(r.URL.Path, "/agente/fiscal/pedidos/"):
			if s.rechazos > 0 {
				s.rechazos--
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			var cuerpo map[string]any
			_ = json.NewDecoder(r.Body).Decode(&cuerpo)
			s.resultados = append(s.resultados, cuerpo)
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

func (s *servidorQueInsiste) resultado() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.resultados) == 0 {
		return nil
	}
	return s.resultados[0]
}

const pedidoDeTique = `{"pedidos":[{"id":21,"accion":"comprobante","controlador":{"marca":"hasar","conexion":"192.168.1.50"},
	"comprobante":{"tipo":"tique","total":"8500.00","items":[{"descripcion":"Pizza muzzarella","cantidad":"1","precio":"8500.00","iva":"21.00"}],
	"pagos":[{"descripcion":"Efectivo","monto":"8500.00","medio":"efectivo"}]}}]}`

// correrEn es correrContra, pero deja elegir la carpeta de configuración (el
// diario fiscal vive al lado) y devuelve cómo frenar el agente.
func correrEn(t *testing.T, carpeta, servidor string) (frenar func()) {
	t.Helper()
	soltarArchivoDeCaidas(t)
	t.Setenv("MANDUCAR_IMPRESION_CONFIG", filepath.Join(carpeta, "impresion.json"))
	if err := config.Save(config.Config{Server: servidor, Token: "tok", AgentID: 1}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	fin := make(chan struct{})
	go func() { _ = Run(ctx, "test"); close(fin) }()
	frenar = func() {
		cancel()
		select {
		case <-fin:
		case <-time.After(3 * time.Second):
		}
	}
	t.Cleanup(frenar)
	return frenar
}

func TestUnComprobanteSeEmiteYVuelveConElNumeroQueLePusoElEquipo(t *testing.T) {
	equipo := conEquipoFalso(t)
	srv := nuevoServidorQueInsiste(t, pedidoDeTique)
	correrEn(t, t.TempDir(), srv.web.URL)

	waitFor(t, 4*time.Second, "el resultado del comprobante", func() bool { return srv.resultado() != nil })
	res := srv.resultado()
	emitido, _ := res["comprobante"].(map[string]any)
	if res["ok"] != true || emitido["numero"] != float64(345) || emitido["puntoDeVenta"] != float64(7) || emitido["tipo"] != "tique" {
		t.Fatalf("no volvió el comprobante que emitió el equipo: %v", res)
	}
	if equipo.veces() != 1 || equipo.emitidos[0].Items[0].Descripcion != "Pizza muzzarella" || equipo.emitidos[0].Total != "8500.00" {
		t.Errorf("al equipo le llegó %+v", equipo.emitidos)
	}
}

// Lo que un controlador fiscal emite no se borra. Si el resultado no llega al
// servidor (se cortó internet), el servidor vuelve a entregar el pedido: el
// agente contesta con lo que ya hizo, y no emite de nuevo.
func TestSiElResultadoNoLlegaElComprobanteNoSeEmiteDosVeces(t *testing.T) {
	equipo := conEquipoFalso(t)
	srv := nuevoServidorQueInsiste(t, pedidoDeTique)
	srv.rechazos = 1
	correrEn(t, t.TempDir(), srv.web.URL)

	waitFor(t, 12*time.Second, "el resultado, en la segunda vuelta", func() bool { return srv.resultado() != nil })
	if equipo.veces() != 1 {
		t.Fatalf("emitió %d veces el mismo pedido", equipo.veces())
	}
	emitido, _ := srv.resultado()["comprobante"].(map[string]any)
	if emitido["numero"] != float64(345) {
		t.Errorf("la segunda vez tenía que contestar con el mismo comprobante: %v", srv.resultado())
	}
}

// Tampoco si entre medio se reinicia la computadora: lo que emitió queda
// anotado en el disco, al lado de la configuración.
func TestDespuesDeReiniciarElAgenteTampocoLoEmiteDeNuevo(t *testing.T) {
	equipo := conEquipoFalso(t)
	carpeta := t.TempDir()
	srv := nuevoServidorQueInsiste(t, pedidoDeTique)
	srv.rechazos = 1000 // internet caído mientras dure la primera corrida
	frenar := correrEn(t, carpeta, srv.web.URL)
	waitFor(t, 4*time.Second, "que lo emita", func() bool { return equipo.veces() == 1 })
	frenar()

	srv.mu.Lock()
	srv.rechazos = 0
	srv.mu.Unlock()
	correrEn(t, carpeta, srv.web.URL)

	waitFor(t, 6*time.Second, "el resultado, después de reiniciar", func() bool { return srv.resultado() != nil })
	if equipo.veces() != 1 {
		t.Fatalf("emitió %d veces el mismo pedido", equipo.veces())
	}
	if emitido, _ := srv.resultado()["comprobante"].(map[string]any); emitido["numero"] != float64(345) {
		t.Errorf("volvió %v", srv.resultado())
	}
}

// Si el agente se cerró mientras emitía (un corte de luz), no sabe cómo
// terminó: no lo emite de nuevo y lo dice.
func TestUnComprobanteQueQuedoAMediasVuelveComoIncierto(t *testing.T) {
	equipo := conEquipoFalso(t)
	carpeta := t.TempDir()
	empezado := `[{"id":21,"accion":"comprobante","empezado":"2026-11-22T13:00:00Z"}]`
	if err := os.WriteFile(filepath.Join(carpeta, "impresion-fiscal.json"), []byte(empezado), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := nuevoServidorQueInsiste(t, pedidoDeTique)
	correrEn(t, carpeta, srv.web.URL)

	waitFor(t, 4*time.Second, "el resultado", func() bool { return srv.resultado() != nil })
	res := srv.resultado()
	if res["ok"] != false || res["incierto"] != true || !strings.Contains(fmt.Sprint(res["error"]), "papel") {
		t.Errorf("quiero un «no se sabe» que mande a mirar el papel, vino: %v", res)
	}
	if equipo.veces() != 0 {
		t.Errorf("lo emitió %d veces: un pedido que quedó a medias no se vuelve a mandar al equipo", equipo.veces())
	}
}

func TestCuandoElEquipoNoSabeSiSalioElResultadoEsIncierto(t *testing.T) {
	equipo := conEquipoFalso(t)
	equipo.errEmitir = fmt.Errorf("%w: se mandó a cerrar el tique nº 345", fiscal.ErrIncierto)
	srv := nuevoServidorQueInsiste(t, pedidoDeTique)
	correrEn(t, t.TempDir(), srv.web.URL)

	waitFor(t, 4*time.Second, "el resultado", func() bool { return srv.resultado() != nil })
	if res := srv.resultado(); res["ok"] != false || res["incierto"] != true {
		t.Errorf("quiero incierto, vino: %v", res)
	}

	// Un error común (se sabe que no salió) no es incierto.
	otro := conEquipoFalso(t)
	otro.errEmitir = errors.New("no se emitió el comprobante: falta papel")
	srv2 := nuevoServidorQueInsiste(t, pedidoDeTique)
	correrEn(t, t.TempDir(), srv2.web.URL)
	waitFor(t, 4*time.Second, "el resultado", func() bool { return srv2.resultado() != nil })
	if res := srv2.resultado(); res["ok"] != false || res["incierto"] == true {
		t.Errorf("quiero un no a secas, vino: %v", res)
	}
}

func TestElCierreZVuelveConLoQueInformoElEquipo(t *testing.T) {
	equipo := conEquipoFalso(t)
	srv := nuevoServidorQueInsiste(t, `{"pedidos":[{"id":30,"accion":"cierre","cierre":"Z","controlador":{"marca":"hasar","conexion":"192.168.1.50"}}]}`)
	correrEn(t, t.TempDir(), srv.web.URL)

	waitFor(t, 4*time.Second, "el resultado del cierre", func() bool { return srv.resultado() != nil })
	cierre, _ := srv.resultado()["cierre"].(map[string]any)
	if srv.resultado()["ok"] != true || cierre["tipo"] != "Z" || cierre["numero"] != float64(40) || cierre["total"] != "780.00" {
		t.Errorf("volvió %v", srv.resultado())
	}
	if len(equipo.cierres) != 1 || equipo.cierres[0] != "Z" {
		t.Errorf("al equipo le llegó %v", equipo.cierres)
	}
}
