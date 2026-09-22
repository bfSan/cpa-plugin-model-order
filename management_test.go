package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func decodeEnvelope(t *testing.T, raw []byte, errValue error) []byte {
	t.Helper()
	if errValue != nil {
		t.Fatalf("handler error: %v", errValue)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("envelope unreadable: %v (%s)", err, raw)
	}
	if !env.OK {
		t.Fatalf("envelope reports failure: %s", raw)
	}
	return env.Result
}

func TestCatalogRecordsPerPort(t *testing.T) {
	entries := readCatalogEntries([][]byte{
		[]byte(`{"id":"gpt-5.5","owned_by":"openai"}`),
		[]byte(`{"name":"models/qoder-auto"}`),
		[]byte(`{"slug":"workbuddy-hy3","display_name":"Hy3"}`),
	})
	if len(entries) != 3 {
		t.Fatalf("want 3 entries, got %d", len(entries))
	}
	if !entries[2].slugForm {
		t.Fatal("slug shaped entry should be flagged so the codex port is separable")
	}
	if entries[1].ID != "qoder-auto" {
		t.Fatalf("gemini name should lose its resource prefix, got %q", entries[1].ID)
	}
	if got := portFor("openai", entries); got != portCodex {
		t.Fatalf("a body carrying slug entries must be read as the codex catalog, got %q", got)
	}
	if got := portFor("gemini", entries[:2]); got != portGemini {
		t.Fatalf("want gemini, got %q", got)
	}
	if got := portFor("openai", entries[:1]); got != portOpenAI {
		t.Fatalf("want openai, got %q", got)
	}
}

func TestStatusPayloadReportsRuleAndCatalog(t *testing.T) {
	if err := loadConfig([]byte("strategy: grouped\norder:\n  - \"gpt-*\"\n")); err != nil {
		t.Fatalf("config load failed: %v", err)
	}
	t.Cleanup(func() { _ = loadConfig(nil) })

	catalog.record(portOpenAI, []catalogEntry{{ID: "gpt-5.5", OwnedBy: "openai"}})
	payload := statusPayload()
	if payload["strategy"] != StrategyGrouped {
		t.Fatalf("unexpected strategy %v", payload["strategy"])
	}
	if payload["from_config"] != true {
		t.Fatal("an order taken from config must be reported as configured")
	}
	ordered, ok := payload["order"].([]string)
	if !ok || len(ordered) != 1 || ordered[0] != "gpt-*" {
		t.Fatalf("unexpected order %v", payload["order"])
	}
	if defaults, ok := payload["default_order"].([]string); !ok || len(defaults) == 0 {
		t.Fatalf("default_order must be exposed for the reset button, got %v", payload["default_order"])
	}
	if catalogs, ok := payload["catalogs"].([]catalogSnapshot); !ok || len(catalogs) == 0 {
		t.Fatalf("catalogs missing: %v", payload["catalogs"])
	}
}

// unwrapMgmt drills through the RPC envelope and the base64 body that
// pluginapi.ManagementResponse carries, and decodes the inner JSON payload.
func unwrapMgmt(t *testing.T, raw []byte, errValue error, target any) pluginapi.ManagementResponse {
	t.Helper()
	result := decodeEnvelope(t, raw, errValue)
	var resp pluginapi.ManagementResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("management response unreadable: %v (%s)", err, result)
	}
	if target != nil {
		if err := json.Unmarshal(resp.Body, target); err != nil {
			t.Fatalf("management body unreadable: %v (%s)", err, resp.Body)
		}
	}
	return resp
}

func TestPreviewUsesExplicitIDs(t *testing.T) {
	if err := loadConfig(nil); err != nil {
		t.Fatalf("defaults must load cleanly: %v", err)
	}
	body, _ := json.Marshal(previewRequest{
		Strategy: StrategyGrouped,
		Order:    []string{"qoder-*", "gpt-*"},
		IDs:      []string{"workbuddy-hy3", "gpt-5.5", "qoder-auto"},
	})
	raw, errHandle := handlePreview(body)
	var out struct {
		Ordered []string          `json:"ordered"`
		Matched map[string]string `json:"matched"`
	}
	unwrapMgmt(t, raw, errHandle, &out)
	want := []string{"qoder-auto", "gpt-5.5", "workbuddy-hy3"}
	if strings.Join(out.Ordered, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v want %v", out.Ordered, want)
	}
	if out.Matched["workbuddy-hy3"] != "" {
		t.Fatalf("unmatched model should report no rule, got %q", out.Matched["workbuddy-hy3"])
	}
	if out.Matched["gpt-5.5"] != "gpt-*" {
		t.Fatalf("want rule gpt-*, got %q", out.Matched["gpt-5.5"])
	}
}

func TestPreviewRejectsBadBody(t *testing.T) {
	raw, errHandle := handlePreview([]byte(`{not json`))
	if errHandle != nil {
		t.Fatalf("a malformed body must answer, not fail the call: %v", errHandle)
	}
	var out struct {
		Error string `json:"error"`
	}
	resp := unwrapMgmt(t, raw, nil, &out)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
	if out.Error != "invalid_body" {
		t.Fatalf("want invalid_body, got %q", out.Error)
	}
}

func TestHandleManagementServesPanelAnd404sUnknown(t *testing.T) {
	setManagementBasePath("/v0/management")
	setResourceBasePath("/v0/resource/plugins/model-order")

	raw, errHandle := handleManagement(marshalWire(t, http.MethodGet, "/v0/resource/plugins/model-order/panel", nil))
	page := unwrapMgmt(t, raw, errHandle, nil)
	html := string(page.Body)
	if page.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", page.StatusCode)
	}
	if !strings.Contains(html, "<title>") {
		t.Fatal("panel did not render HTML")
	}
	// The placeholder must be replaced by the host supplied management prefix,
	// otherwise every fetch from the page would 404.
	if strings.Contains(html, "__MO_MANAGEMENT_BASE_PATH_JSON__") {
		t.Fatal("management base path placeholder was not injected")
	}
	// The page sets MANAGEMENT_BASE_PATH from the host supplied prefix, quoted as
	// a JS string literal. Checking both halves keeps the assertion insensitive to
	// how the template happens to space the assignment.
	if !strings.Contains(html, "const MANAGEMENT_BASE_PATH") || !strings.Contains(html, `"/v0/management"`) {
		t.Fatalf("management base path was not stamped into the panel")
	}

	raw, errHandle = handleManagement(marshalWire(t, http.MethodGet, "/v0/management/plugins/model-order/nope", nil))
	missing := unwrapMgmt(t, raw, errHandle, nil)
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown path should 404, got %d", missing.StatusCode)
	}
}

func marshalWire(t *testing.T, method, path string, body []byte) []byte {
	t.Helper()
	raw, err := json.Marshal(managementRequestWire{
		ManagementRequest: pluginapi.ManagementRequest{Method: method, Path: path, Body: body},
	})
	if err != nil {
		t.Fatalf("wire marshal failed: %v", err)
	}
	return raw
}
