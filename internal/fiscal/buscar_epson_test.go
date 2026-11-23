package fiscal

import (
	"context"
	"strings"
	"testing"
)

// Los identificadores son como los da Windows: USB\VID_fabricante&PID_producto\serie.
const (
	usbEpsonFiscal = `USB\VID_04B8&PID_0202\EPSONN1234567890`
	usbEpsonComun  = `USB\VID_04B8&PID_0202\X5PF012345`
)

func TestUnaEpsonFiscalSeReconocePorElRegistroQueLlevaDeSerie(t *testing.T) {
	casos := []struct {
		que      string
		id       string
		registro string
	}{
		{"la fiscal, con el producto 514", usbEpsonFiscal, "EPSONN1234567890"},
		{"la fiscal, con el producto 513", `USB\VID_04B8&PID_0201\EPEPAA0000000042`, "EPEPAA0000000042"},
		{"como venga escrito", `usb\vid_04b8&pid_0202\epsonn1234567890`, "EPSONN1234567890"},
		{"una Epson de tickets común: su serie no es un registro fiscal", usbEpsonComun, ""},
		{"una sin número de serie: Windows le inventa uno", `USB\VID_04B8&PID_0202\5&2A3B4C5D&0&1`, ""},
		{"otro producto de Epson (un escáner)", `USB\VID_04B8&PID_0E15\EPSONN1234567890`, ""},
		{"otro fabricante con una serie parecida", `USB\VID_0403&PID_6001\ABCDEF1234567890`, ""},
		{"una interfaz del mismo equipo, no el equipo", `USB\VID_04B8&PID_0202&MI_00\6&1A2B3C4D&0&0000`, ""},
		{"algo que no es USB", `ACPI\PNP0303\4&1D401FB5&0`, ""},
		{"nada", "", ""},
	}
	for _, c := range casos {
		registro, es := registroDeEpsonFiscal(c.id)
		if registro != c.registro || es != (c.registro != "") {
			t.Errorf("%s (%q): registro %q, fiscal %v; se esperaba %q", c.que, c.id, registro, es, c.registro)
		}
	}
}

func TestBuscarSugiereLaEpsonFiscalEnchufadaPorUSB(t *testing.T) {
	buscarDePrueba(t, 9, nil, nil, "")
	usbEnchufados = func() []string { return []string{`USB\ROOT_HUB30\4&1`, usbEpsonComun, usbEpsonFiscal} }

	encontrados := Buscar(context.Background())

	if len(encontrados) != 1 {
		t.Fatalf("hay una sola fiscal enchufada (la otra Epson es de tickets): %+v", encontrados)
	}
	e := encontrados[0]
	if e.Marca != MarcaEpson || e.Conexion != "usb" {
		t.Errorf("marca y conexión = %q, %q; se esperaba epson por usb", e.Marca, e.Conexion)
	}
	if e.Equipo == nil || e.Equipo.Marca != MarcaEpson || e.Equipo.Registro != "EPSONN1234567890" {
		t.Errorf("tenía que venir con su registro: %+v", e.Equipo)
	}
	if e.Aviso != "" {
		t.Errorf("con la librería puesta no hay nada que avisar: %q", e.Aviso)
	}
}

func TestSiFaltaLaLibreriaLaEpsonApareceConElPorQue(t *testing.T) {
	buscarDePrueba(t, 9, nil, nil, "")
	usbEnchufados = func() []string { return []string{usbEpsonFiscal} }
	libreriaDeEpson = func() LibreriaEpson {
		return LibreriaEpson{Motivo: "En esa computadora no está la librería de Epson."}
	}

	encontrados := Buscar(context.Background())

	if len(encontrados) != 1 || encontrados[0].Marca != MarcaEpson {
		t.Fatalf("está enchufada: tiene que aparecer aunque todavía no se la pueda manejar. %+v", encontrados)
	}
	if !strings.Contains(encontrados[0].Aviso, "librería de Epson") {
		t.Errorf("el aviso tiene que decir qué falta: %q", encontrados[0].Aviso)
	}
}

func TestDosEpsonFiscalesPorUSBSonUnaSolaSugerencia(t *testing.T) {
	buscarDePrueba(t, 9, nil, nil, "")
	usbEnchufados = func() []string { return []string{usbEpsonFiscal, `USB\VID_04B8&PID_0201\EPEPAA0000000042`} }

	encontrados := Buscar(context.Background())

	// Por USB la librería se conecta «a la que está enchufada»: no se elige cuál.
	if len(encontrados) != 1 || encontrados[0].Conexion != "usb" {
		t.Fatalf("por USB hay una sola manera de conectarse: %+v", encontrados)
	}
	if encontrados[0].Equipo != nil && encontrados[0].Equipo.Registro != "" {
		t.Errorf("con dos enchufadas no se sabe cuál va a contestar: no se promete un registro (%q)", encontrados[0].Equipo.Registro)
	}
}

func TestLasHasarVanPrimeroYLaEpsonDespues(t *testing.T) {
	equipo := nuevaHasarFalsa(t)
	difusion := nuevaDifusionFalsa(t)
	buscarDePrueba(t, difusion.puerto(), aca, nil, puertoDe(equipo.direccion()))
	usbEnchufados = func() []string { return []string{usbEpsonFiscal} }

	encontrados := Buscar(context.Background())

	if len(encontrados) != 2 || encontrados[0].Marca != MarcaHasar || encontrados[1].Marca != MarcaEpson {
		t.Fatalf("se esperaba la Hasar de la red y después la Epson del USB: %+v", encontrados)
	}
}
