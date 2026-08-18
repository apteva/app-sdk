package sdk

// telemetry.go — the optional live-telemetry surface.
//
// Apps holding platform.telemetry.read may subscribe to the platform's
// ephemeral telemetry stream (GET /api/apps/callback/telemetry) for
// agents their installing user owns. The stream carries what the
// broadcaster sees in flight — llm.tool_chunk token deltas, tool calls,
// thoughts — filtered server-side by event type and thread prefix.
//
// Ephemeral on purpose: no replay, no cursor. A reconnect starts fresh,
// which is semantically correct for token deltas — stale fragments are
// worthless. Durable facts (the final message) always travel their own
// durable paths.
//
// TelemetryClient is a separate optional interface, not PlatformClient
// methods: adding methods to PlatformClient ripples through every test
// stub in every app. Type-assert to detect support:
//
//	if tc, ok := ctx.PlatformAPI().(sdk.TelemetryClient); ok { … }

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// TelemetrySubscription filters the stream server-side. Empty fields
// mean "no filter on that axis" — but subscribing with no Events filter
// is almost always a mistake (token deltas dominate the firehose), so
// name what you need.
type TelemetrySubscription struct {
	// Events — telemetry event types to receive, e.g. "llm.tool_chunk".
	Events []string
	// ThreadPrefix — only events whose thread id starts with this
	// prefix, e.g. "chat-" for conversation threads. Applied
	// server-side so unrelated threads' content never crosses the wire.
	ThreadPrefix string
	// AgentID — restrict to one agent. Zero = every agent the
	// installing user owns (ownership is enforced server-side either
	// way).
	AgentID int64
}

// TelemetryStreamEvent mirrors the platform's telemetry event shape.
type TelemetryStreamEvent struct {
	ID       string          `json:"id"`
	AgentID  int64           `json:"instance_id"`
	ThreadID string          `json:"thread_id"`
	Type     string          `json:"type"`
	Time     time.Time       `json:"time"`
	Data     json.RawMessage `json:"data"`
}

// TelemetryClient is the optional subscription surface.
type TelemetryClient interface {
	// SubscribeTelemetry opens the filtered stream. The channel closes
	// when ctx is cancelled or the platform refuses the subscription
	// permanently (missing permission, unknown endpoint). Transient
	// drops reconnect internally with backoff — consumers just range
	// over the channel.
	SubscribeTelemetry(ctx context.Context, sub TelemetrySubscription) (<-chan TelemetryStreamEvent, error)
}

// errTelemetryUnsupported marks permanent refusals (403 permission,
// 404 pre-bridge server) that must stop the reconnect loop — retrying
// a missing permission forever would just spam the platform.
var errTelemetryUnsupported = errors.New("telemetry subscription unsupported")

func (c *httpPlatformClient) SubscribeTelemetry(ctx context.Context, sub TelemetrySubscription) (<-chan TelemetryStreamEvent, error) {
	// Fail fast on a permanent refusal so callers can degrade at
	// subscribe time instead of consuming an instantly-closed channel.
	if err := c.probeTelemetry(ctx, sub); err != nil {
		return nil, err
	}

	out := make(chan TelemetryStreamEvent, 256)
	go func() {
		defer close(out)
		backoff := time.Second
		for {
			err := c.streamTelemetryOnce(ctx, sub, out)
			switch {
			case ctx.Err() != nil:
				return
			case errors.Is(err, errTelemetryUnsupported):
				// The platform changed its mind mid-run (permission
				// revoked, downgrade). Stop for good.
				return
			}
			// Transient drop — reconnect fresh. No cursor: ephemera.
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
		}
	}()
	return out, nil
}

func (c *httpPlatformClient) telemetryURL(sub TelemetrySubscription) string {
	q := url.Values{}
	if len(sub.Events) > 0 {
		q.Set("events", strings.Join(sub.Events, ","))
	}
	if sub.ThreadPrefix != "" {
		q.Set("thread_prefix", sub.ThreadPrefix)
	}
	if sub.AgentID != 0 {
		q.Set("agent_id", fmt.Sprint(sub.AgentID))
	}
	u := c.baseURL + "/api/apps/callback/telemetry"
	if encoded := q.Encode(); encoded != "" {
		u += "?" + encoded
	}
	return u
}

// probeTelemetry performs one connection attempt and classifies the
// refusal. On success the response body is closed immediately — the
// real stream is opened by streamTelemetryOnce.
func (c *httpPlatformClient) probeTelemetry(ctx context.Context, sub TelemetrySubscription) error {
	resp, err := c.openTelemetry(ctx, sub)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (c *httpPlatformClient) openTelemetry(ctx context.Context, sub TelemetrySubscription) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.telemetryURL(sub), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.streamClient.Do(req)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return resp, nil
	case http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed:
		resp.Body.Close()
		return nil, fmt.Errorf("%w: HTTP %d (grant platform.telemetry.read, or the platform predates the telemetry bridge)",
			errTelemetryUnsupported, resp.StatusCode)
	default:
		resp.Body.Close()
		return nil, fmt.Errorf("telemetry subscribe: HTTP %d", resp.StatusCode)
	}
}

func (c *httpPlatformClient) streamTelemetryOnce(ctx context.Context, sub TelemetrySubscription, out chan TelemetryStreamEvent) error {
	resp, err := c.openTelemetry(ctx, sub)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue // heartbeats, comments, event separators
		}
		var ev TelemetryStreamEvent
		if err := json.Unmarshal(bytes.TrimPrefix(line, []byte("data: ")), &ev); err != nil {
			continue
		}
		select {
		case out <- ev:
		case <-ctx.Done():
			return ctx.Err()
		default:
			// Consumer is behind on an ephemeral stream: dropping the
			// oldest pending frame beats blocking the read loop.
			select {
			case <-out:
			default:
			}
			select {
			case out <- ev:
			default:
			}
		}
	}
	return scanner.Err()
}

// projectScopedClient forwards the optional surface, same pattern as
// ThreadClient — a project-scoped app keeps the capability when the
// inner client has it.
func (p *projectScopedClient) SubscribeTelemetry(ctx context.Context, sub TelemetrySubscription) (<-chan TelemetryStreamEvent, error) {
	client, ok := p.inner.(TelemetryClient)
	if !ok {
		return nil, errors.New("telemetry API unavailable")
	}
	return client.SubscribeTelemetry(ctx, sub)
}
