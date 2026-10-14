// Package diagnostico corre un diagnóstico remoto: los pasos que armó el
// servidor, en orden, sobre una impresora.
//
// El agente entiende cinco acciones y nada más (spec, «El camino»): mandar
// bytes (como un trabajo, por la cola o directo; en pedazos, en documentos,
// varias veces), esperar, preguntar el estado, preguntarlo una y otra vez, y
// preguntar cualquier cosa y devolver la respuesta en hexa. Nunca ejecuta
// nada que mande el servidor: si alguien lo toma, sólo puede imprimir.
package diagnostico

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eesnaola/manducar-impresion/internal/printer"
)

// Documento es el payload del trabajo DIAGNOSTIC.
type Documento struct {
	Diagnostico int    `json:"diagnostico"`
	Pasos       []Paso `json:"pasos"`
}

// Paso es una acción ya armada por el servidor; Bytes viaja en base64.
type Paso struct {
	N          int    `json:"n"`
	Renglon    string `json:"renglon"`
	Accion     string `json:"accion"`
	Separador  bool   `json:"separador"`
	Bytes      []byte `json:"bytes"`
	Por        string `json:"por"`
	Pedazo     int    `json:"pedazo"`
	PausaMs    int    `json:"pausaMs"`
	Documento  int    `json:"documento"`
	PausaDocMs int    `json:"pausaDocMs"`
	Veces      int    `json:"veces"`
	CadaMs     int    `json:"cadaMs"`
	Ms         int    `json:"ms"`
	DuranteMs  int    `json:"duranteMs"`
	EsperaMs   int    `json:"esperaMs"`
}

// Resultado es lo que vuelve al servidor (POST /agente/diagnosticos/{id}/resultado).
type Resultado struct {
	Diagnostico int             `json:"diagnostico"`
	Version     string          `json:"version"`
	Sistema     string          `json:"sistema"`
	Empezo      string          `json:"empezo"`
	Termino     string          `json:"termino"`
	Entorno     any             `json:"entorno"`
	Pasos       []ResultadoPaso `json:"pasos"`
}

type ResultadoPaso struct {
	N          int       `json:"n"`
	Renglon    string    `json:"renglon"`
	Accion     string    `json:"accion"`
	Separador  bool      `json:"separador,omitempty"`
	Bytes      int       `json:"bytes,omitempty"`
	OK         bool      `json:"ok"`
	NoAplica   string    `json:"noAplica,omitempty"`
	Error      string    `json:"error,omitempty"`
	Ms         int64     `json:"ms"`
	Documentos []int64   `json:"documentos,omitempty"` // ms de cada documento, hasta que salió de la cola
	Escrituras []int64   `json:"escrituras,omitempty"` // ms de cada escritura directa
	Estado     string    `json:"estado,omitempty"`
	Serie      []Muestra `json:"serie,omitempty"`
	Respuesta  string    `json:"respuesta,omitempty"`
}

type Muestra struct {
	Ms     int64  `json:"ms"`
	Estado string `json:"estado"`
}

// Canal es lo que el diagnóstico necesita de la impresora; lo arma el runner.
type Canal interface {
	// Normal manda como un trabajo: TCP a la de red, la cola a la del sistema.
	Normal(ctx context.Context, b []byte) error
	// Cola manda un documento por la cola del sistema (printer.ErrNoAplica en una de red).
	Cola(ctx context.Context, b []byte) error
	// Directo abre la impresora sin la cola (printer.ErrNoAplica si no hay cómo).
	Directo(ctx context.Context) (printer.Directo, error)
	// PuedePreguntar dice si a esta impresora se le puede preguntar y leer la
	// respuesta (printer.ErrNoAplica si no): ver canalDeImpresora en el runner.
	PuedePreguntar() error
}

const (
	// Los tiempos que se anotan por paso: más no se leen, y el resultado
	// guardado tiene tope (64 KB).
	maxTiempos  = 200
	maxMuestras = 600
	// Lo que se devuelve de una pregunta: alcanza para un modelo o un estado.
	maxRespuesta = 256
	// Una escritura directa que tarda más es una impresora trabada.
	plazoEscritura = 30 * time.Second
	plazoEstado    = 700 * time.Millisecond
	drenaje        = 50 * time.Millisecond
)

// Ejecutar corre los pasos en orden. Si el contexto se cancela (el agente se
// cierra o se actualiza), los que faltan quedan «no corrió» y el resultado
// parcial igual vuelve.
func Ejecutar(ctx context.Context, doc Documento, c Canal) []ResultadoPaso {
	out := make([]ResultadoPaso, 0, len(doc.Pasos))
	for i, p := range doc.Pasos {
		if ctx.Err() != nil {
			for _, q := range doc.Pasos[i:] {
				out = append(out, ResultadoPaso{N: q.N, Renglon: q.Renglon, Accion: q.Accion, Separador: q.Separador, Error: "no corrió: el agente se cerró"})
			}
			break
		}
		out = append(out, uno(ctx, p, c))
	}
	return out
}

func uno(ctx context.Context, p Paso, c Canal) ResultadoPaso {
	r := ResultadoPaso{N: p.N, Renglon: p.Renglon, Accion: p.Accion, Separador: p.Separador}
	empezo := time.Now()
	var err error
	switch p.Accion {
	case "mandar":
		r.Bytes = len(p.Bytes)
		err = mandar(ctx, p, c, &r)
	case "esperar":
		if !dormir(ctx, ms(p.Ms)) {
			err = errors.New("se cortó: el agente se cerró")
		}
	case "estado":
		err = conDirecto(ctx, c, func(d printer.Directo) error {
			r.Estado = estado(d)
			return nil
		})
	case "estado repetido":
		err = conDirecto(ctx, c, func(d printer.Directo) error {
			r.Serie = serie(ctx, d, p.DuranteMs, p.CadaMs)
			return nil
		})
	case "preguntar":
		r.Bytes = len(p.Bytes)
		if err = c.PuedePreguntar(); err == nil {
			err = conDirecto(ctx, c, func(d printer.Directo) error { return preguntar(d, p, &r) })
		}
	default:
		err = fmt.Errorf("acción desconocida %q: este agente entiende mandar, esperar, estado, estado repetido y preguntar", p.Accion)
	}
	r.Ms = time.Since(empezo).Milliseconds()
	switch {
	case err == nil:
		r.OK = true
	case errors.Is(err, printer.ErrNoAplica):
		r.NoAplica = strings.TrimPrefix(err.Error(), printer.ErrNoAplica.Error()+": ")
	default:
		r.Error = err.Error()
	}
	return r
}

func mandar(ctx context.Context, p Paso, c Canal, r *ResultadoPaso) error {
	veces := max(p.Veces, 1)
	for v := 0; v < veces; v++ {
		if v > 0 && p.CadaMs > 0 && !dormir(ctx, ms(p.CadaMs)) {
			return errors.New("se cortó: el agente se cerró")
		}
		if err := mandarUnaVez(ctx, p, c, r); err != nil {
			return err
		}
	}
	return nil
}

func mandarUnaVez(ctx context.Context, p Paso, c Canal, r *ResultadoPaso) error {
	switch p.Por {
	case "directo":
		d, err := c.Directo(ctx)
		if err != nil {
			return err
		}
		defer d.Close()
		pedazos := partir(p.Bytes, p.Pedazo)
		for i, pedazo := range pedazos {
			t := time.Now()
			if err := d.Escribir(pedazo, plazoEscritura); err != nil {
				return fmt.Errorf("escritura %d de %d: %w", i+1, len(pedazos), err)
			}
			anotar(&r.Escrituras, time.Since(t))
			if p.PausaMs > 0 && i < len(pedazos)-1 && !dormir(ctx, ms(p.PausaMs)) {
				return errors.New("se cortó: el agente se cerró")
			}
		}
		return nil
	case "cola", "normal", "":
		enviar := c.Normal
		if p.Por == "cola" {
			enviar = c.Cola
		}
		docs := partir(p.Bytes, p.Documento)
		for i, doc := range docs {
			if i > 0 && p.PausaDocMs > 0 && !dormir(ctx, ms(p.PausaDocMs)) {
				return errors.New("se cortó: el agente se cerró")
			}
			t := time.Now()
			if err := enviar(ctx, doc); err != nil {
				if len(docs) > 1 {
					return fmt.Errorf("documento %d de %d: %w", i+1, len(docs), err)
				}
				return err
			}
			anotar(&r.Documentos, time.Since(t))
		}
		return nil
	}
	return fmt.Errorf("forma de mandar desconocida %q", p.Por)
}

func conDirecto(ctx context.Context, c Canal, f func(printer.Directo) error) error {
	d, err := c.Directo(ctx)
	if err != nil {
		return err
	}
	defer d.Close()
	return f(d)
}

// estado: DLE EOT 1 a 4, el último byte de cada respuesta en hexa; «--» si no contestó.
func estado(d printer.Directo) string {
	partes := make([]string, 0, 4)
	for n := byte(1); n <= 4; n++ {
		d.Leer(drenaje) // lo que haya quedado de antes no es esta respuesta
		v := "--"
		if d.Escribir([]byte{0x10, 0x04, n}, time.Second) == nil {
			if b := d.Leer(plazoEstado); len(b) > 0 {
				v = fmt.Sprintf("%02x", b[len(b)-1])
			}
		}
		partes = append(partes, fmt.Sprintf("%d=%s", n, v))
	}
	return strings.Join(partes, " ")
}

func serie(ctx context.Context, d printer.Directo, duranteMs, cadaMs int) []Muestra {
	var out []Muestra
	empezo := time.Now()
	for time.Since(empezo) < ms(duranteMs) && len(out) < maxMuestras {
		out = append(out, Muestra{Ms: time.Since(empezo).Milliseconds(), Estado: estado(d)})
		if !dormir(ctx, ms(max(cadaMs, 50))) {
			break
		}
	}
	return out
}

func preguntar(d printer.Directo, p Paso, r *ResultadoPaso) error {
	d.Leer(drenaje)
	if err := d.Escribir(p.Bytes, 5*time.Second); err != nil {
		return err
	}
	var resp []byte
	vence := time.Now().Add(ms(p.EsperaMs))
	for len(resp) < maxRespuesta {
		falta := time.Until(vence)
		if falta <= 0 {
			break
		}
		b := d.Leer(falta)
		if len(b) == 0 {
			break
		}
		resp = append(resp, b...)
	}
	if len(resp) == 0 {
		return fmt.Errorf("no contestó en %d ms", p.EsperaMs)
	}
	if len(resp) > maxRespuesta {
		resp = resp[:maxRespuesta]
	}
	r.Respuesta = hex.EncodeToString(resp)
	return nil
}

// partir corta b en pedazos de n bytes (n ≤ 0: uno solo).
func partir(b []byte, n int) [][]byte {
	if n <= 0 || n >= len(b) {
		return [][]byte{b}
	}
	var out [][]byte
	for len(b) > n {
		out = append(out, b[:n])
		b = b[n:]
	}
	return append(out, b)
}

func anotar(lista *[]int64, d time.Duration) {
	if len(*lista) < maxTiempos {
		*lista = append(*lista, d.Milliseconds())
	}
}

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

// dormir espera d o hasta que se cancele el contexto; dice si esperó todo.
func dormir(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
