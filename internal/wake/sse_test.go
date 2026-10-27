package wake

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func fijo(jwt string) func() string { return func() string { return jwt } }

func TestListenCallsOnWakePerEventAndSendsBearer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer jwt" || r.URL.Query().Get("match") != "printing/agent/7" || r.URL.Query().Has("topic") {
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		fmt.Fprint(w, ": ping\n\n")
		fl.Flush()
		fmt.Fprint(w, "id: 1\ndata: {\"jobs\":true}\n\n")
		fl.Flush()
		fmt.Fprint(w, "data: {\"jobs\":true}\n\n")
		fl.Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	wakes := make(chan struct{}, 10)
	states := make(chan bool, 10)
	go Listen(ctx, srv.URL, "printing/agent/7", fijo("jwt"), func() { wakes <- struct{}{} }, func(c bool) { states <- c }, func() {})

	if got := <-states; !got {
		t.Fatal("primero conecta")
	}
	for i := 0; i < 2; i++ {
		select {
		case <-wakes:
		case <-time.After(2 * time.Second):
			t.Fatalf("faltó el despertar %d", i+1)
		}
	}
}

// La espera entre reintentos se acorta para todo el paquete acá y no dentro
// de un test: las goroutines de Listen sobreviven al test que las largó y
// escribirle a la variable mientras leen sería una carrera.
func init() {
	backoffStart, backoffMax = 20*time.Millisecond, 5*time.Second
	readTimeout = 100 * time.Millisecond
}

// Cada conexión sirve un evento y se corta. Si la espera creciente no vuelve
// al arranque después de una conexión buena, siete conexiones tardan la suma
// 20+40+80+160+320+640 ms; con el reseteo son seis esperas de 20 ms.
func TestBackoffResetsAfterAGoodConnection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: hay trabajo\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wakes := make(chan struct{}, 100)
	states := make(chan bool, 100)
	start := time.Now()
	go Listen(ctx, srv.URL, "t", fijo("jwt"), func() { wakes <- struct{}{} }, func(c bool) { states <- c }, func() {})

	for i := 0; i < 7; i++ {
		select {
		case <-wakes:
		case <-time.After(600 * time.Millisecond):
			t.Fatalf("sólo llegaron %d despertares en %v: la espera no se reseteó", i, time.Since(start))
		}
	}

	// Y avisa las dos puntas: conectado y desconectado.
	if got := <-states; !got {
		t.Fatal("el primer aviso tiene que ser «conectado»")
	}
	if got := <-states; got {
		t.Fatal("al cortarse tiene que avisar «desconectado»")
	}
}

// Un hub que se queda mudo —ni datos ni ping— no se nota en el socket: la
// conexión parece viva y no llega nada. Se corta por tiempo y se reconecta.
func TestReconnectsWhenTheHubGoesQuiet(t *testing.T) {
	conns := make(chan struct{}, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case conns <- struct{}{}:
		default:
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go Listen(ctx, srv.URL, "t", fijo("jwt"), func() {}, func(bool) {}, func() {})

	for i := 0; i < 3; i++ {
		select {
		case <-conns:
		case <-time.After(2 * time.Second):
			t.Fatalf("hubo %d conexiones: al hub mudo no se lo está cortando", i)
		}
	}
}

// El hub rechaza el token vencido (401) o uno que no cubre el tema (403):
// Listen avisa, y el próximo intento sale con el token que haya ENTONCES, no
// con el del arranque.
func TestARejectedTokenIsReportedAndTheNextAttemptUsesTheFreshOne(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer nuevo" {
					w.WriteHeader(code)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer srv.Close()

			var token atomic.Value
			token.Store("vencido")
			rejected := make(chan struct{}, 10)
			states := make(chan bool, 10)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			go Listen(ctx, srv.URL, "t", func() string { return token.Load().(string) }, func() {},
				func(c bool) { states <- c },
				func() {
					// El que llama consigue uno nuevo (en el agente, el latido).
					token.Store("nuevo")
					rejected <- struct{}{}
				})

			select {
			case <-rejected:
			case <-time.After(2 * time.Second):
				t.Fatal("el rechazo del hub no se avisó")
			}
			select {
			case up := <-states:
				if !up {
					t.Fatal("con el token nuevo tenía que conectar")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("el reintento no salió con el token nuevo")
			}
		})
	}
}

// Un hub que contesta otra cosa (502 de Render) no es un problema del token:
// no se pide uno nuevo.
func TestOtherErrorsAreNotARejection(t *testing.T) {
	calls := make(chan struct{}, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case calls <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	var rejected atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go Listen(ctx, srv.URL, "t", fijo("jwt"), func() {}, func(bool) {}, func() { rejected.Add(1) })

	for i := 0; i < 2; i++ {
		select {
		case <-calls:
		case <-time.After(2 * time.Second):
			t.Fatal("no reintentó")
		}
	}
	if n := rejected.Load(); n != 0 {
		t.Fatalf("un 502 no es un token rechazado, y se avisó %d veces", n)
	}
}
