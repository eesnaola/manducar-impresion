package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"time"

	"github.com/eesnaola/manducar-impresion/internal/api"
	"github.com/eesnaola/manducar-impresion/internal/diagnostico"
	"github.com/eesnaola/manducar-impresion/internal/outbox"
	"github.com/eesnaola/manducar-impresion/internal/printer"
)

// kindDiagnostico: el trabajo que trae un diagnóstico remoto en vez de un papel.
const kindDiagnostico = "DIAGNOSTIC"

var (
	// diagnosticoReintento: entre un intento y otro de mandar el resultado.
	diagnosticoReintento = 5 * time.Second
	// diagnosticoCierre: lo que espera Run, al cerrarse, a que un diagnóstico
	// cortado mande su resultado parcial.
	diagnosticoCierre = 25 * time.Second
)

// correrDiagnostico corre los pasos con el turno de la impresora tomado de
// punta a punta: el semáforo no la pulsa y lo que llega para ella espera
// (spec, «El camino»). El resultado va a su dirección; el trabajo se cierra
// por el camino de siempre (outbox), como cualquier otro.
func (r *runner) correrDiagnostico(ctx context.Context, j api.Job) {
	pasosCtx, ok := r.empezarDiagnostico(ctx, j.ID)
	if !ok {
		log.Printf("✗ diagnóstico (trabajo %d): el agente se está cerrando, no se corre", j.ID)
		r.cerrarTrabajo(j.ID, api.Result{OK: false, Error: "el agente se cerró antes de correrlo"})
		return
	}
	defer r.terminarDiagnostico(j.ID)

	var doc diagnostico.Documento
	if err := json.Unmarshal(j.Payload, &doc); err != nil || doc.Diagnostico == 0 {
		log.Printf("✗ diagnóstico (trabajo %d): no se pudo leer: %v", j.ID, err)
		r.cerrarTrabajo(j.ID, api.Result{OK: false, Error: "el diagnóstico no se pudo leer"})
		return
	}

	turno := r.turno(j.Printer)
	turno.Lock()
	log.Printf("→ diagnóstico %d en %s: %d pasos", doc.Diagnostico, targetLabel(j.Printer), len(doc.Pasos))
	res := diagnostico.Resultado{
		Diagnostico: doc.Diagnostico,
		Version:     r.version,
		Sistema:     api.Platform(),
		Empezo:      time.Now().UTC().Format(time.RFC3339),
		Entorno:     r.entorno(ctx, j.Printer),
	}
	res.Pasos = diagnostico.Ejecutar(pasosCtx, doc, r.canal(j.Printer))
	res.Termino = time.Now().UTC().Format(time.RFC3339)
	turno.Unlock()

	r.mandarDiagnostico(ctx, res)
	r.cerrarTrabajo(j.ID, api.Result{OK: true, WroteSomething: true})
}

// empezarDiagnostico anota el diagnóstico para que Run lo pueda cortar y
// esperar; false si el agente ya se está cerrando (entonces no se empieza).
func (r *runner) empezarDiagnostico(ctx context.Context, id int) (context.Context, bool) {
	r.diagMu.Lock()
	defer r.diagMu.Unlock()
	if r.cerrando {
		return nil, false
	}
	c, cortar := context.WithCancel(ctx)
	if r.diagCorte == nil {
		r.diagCorte = map[int]context.CancelFunc{}
	}
	r.diagCorte[id] = cortar
	r.diags.Add(1)
	return c, true
}

func (r *runner) terminarDiagnostico(id int) {
	r.diagMu.Lock()
	if cortar, ok := r.diagCorte[id]; ok {
		cortar()
		delete(r.diagCorte, id)
	}
	r.diagMu.Unlock()
	r.diags.Done()
}

// cortarDiagnosticos: al cerrarse, los que corren terminan en el próximo
// paso (los que faltan quedan «no corrió») y no se empieza ninguno nuevo.
func (r *runner) cortarDiagnosticos() {
	r.diagMu.Lock()
	defer r.diagMu.Unlock()
	r.cerrando = true
	for _, cortar := range r.diagCorte {
		cortar()
	}
}

// esperarDiagnosticos: hasta d a que los cortados manden lo suyo.
func (r *runner) esperarDiagnosticos(d time.Duration) bool {
	listo := make(chan struct{})
	go func() {
		r.diags.Wait()
		close(listo)
	}()
	select {
	case <-listo:
		return true
	case <-time.After(d):
		return false
	}
}

// mandarDiagnostico: tres intentos. Con el agente cerrándose el contexto ya
// está cancelado: el envío va con uno propio, para que lo parcial llegue.
func (r *runner) mandarDiagnostico(ctx context.Context, res diagnostico.Resultado) {
	base := context.WithoutCancel(ctx)
	for intento := 1; intento <= 3; intento++ {
		c, cancel := context.WithTimeout(base, 20*time.Second)
		err := r.api().ReportDiagnostic(c, res.Diagnostico, res)
		cancel()
		if err == nil {
			log.Printf("✓ diagnóstico %d contado al servidor", res.Diagnostico)
			return
		}
		log.Printf("no se pudo mandar el diagnóstico %d (intento %d de 3): %v", res.Diagnostico, intento, err)
		if intento < 3 && !sleep(base, diagnosticoReintento) {
			return
		}
	}
}

// cerrarTrabajo: al outbox y que lo confirme el flusher, como print().
func (r *runner) cerrarTrabajo(id int, res api.Result) {
	if err := r.out.Add(outbox.Entry{JobID: id, Result: res, At: time.Now()}); err != nil {
		log.Println("no se pudo guardar el resultado:", err)
	}
	r.kickFlush()
}

// canalDeImpresora es el Canal de verdad: «normal» es el mismo write de los
// trabajos; la cola y lo directo, las vías del paquete printer.
type canalDeImpresora struct {
	t     api.Target
	write func(context.Context, api.Target, []byte) printer.Outcome
}

func (c canalDeImpresora) Normal(ctx context.Context, b []byte) error {
	o := c.write(ctx, c.t, b)
	switch {
	case o.OK:
		return nil
	case o.SpoolID != "":
		return errors.New("entró a la cola y seguía ahí")
	case o.Err != nil:
		return o.Err
	}
	return errors.New("no salió")
}

func (c canalDeImpresora) Cola(ctx context.Context, b []byte) error {
	return printer.EscribirCola(ctx, c.t, b)
}

func (c canalDeImpresora) Directo(ctx context.Context) (printer.Directo, error) {
	return printer.AbrirDirecto(ctx, c.t)
}

// PuedePreguntar: estado, estado repetido y preguntar devuelven lo que
// contesta del otro lado. En una
// impresora de red, sólo a los puertos de impresora (9100 a 9109): la
// dirección la manda el servidor, y si no, un servidor tomado podría leer
// cualquier servicio de la red del local. La del sistema la resuelve la
// configuración de esta computadora.
func (c canalDeImpresora) PuedePreguntar() error {
	if c.t.Kind != "network" {
		return nil
	}
	_, puerto, err := net.SplitHostPort(c.t.Address)
	n, errNum := strconv.Atoi(puerto)
	if err != nil || errNum != nil || n < 9100 || n > 9109 {
		return fmt.Errorf("%w: en una impresora de red se pregunta sólo a los puertos de impresora (9100 a 9109), y ésta está en %q", printer.ErrNoAplica, c.t.Address)
	}
	return nil
}
