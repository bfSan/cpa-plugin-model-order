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

// 隐藏按钮曾在 ALL(不限制) 视角下整段消失,被操作者报告为「按钮丢失了」。它必须
// 始终渲染:可隐藏时是「隐藏/恢复」,不可隐藏时是禁用态并把前提写进 title。
// 顺手钉住 45% 常显:按钮若退回 hover-only,48 行的长列表里依然没人找得到。
func TestPreviewHideButtonSurvivesEveryScope(t *testing.T) {
	draw := panelSection(t, "function drawPreview()", "\nfunction setPreviewHidden(")
	if !strings.Contains(draw, "const canHide = !!selectedScope;") {
		t.Fatal("drawPreview must branch on the selected scope")
	}
	// 两个分支都必须在:有 Key 时给出可点的隐藏/恢复,没 Key 时给出禁用态。
	if !strings.Contains(draw, "canHide") || !strings.Contains(draw, "disabled") {
		t.Fatal("drawPreview must render the hide button in both scopes, disabled when unscoped")
	}
	html := renderPanel()
	if !strings.Contains(html, "#preview li .flag{opacity:.45}") {
		t.Fatal("preview action buttons must stay partially visible without hover")
	}
}

// 预览过去只显示 owned_by,分组标签只在右侧「实际下发」出现,同一模型在两个窗口
// 里读起来不是一回事。预览必须复用 firstRule,与右侧同款命中/未分组标签。
// 但同时:纯字母序模式下规则不参与排序,那时挂分组标签等于编造一个不存在的分组。
func TestPreviewShowsTheSameGroupChipAsTheModelsList(t *testing.T) {
	draw := panelSection(t, "function drawPreview()", "\nfunction setPreviewHidden(")
	if !strings.Contains(draw, "firstRule(id)") {
		t.Fatal("drawPreview must reuse firstRule so both windows agree on the group")
	}
	if !strings.Contains(draw, `class="chip ${hit ? "hit" : "none"}"`) {
		t.Fatal("drawPreview must render the same hit/none group chip as renderModels")
	}
	if !strings.Contains(draw, `state.strategy !== "name"`) {
		t.Fatal("the group chip must be suppressed in the pure-alphabetical strategy")
	}
}

// 排序规则只作用于预览。布局必须让这句话自己成立:规则与预览同列上下相邻,否则
// 并排的「规则 | 实际下发」会被读成「规则作用于右边那个列表」。
func TestOrderRulesSitInTheSameColumnAsThePreview(t *testing.T) {
	html := renderPanel()
	for _, want := range []string{
		".card-rules{grid-column:1;grid-row:3}",
		".card-preview{grid-column:1;grid-row:4}",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("panel layout is missing %q", want)
		}
	}
	// 右侧那张只占第 2 列,不能横跨到规则的列上。
	if !strings.Contains(html, ".card-models{grid-column:2;grid-row:3/span 2}") {
		t.Fatal("the actual-models card must stay in column 2")
	}
	// 显式行号会让自动放置的元素(操作栏、跨列的别名卡)掉到所有卡片之后,
	// 所以它们也必须显式定位。
	for _, want := range []string{
		".actions{grid-column:1/-1;grid-row:1}",
		".card-full{grid-column:1/-1;grid-row:2}",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("explicit rows pushed %q out of place; it needs its own row", want)
		}
	}
	// 单列时必须清掉全部显式定位,否则 span 会造出隐式第二列。
	if !strings.Contains(html, ".actions,.card-full,.card-rules,.card-models,.card-preview{grid-column:1/-1;grid-row:auto}") {
		t.Fatal("the single-column media query must reset every explicit grid placement")
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
