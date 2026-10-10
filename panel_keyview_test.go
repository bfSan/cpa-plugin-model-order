package main

import (
	"strings"
	"testing"
)

// Panel structure for the per-key visibility editor and drag ordering.
//
// These are HTML-shape assertions, matching the sibling panel tests: the panel is
// served as one embedded document, so the contract that matters is which hooks
// exist and what they must not do. Behavioural coverage of the matching itself
// lives in keyview_test.go.

func panelSection(t *testing.T, startMarker, endMarker string) string {
	t.Helper()
	html := renderPanel()
	start := strings.Index(html, startMarker)
	if start < 0 {
		t.Fatalf("panel is missing %q", startMarker)
	}
	end := strings.Index(html[start:], endMarker)
	if end < 0 {
		t.Fatalf("panel is missing the closing marker %q after %q", endMarker, startMarker)
	}
	return html[start : start+end]
}

// The preview must offer a key selector and a way to save visibility, and it must
// expose the two hooks the editor hangs off.
func TestPanelExposesPerKeyVisibilityControls(t *testing.T) {
	html := renderPanel()
	for _, want := range []string{
		`id="keyPicker"`,
		`id="btnSaveVisibility"`,
		`id="btnRevertVisibility"`,
		`id="pvCount"`,
		`id="pvNote"`,
		`async function loadAPIKeys()`,
		`function buildAccessTable()`,
		`async function saveVisibility()`,
		`function setPreviewHidden(`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("panel is missing %q", want)
		}
	}
	// The old nudging buttons are gone: ordering is drag plus the arrow keys.
	for _, unwanted := range []string{`id="pvUp"`, `id="pvDown"`, `id="pvHome"`} {
		if strings.Contains(html, unwanted) {
			t.Fatalf("panel still ships the removed %q control", unwanted)
		}
	}
}

// The scope derivation has to match the plugin byte for byte, or a policy written
// from the panel matches no key. This pins the prefix and the separator.
func TestPanelDerivesTheSameCallerScopeAsThePlugin(t *testing.T) {
	html := renderPanel()
	if !strings.Contains(html, `"cli-proxy-api:caller-scope:v1\0"`) {
		t.Fatal("panel does not hash the caller-scope prefix the plugin and CPA use")
	}
	// The fallback exists for plain-HTTP remote panels, where crypto.subtle is
	// absent; without it the key list silently yields nothing off localhost.
	if !strings.Contains(html, "function sha256FallbackHex(") {
		t.Fatal("panel has no pure-JS SHA-256 fallback for non-secure origins")
	}
	// The raw key must never be sent to the plugin: only the digest is.
	if strings.Contains(html, `body.scope = key`) || strings.Contains(html, "credential: ") {
		t.Fatal("the panel must send the scope digest, never the credential itself")
	}
}

// Saving visibility must patch only the access key. A PATCH replaces the value it
// carries wholesale, so sending order here would clobber unsaved rule edits, and
// dropping label/allow_models from a stored policy would silently disarm it.
func TestVisibilitySavePatchesOnlyAccessAndPreservesOtherFields(t *testing.T) {
	save := panelSection(t, "async function saveVisibility()", "\nfunction buildAccessTable()")
	if !strings.Contains(save, "JSON.stringify({ access: buildAccessTable() })") {
		t.Fatal("saving visibility must patch exactly the access key")
	}
	for _, unwanted := range []string{"order:", "strategy:", "case_sensitive:"} {
		if strings.Contains(save, unwanted) {
			t.Fatalf("saving visibility must not carry %q: a PATCH replaces that key wholesale", unwanted)
		}
	}

	build := panelSection(t, "function buildAccessTable()", "\n/* ---- 模型别名")
	for _, want := range []string{"entry.label = prior.label", "entry.allow_models = allow"} {
		if !strings.Contains(build, want) {
			t.Fatalf("buildAccessTable must carry stored policy fields through, missing %q", want)
		}
	}
}

// A hidden model stays in the list, greyed, so the row can be restored. Dropping
// it would leave a deny with no way back.
func TestPreviewKeepsDeniedModelsListedAndRestorable(t *testing.T) {
	draw := panelSection(t, "function drawPreview()", "\nfunction setPreviewHidden(")
	for _, want := range []string{
		`hidden ? "denied" : ""`,
		`data-pv-act="${hidden ? "restore" : "hide"}"`,
		`data-pv-id="${esc(id)}"`,
		`<span class="chip denied">已隐藏</span>`,
	} {
		if !strings.Contains(draw, want) {
			t.Fatalf("drawPreview is missing %q", want)
		}
	}
	// Denied rows are rendered from the same previewIds array as the others, so
	// they keep their position rather than being appended or dropped.
	if !strings.Contains(draw, "previewIds.map((id, i) => {") {
		t.Fatal("drawPreview must render every id in order, including denied ones")
	}
}

// Hiding one model must write one exact id. A wildcard would take out a whole
// family from a single click, which is the accident this editor should not enable.
func TestHideWritesAnExactModelIdNotAWildcard(t *testing.T) {
	set := panelSection(t, "function setPreviewHidden(", "\n/* ---- 模型别名")
	if !strings.Contains(set, "list.push(id)") {
		t.Fatal("hiding must append the exact model id")
	}
	for _, unwanted := range []string{`+ "*"`, `-*`, `"*"`} {
		if strings.Contains(set, unwanted) {
			t.Fatalf("hiding must not build a pattern (%q found)", unwanted)
		}
	}
}

// The preview is scoped only when a key is selected, and the draft deny list rides
// along so an unsaved hide is visible before it is written.
func TestPreviewSendsScopeAndDraftDenyOnlyWhenScoped(t *testing.T) {
	render := panelSection(t, "async function renderPreview()", "\n// localDenied")
	for _, want := range []string{
		"if (selectedScope) {",
		"body.scope = selectedScope",
		"body.deny = denyDrafts[selectedScope] || []",
		"deniedNow = new Set(denied)",
	} {
		if !strings.Contains(render, want) {
			t.Fatalf("renderPreview is missing %q", want)
		}
	}
	// With no key selected the request must stay the unscoped one, so the ALL view
	// shows the whole listing.
	if !strings.Contains(render, "const body = { strategy: state.strategy, order: state.order, case_sensitive: state.caseSensitive, port };") {
		t.Fatal("the unscoped preview request shape changed")
	}
}

// Drag ordering for the preview, mirroring the rule list: pointer events, a
// keyboard path, and the manual order kept apart from the saved rule list.
func TestPreviewSupportsDragOrdering(t *testing.T) {
	html := renderPanel()
	for _, want := range []string{
		`data-pv-drag="${i}"`,
		`function movePreview(from, to)`,
		`$("preview").addEventListener("pointerdown"`,
		`$("preview").addEventListener("pointermove"`,
		`$("preview").addEventListener("pointerup"`,
		`$("preview").addEventListener("pointercancel"`,
		`$("preview").addEventListener("keydown"`,
		`manualOrder = previewIds.slice()`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("preview drag ordering is missing %q", want)
		}
	}
	// Dragging must not rewrite the saved rule list: that only happens through the
	// explicit generate button.
	preview := panelSection(t, "function movePreview(from, to)", "\nlet previewDrag")
	if strings.Contains(preview, "state.order =") {
		t.Fatal("dragging the preview must not overwrite the saved rules")
	}
	// A click on the handle without movement is a selection, not a reorder.
	finish := panelSection(t, "function finishPreviewDrag(", `$("preview").addEventListener("pointerdown"`)
	if !strings.Contains(finish, "if (!moved) return;") {
		t.Fatal("a handle click without movement must not reorder")
	}
}

// Switching keys with unsaved visibility edits must warn before discarding them.
func TestSwitchingKeysGuardsUnsavedVisibility(t *testing.T) {
	handler := panelSection(t, `$("keyPicker").addEventListener("click"`, "function visibilityDirty(")
	if !strings.Contains(handler, "visibilityDirty(selectedScope)") {
		t.Fatal("switching keys must check for unsaved visibility edits")
	}
	if !strings.Contains(handler, "confirm(") {
		t.Fatal("switching keys must ask before dropping unsaved visibility edits")
	}
	if !strings.Contains(handler, "denyDrafts[selectedScope] = (knownDeny[selectedScope] || []).slice()") {
		t.Fatal("declining the switch must restore the draft from the saved baseline")
	}
}

// The saved policy list is the baseline for dirty checks and revert, so it has to
// be adopted from status rather than guessed.
func TestPanelAdoptsStoredPolicies(t *testing.T) {
	html := renderPanel()
	for _, want := range []string{
		`state.policies = Array.isArray(d.policies) ? d.policies : []`,
		"function adoptPolicies(policies)",
		`knownDeny[scope] = Array.isArray(p.deny_models) ? p.deny_models.map(String) : []`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("panel does not adopt stored policies, missing %q", want)
		}
	}
	// allow_models is deliberately not turned into an editable draft.
	if strings.Contains(html, "allowDrafts") {
		t.Fatal("allow_models must not become a point-and-click draft")
	}
}
