package sdk

// HTTP-based Emitter — POSTs to apteva-server's
// /api/app-events/internal/emit using the sidecar's APTEVA_APP_TOKEN.
//
// Fire-and-forget by design. The platform's bus is in-memory only;
// a missed event is recovered by the dashboard reconnecting with
// since=<lastSeq>, and the app's own DB is the durable source. So
// if the publish fails (network blip, server restarting), losing
// the UI fanout is the right trade vs. blocking the caller.
//
// Each call runs in its own goroutine with a hard 100ms timeout so
// pathological cases (server hung, DNS slow) never stretch a tool-
// call response.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const emitTimeout = 100 * time.Millisecond

type httpEmitter struct {
	gatewayURL string
	token      string
	logger     Logger
	client     *http.Client
}

func newHTTPEmitter(gatewayURL, token string, logger Logger) *httpEmitter {
	if logger == nil {
		logger = silentLogger{}
	}
	gatewayURL = strings.TrimSuffix(gatewayURL, "/")
	return &httpEmitter{
		gatewayURL: gatewayURL,
		token:      token,
		logger:     logger,
		client: &http.Client{
			Timeout: emitTimeout,
		},
	}
}

func (e *httpEmitter) Emit(topic string, data any) {
	// Skip silently when not configured — tests, manifests with no
	// platform, dev runs against a stubbed harness.
	if e == nil || e.gatewayURL == "" || e.token == "" {
		return
	}
	if strings.TrimSpace(topic) == "" {
		return
	}
	go e.send(topic, data)
}

func (e *httpEmitter) send(topic string, data any) {
	body := struct {
		Topic string `json:"topic"`
		Data  any    `json:"data,omitempty"`
	}{Topic: topic, Data: data}
	buf, err := json.Marshal(body)
	if err != nil {
		e.logger.Warn("emit: marshal failed", "topic", topic, "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), emitTimeout)
	defer cancel()
	url := e.gatewayURL + "/api/app-events/internal/emit"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(buf))
	if err != nil {
		e.logger.Warn("emit: build request failed", "topic", topic, "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.token)
	resp, err := e.client.Do(req)
	if err != nil {
		// Logged at debug-ish level so a flaky platform doesn't spam
		// the sidecar's stderr; the dashboard recovers via reconnect.
		e.logger.Warn("emit: post failed", "topic", topic, "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		e.logger.Warn("emit: non-2xx", "topic", topic, "status", resp.StatusCode)
	}
}
