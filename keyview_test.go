package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The per-key listing view. The plugin already filters listings per caller; what
// these tests pin is the editing contract around it: an operator has to be able
// to SEE what one key receives, and a model already denied has to remain
// editable, or the deny could never be undone.

// scopedPreviewOut is the wire shape the panel reads.
type scopedPreviewOut struct {
	Port    string            `json:"port"`
	Ordered []string          `json:"ordered"`
	Matched map[string]string `json:"matched"`
	Denied  []string          `json:"denied"`
	Scoped  bool              `json:"scoped"`
}

func runScopedPreview(t *testing.T, req previewRequest) scopedPreviewOut {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal preview request: %v", err)
	}
	raw, errHandle := handlePreview(body)
	var out scopedPreviewOut
	unwrapMgmt(t, raw, errHandle, &out)
	return out
}

// previewFeeder records the same listing through both stores, mirroring what
// governBody does on a real /v1/models response.
func previewFeeder(t *testing.T, port string, ids []string) {
	t.Helper()
	entries := readCatalogEntries(parseElements(t, ids))
	fullCatalog.record(port, entries)
	catalog.record(port, entries)
	t.Cleanup(func() {
		catalog.reset()
		fullCatalog.reset()
	})
}

// An empty scope is the unrestricted view: every model is listed and nothing is
// marked denied. This is what "no key selected" must show.
func TestScopedPreviewWithoutScopeListsEverything(t *testing.T) {
	loadPolicyConfig(t, `
strategy: name
access:
  - caller_scope: `+workbuddyKeyScope+`
    deny_models: ["gpt-*"]
`)
	previewFeeder(t, portOpenAI, []string{"workbuddy-auto", "gpt-5.5", "qoder-auto"})

	out := runScopedPreview(t, previewRequest{Port: portOpenAI})
	if out.Scoped {
		t.Error("a preview without a scope must not report itself as scoped")
	}
	if len(out.Denied) != 0 {
		t.Errorf("nothing is denied without a key selected, got %v", out.Denied)
	}
	if len(out.Ordered) != 3 {
		t.Errorf("ordered = %v, want all three models", out.Ordered)
	}
}

// A denied model stays in `ordered` and is reported in `denied`. Dropping it
// would remove the row the operator needs in order to restore it.
func TestScopedPreviewKeepsDeniedModelsListed(t *testing.T) {
	loadPolicyConfig(t, `
strategy: name
access:
  - caller_scope: `+workbuddyKeyScope+`
    deny_models: ["gpt-*"]
`)
	previewFeeder(t, portOpenAI, []string{"workbuddy-auto", "gpt-5.5", "qoder-auto"})

	out := runScopedPreview(t, previewRequest{Port: portOpenAI, Scope: workbuddyKeyScope})
	if !out.Scoped {
		t.Error("a preview with a scope must report itself as scoped")
	}
	if len(out.Ordered) != 3 {
		t.Fatalf("a denied model must stay listed so it can be restored: got %v", out.Ordered)
	}
	if len(out.Denied) != 1 || out.Denied[0] != "gpt-5.5" {
		t.Fatalf("denied = %v, want [gpt-5.5]", out.Denied)
	}
}

// The panel edits an unsaved deny list, so the preview has to honour the list it
// is sent rather than only what is already stored.
func TestScopedPreviewHonoursUnsavedDenyList(t *testing.T) {
	loadPolicyConfig(t, "strategy: name\n")
	previewFeeder(t, portOpenAI, []string{"workbuddy-auto", "gpt-5.5", "qoder-auto"})

	unsaved := []string{"qoder-*"}
	out := runScopedPreview(t, previewRequest{Port: portOpenAI, Scope: workbuddyKeyScope, Deny: &unsaved})
	if len(out.Denied) != 1 || out.Denied[0] != "qoder-auto" {
		t.Fatalf("an unsaved deny must show up immediately, got %v", out.Denied)
	}

	// Sending an explicit empty deny list clears the mark: the operator un-hid
	// every model and expects to see that before saving.
	empty := []string{}
	cleared := runScopedPreview(t, previewRequest{Port: portOpenAI, Scope: workbuddyKeyScope, Deny: &empty})
	if len(cleared.Denied) != 0 {
		t.Fatalf("an explicitly empty deny list must clear every mark, got %v", cleared.Denied)
	}
}

// The regression this store split exists for: once a model is denied, the
// caller-visible catalog no longer holds it. Previewing against that store would
// silently lose the row and with it the only way to restore the model.
func TestScopedPreviewReadsPrePolicyCatalogSoDeniesStayRestorable(t *testing.T) {
	loadPolicyConfig(t, `
strategy: name
access:
  - caller_scope: `+workbuddyKeyScope+`
    deny_models: ["gpt-*"]
`)
	fullCatalog.record(portOpenAI, readCatalogEntries(parseElements(t, []string{"gpt-5.5", "workbuddy-auto"})))
	// The caller-visible snapshot has already lost the denied model.
	catalog.record(portOpenAI, readCatalogEntries(parseElements(t, []string{"workbuddy-auto"})))
	t.Cleanup(func() {
		catalog.reset()
		fullCatalog.reset()
	})

	out := runScopedPreview(t, previewRequest{Port: portOpenAI, Scope: workbuddyKeyScope})
	found := false
	for _, id := range out.Ordered {
		if id == "gpt-5.5" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a denied model vanished from the scoped preview, so it can never be restored: %v", out.Ordered)
	}
	if len(out.Denied) != 1 || out.Denied[0] != "gpt-5.5" {
		t.Fatalf("denied = %v, want [gpt-5.5]", out.Denied)
	}
}

// An unrestricted key has no table entry. Selecting it must show the whole
// listing rather than blanking it, which is the failure mode an allow-list-shaped
// default would produce.
func TestScopedPreviewForUnconfiguredScopeShowsEverything(t *testing.T) {
	loadPolicyConfig(t, `
strategy: name
access:
  - caller_scope: `+workbuddyKeyScope+`
    deny_models: ["gpt-*"]
`)
	previewFeeder(t, portOpenAI, []string{"workbuddy-auto", "gpt-5.5"})

	out := runScopedPreview(t, previewRequest{Port: portOpenAI, Scope: allKeyScope})
	if len(out.Ordered) != 2 || len(out.Denied) != 0 {
		t.Fatalf("an unconfigured key must see everything, got ordered=%v denied=%v", out.Ordered, out.Denied)
	}
}

// statusPayload must expose the pre-policy catalog, or the panel has no way to
// build the per-key view for a model that is currently denied.
func TestStatusExposesPrePolicyCatalog(t *testing.T) {
	loadPolicyConfig(t, "strategy: name\n")
	fullCatalog.record(portOpenAI, readCatalogEntries(parseElements(t, []string{"gpt-5.5", "workbuddy-auto"})))
	catalog.record(portOpenAI, readCatalogEntries(parseElements(t, []string{"workbuddy-auto"})))
	t.Cleanup(func() {
		catalog.reset()
		fullCatalog.reset()
	})

	payload := statusPayload()
	full, ok := payload["full_catalogs"].([]catalogSnapshot)
	if !ok || len(full) == 0 {
		t.Fatalf("status is missing full_catalogs: %#v", payload["full_catalogs"])
	}
	if len(full[0].Entries) != 2 {
		t.Fatalf("full_catalogs must hold the pre-policy listing, got %d entries", len(full[0].Entries))
	}
}

// parseElements builds the raw JSON elements parseModelList would produce, so a
// test can feed both catalog stores without going through the wire.
func parseElements(t *testing.T, ids []string) [][]byte {
	t.Helper()
	list, err := parseModelList(listingBody(ids))
	if err != nil {
		t.Fatalf("fixture listing did not parse: %v", err)
	}
	return list.elements
}

// A non-OpenAI port is not filtered, so a scoped preview there must not invent a
// deny that the runtime would never apply.
func TestScopedPreviewDoesNotDenyOnUnfilteredPorts(t *testing.T) {
	loadPolicyConfig(t, `
strategy: name
access:
  - caller_scope: `+workbuddyKeyScope+`
    deny_models: ["gpt-*"]
`)
	previewFeeder(t, portClaude, []string{"gpt-5.5", "workbuddy-auto"})

	out := runScopedPreview(t, previewRequest{Port: portClaude, Scope: workbuddyKeyScope})
	if len(out.Denied) != 0 {
		t.Fatalf("the claude port is not filtered, so nothing may be marked denied: %v", out.Denied)
	}
}

// ensure the test fixtures keep using the real header shape the runtime reads.
func TestScopedPreviewFixtureMatchesRuntimeHeaders(t *testing.T) {
	headers := http.Header{"Authorization": {"Bearer wba-workbuddy-f27a552fca4d2d0a00a3001073b2d291c0e264bdc3a81eda"}}
	if got := callerScope(callerCredential(headers)); got != workbuddyKeyScope {
		t.Fatalf("fixture scope %q does not match the runtime digest %q", workbuddyKeyScope, got)
	}
	if strings.TrimSpace(workbuddyKeyScope) == "" {
		t.Fatal("workbuddyKeyScope must not be blank")
	}
}
