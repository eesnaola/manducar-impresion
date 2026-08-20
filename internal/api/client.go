// Package api es el cliente del contrato /agente/ del servidor de Manducar.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"
)

var (
	ErrUnauthorized = errors.New("el servidor no reconoce el token: volvé a vincular la computadora")
	ErrUnknownJob   = errors.New("el servidor no conoce ese trabajo")
)

var jsonUnmarshal = json.Unmarshal

// errorMax: lo más largo que se muestra de un error del servidor.
const errorMax = 300

type Client struct {
	server  string
	token   string
	version string
	http    *http.Client
}

func New(server, token, version string) *Client {
	return &Client{
		server:  strings.TrimRight(server, "/"),
		token:   token,
		version: version,
		http:    &http.Client{Timeout: 20 * time.Second},
	}
}

func Platform() string { return runtime.GOOS + "-" + runtime.GOARCH }

func (c *Client) Pair(ctx context.Context, code, name string, systemPrinters []string) (PairResponse, error) {
	var out PairResponse
	err := c.post(ctx, "/agente/vincular", map[string]any{
		"code": code, "name": name, "platform": Platform(), "version": c.version, "systemPrinters": systemPrinters,
	}, &out)
	if err != nil {
		return out, err
	}
	return out, nil
}

// Heartbeat: systemPrinters nil = no mandar la lista en este latido.
func (c *Client) Heartbeat(ctx context.Context, systemPrinters []string) (HeartbeatResponse, error) {
	body := map[string]any{"version": c.version}
	if systemPrinters != nil {
		body["systemPrinters"] = systemPrinters
	}
	var out HeartbeatResponse
	return out, c.post(ctx, "/agente/latido", body, &out)
}

func (c *Client) FetchJobs(ctx context.Context) ([]Job, error) {
	var out struct {
		Jobs []Job `json:"jobs"`
	}
	if err := c.post(ctx, "/agente/trabajos", map[string]any{}, &out); err != nil {
		return nil, err
	}
	return out.Jobs, nil
}

// ReportPrinterHealth cuenta el semáforo. Es efímero: si no llega, llega el
// del próximo latido — por eso no pasa por el outbox.
func (c *Client) ReportPrinterHealth(ctx context.Context, estados []PrinterHealth) error {
	return c.post(ctx, "/agente/impresoras/estado", map[string]any{"estados": estados}, nil)
}

func (c *Client) Report(ctx context.Context, jobID int, r Result) error {
	return c.post(ctx, fmt.Sprintf("/agente/trabajos/%d/resultado", jobID), r, nil)
}

func (c *Client) post(ctx context.Context, path string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.server+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "manducar-impresion/"+c.version)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))

	switch {
	case res.StatusCode == http.StatusUnauthorized:
		return ErrUnauthorized
	case res.StatusCode == http.StatusNotFound:
		if strings.HasPrefix(path, "/agente/trabajos/") {
			return ErrUnknownJob
		}
		return fmt.Errorf("%s: %s", path, errorText(data, "no encontrado"))
	case res.StatusCode >= 400:
		return fmt.Errorf("%s: HTTP %d: %s", path, res.StatusCode, errorText(data, ""))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

func errorText(data []byte, fallback string) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error != "" {
		return clip(e.Error)
	}
	if fallback != "" {
		return fallback
	}
	return clip(string(data))
}

// clip deja el error en una línea y de largo mirable: cuando el que contesta
// es un proxy, el cuerpo es una página de error entera, y volcarla al log no
// dice más que sus primeros 300 caracteres.
func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > errorMax {
		// ToValidUTF8 se come el pedazo de carácter que quedó cortado.
		s = strings.TrimSpace(strings.ToValidUTF8(s[:errorMax], "")) + "…"
	}
	return s
}
