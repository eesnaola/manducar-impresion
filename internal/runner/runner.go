// Package runner es el bucle del agente: late contra el servidor, escucha el
// hub, busca los trabajos que le tocan, los imprime en orden por impresora y
// confirma cada resultado. Todo lo que no se pudo confirmar queda en el
// outbox hasta que el servidor lo acepte.
package runner

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eesnaola/manducar-impresion/internal/api"
	"github.com/eesnaola/manducar-impresion/internal/config"
	"github.com/eesnaola/manducar-impresion/internal/outbox"
	"github.com/eesnaola/manducar-impresion/internal/printer"
	"github.com/eesnaola/manducar-impresion/internal/update"
	"github.com/eesnaola/manducar-impresion/internal/wake"
)

// Los plazos son variables y no constantes para que los tests no tarden
// minutos. En producción nadie los toca.
var (
	defaultBeat       = 30 * time.Second      // cada cuánto se late si el servidor no dice otra cosa
	minBeat           = 5 * time.Second       // por más que el servidor pida menos, no se late más seguido
	unpairedBeat      = 5 * time.Minute       // ritmo cuando el servidor no nos reconoce
	fallbackPoll      = 5 * time.Second       // poll de respaldo mientras el hub esté caído
	hubQuietReconnect = 3 * time.Second       // una caída más corta que esto no se loguea
	printersEvery     = 5 * time.Minute       // cada cuánto se manda la lista de impresoras del sistema
	listTimeout       = 10 * time.Second      // lo que se le aguanta al sistema para listar impresoras
	rewakeDelay       = time.Second           // reintento tras un despertar que no encontró nada
	jwtCheckEvery     = time.Minute           // cada cuánto se mira si el latido renovó el JWT del hub
	updateWait        = 5 * time.Second       // cuánto espera la actualización a que no quede nada en curso
	shutdownWait      = 3 * time.Second       // cuánto se le da a lo que se está imprimiendo al salir
	flushWait         = 5 * time.Second       // plazo del último vaciado del outbox al salir
	idleTick          = 50 * time.Millisecond // paso con el que se mira si terminaron los trabajos
	backlogWarn       = 20                    // al llegar a esta cola, la impresora es noticia
	updateRetry       = time.Hour             // cuánto se espera antes de reintentar una actualización que falló
	selfKick          = 11 * time.Second      // cuándo se vuelve a preguntar tras un trabajo que no escribió nada
	logRepeatEvery    = 5 * time.Minute       // cada cuánto se repite en el log una línea de error que se repite
)

// listSystem es variable para que los tests no vayan al CUPS de verdad.
var listSystem = printer.ListSystemContext

type runner struct {
	// client se reemplaza entero cuando vuelven a vincular la computadora
	// mientras el agente corre: el que lo usa lo lee de acá, nunca lo guarda.
	client  atomic.Pointer[api.Client]
	out     *outbox.Outbox
	version string
	// write es printer.Write; los tests le ponen una impresora de mentira.
	write func(context.Context, api.Target, []byte) printer.Outcome

	// La config la reescribe el latido (el JWT del hub se renueva) y la lee
	// el bucle del SSE.
	cfgMu sync.Mutex
	cfg   config.Config

	// kick es el pedido de búsqueda: tiene lugar para uno, así un pedido que
	// llega mientras se busca queda esperando y no se pierde nunca. wokenKick
	// dice si alguno de los pedidos fundidos vino del hub.
	kick      chan struct{}
	wokenKick atomic.Bool

	// flushKick pide vaciar el outbox; lo atiende una sola goroutine, así
	// reportar nunca frena a la impresora. flushStop la despide y flushDone
	// avisa que terminó.
	flushKick chan struct{}
	flushStop chan struct{}
	flushDone chan struct{}

	sseUp         atomic.Bool
	hubMu         sync.Mutex
	hubDownTimer  *time.Timer
	hubEverUp     bool
	hubDownLogged bool
	unpaired      atomic.Bool  // el servidor rechazó el token: no hay nada que buscar
	updating      atomic.Bool  // se está reemplazando el binario: no se traen trabajos nuevos
	inFlight      atomic.Int64 // trabajos en cola o imprimiéndose

	// Las líneas de error que se repiten (el servidor caído late cada 30 s)
	// se juntan: una y después una cada logRepeatEvery.
	beatErr  *throttle
	fetchErr *throttle
	listErr  *throttle

	printersMu sync.Mutex
	printers   map[string]*queue // una cola y un worker por impresora

	// Lo de la actualización lo toca sólo el latido, que es uno solo.
	pending  *api.Release // versión anunciada por el servidor
	failed   *api.Release // la última que no se pudo aplicar
	failedAt time.Time    // cuándo falló, para no reintentarla cada 30 s
}

func newRunner(cfg config.Config, out *outbox.Outbox, version string) *runner {
	r := &runner{
		cfg:       cfg,
		out:       out,
		version:   version,
		write:     printer.Write,
		kick:      make(chan struct{}, 1),
		flushKick: make(chan struct{}, 1),
		flushStop: make(chan struct{}),
		flushDone: make(chan struct{}),
		printers:  map[string]*queue{},
		beatErr:   newThrottle(logRepeatEvery),
		fetchErr:  newThrottle(logRepeatEvery),
		listErr:   newThrottle(logRepeatEvery),
	}
	r.client.Store(api.New(cfg.Server, cfg.Token, version))
	return r
}

// api es el cliente de ahora: lo puede haber reemplazado una vinculación
// nueva mientras el agente corría.
func (r *runner) api() *api.Client { return r.client.Load() }

func Run(ctx context.Context, version string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfgPath, err := config.Path()
	if err != nil {
		return err
	}
	out, err := outbox.Open(filepath.Join(filepath.Dir(cfgPath), "impresion-outbox.json"))
	if err != nil {
		return err
	}
	r := newRunner(cfg, out, version)
	log.Printf("manducar-impresion %s — servidor %s, agente %d", version, cfg.Server, cfg.AgentID)

	// Los trabajos corren con su propio contexto: al salir se les da un rato
	// para terminar en vez de dejar media comanda impresa.
	jobCtx, jobCancel := context.WithCancel(context.WithoutCancel(ctx))
	defer jobCancel()

	// El que confirma resultados es uno solo y no frena a nadie. Arranca
	// primero: lo que haya quedado sin confirmar de la corrida anterior se
	// reporta apenas pueda, sin demorar el arranque.
	go r.flusher(jobCtx)
	r.kickFlush()

	// Latido ahora y después cada N segundos; cada 5 minutos con la lista de impresoras.
	go r.heartbeatLoop(ctx)
	// SSE, con la config actual; el latido puede cambiar el JWT.
	go r.sseLoop(ctx)
	// Poll de respaldo mientras el SSE esté caído.
	go r.fallbackLoop(ctx)
	// Un solo buscador: los demás le piden que busque.
	go r.fetcher(ctx, jobCtx)

	<-ctx.Done()
	log.Println("saliendo")
	if !r.waitIdle(jobCtx, shutdownWait) {
		log.Println("quedaron trabajos sin terminar: se reportan al volver a arrancar")
	}
	// Último vaciado, con plazo: un servidor que no contesta no puede dejar
	// el proceso colgado. Lo que no entre queda en el outbox.
	close(r.flushStop)
	select {
	case <-r.flushDone:
	case <-time.After(flushWait):
		log.Println("el servidor no confirmó los últimos resultados: quedan en el outbox")
	}
	return nil
}

func (r *runner) heartbeatLoop(ctx context.Context) {
	every := defaultBeat
	var lastPrinters time.Time
	for {
		// La lista de impresoras del sistema no cambia seguido: va en el
		// primer latido y después cada 5 minutos.
		var names []string
		if lastPrinters.IsZero() || time.Since(lastPrinters) >= printersEvery {
			names = r.systemPrinters(ctx, listTimeout)
		}
		res, err := r.api().Heartbeat(ctx, names)
		switch {
		case errors.Is(err, api.ErrUnauthorized):
			// Puede que lo hayan vuelto a vincular recién, con el agente
			// corriendo: si el archivo tiene un token nuevo, se sigue con
			// ése y no hace falta reiniciar nada.
			if r.reloadToken() {
				log.Println("esta computadora se volvió a vincular: sigo con el token nuevo")
				r.unpaired.Store(false)
				every = minBeat
				break
			}
			// No se sale: como servicio, salir es reiniciar en loop y
			// martillar al servidor. Se afloja el ritmo y se sigue.
			log.Println("el servidor no reconoce esta computadora: volvé a vincularla con «manducar-impresion vincular»")
			// Mientras no nos reconozcan no hay nada que buscar: el poll de
			// respaldo se apaga y no se llena el log de rechazos.
			r.unpaired.Store(true)
			every = unpairedBeat
		case err != nil:
			if ctx.Err() != nil {
				return
			}
			r.beatErr.Printf("latido: %v", err)
		default:
			r.unpaired.Store(false)
			// Cada latido bueno es una oportunidad de confirmar lo que quedó
			// en el outbox: si no, un resultado que no entró se queda ahí
			// hasta la próxima impresión, que puede ser mañana.
			r.kickFlush()
			// La lista se da por entregada recién cuando el latido salió bien.
			if names != nil {
				lastPrinters = time.Now()
			}
			every = defaultBeat
			if res.HeartbeatSeconds > 0 {
				every = max(time.Duration(res.HeartbeatSeconds)*time.Second, minBeat)
			}
			if res.Mercure.JWT != "" {
				r.setMercure(config.Mercure(res.Mercure))
			}
			if res.Agent != nil && res.Agent.Version != r.version {
				r.pending = res.Agent
			}
			if res.JobsPending > 0 {
				r.kickFetch(false)
			}
			r.maybeUpdate(ctx)
		}
		if !sleep(ctx, every) {
			return
		}
	}
}

// systemPrinters lista las impresoras del sistema con un plazo: un CUPS
// colgado no puede congelar el latido. Sin lista, se late sin lista. El plazo
// va por contexto y no por reloj aparte: así el `lpstat` se muere con él y no
// quedan ni la goroutine ni el proceso hijo colgados para siempre.
func (r *runner) systemPrinters(ctx context.Context, d time.Duration) []string {
	c, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	names, err := listSystem(c)
	if err != nil {
		r.listErr.Printf("no se pudieron listar las impresoras del sistema (%v): este latido va sin lista", err)
		return nil
	}
	if names == nil {
		names = []string{}
	}
	return names
}

func (r *runner) sseLoop(ctx context.Context) {
	for ctx.Err() == nil {
		m := r.mercure()
		if m.URL == "" || m.JWT == "" {
			// Todavía no hay hub: lo trae el primer latido.
			if !sleep(ctx, fallbackPoll) {
				return
			}
			continue
		}
		sub, cancel := context.WithCancel(ctx)
		// Se reengancha cuando el JWT cambia: el latido lo renueva.
		go func(jwt string) {
			for sleep(sub, jwtCheckEvery) {
				if r.mercure().JWT != jwt {
					cancel()
					return
				}
			}
		}(m.JWT)
		wake.Listen(sub, m.URL, m.Topic, m.JWT,
			func() { r.kickFetch(true) },
			func(up bool) {
				r.sseUp.Store(up)
				r.hubState(up)
			},
		)
		cancel()
	}
}

// hubState loguea los cambios del hub sin ruido: el hub de Mercure corta
// las conexiones largas cada diez minutos (su write_timeout) y el agente
// vuelve en un segundo; ese par «desconectado / conectado» no le dice nada
// a nadie. Se avisa sólo si la caída dura más de unos segundos, y la vuelta
// sólo después de haber avisado la caída.
func (r *runner) hubState(up bool) {
	r.hubMu.Lock()
	defer r.hubMu.Unlock()
	if up {
		if r.hubDownTimer != nil {
			r.hubDownTimer.Stop()
			r.hubDownTimer = nil
		}
		if !r.hubEverUp {
			r.hubEverUp = true
			log.Println("hub: conectado")
		} else if r.hubDownLogged {
			r.hubDownLogged = false
			log.Println("hub: conectado de nuevo")
		}
		return
	}
	if r.hubDownTimer != nil {
		return
	}
	r.hubDownTimer = time.AfterFunc(hubQuietReconnect, func() {
		r.hubMu.Lock()
		defer r.hubMu.Unlock()
		r.hubDownTimer = nil
		if !r.sseUp.Load() {
			r.hubDownLogged = true
			log.Println("hub: desconectado; mientras tanto se busca trabajo cada 5 s")
		}
	})
}

func (r *runner) fallbackLoop(ctx context.Context) {
	t := time.NewTicker(fallbackPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !r.sseUp.Load() && !r.unpaired.Load() {
				r.kickFetch(false)
			}
		}
	}
}

// kickFetch pide una búsqueda. El canal tiene lugar para uno: si ya hay un
// pedido esperando, éste se funde con aquél —y si alguno de los dos vino del
// hub, la búsqueda cuenta como despertar—. Así no se pierde ningún aviso.
func (r *runner) kickFetch(woken bool) {
	if woken {
		r.wokenKick.Store(true)
	}
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// fetcher es el único que busca: no hay dos búsquedas a la vez porque no hay
// dos buscadores.
func (r *runner) fetcher(ctx, jobCtx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.kick:
			r.fetch(ctx, jobCtx, r.wokenKick.Swap(false))
		}
	}
}

// fetch trae los trabajos y los reparte. Tras un despertar sin trabajos
// insiste una vez a 1 s: el trabajo puede estar en una transacción que
// todavía no confirmó.
func (r *runner) fetch(ctx, jobCtx context.Context, woken bool) {
	for ctx.Err() == nil {
		// Con el token rechazado no se pregunta: ni pedido ni línea de log.
		// Con el binario en reemplazo, tampoco: lo que entre ahora se
		// imprimiría a medias y el reinicio lo cortaría.
		if r.unpaired.Load() || r.updating.Load() {
			return
		}
		jobs, err := r.api().FetchJobs(ctx)
		if err != nil {
			if errors.Is(err, api.ErrUnauthorized) {
				r.unpaired.Store(true)
			}
			if ctx.Err() == nil {
				r.fetchErr.Printf("buscar trabajos: %v", err)
			}
			return
		}
		for _, j := range jobs {
			r.enqueue(jobCtx, j)
		}
		if len(jobs) == 0 && woken {
			woken = false
			if !sleep(ctx, rewakeDelay) {
				return
			}
			continue
		}
		return
	}
}

// queue es la cola de una impresora: sin tope, así el que reparte nunca se
// queda esperando a la impresora más lenta.
type queue struct {
	name string
	sig  chan struct{} // capacidad 1: «hay algo para sacar»

	mu   sync.Mutex
	jobs []api.Job
}

func newQueue(name string) *queue {
	return &queue{name: name, sig: make(chan struct{}, 1)}
}

// push encola y devuelve cuántos quedaron esperando.
func (q *queue) push(j api.Job) int {
	q.mu.Lock()
	q.jobs = append(q.jobs, j)
	n := len(q.jobs)
	q.mu.Unlock()
	select {
	case q.sig <- struct{}{}:
	default:
	}
	return n
}

// discard vacía la cola y devuelve cuántos quedaron sin imprimir.
func (q *queue) discard() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := len(q.jobs)
	q.jobs = nil
	return n
}

func (q *queue) pop() (api.Job, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.jobs) == 0 {
		return api.Job{}, false
	}
	j := q.jobs[0]
	q.jobs = q.jobs[1:]
	return j, true
}

// enqueue manda el trabajo a la cola de su impresora. Una impresora imprime
// de a uno y en orden; dos impresoras distintas, en paralelo. No bloquea ni
// se queda con ningún candado.
func (r *runner) enqueue(jobCtx context.Context, j api.Job) {
	key := j.Printer.Kind + "|" + j.Printer.Address + "|" + j.Printer.SystemName
	r.printersMu.Lock()
	q, ok := r.printers[key]
	if !ok {
		q = newQueue(targetLabel(j.Printer))
		r.printers[key] = q
		go r.worker(jobCtx, q)
	}
	r.printersMu.Unlock()

	// Se cuenta desde que entra a la cola: una actualización no puede colarse
	// entre el que espera y el que se está imprimiendo.
	r.inFlight.Add(1)
	// Se avisa al cruzar el umbral, no en cada trabajo.
	if n := q.push(j); n == backlogWarn {
		log.Printf("la impresora %s tiene %d trabajos esperando", q.name, n)
	}
}

func (r *runner) worker(jobCtx context.Context, q *queue) {
	// Al salir, lo que quedó en la cola no se imprimió: deja de contar como
	// trabajo en curso (el servidor lo vuelve a dar al arrancar).
	defer func() {
		if n := q.discard(); n > 0 {
			r.inFlight.Add(int64(-n))
			log.Printf("quedaron %d trabajos sin imprimir en %s: se buscan de nuevo al arrancar", n, q.name)
		}
	}()
	for {
		select {
		case <-jobCtx.Done():
			return
		case <-q.sig:
			for {
				j, ok := q.pop()
				if !ok {
					break
				}
				r.print(jobCtx, j)
				if jobCtx.Err() != nil {
					return
				}
			}
		}
	}
}

func (r *runner) print(jobCtx context.Context, j api.Job) {
	defer r.inFlight.Add(-1) // lo sumó enqueue

	log.Printf("→ trabajo %d (%s) a %s, %d bytes", j.ID, j.Kind, targetLabel(j.Printer), len(j.Payload))
	out := r.write(jobCtx, j.Printer, j.Payload)
	res := api.Result{OK: out.OK, WroteSomething: out.WroteSomething}
	if out.Err != nil {
		res.Error = out.Err.Error()
		log.Printf("✗ trabajo %d: %v", j.ID, out.Err)
	} else {
		log.Printf("✓ trabajo %d salió", j.ID)
	}
	// Al outbox ANTES de reportar: si el proceso muere acá, se reporta al volver.
	if err := r.out.Add(outbox.Entry{JobID: j.ID, Result: res, At: time.Now()}); err != nil {
		log.Println("no se pudo guardar el resultado:", err)
	}
	// Confirmarlo es tarea del flusher: la impresora sigue con el que viene.
	r.kickFlush()

	// No escribió nada: el servidor lo reprograma para dentro de 10 o 20
	// segundos, pero no vuelve a avisar por el hub. Con el hub sano el poll
	// de respaldo no corre, así que si no preguntamos solos la comanda espera
	// al próximo trabajo o al próximo latido. Se pregunta una vez, pasado el
	// plazo del servidor.
	if !res.OK && !res.WroteSomething {
		time.AfterFunc(selfKick, func() {
			if jobCtx.Err() == nil {
				r.kickFetch(false)
			}
		})
	}
}

// kickFlush pide confirmar lo que haya en el outbox. Como el canal tiene
// lugar para uno, varios pedidos se funden en una vuelta sola.
func (r *runner) kickFlush() {
	select {
	case r.flushKick <- struct{}{}:
	default:
	}
}

// flusher es el único que reporta: al ser uno solo, dos impresoras que
// terminan juntas no reportan lo mismo dos veces, y ninguna espera a la red.
func (r *runner) flusher(jobCtx context.Context) {
	defer close(r.flushDone)
	for {
		select {
		case <-jobCtx.Done():
			return
		case <-r.flushStop:
			// Última vuelta, con plazo.
			last, cancel := context.WithTimeout(jobCtx, flushWait)
			r.out.Flush(last, r.api().Report)
			cancel()
			return
		case <-r.flushKick:
			r.out.Flush(jobCtx, r.api().Report)
		}
	}
}

// maybeUpdate actualiza sólo entre trabajos: espera un rato corto a que las
// impresoras se queden sin nada y, si siguen ocupadas, lo vuelve a intentar
// en el próximo latido.
func (r *runner) maybeUpdate(ctx context.Context) {
	rel := r.pending
	if rel == nil {
		return
	}
	// Un hash mal cargado en el servidor no puede hacer que el agente se baje
	// el mismo binario cada 30 segundos para siempre: la que falló espera una
	// hora. Si el servidor publica otra versión (u otro hash), se prueba ya.
	if r.failed != nil && *r.failed == *rel && time.Since(r.failedAt) < updateRetry {
		return
	}
	if !r.waitIdle(ctx, updateWait) {
		log.Printf("actualización a %s en espera: hay trabajos imprimiéndose", rel.Version)
		return
	}
	// Desde acá no entran trabajos nuevos: bajar el binario tarda, y el
	// reinicio de después no puede cortar una comanda a la mitad.
	r.updating.Store(true)
	r.pending = nil
	target, err := update.Apply(ctx, rel.URL, rel.SHA256)
	if err != nil {
		r.updating.Store(false)
		r.failed, r.failedAt = rel, time.Now()
		log.Printf("actualización a %s: %v (se vuelve a intentar en %s)", rel.Version, err, updateRetry)
		return
	}
	// Entre el waitIdle de arriba y el reemplazo pudo colarse un trabajo que
	// ya venía en camino: se le da su rato antes de reiniciar.
	if !r.waitIdle(ctx, updateWait) {
		log.Println("hay algo imprimiéndose todavía: se reinicia igual y el servidor lo vuelve a dar")
	}
	log.Printf("actualizado a %s; reiniciando", rel.Version)
	update.Restart(target)
}

// waitIdle espera hasta d a que no quede nada en cola ni imprimiéndose.
func (r *runner) waitIdle(ctx context.Context, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for r.inFlight.Load() > 0 {
		if time.Now().After(deadline) || !sleep(ctx, idleTick) {
			return false
		}
	}
	return true
}

// reloadToken relee la configuración del disco y dice si el token cambió: es
// lo que pasa cuando vuelven a vincular la computadora sin reiniciar el
// servicio. Con el token nuevo se arma un cliente nuevo, y el que estaba
// buscando o reportando lo toma en su próxima vuelta.
func (r *runner) reloadToken() bool {
	cfg, err := config.Load()
	if err != nil || cfg.Token == "" {
		return false
	}
	r.cfgMu.Lock()
	if cfg.Token == r.cfg.Token {
		r.cfgMu.Unlock()
		return false
	}
	r.cfg.Server, r.cfg.Token, r.cfg.AgentID = cfg.Server, cfg.Token, cfg.AgentID
	if cfg.Mercure.JWT != "" {
		r.cfg.Mercure = cfg.Mercure
	}
	r.cfgMu.Unlock()
	r.client.Store(api.New(cfg.Server, cfg.Token, r.version))
	return true
}

func (r *runner) mercure() config.Mercure {
	r.cfgMu.Lock()
	defer r.cfgMu.Unlock()
	return r.cfg.Mercure
}

func (r *runner) setMercure(m config.Mercure) {
	r.cfgMu.Lock()
	defer r.cfgMu.Unlock()
	r.cfg.Mercure = m
}

// targetLabel es cómo se nombra a la impresora en el log.
func targetLabel(t api.Target) string {
	switch {
	case t.Address != "":
		return t.Address
	case t.SystemName != "":
		return t.SystemName
	default:
		return t.Kind
	}
}

// sleep espera d y dice si el contexto siguió vivo.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// throttle junta en el log las líneas iguales que se repiten: sale la
// primera, y mientras siga repitiéndose la misma sale una cada `every`, con
// la cuenta de las que se callaron. Un servidor caído late cada 30 segundos:
// sin esto, el archivo se llena con la misma frase toda la noche.
type throttle struct {
	every time.Duration

	mu   sync.Mutex
	last string
	at   time.Time
	n    int
}

func newThrottle(every time.Duration) *throttle { return &throttle{every: every} }

func (t *throttle) Printf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	t.mu.Lock()
	igual := msg == t.last
	if igual && time.Since(t.at) < t.every {
		t.n++
		t.mu.Unlock()
		return
	}
	anterior, callados := t.last, t.n
	t.last, t.at, t.n = msg, time.Now(), 0
	t.mu.Unlock()

	switch {
	case callados > 0 && igual:
		log.Printf("%s (y %d veces más)", msg, callados)
	case callados > 0:
		log.Printf("«%s» se repitió %d veces más", anterior, callados)
		log.Print(msg)
	default:
		log.Print(msg)
	}
}
