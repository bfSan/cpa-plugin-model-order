package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestAliasReportRouteServesTheOpenAIPort(t *testing.T) {
	setManagementBasePath("/v0/management")
	setResourceBasePath("/v0/resource/plugins/model-registry")
	catalog.reset()

	// Capture a listing the way real traffic would, then ask for the report.
	loadPolicyConfig(t, "strategy: name\n")
	headers := http.Header{"Authorization": {"Bearer any-key"}}
	// Alphabetical, so the "name" strategy reproduces it and nothing changes.
	body := ownedListingBody([][2]string{
		{"anthropic/claude-opus-5.5", "cline"},
		{"hy4-preview-x", "workbuddy"},
		{"qoder-auto", "qoder"},
		{"space-bunny", "workbuddy"},
	})
	if _, changed := governBody(portOpenAI, headers, body); changed {
		t.Fatal("fixture should already be in the configured order")
	}

	reportBody, errMarshal := json.Marshal(map[string]any{"channels": []string{"qoder", "workbuddy"}})
	if errMarshal != nil {
		t.Fatalf("marshal: %v", errMarshal)
	}
	raw, errHandle := handleManagement(marshalWire(t, http.MethodPost, "/v0/management/plugins/model-registry/alias-report", reportBody))
	var payload struct {
		Reports []aliasReport `json:"reports"`
	}
	resp := unwrapMgmt(t, raw, errHandle, &payload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if len(payload.Reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(payload.Reports))
	}
	report := payload.Reports[0]
	if report.Port != portOpenAI {
		t.Errorf("port = %q, want %q", report.Port, portOpenAI)
	}
	if len(report.Missing) != 2 {
		t.Fatalf("missing = %+v, want the two bare workbuddy names", report.Missing)
	}
	for _, row := range report.Missing {
		if row.Channel != "workbuddy" {
			t.Errorf("channel = %q, want workbuddy", row.Channel)
		}
		if !strings.HasPrefix(row.Alias, "workbuddy-") {
			t.Errorf("alias %q is not provider prefixed", row.Alias)
		}
	}
	if report.Ignored != 2 {
		t.Errorf("ignored = %d, want 2 (one prefixed, one pass-through)", report.Ignored)
	}
}

// With no traffic captured the route must say so, rather than return an empty
// report that reads like "no missing aliases found".
func TestAliasReportRouteSaysWhenNothingCaptured(t *testing.T) {
	setManagementBasePath("/v0/management")
	setResourceBasePath("/v0/resource/plugins/model-registry")
	catalog.reset()

	raw, errHandle := handleManagement(marshalWire(t, http.MethodGet, "/v0/management/plugins/model-registry/alias-report", nil))
	var payload struct {
		Error   string        `json:"error"`
		Reports []aliasReport `json:"reports"`
	}
	resp := unwrapMgmt(t, raw, errHandle, &payload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if payload.Error != "no_listing_captured" {
		t.Fatalf("error = %q, want no_listing_captured", payload.Error)
	}
	if len(payload.Reports) != 0 {
		t.Errorf("reports should be empty, got %+v", payload.Reports)
	}
}

func TestStatusRouteReportsPoliciesByScope(t *testing.T) {
	setManagementBasePath("/v0/management")
	setResourceBasePath("/v0/resource/plugins/model-registry")
	loadPolicyConfig(t, `
strategy: name
access:
  - caller_scope: `+workbuddyKeyScope+`
    label: workbuddy key
    allow_models: ["workbuddy-*"]
`)

	raw, errHandle := handleManagement(marshalWire(t, http.MethodGet, "/v0/management/plugins/model-registry/status", nil))
	var payload struct {
		PolicyCount int `json:"policy_count"`
		Policies    []struct {
			CallerScope string   `json:"caller_scope"`
			Label       string   `json:"label"`
			AllowModels []string `json:"allow_models"`
		} `json:"policies"`
	}
	resp := unwrapMgmt(t, raw, errHandle, &payload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if payload.PolicyCount != 1 {
		t.Fatalf("policy_count = %d, want 1", payload.PolicyCount)
	}
	if len(payload.Policies) != 1 {
		t.Fatalf("policies = %d, want 1", len(payload.Policies))
	}
	got := payload.Policies[0]
	if got.CallerScope != workbuddyKeyScope {
		t.Errorf("caller_scope = %q, want %q", got.CallerScope, workbuddyKeyScope)
	}
	if got.Label != "workbuddy key" {
		t.Errorf("label = %q, want %q", got.Label, "workbuddy key")
	}
	if len(got.AllowModels) != 1 || got.AllowModels[0] != "workbuddy-*" {
		t.Errorf("allow_models = %v, want [workbuddy-*]", got.AllowModels)
	}
}

// The panel carries the alias UI, so a regression that dropped the section or
// the write path would otherwise only show up in a browser.
func TestPanelCarriesTheAliasSection(t *testing.T) {
	setManagementBasePath("/v0/management")
	setResourceBasePath("/v0/resource/plugins/model-registry")

	raw, errHandle := handleManagement(marshalWire(t, http.MethodGet, "/v0/resource/plugins/model-registry/panel", nil))
	page := unwrapMgmt(t, raw, errHandle, nil)
	html := string(page.Body)
	for _, needle := range []string{
		`id="aliasTable"`,
		`id="btnAliasScan"`,
		`id="btnAliasApply"`,
		// The write path must go to CPA's own alias endpoint, not anywhere else.
		`/oauth-model-alias"`,
		`method: "PATCH"`,
		`{ channel, aliases: existing }`,
	} {
		if !strings.Contains(html, needle) {
			t.Errorf("panel is missing %q", needle)
		}
	}
	// PUT replaces the whole table across every channel, which is the one call
	// here that would destroy the other providers' aliases.
	if strings.Contains(html, `ALIAS_PATH, {\n    method: "PUT"`) {
		t.Error("the panel must never PUT the alias table")
	}
}

func TestAliasRouteIsRegistered(t *testing.T) {
	registration := managementRegistration()
	var paths []string
	for _, route := range registration.Routes {
		paths = append(paths, route.Method+" "+route.Path)
	}
	joined := strings.Join(paths, "\n")
	if !strings.Contains(joined, "GET /plugins/model-registry/alias-report") {
		t.Errorf("alias-report is not registered: %s", joined)
	}
	if !strings.Contains(joined, "GET /plugins/model-registry/status") {
		t.Errorf("status is not registered: %s", joined)
	}
}

// The status route's policy list is sorted by scope so the panel renders in a
// stable order across reloads.
func TestStatusPoliciesAreSorted(t *testing.T) {
	setManagementBasePath("/v0/management")
	setResourceBasePath("/v0/resource/plugins/model-registry")
	loadPolicyConfig(t, `
strategy: name
access:
  - caller_scope: fff
  - caller_scope: aaa
  - caller_scope: mmm
`)
	payload := statusPayload()
	policies, ok := payload["policies"].([]map[string]any)
	if !ok {
		t.Fatalf("policies has type %T, want []map[string]any", payload["policies"])
	}
	var order []string
	for _, p := range policies {
		scope, _ := p["caller_scope"].(string)
		order = append(order, scope)
	}
	want := []string{"aaa", "fff", "mmm"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

var _ = pluginapi.ManagementRequest{}
