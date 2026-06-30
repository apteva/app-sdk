package sdk

import "testing"

func TestRequiredAppRefParsesEvents(t *testing.T) {
	m, err := ParseManifest([]byte(`
schema: apteva-app/v1
name: hosting
display_name: Hosting
version: 1.0.0
requires:
  apps:
    - name: billing
      optional: true
      events:
        - invoice.paid
        - invoice.failed
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
  port: 8080
`))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if len(m.Requires.Apps) != 1 {
		t.Fatalf("apps len=%d, want 1", len(m.Requires.Apps))
	}
	got := m.Requires.Apps[0].Events
	if len(got) != 2 || got[0] != "invoice.paid" || got[1] != "invoice.failed" {
		t.Fatalf("events=%v", got)
	}
}

func TestEventNamePrefersEventAlias(t *testing.T) {
	ev := Event{Event: "invoice.paid", Topic: "legacy.topic"}
	if got := ev.Name(); got != "invoice.paid" {
		t.Fatalf("Name()=%q", got)
	}
	ev = Event{Topic: "legacy.topic"}
	if got := ev.Name(); got != "legacy.topic" {
		t.Fatalf("Name()=%q", got)
	}
}
