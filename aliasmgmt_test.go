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

// The panel carries the alias editor, so a regression that dropped the section or
// the write path would otherwise only show up in a browser.
func TestPanelCarriesTheAliasSection(t *testing.T) {
	setManagementBasePath("/v0/management")
	setResourceBasePath("/v0/resource/plugins/model-registry")

	raw, errHandle := handleManagement(marshalWire(t, http.MethodGet, "/v0/resource/plugins/model-registry/panel", nil))
	page := unwrapMgmt(t, raw, errHandle, nil)
	html := string(page.Body)
	for _, needle := range []string{
		`id="aliasTable"`,
		`id="aliasChannels"`,
		// Load, save and revert. The editor has to be able to read the table back
		// as well as write it, or an operator cannot tell what CPA actually holds.
		`id="btnAliasLoad"`,
		`id="btnAliasSave"`,
		`id="btnAliasRevert"`,
		// Free-form editing: add a row, delete a row, and channels themselves.
		`id="btnAliasAdd"`,
		`id="aliasNewName"`,
		`id="aliasNewValue"`,
		`newChannel`,
		// The write path must go to CPA's own alias endpoint, not anywhere else.
		`/oauth-model-alias"`,
		`method: "PATCH"`,
		`channel: ch,`,
	} {
		if !strings.Contains(html, needle) {
			t.Errorf("panel is missing %q", needle)
		}
	}
	// PUT replaces the whole table across every channel, which is the one call
	// here that would destroy the other providers' aliases.
	if strings.Contains(html, `ALIAS_PATH, {
    method: "PUT"`) {
		t.Error("the panel must never PUT the alias table")
	}
	// Only channels the operator actually changed may be written: PATCH replaces
	// the whole channel, so a needless write could clobber rows added by someone
	// else in the meantime.
	if !strings.Contains(html, "aliasDirtyChannels") {
		t.Error("the panel must track which channels were changed")
	}
}

// The editor is a full CRUD surface now, not a one-shot "fill the gaps" list, so
// the section must not be presented as read-only.
func TestAliasSectionIsEditableNotReadOnly(t *testing.T) {
	setManagementBasePath("/v0/management")
	setResourceBasePath("/v0/resource/plugins/model-registry")

	raw, errHandle := handleManagement(marshalWire(t, http.MethodGet, "/v0/resource/plugins/model-registry/panel", nil))
	page := unwrapMgmt(t, raw, errHandle, nil)
	html := string(page.Body)
	// The stale "missing aliases only" framing must be gone.
	for _, stale := range []string{`id="btnAliasScan"`, `id="btnAliasApply"`, "缺失的模型别名"} {
		if strings.Contains(html, stale) {
			t.Errorf("panel still carries the read-only wording %q", stale)
		}
	}
	// Editing goes through a draft rather than the loaded table, so that the save
	// button can tell "nothing changed" from "changed and reverted".
	for _, needle := range []string{"let aliasTable", "let aliasDraft", "function aliasIsDirty", "function aliasTouched"} {
		if !strings.Contains(html, needle) {
			t.Errorf("panel is missing %q", needle)
		}
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

// --- regressions from the live deployment (2026-10-06) ---------------------
//
// Three defects were reported against this panel. Each is pinned here at the
// source level so a plain `go test` catches a regression without a browser; the
// playwright suite (scripts/alias-editor-test.js) covers the same ground
// behaviourally. The comments record the user-visible symptom, because that is
// what a future reader will be matching against a bug report.

// Bug 3: saving must not strip row fields other than name/alias.
//
// Symptom: "我之前新增一个上游不存在的模型名，过一会又消失了" and, worse, every
// alias in the channel silently losing force-mapping after any save. CPA stores
// force-mapping:true on each row; an editor that rebuilt rows as {name, alias}
// dropped it, and because PATCH replaces the whole channel the loss was written
// straight to config.yaml. Measured effect of losing it: qoder-deepseek-v4.1-flash
// immediately answered 503 auth_unavailable while qoder-auto still worked.
func TestAliasEditorPreservesNonEditableRowFields(t *testing.T) {
	html := renderPanel()
	// Rows are cloned wholesale, never rebuilt from two fields.
	for _, needle := range []string{
		"cloneAliasRow",
		"cloneAliasTable",
		"ALIAS_EDITABLE_KEYS",
		"aliasPayloadRows",
	} {
		if !strings.Contains(html, needle) {
			t.Errorf("panel is missing %q, which is how non-editable row fields survive a save", needle)
		}
	}
	// The exact shape that dropped force-mapping must be gone from the write path.
	if strings.Contains(html, "aliases: aliasDraft[ch].map(r => ({ name: r.name, alias: r.alias }))") {
		t.Error("the save path rebuilds rows as {name, alias}, which strips force-mapping")
	}
	// And the comparison must look at whole rows, or a field-only change is
	// invisible and the save can be skipped or mis-scoped.
	if !strings.Contains(html, "normalizeAliasRowForCompare") {
		t.Error("dirty checking must compare whole rows, not just name+alias")
	}
}

// Bug 1: an empty channel must never be sent as an empty PATCH.
//
// Symptom: "插件自定义新增渠道没用，提示渠道不存在". CPA answers an empty aliases
// array with 404 {"error":"channel not found"} — it has no representation for a
// nameless channel, so creating one and saving immediately always failed with a
// message that reads like the channel does not exist (it does not, yet).
func TestAliasEditorNeverPatchesAnEmptyChannel(t *testing.T) {
	html := renderPanel()
	for _, needle := range []string{
		"aliasEmptyChannels",
		"deleteAliasChannel",
	} {
		if !strings.Contains(html, needle) {
			t.Errorf("panel is missing %q, which is how empty channels are handled", needle)
		}
	}
	// DELETE is the only accepted way to retire a channel, so the editor needs it.
	if !strings.Contains(html, `method: "DELETE"`) {
		t.Error("the panel must DELETE a channel it empties; CPA rejects an empty PATCH")
	}
	// The guard has to sit on the write loop itself: collecting the empty set
	// without filtering the PATCH loop would still send the losing request.
	if !strings.Contains(html, "for (const ch of writable)") {
		t.Error("the PATCH loop must run over the non-empty channels only")
	}
}

// Bug 2: a newly created channel must render, not throw.
//
// Symptom: "自定义渠道之后，点击添加，列表页面不会实时展示". newChannel() writes
// only aliasDraft, so aliasTable[newChannel] was undefined and the row loop's
// .find() on it threw TypeError. The throw aborted renderAlias mid-DOM-build:
// the table stayed empty and — because nothing caught it — no error was shown
// at all. Verified against the pre-fix page in a real browser: the error is
// "TypeError: Cannot read properties of undefined (reading 'find')".
func TestAliasEditorRendersNewChannelsWithoutThrowing(t *testing.T) {
	html := renderPanel()
	// The baseline for a channel that does not exist on the wire yet is a safe
	// empty list, so the row loop can always run.
	if strings.Contains(html, "aliasTable[aliasChannel].find(") {
		t.Error("renderAlias indexes aliasTable[aliasChannel] directly; a new channel is undefined there")
	}
	if !strings.Contains(html, "const baselineRows = (aliasTable && aliasTable[aliasChannel]) || [];") {
		t.Error("renderAlias must default the baseline to an empty list")
	}
	// Belt and braces: rendering is wrapped so a future throw becomes a visible
	// message instead of a silent no-op.
	if !strings.Contains(html, "renderAliasSafely") {
		t.Error("renderAlias must be called through a wrapper that surfaces failures")
	}
	// Every call site goes through the wrapper (the definition and the wrapper's
	// own internal call are the only bare references allowed).
	bare := strings.Count(html, "renderAlias();")
	if bare != 1 {
		t.Errorf("bare renderAlias() call sites = %d, want exactly 1 (inside renderAliasSafely)", bare)
	}
}
