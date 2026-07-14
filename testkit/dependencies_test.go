package testkit

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDepProxyForwardsBindingGatedStreamingPath(t *testing.T) {
	var gotPath, gotAuth, gotRange string
	dep := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotRange = r.Header.Get("Range")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "video-bytes")
	}))
	defer dep.Close()

	sc := &Sidecar{url: dep.URL, token: "storage-token"}
	prefix := "/api/apps/callback/apps/storage/proxy/"
	proxy := httptest.NewServer(newDepProxy(t, sc, prefix))
	defer proxy.Close()

	req, err := http.NewRequest(http.MethodGet, proxy.URL+prefix+"files/5300/content?project_id=prod", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer media-token")
	req.Header.Set("Range", "bytes=0-1023")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusPartialContent || string(body) != "video-bytes" {
		t.Fatalf("response = %d %q", resp.StatusCode, body)
	}
	if gotPath != "/files/5300/content" || gotAuth != "Bearer storage-token" || gotRange != "bytes=0-1023" {
		t.Fatalf("upstream path=%q auth=%q range=%q", gotPath, gotAuth, gotRange)
	}
}

func TestDepCallHandlerForwardsModernCallAppShape(t *testing.T) {
	var gotAuth string
	dep := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var rpc struct {
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
			t.Fatal(err)
		}
		if rpc.Method != "tools/call" || rpc.Params.Name != "files_upload" || rpc.Params.Arguments["name"] != "out.pdf" {
			t.Fatalf("unexpected RPC: %+v", rpc)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
	}))
	defer dep.Close()

	sc := &Sidecar{url: dep.URL, token: "dep-token"}
	proxy := httptest.NewServer(newDepCallHandler(sc))
	defer proxy.Close()

	resp, err := http.Post(proxy.URL, "application/json", strings.NewReader(`{"tool":"files_upload","input":{"name":"out.pdf"}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if gotAuth != "Bearer dep-token" {
		t.Fatalf("authorization = %q", gotAuth)
	}
}
