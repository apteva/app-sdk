package manifestschema

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestPublishedSchemaCurrent(t *testing.T) {
	want, e := json.MarshalIndent(Generate(), "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	got, e := os.ReadFile("../../schemas/apteva-app-v1.schema.json")
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(bytes.TrimSpace(got), want) {
		t.Fatal("regenerate: go run ./cmd/apteva-app-contract schema > schemas/apteva-app-v1.schema.json")
	}
}
