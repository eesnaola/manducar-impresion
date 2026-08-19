// Package wake escucha el topic del agente en el hub de Mercure y avisa
// cuando el servidor dice «hay trabajo». No lleva el trabajo: eso se busca
// por HTTP. Si el hub se cae, el que llama pasa a pollear.
package wake

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// La espera entre reintentos arranca en backoffStart y se duplica hasta
// backoffMax. Son variables para que los tests no tarden medio minuto.
var (
	backoffStart = time.Second
	backoffMax   = 30 * time.Second
)

// readTimeout: Mercure manda un ping cada ~40 s. Si no llega ni una línea en
// todo este rato, la conexión está muerta aunque el socket siga abierto (un
// router que se comió la conexión, un NAT que la olvidó): se corta y se
// vuelve a conectar.
var readTimeout = 90 * time.Second

// Sin timeout total: la conexión vive mientras viva el proceso. El transporte
// es el de siempre, así las conexiones muertas no se acumulan.
var client = &http.Client{}

// Listen se queda escuchando hasta que ctx termine. Llama onWake() por cada
// evento con datos y onState(true/false) al conectarse y al caerse.
func Listen(ctx context.Context, hubURL, topic, jwt string, onWake func(), onState func(connected bool)) {
	backoff := backoffStart
	for ctx.Err() == nil {
		connected := false
		// connect llama a onState en esta misma goroutine: el flag no corre.
		err := connect(ctx, hubURL, topic, jwt, onWake, func(up bool) {
			if up {
				connected = true
			}
			onState(up)
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Println("hub:", err)
		}
		if connected {
			// Hubo conexión buena: la próxima caída se reintenta enseguida.
			backoff = backoffStart
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < backoffMax {
			if backoff *= 2; backoff > backoffMax {
				backoff = backoffMax
			}
		}
	}
}

func connect(ctx context.Context, hubURL, topic, jwt string, onWake func(), onState func(bool)) error {
	// Contexto propio: el perro guardián de la lectura lo corta.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	u, err := url.Parse(hubURL)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("topic", topic)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+jwt)

	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return &httpError{res.StatusCode}
	}
	onState(true)
	defer onState(false)

	// Si no llega ni un ping en readTimeout, se corta y se reconecta.
	var mudo atomic.Bool
	watchdog := time.AfterFunc(readTimeout, func() { mudo.Store(true); cancel() })
	defer watchdog.Stop()

	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	hasData := false
	for sc.Scan() {
		watchdog.Reset(readTimeout)
		line := sc.Text()
		switch {
		case line == "":
			// Fin del evento: si trajo datos, hay algo que buscar.
			if hasData {
				onWake()
			}
			hasData = false
		case strings.HasPrefix(line, "data:"):
			hasData = true
		}
	}
	if mudo.Load() {
		return fmt.Errorf("no dijo nada en %s; se corta y se vuelve a conectar", readTimeout)
	}
	return sc.Err()
}

type httpError struct{ code int }

func (e *httpError) Error() string { return "contestó HTTP " + http.StatusText(e.code) }
