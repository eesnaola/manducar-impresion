package runner

import (
	"bytes"
	"context"
	"encoding/base64"
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
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eesnaola/manducar-impresion/internal/api"
	"github.com/eesnaola/manducar-impresion/internal/caida"
	"github.com/eesnaola/manducar-impresion/internal/config"
	"github.com/eesnaola/manducar-impresion/internal/diagnostico"
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

// soltarArchivoDeCaidas va en cada test que corre Run en este proceso, justo
// después del t.TempDir() de la configuración. Run llama a caida.Preparar, que
// le pide a Go copiar las caídas a impresion.crash de ese directorio: Go se
// queda con el archivo abierto mientras viva el proceso, y en Windows un
// archivo abierto no se borra, así que la limpieza del TempDir fallaría. Las
// limpiezas corren al revés: ésta, registrada después, suelta el archivo antes.
func soltarArchivoDeCaidas(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { _ = debug.SetCrashOutput(nil, debug.CrashOptions{}) })
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
	soltarArchivoDeCaidas(t)
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
	soltarArchivoDeCaidas(t)
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
	soltarArchivoDeCaidas(t)
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
	soltarArchivoDeCaidas(t)
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
	soltarArchivoDeCaidas(t)
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

// El semáforo: el latido trae impresoras, el pulso las mira y el reporte
// llega al servidor; la que está imprimiendo no se pulsa.
func TestPulsarImpresorasReportaElSemaforo(t *testing.T) {
	var mu sync.Mutex
	var recibido []api.PrinterHealth
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/agente/impresoras/estado" {
			var body struct {
				Estados []api.PrinterHealth `json:"estados"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			recibido = body.Estados
			mu.Unlock()
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	r := testRunner(t, srv.URL)
	r.probe = func(_ context.Context, tg api.Target) string {
		if tg.Address == "127.0.0.1:1" {
			return "UNREACHABLE"
		}
		return "OK"
	}
	// La 2 está imprimiendo: su cola tiene un trabajo → OK sin pulsar. La
	// cola se arma a mano (sin worker) para que el test no dependa de una
	// carrera con el que imprime.
	ocupada := api.Target{Kind: "network", Address: "127.0.0.1:1"}
	q := newQueue("ocupada")
	q.push(api.Job{ID: 9, Printer: ocupada})
	r.printersMu.Lock()
	r.printers[ocupada.Kind+"|"+ocupada.Address+"|"+ocupada.SystemName] = q
	r.printersMu.Unlock()

	r.pulsarImpresoras(context.Background(), []api.PrinterRef{
		{ID: 1, Target: api.Target{Kind: "network", Address: "127.0.0.1:9100"}},
		{ID: 2, Target: api.Target{Kind: "network", Address: "127.0.0.1:1"}},
	})

	mu.Lock()
	defer mu.Unlock()
	if len(recibido) != 2 {
		t.Fatalf("llegaron %d estados: %+v", len(recibido), recibido)
	}
	if recibido[0].ID != 1 || recibido[0].Health != "OK" {
		t.Errorf("la 1: %+v", recibido[0])
	}
	// La 2 comparte target con la cola ocupada: cuenta como viva sin pulsar.
	if recibido[1].ID != 2 || recibido[1].Health != "OK" {
		t.Errorf("la 2 (imprimiendo): %+v", recibido[1])
	}
}

// La que está imprimiendo ahora no se pulsa aunque su cola ya esté vacía: el
// worker saca el trabajo de la cola antes de mandarlo, y por USB la pregunta
// del pulso se metería en el medio de los bytes de la comanda.
func TestNoSePulsaLaImpresoraMientrasImprime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	r := testRunner(t, srv.URL)
	escribiendo := make(chan struct{})
	soltar := make(chan struct{})
	r.write = func(context.Context, api.Target, []byte) printer.Outcome {
		close(escribiendo)
		<-soltar
		return printer.Outcome{OK: true, WroteSomething: true}
	}
	var pulsos atomic.Int32
	r.probe = func(context.Context, api.Target) string {
		pulsos.Add(1)
		return "OK"
	}

	barra := api.Target{Kind: "system", SystemName: "Barra termica"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.enqueue(ctx, api.Job{ID: 7, Printer: barra, Payload: []byte("x")})
	<-escribiendo

	r.pulsarImpresoras(context.Background(), []api.PrinterRef{{ID: 1, Target: barra}})
	close(soltar)
	waitFor(t, time.Second, "el trabajo no terminó", func() bool { return r.inFlight.Load() == 0 })

	if n := pulsos.Load(); n != 0 {
		t.Fatalf("se pulsó %d veces mientras imprimía", n)
	}
}

// La caída que quedó anotada se cuenta una sola vez, después del primer
// latido bueno, y el archivo queda vacío.
func TestLaCaidaAnotadaSeCuentaUnaVezDespuesDelPrimerLatido(t *testing.T) {
	var mu sync.Mutex
	var informes []api.CrashReport
	var latidos atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agente/latido":
			latidos.Add(1)
			_, _ = w.Write([]byte(`{"mercure":{},"jobsPending":0,"heartbeatSeconds":1}`))
		case "/agente/caida":
			var inf api.CrashReport
			_ = json.NewDecoder(r.Body).Decode(&inf)
			mu.Lock()
			informes = append(informes, inf)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			_, _ = w.Write([]byte(`{"jobs":[]}`))
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	soltarArchivoDeCaidas(t)
	t.Setenv("MANDUCAR_IMPRESION_CONFIG", filepath.Join(dir, "impresion.json"))
	if err := config.Save(config.Config{Server: srv.URL, Token: "tok", AgentID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, caida.Archivo), []byte("panic: se cayó la otra vez\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ran := make(chan error, 1)
	go func() { ran <- Run(ctx, "test") }()

	waitFor(t, 3*time.Second, "no contó la caída", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(informes) > 0
	})
	// El latido es de 1 s: se espera a que haya pasado otro, así se prueba que
	// después del primero no vuelve a mandar nada.
	waitFor(t, 3*time.Second, "no hubo un segundo latido", func() bool { return latidos.Load() >= 2 })
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-ran

	mu.Lock()
	defer mu.Unlock()
	if len(informes) != 1 || informes[0].Resumen != "panic: se cayó la otra vez" || informes[0].Version != "test" {
		t.Fatalf("informes = %+v", informes)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, caida.Archivo)); strings.TrimSpace(string(b)) != "" {
		t.Errorf("el archivo de la caída quedó con %q", b)
	}
}

// Si el servidor no la toma, la caída queda para el próximo arranque: se
// intentó una vez, no se insiste en los latidos siguientes y no se pierde.
func TestSiNoSePudoContarLaCaidaQuedaParaLaProxima(t *testing.T) {
	var intentos, latidos atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agente/latido":
			latidos.Add(1)
			_, _ = w.Write([]byte(`{"mercure":{},"jobsPending":0,"heartbeatSeconds":1}`))
		case "/agente/caida":
			intentos.Add(1)
			w.WriteHeader(500)
		default:
			_, _ = w.Write([]byte(`{"jobs":[]}`))
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	soltarArchivoDeCaidas(t)
	t.Setenv("MANDUCAR_IMPRESION_CONFIG", filepath.Join(dir, "impresion.json"))
	if err := config.Save(config.Config{Server: srv.URL, Token: "tok", AgentID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, caida.Archivo), []byte("panic: se cayó la otra vez\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ran := make(chan error, 1)
	go func() { ran <- Run(ctx, "test") }()

	waitFor(t, 3*time.Second, "no intentó contar la caída", func() bool { return intentos.Load() >= 1 })
	// Con el latido de 1 s, un segundo latido es la oportunidad de reintentar:
	// no tiene que aprovecharla.
	waitFor(t, 3*time.Second, "no hubo un segundo latido", func() bool { return latidos.Load() >= 2 })
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-ran

	if n := intentos.Load(); n != 1 {
		t.Errorf("intentos de contar la caída = %d, tenía que ser 1 por arranque", n)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, caida.Archivo)); !strings.Contains(string(b), "panic: se cayó la otra vez") {
		t.Errorf("se perdió la caída que no se pudo contar: %q", b)
	}
}

// La guarda de «una sola vez por arranque», sin pasar por el latido: llamadas
// repetidas y simultáneas a informarCaida, con un servidor lento que las deja
// encimarse, hacen exactamente un intento.
func TestInformarCaidaIntentaUnaSolaVezPorArranque(t *testing.T) {
	var intentos atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/agente/caida" {
			intentos.Add(1)
			time.Sleep(100 * time.Millisecond)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, caida.Archivo), []byte("panic: se cayó la otra vez\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := testRunner(t, srv.URL)
	r.crashDir = dir

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.informarCaida(context.Background())
		}()
	}
	wg.Wait()
	r.informarCaida(context.Background()) // y una más, ya sin competencia

	if n := intentos.Load(); n != 1 {
		t.Errorf("intentos = %d, tenía que ser 1", n)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, caida.Archivo)); !strings.Contains(string(b), "panic: se cayó la otra vez") {
		t.Errorf("se perdió la caída que no se pudo contar: %q", b)
	}
}

// Sin directorio de caídas (los runners que no pasan por Run) no hay nada que
// contar ni se toca el servidor.
func TestInformarCaidaSinDirectorioNoHaceNada(t *testing.T) {
	var intentos atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		intentos.Add(1)
	}))
	defer srv.Close()

	r := testRunner(t, srv.URL)
	r.informarCaida(context.Background())

	if n := intentos.Load(); n != 0 {
		t.Errorf("le habló al servidor %d veces sin tener qué contar", n)
	}
}

// Texto suelto en impresion.crash (algo que el agente escribió en la salida de
// error) no se cuenta, y se vacía: si quedara, se mezclaría con la próxima
// caída de verdad.
func TestTextoSueltoEnLaCaidaNoSeCuentaYSeVacia(t *testing.T) {
	var intentos atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/agente/caida" {
			intentos.Add(1)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, caida.Archivo), []byte("no se pudo rotar el log: el archivo está en uso\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := testRunner(t, srv.URL)
	r.crashDir = dir
	r.informarCaida(context.Background())

	if n := intentos.Load(); n != 0 {
		t.Errorf("contó como caída un texto suelto (%d intentos)", n)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, caida.Archivo)); len(b) != 0 {
		t.Errorf("el texto suelto quedó en el archivo: %q", b)
	}
}

// La caída se cuenta antes de decidir si actualizarse: una actualización que
// sale (os.Exit) con el informe en el aire lo dejaría contado y sin vaciar, y
// el arranque siguiente lo contaría otra vez.
func TestLaCaidaSeCuentaAntesDeDecidirLaActualizacion(t *testing.T) {
	var vaciadoAlBajar atomic.Int32 // 0: no bajó; 1: bajó con la caída vaciada; 2: bajó antes
	var dir string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agente/latido":
			// Un sha256 bien formado: la descarga devuelve 404 y el binario de
			// test no se toca.
			fmt.Fprintf(w, `{"mercure":{},"jobsPending":0,"heartbeatSeconds":30,"agent":{"version":"9.9.9","sha256":"%s","url":"http://%s/descarga"}}`, strings.Repeat("ab", 32), r.Host)
		case "/agente/caida":
			time.Sleep(300 * time.Millisecond) // un servidor lento
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/descarga":
			b, _ := os.ReadFile(filepath.Join(dir, caida.Archivo))
			if strings.TrimSpace(string(b)) == "" {
				vaciadoAlBajar.CompareAndSwap(0, 1)
			} else {
				vaciadoAlBajar.CompareAndSwap(0, 2)
			}
			w.WriteHeader(404)
		default:
			_, _ = w.Write([]byte(`{"jobs":[]}`))
		}
	}))
	defer srv.Close()

	dir = t.TempDir()
	soltarArchivoDeCaidas(t)
	t.Setenv("MANDUCAR_IMPRESION_CONFIG", filepath.Join(dir, "impresion.json"))
	if err := config.Save(config.Config{Server: srv.URL, Token: "tok", AgentID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, caida.Archivo), []byte("panic: se cayó la otra vez\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ran := make(chan error, 1)
	go func() { ran <- Run(ctx, "test") }()

	waitFor(t, 3*time.Second, "no intentó bajar la actualización", func() bool { return vaciadoAlBajar.Load() != 0 })
	cancel()
	<-ran
	if vaciadoAlBajar.Load() != 1 {
		t.Fatal("empezó a actualizarse con la caída todavía sin contar")
	}
}

func TestVersionNumerica(t *testing.T) {
	casos := map[string]struct {
		x  [3]int
		ok bool
	}{
		"0.1.9":       {[3]int{0, 1, 9}, true},
		"0.1.10":      {[3]int{0, 1, 10}, true},
		"v1.2.3":      {[3]int{1, 2, 3}, true},
		"0.1.9-rc1":   {[3]int{0, 1, 9}, true},
		"0.0.1-vigia": {[3]int{0, 0, 1}, true},
		"dev":         {ok: false},
		"test":        {ok: false},
		"0.1":         {ok: false},
		"":            {ok: false},
	}
	for v, c := range casos {
		x, ok := versionNumerica(v)
		if ok != c.ok || (ok && x != c.x) {
			t.Errorf("versionNumerica(%q) = %v, %v; quiero %v, %v", v, x, ok, c.x, c.ok)
		}
	}
}

// Las versiones anteriores a la 0.1.9 no entienden «vigilar»: un agente
// vigilado que se bajara a una de ésas dejaría la computadora sin agente en el
// próximo inicio de sesión. Las que no son X.Y.Z (las de desarrollo, las del
// E2E) no se frenan nunca.
func TestUnaBajaQueRompeElArranqueConVigia(t *testing.T) {
	casos := []struct {
		propia, anunciada string
		vigilado, frena   bool
	}{
		{"0.1.9", "0.1.8", true, true},
		{"0.1.9", "0.1.8", false, false},
		{"0.1.9", "0.1.10", true, false},
		{"0.1.10", "0.1.9", true, false},
		{"0.2.0", "0.1.8", true, true},
		{"0.1.9-rc1", "0.1.8", true, true},
		{"0.1.8", "0.1.7", true, false}, // la propia es de antes del vigía: no hay arranque que romper
		{"0.0.1-vigia", "0.0.2-vigia", true, false},
		{"0.0.2-vigia", "0.0.1-vigia", true, false},
		{"dev", "0.1.8", true, false},
		{"0.1.9", "dev", true, false},
	}
	for _, c := range casos {
		if got := bajaSinVigia(c.propia, c.anunciada, c.vigilado); got != c.frena {
			t.Errorf("bajaSinVigia(%q, %q, vigilado %v) = %v; quiero %v", c.propia, c.anunciada, c.vigilado, got, c.frena)
		}
	}
}

// En el runner: la baja anunciada no queda pendiente, se avisa en el log una
// vez por anuncio, y una versión que sí sirve se toma como siempre.
func TestElVigiladoNoSeBajaAntesDelVigia(t *testing.T) {
	var buf lockedBuffer
	viejo := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(viejo)

	r := testRunner(t, "http://127.0.0.1:0")
	r.version, r.vigilado = "0.1.9", true
	baja := &api.Release{Version: "0.1.8", SHA256: "aa", URL: "http://x/0.1.8"}

	r.anunciada(baja)
	r.anunciada(baja)
	if r.pending != nil {
		t.Fatalf("quedó pendiente %+v", r.pending)
	}
	aviso := "no bajo a 0.1.8: las versiones anteriores a la 0.1.9 no entienden el arranque con vigía; una vuelta atrás tiene que salir como versión nueva"
	if n := strings.Count(buf.String(), aviso); n != 1 {
		t.Errorf("el aviso salió %d veces:\n%s", n, buf.String())
	}

	sube := &api.Release{Version: "0.1.10", SHA256: "bb", URL: "http://x/0.1.10"}
	r.anunciada(sube)
	if r.pending != sube {
		t.Errorf("pending = %+v; quiero la 0.1.10", r.pending)
	}

	// Sin vigía, la misma baja se toma: el que la publica sabe lo que hace.
	r.vigilado = false
	r.anunciada(baja)
	if r.pending != baja {
		t.Errorf("sin vigía, pending = %+v; quiero la 0.1.8", r.pending)
	}

	// La propia no se anuncia como pendiente.
	r.pending = nil
	r.anunciada(&api.Release{Version: r.version})
	if r.pending != nil {
		t.Errorf("la versión propia quedó pendiente")
	}
}

// esperarOutboxVacio: el flusher confirmó todo y ya no escribe el outbox,
// que vive en el TempDir del test (si no, el borrado del final choca con la
// última escritura).
func esperarOutboxVacio(t *testing.T, r *runner) {
	t.Helper()
	waitFor(t, 3*time.Second, "el outbox vacío", func() bool { return len(r.out.Pending()) == 0 })
}

// trabajoDeDiagnostico arma un DIAGNOSTIC como lo manda el servidor.
func trabajoDeDiagnostico(id, diag int, pasos string) api.Job {
	return api.Job{
		ID:      id,
		Kind:    "DIAGNOSTIC",
		Printer: api.Target{Kind: "network", Address: "127.0.0.1:9"},
		Payload: []byte(fmt.Sprintf(`{"diagnostico":%d,"pasos":%s}`, diag, pasos)),
	}
}

// servidorDeDiagnostico junta lo que el agente le cuenta de un diagnóstico y de los trabajos.
type servidorDeDiagnostico struct {
	mu          sync.Mutex
	resultados  map[int]diagnostico.Resultado
	trabajos    map[int]api.Result
	diagnostico chan struct{}
}

func nuevoServidorDeDiagnostico(t *testing.T) (*servidorDeDiagnostico, string) {
	s := &servidorDeDiagnostico{resultados: map[int]diagnostico.Resultado{}, trabajos: map[int]api.Result{}, diagnostico: make(chan struct{}, 10)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var id int
		switch {
		case strings.HasPrefix(r.URL.Path, "/agente/diagnosticos/"):
			fmt.Sscanf(r.URL.Path, "/agente/diagnosticos/%d/resultado", &id)
			var res diagnostico.Resultado
			_ = json.NewDecoder(r.Body).Decode(&res)
			s.mu.Lock()
			s.resultados[id] = res
			s.mu.Unlock()
			s.diagnostico <- struct{}{}
		case strings.HasPrefix(r.URL.Path, "/agente/trabajos/"):
			fmt.Sscanf(r.URL.Path, "/agente/trabajos/%d/resultado", &id)
			var res api.Result
			_ = json.NewDecoder(r.Body).Decode(&res)
			s.mu.Lock()
			s.trabajos[id] = res
			s.mu.Unlock()
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return s, srv.URL
}

func (s *servidorDeDiagnostico) trabajo(id int) (api.Result, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.trabajos[id]
	return r, ok
}

func TestUnDiagnosticoCorreYMandaElResultado(t *testing.T) {
	s, url := nuevoServidorDeDiagnostico(t)
	r := testRunner(t, url)
	var mu sync.Mutex
	var escrito []string
	r.write = func(_ context.Context, _ api.Target, b []byte) printer.Outcome {
		mu.Lock()
		escrito = append(escrito, string(b))
		mu.Unlock()
		return printer.Outcome{OK: true, WroteSomething: true}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.flusher(ctx)

	r.enqueue(ctx, trabajoDeDiagnostico(5, 9, `[{"n":1,"renglon":"texto","accion":"mandar","bytes":"SE9MQQ==","por":"normal","veces":1},{"n":2,"renglon":"esperar 1 ms","accion":"esperar","ms":1}]`))

	waitFor(t, 3*time.Second, "el cierre del trabajo del diagnóstico", func() bool { _, ok := s.trabajo(5); return ok })
	esperarOutboxVacio(t, r)
	s.mu.Lock()
	res := s.resultados[9]
	s.mu.Unlock()
	if len(res.Pasos) != 2 || !res.Pasos[0].OK || !res.Pasos[1].OK || res.Version != "test" || res.Diagnostico != 9 {
		t.Fatalf("resultado %+v", res)
	}
	if job, _ := s.trabajo(5); !job.OK {
		t.Fatalf("el trabajo se cerró con %+v", job)
	}
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(escrito) != "[HOLA]" {
		t.Fatalf("escrito %v", escrito)
	}
}

// Mientras corre el diagnóstico la impresora es suya: el semáforo no la
// pulsa y el trabajo que llega espera a que termine (spec, «El camino»).
func TestElDiagnosticoTieneElTurnoDeLaImpresora(t *testing.T) {
	s, url := nuevoServidorDeDiagnostico(t)
	r := testRunner(t, url)
	soltar := make(chan struct{})
	var mu sync.Mutex
	var escrito []string
	r.write = func(_ context.Context, _ api.Target, b []byte) printer.Outcome {
		if string(b) == "DIAG" {
			<-soltar
		}
		mu.Lock()
		escrito = append(escrito, string(b))
		mu.Unlock()
		return printer.Outcome{OK: true, WroteSomething: true}
	}
	var pulsos atomic.Int32
	r.probe = func(context.Context, api.Target) string { pulsos.Add(1); return "OK" }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.flusher(ctx)

	diag := trabajoDeDiagnostico(5, 9, `[{"n":1,"renglon":"x","accion":"mandar","bytes":"RElBRw==","por":"normal","veces":1}]`)
	r.enqueue(ctx, diag)
	comun := api.Job{ID: 6, Kind: "COMANDA", Printer: diag.Printer, Payload: []byte("COMANDA")}
	r.enqueue(ctx, comun)
	time.Sleep(100 * time.Millisecond)
	r.pulsarImpresoras(ctx, []api.PrinterRef{{ID: 1, Target: diag.Printer}})

	if pulsos.Load() != 0 {
		t.Fatalf("se pulsó la impresora en medio del diagnóstico (%d)", pulsos.Load())
	}
	mu.Lock()
	if len(escrito) != 0 {
		t.Fatalf("salió %v antes de que terminara el diagnóstico", escrito)
	}
	mu.Unlock()

	close(soltar)
	waitFor(t, 3*time.Second, "la comanda después del diagnóstico", func() bool { _, ok := s.trabajo(6); return ok })
	esperarOutboxVacio(t, r)
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(escrito) != "[DIAG COMANDA]" {
		t.Fatalf("orden %v", escrito)
	}
}

func TestUnDiagnosticoIlegibleCierraElTrabajoConError(t *testing.T) {
	s, url := nuevoServidorDeDiagnostico(t)
	r := testRunner(t, url)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.flusher(ctx)

	r.enqueue(ctx, api.Job{ID: 5, Kind: "DIAGNOSTIC", Printer: api.Target{Kind: "network", Address: "127.0.0.1:9"}, Payload: []byte("{")})

	waitFor(t, 3*time.Second, "el cierre del trabajo", func() bool { _, ok := s.trabajo(5); return ok })
	esperarOutboxVacio(t, r)
	job, _ := s.trabajo(5)
	if job.OK || !strings.Contains(job.Error, "no se pudo leer") {
		t.Fatalf("trabajo %+v", job)
	}
	if len(s.resultados) != 0 {
		t.Fatal("mandó un resultado de un diagnóstico que no se leyó")
	}
}

// El agente se cierra a la mitad: lo que corrió y lo que faltó igual llegan.
func TestSiElAgenteSeCierraElResultadoParcialIgualSale(t *testing.T) {
	s, url := nuevoServidorDeDiagnostico(t)
	r := testRunner(t, url)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.flusher(ctx)
	jobCtx, cerrar := context.WithCancel(context.Background())

	r.enqueue(jobCtx, trabajoDeDiagnostico(5, 9, `[{"n":1,"renglon":"esperar 10","accion":"esperar","ms":10000},{"n":2,"renglon":"estado","accion":"estado"}]`))
	time.Sleep(100 * time.Millisecond)
	cerrar()

	select {
	case <-s.diagnostico:
	case <-time.After(3 * time.Second):
		t.Fatal("no llegó el resultado parcial")
	}
	waitFor(t, 3*time.Second, "el cierre del trabajo", func() bool { _, ok := s.trabajo(5); return ok })
	esperarOutboxVacio(t, r)
	s.mu.Lock()
	defer s.mu.Unlock()
	res := s.resultados[9]
	if len(res.Pasos) != 2 || res.Pasos[1].Error != "no corrió: el agente se cerró" {
		t.Fatalf("resultado %+v", res)
	}
}

// Run de verdad: al cerrarse el agente con un diagnóstico corriendo, el
// resultado parcial y el cierre del trabajo salen antes de que Run vuelva
// (después el proceso termina con os.Exit y ya no hay quién los mande).
func TestAlCerrarseElAgenteElDiagnosticoParcialSaleAntesDeTerminar(t *testing.T) {
	doc, _ := json.Marshal(map[string]any{"diagnostico": 9, "pasos": []map[string]any{
		{"n": 1, "renglon": "esperar 30", "accion": "esperar", "ms": 30000},
		{"n": 2, "renglon": "estado", "accion": "estado"},
	}})
	var (
		mu        sync.Mutex
		dado      bool
		resultado *diagnostico.Resultado
		trabajo   *api.Result
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agente/latido":
			_, _ = w.Write([]byte(`{"mercure":{"url":"http://127.0.0.1:1/.well-known/mercure","jwt":"j","topic":"t"},"agent":null,"jobsPending":1,"heartbeatSeconds":5}`))
			return
		case "/agente/trabajos":
			mu.Lock()
			defer mu.Unlock()
			if dado {
				_, _ = w.Write([]byte(`{"jobs":[]}`))
				return
			}
			dado = true
			_ = json.NewEncoder(w).Encode(map[string]any{"jobs": []map[string]any{{
				"id": 5, "kind": "DIAGNOSTIC", "printer": map[string]any{"kind": "network", "address": "127.0.0.1:9"},
				"payload": base64.StdEncoding.EncodeToString(doc),
			}}})
			return
		case "/agente/diagnosticos/9/resultado":
			var res diagnostico.Resultado
			_ = json.NewDecoder(r.Body).Decode(&res)
			mu.Lock()
			resultado = &res
			mu.Unlock()
		case "/agente/trabajos/5/resultado":
			var res api.Result
			_ = json.NewDecoder(r.Body).Decode(&res)
			mu.Lock()
			trabajo = &res
			mu.Unlock()
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	soltarArchivoDeCaidas(t)
	t.Setenv("MANDUCAR_IMPRESION_CONFIG", filepath.Join(dir, "impresion.json"))
	if err := config.Save(config.Config{Server: srv.URL, Token: "tok", AgentID: 1}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	termino := make(chan error, 1)
	go func() { termino <- Run(ctx, "test") }()

	waitFor(t, 5*time.Second, "que el agente tome el diagnóstico", func() bool { mu.Lock(); defer mu.Unlock(); return dado })
	time.Sleep(300 * time.Millisecond) // ya está en el «esperar 30»
	cancel()
	select {
	case <-termino:
	case <-time.After(10 * time.Second):
		t.Fatal("Run no volvió")
	}

	mu.Lock()
	defer mu.Unlock()
	if resultado == nil || len(resultado.Pasos) != 2 || !strings.Contains(resultado.Pasos[0].Error, "se cortó") || resultado.Pasos[1].Error != "no corrió: el agente se cerró" {
		t.Fatalf("resultado %+v", resultado)
	}
	if trabajo == nil || !trabajo.OK {
		t.Fatalf("el trabajo no se cerró antes de que Run volviera: %+v", trabajo)
	}
}

// preguntar devuelve lo que contesta del otro lado: en una impresora de
// red, sólo hacia puertos de impresora. Si no, un servidor tomado podría
// leer cualquier servicio de la red del local.
func TestPreguntarEnUnaDeRedSoloVaAPuertosDeImpresora(t *testing.T) {
	casos := []struct {
		t      api.Target
		aplica bool
	}{
		{api.Target{Kind: "network", Address: "192.168.0.50:9100"}, true},
		{api.Target{Kind: "network", Address: "192.168.0.50:9109"}, true},
		{api.Target{Kind: "network", Address: "192.168.0.1:80"}, false},
		{api.Target{Kind: "network", Address: "192.168.0.1:22"}, false},
		{api.Target{Kind: "network", Address: "sin-puerto"}, false},
		{api.Target{Kind: "system", SystemName: "Barra"}, true},
	}
	for _, c := range casos {
		err := canalDeImpresora{t: c.t}.PuedePreguntar()
		if c.aplica != (err == nil) || (err != nil && !errors.Is(err, printer.ErrNoAplica)) {
			t.Errorf("%+v: %v", c.t, err)
		}
	}
}
