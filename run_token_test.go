package sdk

import "testing"

func TestAppOutboundTokenDefaultsAndOverride(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "inbound-token")
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "")
	if got := appOutboundToken(); got != "inbound-token" {
		t.Fatalf("default token = %q", got)
	}
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "outbound-token")
	if got := appOutboundToken(); got != "outbound-token" {
		t.Fatalf("override token = %q", got)
	}
}
