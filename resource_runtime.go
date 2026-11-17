package sdk

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"sync"
)

// ResourceEnvelope is fenced to one execution. Revision is monotonic within it.
type ResourceEnvelope struct {
	InstallID    string         `json:"install_id"`
	DeploymentID string         `json:"deployment_id"`
	Generation   string         `json:"generation"`
	Revision     int64          `json:"revision"`
	Policy       ResourcePolicy `json:"policy"`
}
type SocketClassStatus struct {
	Failures     uint64             `json:"failures"`
	LastRevision int64              `json:"last_revision"`
	Sockets      uint64             `json:"sockets"`
	Last         SocketPolicyResult `json:"last"`
}
type resourceRuntime struct {
	mu          sync.Mutex
	declaration *ResourceDeclaration
	envelope    ResourceEnvelope
	governor    *Governor
	sockets     map[string]SocketClassStatus
}

func newResourceRuntime(d *ResourceDeclaration, raw string) (*resourceRuntime, error) {
	e := ResourceEnvelope{InstallID: os.Getenv("APTEVA_INSTALL_ID"), DeploymentID: os.Getenv("APTEVA_DEPLOYMENT_ID"), Generation: os.Getenv("APTEVA_DEPLOYMENT_GENERATION")}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			return nil, fmt.Errorf("invalid resource policy envelope: %w", err)
		}
		if e.InstallID != os.Getenv("APTEVA_INSTALL_ID") || e.DeploymentID != os.Getenv("APTEVA_DEPLOYMENT_ID") || e.Generation != os.Getenv("APTEVA_DEPLOYMENT_GENERATION") {
			return nil, fmt.Errorf("resource policy execution mismatch")
		}
	}
	p, err := ResolveResources(d, &e.Policy)
	if err != nil {
		return nil, err
	}
	e.Policy = p
	g, err := NewGovernor(p)
	if err != nil {
		return nil, err
	}
	return &resourceRuntime{declaration: d, envelope: e, governor: g, sockets: map[string]SocketClassStatus{}}, nil
}
func (c *AppCtx) ApplyUDPSocketPolicy(conn *net.UDPConn, class string) SocketPolicyResult {
	if c == nil || c.resources == nil {
		return ApplyUDPSocketPolicy(conn, SocketPolicy{})
	}
	rt := c.resources
	rt.mu.Lock()
	p, ok := rt.envelope.Policy.Network[class]
	revision := rt.envelope.Revision
	rt.mu.Unlock()
	if !ok {
		return SocketPolicyResult{Errors: []string{"undeclared/unconfigured traffic class"}}
	}
	result := ApplyUDPSocketPolicy(conn, p)
	rt.mu.Lock()
	s := rt.sockets[class]
	s.Sockets++
	s.LastRevision = revision
	if len(result.Errors) > 0 {
		s.Failures++
	}
	s.Last = result
	rt.sockets[class] = s
	rt.mu.Unlock()
	return result
}
func (c *AppCtx) ResourceStatus() any {
	if c == nil || c.resources == nil {
		return map[string]any{"supported": false}
	}
	rt := c.resources
	rt.mu.Lock()
	defer rt.mu.Unlock()
	sockets := map[string]SocketClassStatus{}
	for k, v := range rt.sockets {
		sockets[k] = v
	}
	return map[string]any{"contract": ResourcePolicyVersion, "revision": rt.envelope.Revision, "policy": rt.envelope.Policy, "workloads": rt.governor.Snapshot(), "sockets": sockets, "network_apply": "new_sockets", "process_apply": "backend_owned"}
}
func mountResourceRoutes(mux *http.ServeMux, c *AppCtx) {
	mux.HandleFunc("/_apteva/runtime/app/resources", func(w http.ResponseWriter, r *http.Request) {
		// Do not inherit development no-auth or app route exceptions.
		if token := os.Getenv("APTEVA_APP_TOKEN"); token == "" || r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", 401)
			return
		}
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(c.ResourceStatus())
			return
		}
		if r.Method != http.MethodPut {
			http.Error(w, "method not allowed", 405)
			return
		}
		var next ResourceEnvelope
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		dec.DisallowUnknownFields()
		if dec.Decode(&next) != nil || dec.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid resource envelope", 400)
			return
		}
		rt := c.resources
		if rt == nil {
			http.Error(w, "resource controls unavailable", 409)
			return
		}
		p, e := ResolveResources(rt.declaration, &next.Policy)
		if e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		next.Policy = p
		rt.mu.Lock()
		current := rt.envelope
		if !reflect.DeepEqual(next.Policy.Process, current.Policy.Process) {
			rt.mu.Unlock()
			http.Error(w, "process policy requires backend deployment", 409)
			return
		}
		if next.InstallID != current.InstallID || next.DeploymentID != current.DeploymentID || next.Generation != current.Generation || next.Revision < current.Revision || next.Revision < 1 {
			rt.mu.Unlock()
			http.Error(w, "stale execution or revision", 409)
			return
		}
		if next.Revision == current.Revision {
			a, _ := json.Marshal(next)
			b, _ := json.Marshal(current)
			if string(a) != string(b) {
				rt.mu.Unlock()
				http.Error(w, "revision conflict", 409)
				return
			}
		} else {
			if e = rt.governor.Configure(p); e != nil {
				rt.mu.Unlock()
				http.Error(w, e.Error(), 409)
				return
			}
			rt.envelope = next
		}
		rt.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(next.Revision, 10)))
		_ = json.NewEncoder(w).Encode(c.ResourceStatus())
	})
}
