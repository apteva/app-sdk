package sdk

import (
	"encoding/json"
	"testing"
)

func TestRuntimeIngressValidationAndJSON(t *testing.T) {
	good := &RuntimeIngress{Routes: []RuntimeIngressRoute{{Path: "/runtime-browser/*"}, {Path: "/webhook/*"}}, Endpoints: map[string]string{"browser": "", "callbacks": ""}, BrowserPath: "/runtime-browser"}
	if e := ValidateRuntimeIngress(good); e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{"/", "/*", "/_apteva/runtime/app/activate", "/mcp", "/a/../b", "/a/%2e%2e/b", "/x\n}"} {
		bad := *good
		bad.Routes = []RuntimeIngressRoute{{Path: p}}
		if ValidateRuntimeIngress(&bad) == nil {
			t.Errorf("unsafe public route %q", p)
		}
	}
	raw, _ := json.Marshal(good)
	var out RuntimeIngress
	if e := json.Unmarshal(raw, &out); e != nil || out.BrowserPath != good.BrowserPath {
		t.Fatal(e)
	}
}
