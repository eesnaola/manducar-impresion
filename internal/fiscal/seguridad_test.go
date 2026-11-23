package fiscal

import (
	"context"
	"strings"
	"testing"
)

// Lo que llega del servidor no se da por bien armado: un comprobante raro
// tiene que volver con un error, no tirar abajo el agente.
func TestSoloUnaNotaDeCreditoLlevaComprobanteAsociado(t *testing.T) {
	for nombre, asociado := range map[string]Asociado{
		"con la fecha mal":   {Tipo: TipoTique, PuntoDeVenta: 7, Numero: 12, Fecha: "22/11/2026"},
		"sin fecha":          {Tipo: TipoTique, PuntoDeVenta: 7, Numero: 12},
		"aunque esté entero": {Tipo: TipoTique, PuntoDeVenta: 7, Numero: 12, Fecha: "2026-11-22"},
	} {
		falsa := nuevaHasarFalsa(t)
		c := tiqueDePrueba()
		c.Asociado = &asociado

		_, err := abrirFalsa(t, falsa).Emitir(context.Background(), c)

		if err == nil || !strings.Contains(err.Error(), "nota de crédito") {
			t.Errorf("un tique %s: quiero el rechazo, vino %v", nombre, err)
		}
		if vino := falsa.secuencia(); vino != "" {
			t.Errorf("un tique %s no tenía que llegarle al equipo; llegó %s", nombre, vino)
		}
	}
}

// La dirección de una Hasar es un equipo de la red, con su puerto si hace
// falta. El agente le manda ahí la clave de acceso: no la manda a una
// dirección con camino, con usuario o con cualquier otra cosa adentro.
func TestLaDireccionDeLaHasarEsUnEquipoYNadaMas(t *testing.T) {
	for _, buena := range []string{"192.168.1.50", "192.168.1.50:8080", "caja.local", "http://192.168.1.50/", " 192.168.1.50 ", "[fe80::1]:80", "IMPRESORA-FISCAL"} {
		if _, err := Abrir(Controlador{Marca: MarcaHasar, Conexion: buena}); err != nil {
			t.Errorf("%q es una dirección válida: %v", buena, err)
		}
	}
	for _, mala := range []string{
		"192.168.1.50/admin", "192.168.1.50/../otra?x=1", "usuario:clave@192.168.1.50", "192.168.1.50?x=1", "192.168.1.50#x",
		"192.168.1.50 otro.sitio", "192.168.1.50:puerto", "192.168.1.50:999999", "http://", "ftp://192.168.1.50", "a\nb", "",
	} {
		_, err := Abrir(Controlador{Marca: MarcaHasar, Conexion: mala})
		if err == nil {
			t.Errorf("%q no es la dirección de un equipo y se aceptó", mala)
			continue
		}
		if !strings.Contains(err.Error(), "dirección") {
			t.Errorf("%q: el error tiene que hablar de la dirección: %v", mala, err)
		}
	}
}

func TestLaDireccionDeUnaEpsonPorRedTambien(t *testing.T) {
	for _, buena := range []string{"192.168.1.60", "192.168.1.60:443"} {
		if puerto, err := puertoEpson(buena); err != nil || puerto != "lan:"+buena {
			t.Errorf("puertoEpson(%q) = %q, %v", buena, puerto, err)
		}
	}
	for _, mala := range []string{"192.168.1.60/x", "u:p@192.168.1.60", "192.168.1.60 x", "lan:192.168.1.60", "http://otro.sitio/x"} {
		if puerto, err := puertoEpson(mala); err == nil {
			t.Errorf("puertoEpson(%q) = %q: no es la dirección de un equipo y se aceptó", mala, puerto)
		}
	}
}
