package fiscal

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// campos arma un comando o una respuesta como los escribe el manual de Hasar:
// «[ESC][40][FS]82» es campos(0x40, "82").
func campos(id byte, valores ...string) []byte {
	b := []byte{0x1B, id}
	for _, v := range valores {
		b = append(b, 0x1C)
		b = append(b, v...)
	}
	return b
}

// Los ejemplos son los del «Quick Manual de Comandos» de Hasar (Rev007): el
// mismo comando en formato empaquetado y en XML. Lo que arma el agente desde
// los campos con nombre tiene que ser, byte a byte, lo que el manual escribe
// empaquetado.
func TestLosComandosSeArmanComoLosEscribeElManualDeHasar(t *testing.T) {
	casos := []struct {
		comando  string
		campos   any
		esperado []byte
	}{
		{"ConsultarVersion", nil, campos(0x7F)},
		{"ConsultarDatosInicializacion", nil, campos(0x73)},
		{"Cancelar", nil, campos(0x98)},
		{"AbrirDocumento", hasarCodigo{"TiqueFacturaB"}, campos(0x40, "82")},
		{"ConsultarEstado", hasarCodigo{"TiqueFacturaA"}, campos(0x2A, "81")},
		{"ConsultarSubtotal", map[string]string{"Impresion": "NoImprimeSubtotal", "ModoDisplay": "DisplayNo"}, campos(0x43, "N", "0")},
		{"CerrarJornadaFiscal", map[string]string{"Reporte": "ReporteZ"}, campos(0x39, "Z")},
		{"CerrarJornadaFiscal", map[string]string{"Reporte": "ReporteX"}, campos(0x39, "X")},
		{"CerrarDocumento", struct{}{}, campos(0x45)},
		{"CerrarDocumento", map[string]string{"Copias": "2", "DireccionEMail": "cliente@suempresa.com.ar"}, campos(0x45, "2", "cliente@suempresa.com.ar")},
		{
			"CargarDocumentoAsociado",
			hasarAsociado{NumeroLinea: "1", CodigoComprobante: "RemitoR", NumeroPos: "99998", NumeroComprobante: "00000876"},
			campos(0x93, "1", "91", "99998", "00000876"),
		},
		{
			// El manual lo escribe con los tres datos adicionales vacíos al final
			// ([FS][FS][FS]); en CargarDocumentoAsociado, en cambio, omite el
			// último campo opcional. Acá se omiten los vacíos del final.
			"CargarDatosCliente",
			hasarCliente{RazonSocial: "ELSIS SRL - Electrosistemas a medida", NumeroDocumento: "22222222226", ResponsabilidadIVA: "ResponsableInscripto", TipoDocumento: "TipoCUIT", Domicilio: "Avenida Julio Argentino Roca 852 - CABA"},
			campos(0x62, "ELSIS SRL - Electrosistemas a medida", "22222222226", "I", "C", "Avenida Julio Argentino Roca 852 - CABA"),
		},
		{
			"ImprimirItem",
			map[string]string{
				"Descripcion": "Cable miniplug 1.5", "Cantidad": "1.0", "PrecioUnitario": "200.00", "CondicionIVA": "Gravado", "AlicuotaIVA": "21.00",
				"OperacionMonto": "ModoSumaMonto", "TipoImpuestoInterno": "IIVariableKIVA", "MagnitudImpuestoInterno": "0.00", "ModoDisplay": "DisplayNo",
				"ModoBaseTotal": "ModoPrecioTotal", "UnidadReferencia": "20", "CodigoProducto": "779123456789", "CodigoInterno": "C1130", "UnidadMedida": "62",
			},
			campos(0x42, "Cable miniplug 1.5", "1.0", "200.00", "7", "21.00", "M", "0", "0.00", "0", "T", "20", "779123456789", "C1130", "62"),
		},
		{
			"ImprimirAjuste",
			map[string]string{"Descripcion": "Promocion Fidelidad", "Monto": "18.00", "ModoDisplay": "DisplayNo", "ModoBaseTotal": "ModoPrecioTotal", "CodigoProducto": "7790001001030", "Operacion": "AjusteNeg"},
			campos(0x54, "Promocion Fidelidad", "18.00", "0", "T", "7790001001030", "D"),
		},
		{
			"ImprimirPago",
			map[string]string{
				"Descripcion": "Tarjeta de Credito", "Monto": "200.00", "Operacion": "Pagar", "ModoDisplay": "DisplayNo",
				"DescripcionAdicional": "Nro.: *******3245", "CodigoFormaPago": "TarjetaDeCredito", "Cuotas": "6", "Cupones": "12345678", "Referencia": "ABC123",
			},
			campos(0x44, "Tarjeta de Credito", "200.00", "T", "0", "Nro.: *******3245", "20", "6", "12345678", "ABC123"),
		},
	}
	for _, c := range casos {
		armado, err := armarComando(c.comando, c.campos)
		if err != nil {
			t.Errorf("%s: %v", c.comando, err)
			continue
		}
		if string(armado) != string(c.esperado) {
			t.Errorf("%s:\n  armado   %q\n  el manual %q", c.comando, armado, c.esperado)
		}
	}
}

// Lo que el agente manda hoy por la red, por el puerto: el campo que no se
// manda deja su lugar vacío y los demás no se corren.
func TestLosCamposQueNoSeMandanDejanSuLugar(t *testing.T) {
	ajuste, err := armarComando("ImprimirAjuste", hasarAjuste{Descripcion: "Descuento efectivo", Monto: "500.00", ModoDisplay: "DisplayNo", ModoBaseTotal: "ModoPrecioTotal", Operacion: "AjusteNeg"})
	if err != nil {
		t.Fatal(err)
	}
	// El quinto campo es el código de producto, que no se manda: la operación sigue siendo el sexto.
	if esperado := campos(0x54, "Descuento efectivo", "500.00", "0", "T", "", "D"); string(ajuste) != string(esperado) {
		t.Errorf("ajuste:\n  armado   %q\n  esperado %q", ajuste, esperado)
	}

	pago, err := armarComando("ImprimirPago", hasarPago{Descripcion: "Efectivo", Monto: "8500.00", Operacion: "Pagar", ModoDisplay: "DisplayNo", CodigoFormaPago: "Efectivo"})
	if err != nil {
		t.Fatal(err)
	}
	if esperado := campos(0x44, "Efectivo", "8500.00", "T", "0", "", "8"); string(pago) != string(esperado) {
		t.Errorf("pago:\n  armado   %q\n  esperado %q", pago, esperado)
	}

	// Un renglón sin IVA informado (el tique de un monotributista).
	item, err := armarComando("ImprimirItem", hasarItem{
		Descripcion: "Pizza muzzarella", Cantidad: "2", PrecioUnitario: "8500.00", OperacionMonto: "ModoSumaMonto",
		TipoImpuestoInterno: "IIVariableKIVA", MagnitudImpuestoInterno: "0.00", ModoDisplay: "DisplayNo", ModoBaseTotal: "ModoPrecioTotal", UnidadReferencia: "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if esperado := campos(0x42, "Pizza muzzarella", "2", "8500.00", "", "", "M", "0", "0.00", "0", "T", "1"); string(item) != string(esperado) {
		t.Errorf("item:\n  armado   %q\n  esperado %q", item, esperado)
	}
}

func TestCadaValorQueMandaElAgenteTieneSuCodigoEmpaquetado(t *testing.T) {
	// Lo que hoy se manda por nombre tiene que tener traducción: un nombre que
	// pasara de largo llegaría al equipo como texto y lo rechazaría.
	for _, nombre := range hasarDocumentos {
		if _, ok := valoresDeHasar["TipoDocumento"][nombre]; !ok {
			t.Errorf("el tipo de documento %s no tiene código empaquetado", nombre)
		}
	}
	for _, nombre := range hasarResponsables {
		if _, ok := valoresDeHasar["ResponsabilidadIVA"][nombre]; !ok {
			t.Errorf("la responsabilidad %s no tiene código empaquetado", nombre)
		}
	}
	for _, nombre := range hasarCondiciones {
		if _, ok := valoresDeHasar["CondicionIVA"][nombre]; !ok {
			t.Errorf("la condición de IVA %s no tiene código empaquetado", nombre)
		}
	}
	for _, nombre := range hasarMedios {
		if _, ok := valoresDeHasar["CodigoFormaPago"][nombre]; !ok {
			t.Errorf("el medio de pago %s no tiene código empaquetado", nombre)
		}
	}
	for _, tipo := range []string{TipoTique, TipoFactura, TipoNotaDeCredito} {
		for _, letra := range []string{"", "A", "B", "C", "M"} {
			if (tipo == TipoFactura) == (letra == "") && tipo != TipoNotaDeCredito {
				continue
			}
			if nombre := codigoHasar(tipo, letra); valoresDeHasar["CodigoComprobante"][nombre] == "" {
				t.Errorf("el comprobante %s no tiene código empaquetado", nombre)
			}
		}
	}
	// El documento sin identificar va como un espacio, que no es «vacío».
	sinIdentificar, err := armarComando("CargarDatosCliente", hasarCliente{RazonSocial: "Juan", NumeroDocumento: "", ResponsabilidadIVA: "ConsumidorFinal", TipoDocumento: "TipoNinguno"})
	if err != nil || string(sinIdentificar) != string(campos(0x62, "Juan", "", "C", " ")) {
		t.Errorf("cliente sin identificar: %q (%v)", sinIdentificar, err)
	}
}

func TestUnCampoQueElComandoNoTieneEsUnErrorYNoSeManda(t *testing.T) {
	if _, err := armarComando("AbrirDocumento", map[string]string{"CodigoComprobante": "Tique", "Inventado": "1"}); err == nil || !strings.Contains(err.Error(), "Inventado") {
		t.Errorf("un campo que no existe tiene que frenar el comando: %v", err)
	}
	if _, err := armarComando("Bailar", nil); err == nil {
		t.Error("un comando que no se sabe empaquetar tiene que dar error")
	}
}

func TestLosTextosVanEnLaCodificacionDelEquipoYSinCaracteresDeControl(t *testing.T) {
	armado, err := armarComando("ImprimirItem", map[string]string{"Descripcion": "Ñoquis \x1cde\x02 papa «caseros»", "Cantidad": "1", "PrecioUnitario": "1.00"})
	if err != nil {
		t.Fatal(err)
	}
	// CP-437: Ñ es A5; un separador o un comienzo de paquete adentro de un texto rompería la trama.
	esperado := append([]byte{0x1B, 0x42, 0x1C, 0xA5}, []byte("oquis  de  papa \xAEcaseros\xAF\x1c1\x1c1.00")...)
	if string(armado) != string(esperado) {
		t.Errorf("\n  armado   %q\n  esperado %q", armado, esperado)
	}
}

// Las respuestas, también con los ejemplos del manual: empaquetadas tienen
// que decir lo mismo que su versión con nombres.
func TestLasRespuestasSeLeenComoLasEscribeElManualDeHasar(t *testing.T) {
	casos := []struct {
		comando   string
		respuesta []byte
		esperado  string
	}{
		{
			"ConsultarVersion",
			campos(0x7F, "0000", "0000", "Hasar Modelo V: nn.nn", "Hasar", "D6", "6.00A", "29/01/2015 15:26:08", "1"),
			`{"Estado":{"Impresora":[],"Fiscal":[]},"Version":"Hasar Modelo V: nn.nn","Marca":"Hasar","NombreProducto":"D6","VersionMotor":"6.00A","FechaFirmware":"29/01/2015 15:26:08","VersionProtocolo":"1"}`,
		},
		{
			"ConsultarDatosInicializacion",
			campos(0x73, "0000", "0200", "30123456781", "KROKANTISSIMO SA", "HHAASS1234567890", "99998", "150101", "IIBB-123456789012345", "I", "20150101"),
			`{"Estado":{"Impresora":[],"Fiscal":["MemoriaFiscalInicializada"]},"CUIT":"30123456781","RazonSocial":"KROKANTISSIMO SA","Registro":"HHAASS1234567890","NumeroPos":"99998","FechaInicioActividades":"150101","IngBrutos":"IIBB-123456789012345","ResponsabilidadIVA":"I","FechaInicioActividadesCompleta":"20150101"}`,
		},
		{
			// El ejemplo del manual: 0004 de estado auxiliar es «DatosClienteAlmacenados», 3 «EnJornadaFiscal», 83 «Tique», 81 «TiqueFacturaA».
			"ConsultarEstado",
			campos(0x2A, "0000", "0000", "0004", "3", "83", "81", "2", "0", "0"),
			`{"Estado":{"Impresora":[],"Fiscal":[]},"EstadoAuxiliar":["DatosClienteAlmacenados"],"EstadoInterno":"EnJornadaFiscal","ComprobanteEnCurso":"Tique","CodigoComprobante":"TiqueFacturaA","NumeroUltimoComprobante":"2","CantidadEmitidos":"0","CantidadCancelados":"0"}`,
		},
		{"AbrirDocumento", campos(0x40, "0000", "0000", "00000004", "1"), `{"Estado":{"Impresora":[],"Fiscal":[]},"NumeroComprobante":"00000004","IndiceAuditoria":"1"}`},
		{"CerrarDocumento", campos(0x45, "0000", "0000", "00000345", "1", "1"), `{"Estado":{"Impresora":[],"Fiscal":[]},"NumeroComprobante":"00000345","CantidadDePaginas":"1","IndiceAuditoria":"1"}`},
		{"Cancelar", campos(0x98, "0000", "0000"), `{"Estado":{"Impresora":[],"Fiscal":[]}}`},
		{"ImprimirItem", campos(0x42, "0000", "0000", "1"), `{"Estado":{"Impresora":[],"Fiscal":[]},"IndiceAuditoria":"1"}`},
		{"ImprimirAjuste", campos(0x54, "0000", "0000", "1", "1"), `{"Estado":{"Impresora":[],"Fiscal":[]},"IndiceAuditoria":"1","CantidadIVAs":"1"}`},
		{"ImprimirPago", campos(0x44, "0000", "0000", "0.00"), `{"Estado":{"Impresora":[],"Fiscal":[]},"Saldo":"0.00"}`},
		{
			"ConsultarSubtotal",
			campos(0x43, "0000", "0000", "103.00", "990.40", "9.79", "999.90", "19.40", "682.80", "278.61", "0.01"),
			`{"Estado":{"Impresora":[],"Fiscal":[]},"CantidadItems":"103.00","Subtotal":"990.40","MontoIVA":"9.79","MontoPagado":"999.90","MontoOtrosTributos":"19.40","MontoImpInternos":"682.80","MontoBase":"278.61","AjusteRedondeo":"0.01"}`,
		},
		{
			"CerrarJornadaFiscal",
			campos(0x39, "0000", "0000", "X", "1", "150202", "105842", "150202", "121145", "750.00", "43.42", "35.26", "1", "0.00", "0.00", "0.00", "0", "1"),
			`{"Estado":{"Impresora":[],"Fiscal":[]},"Reporte":"ReporteX","Numero":"1","FechaInicio":"150202","HoraInicio":"105842","FechaCierre":"150202","HoraCierre":"121145","DF_Total":"750.00","DF_TotalIVA":"43.42","DF_TotalTributos":"35.26","DF_CantidadEmitidos":"1","NC_Total":"0.00","NC_TotalIVA":"0.00","NC_TotalTributos":"0.00","NC_CantidadEmitidos":"0","DNFH_CantidadEmitidos":"1"}`,
		},
		{
			"CerrarJornadaFiscal",
			campos(0x39, "0000", "0000", "Z", "40", "150130", "780.00", "705.00", "0.00", "0.00", "0.00", "75.00", "3", "0", "0.00", "0.00", "0.00", "0.00", "0.00", "0.00", "0", "0", "20.00", "1"),
			`{"Estado":{"Impresora":[],"Fiscal":[]},"Reporte":"ReporteZ","Numero":"40","Fecha":"150130","DF_Total":"780.00","DF_TotalGravado":"705.00","DF_TotalNoGravado":"0.00","DF_TotalExento":"0.00","DF_TotalIVA":"0.00","DF_TotalTributos":"75.00","DF_CantidadEmitidos":"3","DF_CantidadCancelados":"0","NC_Total":"0.00","NC_TotalGravado":"0.00","NC_TotalNoGravado":"0.00","NC_TotalExento":"0.00","NC_TotalIVA":"0.00","NC_TotalTributos":"0.00","NC_CantidadEmitidos":"0","NC_CantidadCancelados":"0","DNFH_Total":"20.00","DNFH_CantidadEmitidos":"1"}`,
		},
	}
	for _, c := range casos {
		leido, err := leerRespuesta(c.comando, c.respuesta)
		if err != nil {
			t.Errorf("%s: %v", c.comando, err)
			continue
		}
		var tengo, quiero any
		if err := json.Unmarshal(leido, &tengo); err != nil {
			t.Errorf("%s: lo leído no es JSON: %v\n%s", c.comando, err, leido)
			continue
		}
		_ = json.Unmarshal([]byte(c.esperado), &quiero)
		if !reflect.DeepEqual(tengo, quiero) {
			t.Errorf("%s:\n  leído    %s\n  esperado %s", c.comando, leido, c.esperado)
		}
	}
}

// Un equipo con un protocolo anterior manda menos campos al final: lo que no
// vino, no está.
func TestUnaRespuestaMasCortaSeLeeHastaDondeLlega(t *testing.T) {
	leido, err := leerRespuesta("AbrirDocumento", campos(0x40, "0000", "0000", "00000004"))
	if err != nil {
		t.Fatal(err)
	}
	var r map[string]any
	_ = json.Unmarshal(leido, &r)
	if r["NumeroComprobante"] != "00000004" {
		t.Errorf("número = %v", r["NumeroComprobante"])
	}
	if _, vino := r["IndiceAuditoria"]; vino {
		t.Error("el índice de auditoría no vino y aparece")
	}
}

// Por el puerto el rechazo no viene con un texto: viene en los bits del
// estado fiscal, y el motivo de impresora (papel, tapa) en los del otro.
func TestElRechazoSeLeeDelEstadoFiscal(t *testing.T) {
	casos := []struct {
		que       string
		impresora string
		fiscal    string
		dice      []string
	}{
		{"no es válido en ese estado", "0000", "0220", []string{"ImprimirPago", "estado"}},
		{"un parámetro mal", "0000", "0210", []string{"parámetro"}},
		{"error de ejecución sin papel", "0020", "8200", []string{"ejecución", "papel"}},
		{"la tapa abierta", "0100", "0208", []string{"tapa"}},
	}
	for _, c := range casos {
		_, err := leerRespuesta("ImprimirPago", campos(0x44, c.impresora, c.fiscal))
		if err == nil {
			t.Errorf("%s: el estado fiscal %s trae un error y se dio por bueno", c.que, c.fiscal)
			continue
		}
		for _, palabra := range c.dice {
			if !strings.Contains(err.Error(), palabra) {
				t.Errorf("%s: el error tenía que nombrar «%s»: %v", c.que, palabra, err)
			}
		}
	}
	// Lo que no es un error no frena: memoria inicializada, documento abierto, cajón abierto.
	if _, err := leerRespuesta("ImprimirPago", campos(0x44, "4000", "3200", "0.00")); err != nil {
		t.Errorf("no había ningún error en el estado: %v", err)
	}
}

func TestUnaRespuestaQueNoEsDeEseComandoNoSeAcepta(t *testing.T) {
	for que, respuesta := range map[string][]byte{
		"de otro comando":    campos(0x45, "0000", "0000", "00000345", "1"),
		"sin los estados":    campos(0x44, "0000"),
		"un estado sin hexa": campos(0x44, "hola", "0000"),
		"vacía":              {},
		"sin el comienzo":    []byte("0000\x1c0000"),
	} {
		if _, err := leerRespuesta("ImprimirPago", respuesta); err == nil {
			t.Errorf("una respuesta %s se dio por buena", que)
		}
	}
}

func TestLaTramaLlevaComienzoSecuenciaFinYSuma(t *testing.T) {
	paquete := empaquetar(0x21, campos(0x7F))
	// STX, la secuencia, el comando, ETX y la suma de todo lo anterior en cuatro dígitos hexa.
	// 02 + 21 + 1B + 7F + 03 = 0xC0.
	if esperado := "\x02\x21\x1b\x7f\x0300C0"; string(paquete) != esperado {
		t.Fatalf("paquete = %q, se esperaba %q", paquete, esperado)
	}
	sn, contenido, err := desempaquetar(paquete)
	if err != nil || sn != 0x21 || string(contenido) != string(campos(0x7F)) {
		t.Errorf("desempaquetar = %x, %q, %v", sn, contenido, err)
	}
	// La suma en minúsculas también vale; una que no da, no.
	if _, _, err := desempaquetar([]byte("\x02\x21\x1b\x7f\x0300c0")); err != nil {
		t.Errorf("la suma en minúsculas tiene que valer: %v", err)
	}
	for que, malo := range map[string]string{
		"la suma no da":      "\x02\x21\x1b\x7f\x0300C1",
		"falta el comienzo":  "\x21\x1b\x7f\x0300C0",
		"falta el fin":       "\x02\x21\x1b\x7f00C0",
		"la suma incompleta": "\x02\x21\x1b\x7f\x0300",
		"secuencia inválida": "\x02\x05\x1b\x7f\x0300A4",
	} {
		if _, _, err := desempaquetar([]byte(malo)); err == nil {
			t.Errorf("un paquete al que %s se dio por bueno", que)
		}
	}
}

func TestLaSecuenciaVaDe20A7FYDaLaVuelta(t *testing.T) {
	if siguienteSecuencia(0x20) != 0x21 || siguienteSecuencia(0x7E) != 0x7F || siguienteSecuencia(0x7F) != 0x20 {
		t.Errorf("la secuencia no avanza como dice el manual: %x %x %x", siguienteSecuencia(0x20), siguienteSecuencia(0x7E), siguienteSecuencia(0x7F))
	}
}
