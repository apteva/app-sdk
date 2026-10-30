package sdk

import (
	"net/http"
	"strings"
	"testing"
)

func TestExportContractNames(t *testing.T) {
	for _, contract := range []string{SummaryExportContract, EntitiesExportContract, "acme.orders/v2", "com.acme.orders/v10", "my-app.open-tickets/v1"} {
		if !exportContractPattern.MatchString(contract) {
			t.Errorf("%q rejected", contract)
		}
	}
	for _, contract := range []string{"", "summary/v1", "apteva.summary", "apteva.summary/v0", "apteva.summary/V1", "Apteva.summary/v1", "apteva..summary/v1", "apteva.summary/v1/extra", ".summary/v1", "apteva.summary./v1"} {
		if exportContractPattern.MatchString(contract) {
			t.Errorf("%q accepted", contract)
		}
	}
}

// The documented example must stay valid: built-in contracts once failed the
// SDK's own pattern, so no app could declare or discover them.
func TestManifestAcceptsBuiltInExportContracts(t *testing.T) {
	manifest, err := ParseManifest([]byte(`
schema: apteva-app/v1
name: crm
version: 1.0.0
provides:
  http_routes:
    - prefix: /exports/
  permissions:
    - name: crm.read
  publishes:
    - name: ticket.updated
  exports:
    - id: overview
      contract: apteva.summary/v1
      label: CRM overview
      endpoint: /exports/overview
      permissions: [crm.read]
      changes_on: [ticket.updated]
    - id: tickets
      contract: apteva.entities/v1
      label: Tickets
      endpoint: /exports/tickets
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := manifest.Provides.Exports; len(got) != 2 || got[0].Contract != SummaryExportContract || got[1].Contract != EntitiesExportContract {
		t.Fatalf("exports=%+v", got)
	}

	_, err = ParseManifest([]byte(`
schema: apteva-app/v1
name: crm
version: 1.0.0
provides:
  http_routes:
    - prefix: /exports/
  exports:
    - id: overview
      contract: summary/v1
      label: CRM overview
      endpoint: /exports/overview
`))
	if err == nil || !strings.Contains(err.Error(), "namespace.name/vN") {
		t.Fatalf("err=%v", err)
	}
}

func TestBuiltInContractsWorkForRoutesAndSelectors(t *testing.T) {
	for _, contract := range []string{SummaryExportContract, EntitiesExportContract} {
		route, err := AppExportRoute(AppExport{ID: "x", Contract: contract, Label: "X", Endpoint: "/exports/x"}, func(*http.Request) (any, error) { return nil, nil })
		if err != nil || route.Method != http.MethodGet {
			t.Fatalf("%s: route=%+v err=%v", contract, route, err)
		}
		if err := validateContributionSelector(ContributionKindExport, contract, "", ""); err != nil {
			t.Fatalf("%s: %v", contract, err)
		}
	}
}
