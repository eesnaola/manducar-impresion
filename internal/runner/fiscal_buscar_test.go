package runner

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eesnaola/manducar-impresion/internal/fiscal"
)

// buscarDePrueba pone lo que «encuentra» la búsqueda, para que los tests del
// runner no salgan a recorrer la red de la máquina que los corre. Devuelve
// cuántas veces se buscó.
func buscarDePrueba(t *testing.T, encontrados []fiscal.Encontrado) *atomic.Int32 {
	t.Helper()
	var veces atomic.Int32
	anterior := buscarFiscal
	buscarFiscal = func(context.Context) []fiscal.Encontrado {
		veces.Add(1)
		return encontrados
	}
	t.Cleanup(func() { buscarFiscal = anterior })
	return &veces
}

func (s *servidorFiscal) busqueda() (map[string]any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.busquedas) == 0 {
		return nil, false
	}
	return s.busquedas[0], true
}

// «Buscar» en Agregar impresora fiscal: el servidor le pide a esta
// computadora que busque en su red, y ella devuelve lo que encontró.
func TestUnaBusquedaPedidaDesdeElPanelDevuelveLoEncontrado(t *testing.T) {
	buscarDePrueba(t, []fiscal.Encontrado{
		{Marca: fiscal.MarcaHasar, Conexion: "192.168.0.57", Equipo: &fiscal.Equipo{Marca: fiscal.MarcaHasar, Modelo: "Hasar SMH/PT-250F V: 02.00", PuntoDeVenta: 7, CUIT: "30123456781"}},
		{Marca: fiscal.MarcaHasar, Conexion: "192.168.0.60", Aviso: "Tiene una clave de acceso distinta de la de fábrica."},
	})
	srv := nuevoServidorFiscal(t, `{"pedidos":[],"buscar":"b7c1"}`)
	correrContra(t, srv.web.URL)

	waitFor(t, 4*time.Second, "lo que encontró la búsqueda", func() bool { _, ok := srv.busqueda(); return ok })
	vuelta, _ := srv.busqueda()
	if vuelta["id"] != "b7c1" {
		t.Errorf("la respuesta tiene que decir de qué búsqueda es: %v", vuelta["id"])
	}
	encontrados, _ := vuelta["encontrados"].([]any)
	if len(encontrados) != 2 {
		t.Fatalf("encontró dos y devolvió %v", vuelta["encontrados"])
	}
	primera, _ := encontrados[0].(map[string]any)
	equipo, _ := primera["equipo"].(map[string]any)
	if primera["marca"] != "hasar" || primera["conexion"] != "192.168.0.57" || equipo["modelo"] != "Hasar SMH/PT-250F V: 02.00" || equipo["puntoDeVenta"] != float64(7) {
		t.Errorf("la primera no volvió como se encontró: %v", primera)
	}
	segunda, _ := encontrados[1].(map[string]any)
	if segunda["conexion"] != "192.168.0.60" || segunda["aviso"] == nil || segunda["equipo"] != nil {
		t.Errorf("la segunda trae el aviso y nada del equipo: %v", segunda)
	}
}

// Sin nada encontrado vuelve una lista vacía: el panel distingue «no encontró
// nada» de «todavía no contestó».
func TestUnaBusquedaQueNoEncuentraNadaDevuelveLaListaVacia(t *testing.T) {
	buscarDePrueba(t, nil)
	srv := nuevoServidorFiscal(t, `{"pedidos":[],"buscar":"b7c2"}`)
	correrContra(t, srv.web.URL)

	waitFor(t, 4*time.Second, "la respuesta de la búsqueda", func() bool { _, ok := srv.busqueda(); return ok })
	vuelta, _ := srv.busqueda()
	lista, esLista := vuelta["encontrados"].([]any)
	if !esLista || len(lista) != 0 {
		t.Errorf("quiero una lista vacía, vino %#v", vuelta["encontrados"])
	}
}

func TestSiNadiePidioBuscarNoSeBusca(t *testing.T) {
	veces := buscarDePrueba(t, nil)
	equipo := hasarDePrueba(t)
	srv := nuevoServidorFiscal(t, fmt.Sprintf(`{"pedidos":[{"id":9,"accion":"info","controlador":{"marca":"hasar","conexion":"%s"}}]}`, equipo))
	correrContra(t, srv.web.URL)

	waitFor(t, 4*time.Second, "el resultado del pedido 9", func() bool { _, ok := srv.resultado(9); return ok })
	time.Sleep(200 * time.Millisecond)
	if n := veces.Load(); n != 0 {
		t.Errorf("buscó %d veces en la red sin que nadie lo pidiera", n)
	}
}

// Los pedidos van primero: un comprobante no espera a que termine una búsqueda.
func TestLosPedidosSeAtiendenAntesDeBuscar(t *testing.T) {
	equipo := hasarDePrueba(t)
	var srv *servidorFiscal
	var yaEstabaElPedido atomic.Bool
	anterior := buscarFiscal
	buscarFiscal = func(context.Context) []fiscal.Encontrado {
		_, ok := srv.resultado(9)
		yaEstabaElPedido.Store(ok)
		return nil
	}
	t.Cleanup(func() { buscarFiscal = anterior })
	srv = nuevoServidorFiscal(t, fmt.Sprintf(`{"pedidos":[{"id":9,"accion":"info","controlador":{"marca":"hasar","conexion":"%s"}}],"buscar":"b7c3"}`, equipo))
	correrContra(t, srv.web.URL)

	waitFor(t, 4*time.Second, "la respuesta de la búsqueda", func() bool { _, ok := srv.busqueda(); return ok })
	if !yaEstabaElPedido.Load() {
		t.Error("se puso a buscar antes de contestar el pedido que estaba esperando")
	}
}
