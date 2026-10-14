package diagnostico

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eesnaola/manducar-impresion/internal/printer"
)

type canalFalso struct {
	mu           sync.Mutex
	normal       [][]byte
	cola         [][]byte
	directo      *directoFalso
	errDirecto   error
	errPreguntar error
}

func (c *canalFalso) PuedePreguntar() error { return c.errPreguntar }

func (c *canalFalso) Normal(_ context.Context, b []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.normal = append(c.normal, append([]byte(nil), b...))
	return nil
}

func (c *canalFalso) Cola(_ context.Context, b []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cola = append(c.cola, append([]byte(nil), b...))
	return nil
}

func (c *canalFalso) Directo(context.Context) (printer.Directo, error) {
	if c.errDirecto != nil {
		return nil, c.errDirecto
	}
	if c.directo == nil {
		c.directo = &directoFalso{}
	}
	return c.directo, nil
}

// directoFalso contesta según lo último que se le escribió (en hexa).
type directoFalso struct {
	escrito    [][]byte
	respuestas map[string][]byte
	pendiente  []byte
	cerrado    int
}

func (d *directoFalso) Escribir(b []byte, _ time.Duration) error {
	d.escrito = append(d.escrito, append([]byte(nil), b...))
	d.pendiente = d.respuestas[hex.EncodeToString(b)]
	return nil
}

func (d *directoFalso) Leer(time.Duration) []byte {
	r := d.pendiente
	d.pendiente = nil
	return r
}

func (d *directoFalso) Close() error { d.cerrado++; return nil }

func largos(bs [][]byte) []int {
	var out []int
	for _, b := range bs {
		out = append(out, len(b))
	}
	return out
}

func TestElJsonDelServidorSeLee(t *testing.T) {
	var doc Documento
	err := json.Unmarshal([]byte(`{"diagnostico":9,"pasos":[{"n":1,"renglon":"texto","accion":"mandar","separador":false,"bytes":"SE9MQQ==","por":"directo","pedazo":500,"pausaMs":80,"documento":0,"pausaDocMs":600,"veces":2,"cadaMs":3000}]}`), &doc)
	if err != nil {
		t.Fatal(err)
	}
	p := doc.Pasos[0]
	if doc.Diagnostico != 9 || string(p.Bytes) != "HOLA" || p.Por != "directo" || p.Pedazo != 500 || p.PausaMs != 80 || p.Veces != 2 || p.CadaMs != 3000 {
		t.Fatalf("doc = %+v", doc)
	}
}

func TestMandarComoUnTrabajoEnTandas(t *testing.T) {
	c := &canalFalso{}
	r := Ejecutar(context.Background(), Documento{Pasos: []Paso{{N: 1, Accion: "mandar", Por: "normal", Bytes: []byte("0123456789"), Documento: 4, Veces: 1}}}, c)

	if !r[0].OK || fmt.Sprint(largos(c.normal)) != "[4 4 2]" || len(r[0].Documentos) != 3 || r[0].Bytes != 10 {
		t.Fatalf("resultado %+v, mandado %v", r[0], largos(c.normal))
	}
}

func TestMandarPorLaCola(t *testing.T) {
	c := &canalFalso{}
	Ejecutar(context.Background(), Documento{Pasos: []Paso{{Accion: "mandar", Por: "cola", Bytes: []byte("x"), Veces: 1}}}, c)

	if len(c.cola) != 1 || len(c.normal) != 0 {
		t.Fatalf("cola %d, normal %d", len(c.cola), len(c.normal))
	}
}

func TestMandarDirectoDeAPedazos(t *testing.T) {
	c := &canalFalso{}
	r := Ejecutar(context.Background(), Documento{Pasos: []Paso{{Accion: "mandar", Por: "directo", Bytes: []byte("1234567"), Pedazo: 3, PausaMs: 1, Veces: 1}}}, c)

	if !r[0].OK || fmt.Sprint(largos(c.directo.escrito)) != "[3 3 1]" || len(r[0].Escrituras) != 3 || c.directo.cerrado != 1 {
		t.Fatalf("resultado %+v, escrito %v, cerrado %d", r[0], largos(c.directo.escrito), c.directo.cerrado)
	}
}

func TestMandarVariasVeces(t *testing.T) {
	c := &canalFalso{}
	Ejecutar(context.Background(), Documento{Pasos: []Paso{{Accion: "mandar", Por: "normal", Bytes: []byte("x"), Veces: 3}}}, c)

	if len(c.normal) != 3 {
		t.Fatalf("mandó %d veces", len(c.normal))
	}
}

func TestEstadoDiceLosCuatroBytes(t *testing.T) {
	c := &canalFalso{directo: &directoFalso{respuestas: map[string][]byte{"100401": {0x16}, "100402": {0x12}, "100403": {0x12}, "100404": {0x12}}}}
	r := Ejecutar(context.Background(), Documento{Pasos: []Paso{{Accion: "estado"}}}, c)

	if !r[0].OK || r[0].Estado != "1=16 2=12 3=12 4=12" {
		t.Fatalf("resultado %+v", r[0])
	}
}

func TestEstadoSinRespuestaLoDice(t *testing.T) {
	r := Ejecutar(context.Background(), Documento{Pasos: []Paso{{Accion: "estado"}}}, &canalFalso{})

	if r[0].Estado != "1=-- 2=-- 3=-- 4=--" {
		t.Fatalf("estado = %q", r[0].Estado)
	}
}

func TestEstadoRepetidoJuntaUnaSerie(t *testing.T) {
	c := &canalFalso{directo: &directoFalso{respuestas: map[string][]byte{"100401": {0x16}}}}
	r := Ejecutar(context.Background(), Documento{Pasos: []Paso{{Accion: "estado repetido", DuranteMs: 120, CadaMs: 50}}}, c)

	if !r[0].OK || len(r[0].Serie) < 2 || !strings.HasPrefix(r[0].Serie[0].Estado, "1=16") {
		t.Fatalf("resultado %+v", r[0])
	}
}

func TestPreguntarDevuelveHexa(t *testing.T) {
	c := &canalFalso{directo: &directoFalso{respuestas: map[string][]byte{"1d4901": {0x20, 0x21}}}}
	r := Ejecutar(context.Background(), Documento{Pasos: []Paso{{Accion: "preguntar", Bytes: []byte{0x1d, 0x49, 0x01}, EsperaMs: 100}}}, c)

	if !r[0].OK || r[0].Respuesta != "2021" {
		t.Fatalf("resultado %+v", r[0])
	}
}

func TestPreguntarSinRespuestaEsUnError(t *testing.T) {
	r := Ejecutar(context.Background(), Documento{Pasos: []Paso{{Accion: "preguntar", Bytes: []byte{0x1d, 0x49, 0x01}, EsperaMs: 50}}}, &canalFalso{})

	if r[0].OK || !strings.Contains(r[0].Error, "no contestó") {
		t.Fatalf("resultado %+v", r[0])
	}
}

func TestUnPasoQueNoAplicaSigueConElProximo(t *testing.T) {
	c := &canalFalso{errDirecto: fmt.Errorf("%w: el puerto FILE: no tiene aparato", printer.ErrNoAplica)}
	r := Ejecutar(context.Background(), Documento{Pasos: []Paso{{N: 1, Accion: "estado"}, {N: 2, Accion: "mandar", Por: "normal", Bytes: []byte("x"), Veces: 1}}}, c)

	if r[0].OK || r[0].NoAplica != "el puerto FILE: no tiene aparato" || r[0].Error != "" {
		t.Fatalf("paso 1 %+v", r[0])
	}
	if !r[1].OK {
		t.Fatalf("paso 2 %+v", r[1])
	}
}

func TestUnaAccionDesconocidaEsUnErrorDelPaso(t *testing.T) {
	r := Ejecutar(context.Background(), Documento{Pasos: []Paso{{Accion: "formatear"}}}, &canalFalso{})

	if r[0].OK || !strings.Contains(r[0].Error, "mandar, esperar, estado, estado repetido y preguntar") {
		t.Fatalf("resultado %+v", r[0])
	}
}

func TestSiElAgenteSeCierraLoQueFaltaNoCorrio(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()

	r := Ejecutar(ctx, Documento{Pasos: []Paso{{N: 1, Accion: "esperar", Ms: 5000}, {N: 2, Accion: "estado"}}}, &canalFalso{})

	if len(r) != 2 || r[0].OK || !strings.Contains(r[0].Error, "se cortó") {
		t.Fatalf("paso 1 %+v", r)
	}
	if r[1].Error != "no corrió: el agente se cerró" {
		t.Fatalf("paso 2 %+v", r[1])
	}
}

func TestLosTiemposTienenTope(t *testing.T) {
	c := &canalFalso{}
	r := Ejecutar(context.Background(), Documento{Pasos: []Paso{{Accion: "mandar", Por: "directo", Bytes: make([]byte, 1000), Pedazo: 1, Veces: 1}}}, c)

	if len(r[0].Escrituras) != maxTiempos || len(c.directo.escrito) != 1000 {
		t.Fatalf("escrituras anotadas %d, hechas %d", len(r[0].Escrituras), len(c.directo.escrito))
	}
}

func TestPreguntarDondeNoSePuedeNoAplica(t *testing.T) {
	c := &canalFalso{errPreguntar: fmt.Errorf("%w: el 80 no es un puerto de impresora", printer.ErrNoAplica)}
	r := Ejecutar(context.Background(), Documento{Pasos: []Paso{{Accion: "preguntar", Bytes: []byte{0x1d, 0x49, 0x01}, EsperaMs: 50}}}, c)

	if r[0].NoAplica != "el 80 no es un puerto de impresora" || c.directo != nil {
		t.Fatalf("resultado %+v (abrió directo: %v)", r[0], c.directo != nil)
	}
}

func TestLaRespuestaTieneTope(t *testing.T) {
	c := &canalFalso{directo: &directoFalso{respuestas: map[string][]byte{"1d4901": make([]byte, 1000)}}}
	r := Ejecutar(context.Background(), Documento{Pasos: []Paso{{Accion: "preguntar", Bytes: []byte{0x1d, 0x49, 0x01}, EsperaMs: 50}}}, c)

	if len(r[0].Respuesta) != 2*maxRespuesta {
		t.Fatalf("respuesta de %d caracteres hexa", len(r[0].Respuesta))
	}
}
