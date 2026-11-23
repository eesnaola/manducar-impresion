package fiscal

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Lo que el servidor manda a emitir es un contrato entre dos repos. Las
// muestras de testdata son, tal cual, lo que arma Manducar para cada caso
// (allá las genera y las cuida `FacturarPorLaFiscalTest`, en
// tests/Invoicing/fixtures). Acá se comprueba que el agente las entiende
// enteras —sin un campo que no conozca— y que las puede emitir en las dos
// marcas. Cuando el servidor cambia lo que manda, se trae la copia nueva y
// este test dice si el agente está a la altura.
func TestLosComprobantesDelServidorSeEntiendenYSeEmitenEnLasDosMarcas(t *testing.T) {
	archivos, err := filepath.Glob(filepath.Join("testdata", "comprobante*-del-servidor*.json"))
	if err != nil || len(archivos) != 2 {
		t.Fatalf("faltan las muestras del servidor en testdata: %v %v", archivos, err)
	}
	vistos := 0
	for _, archivo := range archivos {
		crudo, err := os.ReadFile(archivo)
		if err != nil {
			t.Fatal(err)
		}
		var muestras map[string]json.RawMessage
		if err := json.Unmarshal(crudo, &muestras); err != nil {
			t.Fatalf("%s: %v", archivo, err)
		}
		for nombre, muestra := range muestras {
			vistos++
			t.Run(nombre, func(t *testing.T) {
				var c Comprobante
				lector := json.NewDecoder(bytes.NewReader(muestra))
				lector.DisallowUnknownFields() // un campo que el agente no conoce es un dato que se pierde en el papel
				if err := lector.Decode(&c); err != nil {
					t.Fatalf("el agente no entiende lo que manda el servidor: %v", err)
				}
				if err := c.Validar(); err != nil {
					t.Fatalf("el agente lo rechazaría antes de tocar el equipo: %v", err)
				}
				monotributo := strings.Contains(nombre, "monotributista")

				hasar := nuevaHasarFalsa(t)
				responsabilidad := "I"
				if monotributo {
					responsabilidad = "M"
				}
				hasar.contesta["ConsultarDatosInicializacion"] = `{"ConsultarDatosInicializacion":{"Estado":{"Impresora":[],"Fiscal":[]},"CUIT":"` + c.CUIT + `","RazonSocial":"PARRILLA DON PEPE SRL","Registro":"HHAASS1234567890","NumeroPos":"7","ResponsabilidadIVA":"` + responsabilidad + `"}}`
				emitido, err := abrirFalsa(t, hasar).Emitir(context.Background(), c)
				if err != nil {
					t.Errorf("Hasar: %v\n secuencia: %s", err, hasar.secuencia())
				} else if emitido.Numero != 1 || emitido.PuntoDeVenta != 7 || emitido.Tipo != c.Tipo || emitido.Letra != c.Letra {
					t.Errorf("Hasar emitió %+v", emitido)
				}

				// Y la misma Hasar enchufada por USB: es otro idioma, tiene que salir igual.
				porPuerto := nuevaHasarPorPuerto(t)
				porPuerto.enchufar("COM7")
				porPuerto.contesta["ConsultarDatosInicializacion"] = hasar.contesta["ConsultarDatosInicializacion"]
				emitido, err = porPuerto.driver("COM7").Emitir(context.Background(), c)
				if err != nil {
					t.Errorf("Hasar por el puerto: %v\n secuencia: %s", err, porPuerto.secuencia())
				} else if emitido.Numero != 1 || emitido.PuntoDeVenta != 7 || emitido.Tipo != c.Tipo || emitido.Letra != c.Letra {
					t.Errorf("Hasar por el puerto emitió %+v", emitido)
				}
				if porRed, porUSB := sinLaPresentacion(hasar.secuencia()), sinLaPresentacion(porPuerto.secuencia()); porRed != porUSB {
					t.Errorf("por el puerto se le dictó otra cosa que por la red:\n red    %s\n puerto %s", porRed, porUSB)
				}

				epson := nuevaEpsonFalsa()
				if monotributo {
					epson.emisor = "M"
				}
				emitido, err = epsonCon(epson, "usb", 0).Emitir(context.Background(), c)
				if err != nil {
					t.Errorf("Epson: %v\n llamadas: %s", err, strings.Join(epson.llamadas, "\n  "))
				} else if emitido.Numero != 1 || emitido.PuntoDeVenta != 7 || emitido.Tipo != c.Tipo || emitido.Letra != c.Letra {
					t.Errorf("Epson emitió %+v", emitido)
				}
			})
		}
	}
	if vistos < 7 {
		t.Errorf("se probaron %d muestras: faltan casos", vistos)
	}
}

// sinLaPresentacion saca la consulta de versión con la que arranca una
// conversación por el puerto: es del camino, no del comprobante.
func sinLaPresentacion(secuencia string) string {
	return strings.TrimPrefix(secuencia, "ConsultarVersion,")
}
