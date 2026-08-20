package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eesnaola/manducar-impresion/internal/api"
	"github.com/eesnaola/manducar-impresion/internal/config"
	"github.com/eesnaola/manducar-impresion/internal/outbox"
	"github.com/eesnaola/manducar-impresion/internal/printer"
)

// Los plazos del runner se acortan para todo el paquete acá y no adentro de
// un test: las goroutines del runner sobreviven al test que las largó, y
// escribirle a la variable mientras la leen sería una carrera. Por lo mismo,
// la lista de impresoras del sistema se reemplaza acá: los tests no tienen
// por qué hablar con el CUPS de la máquina.
func init() {
	fallbackPoll = 50 * time.Millisecond
	rewakeDelay = 50 * time.Millisecond
	jwtCheckEvery = 50 * time.Millisecond
	updateWait = 200 * time.Millisecond
	shutdownWait = 500 * time.Millisecond
	flushWait = 300 * time.Millisecond
	unpairedBeat = 100 * time.Millisecond
	minBeat = 50 * time.Millisecond
	selfKick = 100 * time.Millisecond
	listSystem = func(context.Context) ([]string, error) { return []string{"Impresora de prueba"}, nil }
}

// fakePrinter escucha en 127.0.0.1:0 y devuelve lo que le escribieron.
func fakePrinter(t *testing.T) (addr string, got <-chan []byte) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan []byte, 1)
	go func() {
		defer ln.Close()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		data, _ := io.ReadAll(conn)
		ch <- data
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String(), ch
}

// testRunner arma un runner contra un servidor de mentira, con su outbox en
// un tempdir.
func testRunner(t *testing.T, server string) *runner {
	t.Helper()
	ob, err := outbox.Open(filepath.Join(t.TempDir(), "impresion-outbox.json"))
	if err != nil {
		t.Fatal(err)
	}
	return newRunner(config.Config{Server: server, Token: "tok"}, ob, "test")
}

// waitFor espera hasta d a que ok() dé verdadero.
func waitFor(t *testing.T, d time.Duration, why string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal(why)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func outboxHas(path string, jobID int) bool {
	ob, err := outbox.Open(path)
	if err != nil {
		return false
	}
	for _, e := range ob.Pending() {
		if e.JobID == jobID {
			return true
		}
	}
	return false
}

func testJob(id int, addr string) api.Job {
	return api.Job{ID: id, Kind: "COMANDA", Printer: api.Target{Kind: "network", Address: addr}, Payload: []byte("x")}
}

func TestRunPrintsWhatTheServerHandsOverAndReportsBack(t *testing.T) {
	addr, got := fakePrinter(t)
	dir := t.TempDir()
	obPath := filepath.Join(dir, "impresion-outbox.json")

	var mu sync.Mutex
	reported := map[int]api.Result{}
	fetches := 0
	outboxLate := false
	done := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/agente/latido":
			// Mercure apunta a un puerto cerrado: el SSE nunca conecta y
			// manda el poll de respaldo.
			_, _ = w.Write([]byte(`{"mercure":{"url":"http://127.0.0.1:1/.well-known/mercure","jwt":"j","topic":"t"},"agent":null,"jobsPending":1,"heartbeatSeconds":30}`))
		case r.URL.Path == "/agente/trabajos":
			mu.Lock()
			fetches++
			first := fetches == 1
			mu.Unlock()
			if first {
				fmt.Fprintf(w, `{"jobs":[{"id":5,"kind":"TEST","printer":{"kind":"network","address":"%s"},"payload":"G0BIb2xh"}]}`, addr)
			} else {
				_, _ = w.Write([]byte(`{"jobs":[]}`))
			}
		case strings.HasPrefix(r.URL.Path, "/agente/trabajos/"):
			var res api.Result
			_ = json.NewDecoder(r.Body).Decode(&res)
			id, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/agente/trabajos/"), "/resultado"))
			mu.Lock()
			reported[id] = res
			// El resultado tiene que estar guardado ANTES de llegar acá: si
			// el proceso se muere en el medio, se reporta al volver.
			if !outboxHas(obPath, id) {
				outboxLate = true
			}
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
			if id == 5 {
				once.Do(func() { close(done) })
			}
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	t.Setenv("MANDUCAR_IMPRESION_CONFIG", filepath.Join(dir, "impresion.json"))
	if err := config.Save(config.Config{Server: srv.URL, Token: "tok", AgentID: 1}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	ran := make(chan error, 1)
	go func() { ran <- Run(ctx, "test") }()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("el servidor nunca recibió el resultado del trabajo 5")
	}

	// El outbox queda vacío cuando el servidor acepta el resultado. Se espera
	// acá y no después de cortar: cortar a mitad del reporte lo dejaría
	// pendiente de verdad.
	waitFor(t, 2*time.Second, "el outbox tenía que quedar vacío", func() bool {
		ob, err := outbox.Open(obPath)
		return err == nil && len(ob.Pending()) == 0
	})

	cancel()
	select {
	case err := <-ran:
		if err != nil {
			t.Fatalf("Run devolvió %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run no volvió al cancelar el contexto")
	}

	select {
	case data := <-got:
		if string(data) != "\x1b@Hola" {
			t.Fatalf("la impresora recibió %q", data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("la impresora no recibió nada")
	}
	mu.Lock()
	defer mu.Unlock()
	if res, ok := reported[5]; !ok || !res.OK || !res.WroteSomething {
		t.Fatalf("resultado: %+v", reported)
	}
	if outboxLate {
		t.Fatal("el resultado se reportó antes de guardarse en el outbox")
	}
	if _, err := os.Stat(obPath); err != nil {
		t.Fatalf("el outbox no se escribió: %v", err)
	}
}

// Tras un despertar del hub, el trabajo puede estar en una transacción que
// todavía no confirmó: hay que volver a preguntar una vez.
func TestFetchAfterAWakeInsistsOnceWhenThereIsNothingYet(t *testing.T) {
	addr, got := fakePrinter(t)
	var mu sync.Mutex
	fetches := 0
	reported := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/agente/trabajos/"):
			_, _ = w.Write([]byte(`{"ok":true}`))
			select {
			case reported <- struct{}{}:
			default:
			}
		case r.URL.Path == "/agente/trabajos":
			mu.Lock()
			fetches++
			n := fetches
			mu.Unlock()
			if n == 1 {
				_, _ = w.Write([]byte(`{"jobs":[]}`))
				return
			}
			fmt.Fprintf(w, `{"jobs":[{"id":9,"kind":"COMANDA","printer":{"kind":"network","address":"%s"},"payload":"G0BIb2xh"}]}`, addr)
		default:
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer srv.Close()

	r := testRunner(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go r.flusher(ctx)
	r.fetch(ctx, ctx, true)

	select {
	case data := <-got:
		if string(data) != "\x1b@Hola" {
			t.Fatalf("la impresora recibió %q", data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("el segundo intento tenía que encontrar el trabajo")
	}
	select {
	case <-reported:
	case <-time.After(2 * time.Second):
		t.Fatal("el resultado del trabajo 9 no se reportó")
	}
	// Y el outbox se vacía: si no se espera, el worker le escribe al tempdir
	// mientras el test lo borra.
	waitFor(t, 2*time.Second, "el outbox tenía que quedar vacío", func() bool { return len(r.out.Pending()) == 0 })

	mu.Lock()
	defer mu.Unlock()
	if fetches != 2 {
		t.Fatalf("tenía que preguntar dos veces, preguntó %d", fetches)
	}
}

// El evento del hub es el que dispara la búsqueda: acá no corre el poll de
// respaldo, así que si no buscó fue porque el SSE no llegó.
func TestAnEventFromTheHubTriggersASearch(t *testing.T) {
	asked := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/agente/trabajos" {
			select {
			case asked <- struct{}{}:
			default:
			}
		}
		_, _ = w.Write([]byte(`{"jobs":[]}`))
	}))
	defer srv.Close()

	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"jobs\":true}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer hub.Close()

	r := testRunner(t, srv.URL)
	r.cfg.Mercure = config.Mercure{URL: hub.URL, JWT: "j", Topic: "printing/agent/1"}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go r.fetcher(ctx, ctx)
	go r.sseLoop(ctx)

	select {
	case <-asked:
	case <-time.After(2 * time.Second):
		t.Fatal("el evento del hub no disparó ninguna búsqueda")
	}
	if !r.sseUp.Load() {
		t.Fatal("el hub tenía que quedar marcado como conectado")
	}
}

// Regla 6: los pedidos que llegan mientras se está buscando no se pierden
// —se busca de nuevo al terminar— y se funden en uno solo.
func TestKicksThatArriveWhileSearchingAreNotLostAndMergeIntoOne(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	fetches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fetches++
		n := fetches
		mu.Unlock()
		if n == 1 {
			<-release
		}
		_, _ = w.Write([]byte(`{"jobs":[]}`))
	}))
	defer srv.Close()

	r := testRunner(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go r.fetcher(ctx, ctx)

	cuantas := func() int {
		mu.Lock()
		defer mu.Unlock()
		return fetches
	}
	r.kickFetch(false)
	waitFor(t, 2*time.Second, "la primera búsqueda nunca llegó al servidor", func() bool { return cuantas() == 1 })

	r.kickFetch(false) // el buscador está ocupado: quedan esperando…
	r.kickFetch(false) // …y los dos se funden en un solo pedido
	close(release)

	waitFor(t, 2*time.Second, "el pedido que llegó mientras buscaba se perdió", func() bool { return cuantas() == 2 })
	time.Sleep(100 * time.Millisecond)
	if n := cuantas(); n != 2 {
		t.Fatalf("los dos pedidos tenían que fundirse en una búsqueda: hubo %d", n)
	}
}

// Un token que el servidor no reconoce no puede matar al agente: como
// servicio se reiniciaría en loop y martillaría al servidor. Afloja el ritmo,
// avisa y sigue vivo.
func TestServerThatDoesNotRecognizeUsKeepsTheAgentAlive(t *testing.T) {
	beats := make(chan struct{}, 100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/agente/latido" {
			select {
			case beats <- struct{}{}:
			default:
			}
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"token desconocido"}`))
			return
		}
		_, _ = w.Write([]byte(`{"jobs":[]}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("MANDUCAR_IMPRESION_CONFIG", filepath.Join(dir, "impresion.json"))
	if err := config.Save(config.Config{Server: srv.URL, Token: "viejo", AgentID: 1}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	ran := make(chan error, 1)
	go func() { ran <- Run(ctx, "test") }()

	for i := 0; i < 3; i++ {
		select {
		case <-beats:
		case <-time.After(2 * time.Second):
			t.Fatalf("dejó de latir después de %d latidos", i)
		}
	}
	select {
	case err := <-ran:
		t.Fatalf("Run salió (%v) y tenía que seguir vivo esperando que lo vinculen de nuevo", err)
	default:
	}

	cancel()
	select {
	case err := <-ran:
		if err != nil {
			t.Fatalf("Run devolvió %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run no volvió al cancelar el contexto")
	}
}

// Una impresora imprime de a uno y en orden; dos impresoras distintas van en
// paralelo.
func TestOnePrinterAtATimeButDifferentPrintersInParallel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	type span struct {
		target     string
		start, end time.Time
	}
	var mu sync.Mutex
	var spans []span
	finished := make(chan struct{}, 3)

	r := testRunner(t, srv.URL)
	r.write = func(_ context.Context, target api.Target, _ []byte) printer.Outcome {
		start := time.Now()
		time.Sleep(150 * time.Millisecond)
		mu.Lock()
		spans = append(spans, span{target.Address, start, time.Now()})
		mu.Unlock()
		finished <- struct{}{}
		return printer.Outcome{OK: true, WroteSomething: true}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go r.flusher(ctx)
	r.enqueue(ctx, testJob(1, "impresora-a"))
	r.enqueue(ctx, testJob(2, "impresora-a"))
	r.enqueue(ctx, testJob(3, "impresora-b"))

	// Los tres cuentan como «en curso» desde que entran a la cola, no desde
	// que la impresora los agarra: una actualización no puede colarse entre
	// el que espera y el que se está imprimiendo.
	if n := r.inFlight.Load(); n != 3 {
		t.Fatalf("tenían que contarse los 3 trabajos en curso, se contaron %d", n)
	}

	for i := 0; i < 3; i++ {
		select {
		case <-finished:
		case <-time.After(4 * time.Second):
			t.Fatalf("sólo salieron %d trabajos", i)
		}
	}
	// El aviso de «terminé» sale desde adentro de la escritura: recién
	// cuando inFlight vuelve a cero el trabajo está guardado y reportado.
	waitFor(t, 2*time.Second, "quedaron trabajos contados como en curso", func() bool { return r.inFlight.Load() == 0 })
	waitFor(t, 2*time.Second, "el outbox tenía que quedar vacío", func() bool { return len(r.out.Pending()) == 0 })

	mu.Lock()
	defer mu.Unlock()
	var a, b []span
	for _, s := range spans {
		if s.target == "impresora-a" {
			a = append(a, s)
		} else {
			b = append(b, s)
		}
	}
	if len(a) != 2 || len(b) != 1 {
		t.Fatalf("salieron %d en la a y %d en la b", len(a), len(b))
	}
	pisan := func(x, y span) bool { return x.start.Before(y.end) && y.start.Before(x.end) }
	if pisan(a[0], a[1]) {
		t.Fatal("la misma impresora imprimió dos trabajos a la vez")
	}
	if !pisan(b[0], a[0]) && !pisan(b[0], a[1]) {
		t.Fatal("dos impresoras distintas tenían que imprimir en paralelo")
	}
}

// Con el token rechazado no hay nada que buscar: el poll de respaldo se
// apaga, así el agente no le pega al servidor cada 5 s ni llena el log.
func TestWhileTheTokenIsRejectedItStopsAskingForJobs(t *testing.T) {
	var mu sync.Mutex
	beats, trabajos := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		switch r.URL.Path {
		case "/agente/latido":
			beats++
		case "/agente/trabajos":
			trabajos++
		}
		mu.Unlock()
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":"token desconocido"}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("MANDUCAR_IMPRESION_CONFIG", filepath.Join(dir, "impresion.json"))
	if err := config.Save(config.Config{Server: srv.URL, Token: "viejo", AgentID: 1}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	ran := make(chan error, 1)
	go func() { ran <- Run(ctx, "test") }()

	cuantos := func(p *int) int {
		mu.Lock()
		defer mu.Unlock()
		return *p
	}
	waitFor(t, 3*time.Second, "dejó de latir", func() bool { return cuantos(&beats) >= 5 })
	// El poll corre cada 50 ms en los tests: sin la traba habría una decena.
	if n := cuantos(&trabajos); n > 1 {
		t.Fatalf("siguió pidiendo trabajos %d veces con el token rechazado", n)
	}

	cancel()
	select {
	case err := <-ran:
		if err != nil {
			t.Fatalf("Run devolvió %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run no volvió al cancelar el contexto")
	}
}

// Un servidor que acepta la conexión y nunca contesta no puede dejar el
// proceso colgado al salir: el último vaciado tiene plazo y lo que no entró
// queda en el outbox.
func TestShutdownDoesNotHangOnASilentServer(t *testing.T) {
	hang := make(chan struct{})
	asked := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/agente/trabajos/"):
			select {
			case asked <- struct{}{}:
			default:
			}
			<-hang // no contesta nunca
		case r.URL.Path == "/agente/latido":
			_, _ = w.Write([]byte(`{"mercure":{"url":"http://127.0.0.1:1/.well-known/mercure","jwt":"j","topic":"t"},"agent":null,"jobsPending":0,"heartbeatSeconds":30}`))
		default:
			_, _ = w.Write([]byte(`{"jobs":[]}`))
		}
	}))
	defer srv.Close()
	defer close(hang) // se libera al handler antes de cerrar el servidor

	dir := t.TempDir()
	t.Setenv("MANDUCAR_IMPRESION_CONFIG", filepath.Join(dir, "impresion.json"))
	if err := config.Save(config.Config{Server: srv.URL, Token: "tok", AgentID: 1}); err != nil {
		t.Fatal(err)
	}
	// Un resultado de la corrida anterior esperando confirmación.
	obPath := filepath.Join(dir, "impresion-outbox.json")
	ob, err := outbox.Open(obPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ob.Add(outbox.Entry{JobID: 7, Result: api.Result{OK: true, WroteSomething: true}, At: time.Now()}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ran := make(chan error, 1)
	go func() { ran <- Run(ctx, "test") }()

	select {
	case <-asked:
	case <-time.After(3 * time.Second):
		t.Fatal("nunca intentó confirmar el resultado que estaba esperando")
	}
	cancel()
	salida := time.Now()
	select {
	case err := <-ran:
		if err != nil {
			t.Fatalf("Run devolvió %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run se quedó colgado esperando a un servidor que no contesta")
	}
	if d := time.Since(salida); d > 2*time.Second {
		t.Fatalf("tardó %v en salir", d)
	}
	if !outboxHas(obPath, 7) {
		t.Fatal("el resultado sin confirmar tenía que quedar en el outbox")
	}
}

// Confirmar es tarea del flusher: un servidor que no contesta el resultado
// no puede frenar a la impresora con el trabajo que sigue.
func TestAReportThatDoesNotComeBackDoesNotStopThePrinter(t *testing.T) {
	hang := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/agente/trabajos/") {
			<-hang
			return
		}
		_, _ = w.Write([]byte(`{"jobs":[]}`))
	}))
	defer srv.Close()
	defer close(hang)

	impresos := make(chan int, 4)
	r := testRunner(t, srv.URL)
	r.write = func(_ context.Context, _ api.Target, _ []byte) printer.Outcome {
		return printer.Outcome{OK: true, WroteSomething: true}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go r.flusher(ctx)
	r.enqueue(ctx, testJob(1, "impresora-a"))
	r.enqueue(ctx, testJob(2, "impresora-a"))
	go func() {
		waitForNoFatal(2*time.Second, func() bool { return len(r.out.Pending()) == 2 })
		for _, e := range r.out.Pending() {
			impresos <- e.JobID
		}
	}()

	// Los dos tienen que salir aunque el primer reporte esté colgado.
	for i := 0; i < 2; i++ {
		select {
		case <-impresos:
		case <-time.After(3 * time.Second):
			t.Fatalf("con el reporte colgado sólo salieron %d trabajos", i)
		}
	}
}

func waitForNoFatal(d time.Duration, ok func() bool) {
	deadline := time.Now().Add(d)
	for !ok() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
}

// Lo que quedó en la cola al cortar no se imprimió: deja de contar como
// trabajo en curso (si no, el contador nunca vuelve a cero y la próxima
// actualización espera al pedo).
func TestJobsAbandonedAtShutdownStopBeingCounted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	started := make(chan struct{}, 1)
	r := testRunner(t, srv.URL)
	r.write = func(ctx context.Context, _ api.Target, _ []byte) printer.Outcome {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done() // se queda imprimiendo hasta que corten
		return printer.Outcome{Err: ctx.Err()}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.flusher(ctx)
	r.enqueue(ctx, testJob(1, "impresora-a"))
	r.enqueue(ctx, testJob(2, "impresora-a"))
	r.enqueue(ctx, testJob(3, "impresora-a"))

	<-started
	if n := r.inFlight.Load(); n != 3 {
		t.Fatalf("tenían que contarse los 3, se contaron %d", n)
	}
	cancel()
	waitFor(t, 2*time.Second, "los trabajos abandonados se siguen contando como en curso", func() bool {
		return r.inFlight.Load() == 0
	})
}

// Un trabajo que no escribió nada lo reprograma el servidor para dentro de
// 10 o 20 segundos, y no vuelve a avisar por el hub: el agente tiene que
// preguntar solo, o la comanda espera al próximo trabajo. (selfKick está
// acortado en el init de este paquete.)
func TestAJobThatWroteNothingAsksAgainByItself(t *testing.T) {
	var mu sync.Mutex
	var enQue []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/agente/trabajos":
			mu.Lock()
			enQue = append(enQue, time.Now())
			n := len(enQue)
			mu.Unlock()
			if n == 1 {
				_, _ = w.Write([]byte(`{"jobs":[{"id":11,"kind":"COMANDA","printer":{"kind":"network","address":"impresora-a"},"payload":"eA=="}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"jobs":[]}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer srv.Close()

	r := testRunner(t, srv.URL)
	r.write = func(context.Context, api.Target, []byte) printer.Outcome {
		return printer.Outcome{Err: errors.New("no se pudo conectar")}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go r.flusher(ctx)
	go r.fetcher(ctx, ctx)
	arranque := time.Now()
	r.kickFetch(false)

	cuantas := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(enQue)
	}
	waitFor(t, 2*time.Second, "el trabajo que no escribió nada no hizo preguntar de nuevo", func() bool { return cuantas() >= 2 })
	mu.Lock()
	segunda := enQue[1]
	mu.Unlock()
	if d := segunda.Sub(arranque); d < selfKick {
		t.Fatalf("la segunda búsqueda llegó a los %v y tenía que esperar el plazo del servidor (%v)", d, selfKick)
	}
	waitFor(t, 2*time.Second, "el outbox tenía que quedar vacío", func() bool { return len(r.out.Pending()) == 0 })
}

// El fracaso llega entero al servidor: `ok:false`, `wroteSomething:false` y el
// motivo legible. De eso depende que el servidor sepa que puede reintentar
// solo, y que el panel muestre por qué no salió.
func TestAFailureIsReportedWithItsReason(t *testing.T) {
	llegó := make(chan api.Result, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/agente/trabajos/") {
			var res api.Result
			_ = json.NewDecoder(r.Body).Decode(&res)
			select {
			case llegó <- res:
			default:
			}
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	r := testRunner(t, srv.URL)
	r.write = func(context.Context, api.Target, []byte) printer.Outcome {
		return printer.Outcome{Err: errors.New("no se pudo conectar a 192.168.0.50:9100")}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go r.flusher(ctx)
	r.enqueue(ctx, testJob(4, "192.168.0.50:9100"))

	select {
	case res := <-llegó:
		if res.OK || res.WroteSomething {
			t.Fatalf("un trabajo que no salió no puede reportarse como hecho: %+v", res)
		}
		if !strings.Contains(res.Error, "192.168.0.50:9100") {
			t.Fatalf("el motivo tenía que llegar al servidor, llegó %q", res.Error)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("el fracaso nunca se reportó")
	}
	waitFor(t, 2*time.Second, "el outbox tenía que quedar vacío", func() bool { return len(r.out.Pending()) == 0 })
}

// Un servidor caído late cada 30 segundos con el mismo error: en el log sale
// una vez, y después una cada tanto con la cuenta de las que se callaron.
func TestThrottleCollapsesTheSameLine(t *testing.T) {
	var buf lockedBuffer
	viejo := log.Writer()
	flags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() { log.SetOutput(viejo); log.SetFlags(flags) }()

	th := newThrottle(time.Hour)
	for i := 0; i < 4; i++ {
		th.Printf("latido: %v", "el servidor no contesta")
	}
	if n := strings.Count(buf.String(), "el servidor no contesta"); n != 1 {
		t.Fatalf("la misma línea salió %d veces:\n%s", n, buf.String())
	}

	// Vencido el plazo, sale de nuevo y dice cuántas se callaron.
	th.at = time.Now().Add(-2 * time.Hour)
	th.Printf("latido: %v", "el servidor no contesta")
	if !strings.Contains(buf.String(), "(y 3 veces más)") {
		t.Fatalf("tenía que contar las que se calló:\n%s", buf.String())
	}

	// Y una línea distinta sale enseguida, sin esperar ningún plazo.
	th.Printf("buscar trabajos: %v", "404")
	if !strings.Contains(buf.String(), "404") {
		t.Fatalf("una línea nueva no se puede callar:\n%s", buf.String())
	}
}

// Los tests que miran el log corren mientras las goroutines de otros tests
// pueden estar escribiéndole: el buffer va con candado.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Vincular de nuevo mientras el servicio corre: el token del archivo cambió y
// el que está corriendo tiene el viejo. En vez de quedarse rechazado hasta
// que alguien reinicie el servicio, relee la config y sigue con el nuevo.
func TestARepairedTokenIsPickedUpWithoutRestarting(t *testing.T) {
	conNuevo := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer nuevo" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"token desconocido"}`))
			return
		}
		if r.URL.Path == "/agente/latido" {
			select {
			case conNuevo <- struct{}{}:
			default:
			}
		}
		_, _ = w.Write([]byte(`{"jobs":[]}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("MANDUCAR_IMPRESION_CONFIG", filepath.Join(dir, "impresion.json"))
	if err := config.Save(config.Config{Server: srv.URL, Token: "viejo", AgentID: 1}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ran := make(chan error, 1)
	go func() { ran <- Run(ctx, "test") }()

	// Alguien corre «vincular» de nuevo en esta computadora.
	time.Sleep(150 * time.Millisecond)
	if err := config.Save(config.Config{Server: srv.URL, Token: "nuevo", AgentID: 1}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-conNuevo:
	case <-time.After(3 * time.Second):
		t.Fatal("siguió latiendo con el token viejo después de volver a vincular")
	}
	cancel()
	select {
	case err := <-ran:
		if err != nil {
			t.Fatalf("Run devolvió %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run no volvió al cancelar el contexto")
	}
}

// El vigilante: un trabajo trabado en el spooler que al final sale se reporta
// como impreso; el que sigue trabado se sigue mirando; el más viejo que
// vigilHasta se suelta.
func TestVigilanteReportaElQueAlFinalSalio(t *testing.T) {
	r := testRunner(t, "http://127.0.0.1:0")
	estados := map[string]printer.SpoolState{"Dummy-1": printer.SpoolStillQueued, "Dummy-2": printer.SpoolPrinted, "Dummy-4": printer.SpoolCanceled}
	falla := errors.New("spooler mudo")
	r.spool = func(_ context.Context, name, id string) (printer.SpoolState, error) {
		if id == "Dummy-3" {
			return printer.SpoolStillQueued, falla
		}
		return estados[id], nil
	}
	r.vigilar(41, "Dummy_Ticket", "Dummy-1")
	r.vigilar(42, "Dummy_Ticket", "Dummy-2")
	r.vigilar(43, "Dummy_Ticket", "Dummy-3")
	r.vigilar(44, "Dummy_Ticket", "Dummy-4")
	// El 43 es viejo y su consulta falla: a vigilHasta se lo suelta.
	r.vigilMu.Lock()
	r.vigilados[2].desde = time.Now().Add(-vigilHasta - time.Minute)
	r.vigilMu.Unlock()

	r.vigilarUnaVuelta(context.Background())

	// El 42 salió (OK) y el 44 fue cancelado (canceled, sin OK).
	var oks, cancelados []int
	for _, e := range r.out.Pending() {
		if e.Result.OK {
			oks = append(oks, e.JobID)
		}
		if e.Result.Canceled {
			cancelados = append(cancelados, e.JobID)
		}
	}
	if len(oks) != 1 || oks[0] != 42 {
		t.Fatalf("impresos: %v", oks)
	}
	if len(cancelados) != 1 || cancelados[0] != 44 {
		t.Fatalf("cancelados: %v", cancelados)
	}
	// El 41 sigue vigilado; el 43 se soltó.
	r.vigilMu.Lock()
	defer r.vigilMu.Unlock()
	if len(r.vigilados) != 1 || r.vigilados[0].jobID != 41 {
		t.Fatalf("vigilados: %+v", r.vigilados)
	}
}
