package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPairSendsCodeAndReadsToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agente/vincular" || r.Method != http.MethodPost {
			t.Errorf("ruta: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["code"] != "123456" || body["platform"] == "" || body["version"] != "9.9.9" {
			t.Errorf("cuerpo: %v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token": "tok", "agentId": 7,
			"store":   map[string]string{"slug": "pizzeria", "name": "La Pizzería"},
			"mercure": map[string]string{"url": "http://hub", "jwt": "j", "topic": "printing/agent/7"},
		})
	}))
	defer srv.Close()

	res, err := New(srv.URL, "", "9.9.9").Pair(context.Background(), "123456", "PC", []string{"POS-80"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Token != "tok" || res.AgentID != 7 || res.Mercure.Topic != "printing/agent/7" || res.Store.Name != "La Pizzería" {
		t.Fatalf("%+v", res)
	}
}

func TestFetchJobsDecodesBase64AndSendsBearer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`{"jobs":[{"id":3,"kind":"COMANDA","printer":{"kind":"network","address":"127.0.0.1:9100"},"payload":"G0A="}]}`))
	}))
	defer srv.Close()

	jobs, err := New(srv.URL, "tok", "1").FetchJobs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != 3 || string(jobs[0].Payload) != "\x1b@" || jobs[0].Printer.Address != "127.0.0.1:9100" {
		t.Fatalf("%+v", jobs)
	}
}

func TestUnauthorizedIsATypedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer srv.Close()
	_, err := New(srv.URL, "malo", "1").Heartbeat(context.Background(), nil)
	if err != ErrUnauthorized {
		t.Fatalf("esperaba ErrUnauthorized, vino %v", err)
	}
}

func TestReportOfUnknownJobIsNotRetried(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer srv.Close()
	err := New(srv.URL, "tok", "1").Report(context.Background(), 99, Result{OK: true, WroteSomething: true})
	if err != ErrUnknownJob {
		t.Fatalf("esperaba ErrUnknownJob, vino %v", err)
	}
}

// `systemPrinters` es opcional: nil quiere decir «en este latido no mando la
// lista» y una lista vacía quiere decir «esta computadora no tiene ninguna».
// Si se confundieran, borrar la última impresora del sistema no se notaría.
func TestHeartbeatOnlySendsThePrintersWhenThereIsAList(t *testing.T) {
	cuerpos := make(chan map[string]any, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		cuerpos <- body
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", "1")

	if _, err := c.Heartbeat(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if body := <-cuerpos; body["systemPrinters"] != nil {
		t.Fatalf("sin lista no se manda la clave: %v", body)
	}

	if _, err := c.Heartbeat(context.Background(), []string{}); err != nil {
		t.Fatal(err)
	}
	body := <-cuerpos
	lista, ok := body["systemPrinters"].([]any)
	if !ok || len(lista) != 0 {
		t.Fatalf("una lista vacía se manda igual —es «no tengo ninguna»—: %v", body)
	}
}

// El cuerpo de un error puede ser una página de HTML entera: al log va una
// línea corta, no el sitio.
func TestErrorTextIsOneShortLine(t *testing.T) {
	largo := "<html>\n  <body>" + strings.Repeat("a", 500) + "</body>\n</html>"
	got := errorText([]byte(largo), "")
	if len(got) > errorMax+10 {
		t.Fatalf("quedó de %d bytes: %q", len(got), got)
	}
	if strings.ContainsAny(got, "\n\r") {
		t.Fatalf("tiene saltos de línea: %q", got)
	}
	if got := errorText([]byte(`{"error":"código vencido"}`), ""); got != "código vencido" {
		t.Fatalf("el mensaje del servidor tiene que pasar tal cual: %q", got)
	}
}
