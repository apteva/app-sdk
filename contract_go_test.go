package sdk_test

import (
	"context"
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/app-sdk/contract"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// Verify checker failures as well as success; never accept a healthy but
// unauthenticated server as an Apteva-compatible app.
func TestAppContractRejectsAuthenticationBypass(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ok":true}`)) }))
	defer s.Close()
	if err := contract.Check(context.Background(), s.URL, "test", contract.BasicFixtures()); err == nil {
		t.Fatal("accepted missing auth")
	}
}
func TestLanguageNeutralManifestSemanticFixtures(t *testing.T) {
	raw, e := os.ReadFile("rust/examples/hello/apteva.yaml")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = sdk.ParseManifest(raw); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"unknown field", func(m map[string]any) { m["language_typo"] = "rust" }},
		{"unsupported schema", func(m map[string]any) { m["schema"] = "other/v1" }},
		{"invalid strategy", func(m map[string]any) { m["runtime"].(map[string]any)["activation_strategy"] = "guess" }},
		{"invalid digest", func(m map[string]any) {
			m["runtime"].(map[string]any)["artifacts"] = map[string]any{"linux-amd64": map[string]any{"url": "https://example.invalid/a.tgz", "sha256": "bad"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m map[string]any
			json.Unmarshal(raw, &m)
			tc.change(m)
			b, _ := json.Marshal(m)
			if _, e := sdk.ParseManifest(b); e == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
}
