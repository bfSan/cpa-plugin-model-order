package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The plain OpenAI listing is projected down to id/object/created/owned_by by
// CPA's own handler, so a client reading it learns nothing about what a model can
// do. The Codex client catalog carries the same models with their capabilities.
// These tests pin the bridge between the two shapes.

func sampleCodexCatalog(t *testing.T) []byte {
	t.Helper()
	return []byte(`{"models":[` +
		`{"slug":"gpt-6-sol","context_window":272000,"max_context_window":872000,` +
		`"input_modalities":["text","image"],` +
		`"supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"}],` +
		`"default_reasoning_level":"medium"},` +
		`{"slug":"plain-model"}` +
		`]}`)
}

func governedBody(t *testing.T, port string, body []byte) []byte {
	t.Helper()
	out, _ := governBody(port, nil, body)
	return out
}

func decodeEntries(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var doc struct {
		Data   []map[string]any `json:"data"`
		Models []map[string]any `json:"models"`
	}
	if errUnmarshal := json.Unmarshal(body, &doc); errUnmarshal != nil {
		t.Fatalf("body is not valid json: %v (%s)", errUnmarshal, body)
	}
	if len(doc.Data) > 0 {
		return doc.Data
	}
	return doc.Models
}

// A model learned from the catalog must arrive on the plain listing with its
// capability fields intact.
func TestCapabilityBackfillIntoOpenAIListing(t *testing.T) {
	beginIsolatedTest(t)

	governedBody(t, portCodex, sampleCodexCatalog(t))

	out := governedBody(t, portOpenAI, []byte(`{"object":"list","data":[`+
		`{"id":"gpt-6-sol","object":"model","owned_by":"workbuddy"}]}`))

	entries := decodeEntries(t, out)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	entry := entries[0]

	if got := entry["context_length"]; got != float64(272000) {
		t.Fatalf("context_length = %v, want 272000", got)
	}
	if got := entry["max_context_length"]; got != float64(872000) {
		t.Fatalf("max_context_length = %v, want 872000", got)
	}
	levels, _ := entry["reasoning_levels"].([]any)
	if len(levels) != 3 || levels[0] != "low" {
		t.Fatalf("reasoning_levels = %v, want [low medium high]", entry["reasoning_levels"])
	}
	if got := entry["default_reasoning_level"]; got != "medium" {
		t.Fatalf("default_reasoning_level = %v, want medium", got)
	}
	modalities, _ := entry["input_modalities"].([]any)
	if len(modalities) != 2 || modalities[1] != "image" {
		t.Fatalf("input_modalities = %v, want [text image]", entry["input_modalities"])
	}
	// The identity fields CPA projected must survive untouched.
	if entry["id"] != "gpt-6-sol" || entry["object"] != "model" || entry["owned_by"] != "workbuddy" {
		t.Fatalf("identity fields were disturbed: %v", entry)
	}
}

// A model with nothing learned for it must come back byte-identical, so an
// unknown model is never given invented facts.
func TestCapabilityBackfillLeavesUnknownModelsAlone(t *testing.T) {
	beginIsolatedTest(t)

	governedBody(t, portCodex, sampleCodexCatalog(t))

	body := []byte(`{"object":"list","data":[{"id":"never-seen","object":"model","owned_by":""}]}`)
	out, changed := governBody(portOpenAI, nil, body)
	if changed {
		t.Fatalf("unknown model must not change the body, got %s", out)
	}
	if string(out) != "" && string(out) != string(body) {
		t.Fatalf("body changed unexpectedly: %s", out)
	}
}

// A catalog entry that learned nothing worth carrying must not create an empty
// record: plain-model has no context window and no reasoning levels.
func TestCapabilityCatalogSkipsFactlessEntries(t *testing.T) {
	beginIsolatedTest(t)

	governedBody(t, portCodex, sampleCodexCatalog(t))

	if _, ok := capabilities.lookup("plain-model"); ok {
		t.Fatal("an entry carrying no capability facts must not be recorded")
	}
	if _, ok := capabilities.lookup("gpt-6-sol"); !ok {
		t.Fatal("an entry carrying facts must be recorded")
	}
}

// Values CPA already declared win over the backfill. A listing that answers with
// its own context_length keeps it.
func TestCapabilityBackfillRespectsExistingFields(t *testing.T) {
	beginIsolatedTest(t)

	governedBody(t, portCodex, sampleCodexCatalog(t))

	out := governedBody(t, portOpenAI, []byte(`{"object":"list","data":[`+
		`{"id":"gpt-6-sol","object":"model","context_length":999}]}`))

	entry := decodeEntries(t, out)[0]
	if got := entry["context_length"]; got != float64(999) {
		t.Fatalf("context_length = %v, want the existing 999 to win", got)
	}
	// The fields it did NOT declare are still filled in.
	if got := entry["max_context_length"]; got != float64(872000) {
		t.Fatalf("max_context_length = %v, want 872000", got)
	}
}

// The backfill must leave the exact numeric formatting of untouched fields
// alone. Pinned because the plugin splices original bytes on purpose.
func TestCapabilityBackfillPreservesNumberFormatting(t *testing.T) {
	beginIsolatedTest(t)

	governedBody(t, portCodex, sampleCodexCatalog(t))

	out := governedBody(t, portOpenAI, []byte(`{"data":[`+
		`{"id":"gpt-6-sol","object":"model","ratio":0.30,"exp":1e2}]}`))

	if !strings.Contains(string(out), `"ratio":0.30`) {
		t.Fatalf("float formatting was rewritten: %s", out)
	}
	if !strings.Contains(string(out), `"exp":1e2`) {
		t.Fatalf("exponent formatting was rewritten: %s", out)
	}
}

// The Claude and Gemini ports already answer with their own capacity vocabulary;
// the OpenAI-shaped backfill must not bleed into them.
func TestCapabilityBackfillOnlyTouchOpenAIPort(t *testing.T) {
	beginIsolatedTest(t)

	governedBody(t, portCodex, sampleCodexCatalog(t))

	body := []byte(`{"models":[{"name":"models/gpt-6-sol","displayName":"GPT"}]}`)
	out, _ := governBody(portGemini, nil, body)
	if strings.Contains(string(out), "context_length") {
		t.Fatalf("gemini listing must not receive openai capability keys: %s", out)
	}
}

// A case-only difference between the two listings is not a different model;
// CPA itself resolves availability with strings.EqualFold.
func TestCapabilityLookupFoldsCase(t *testing.T) {
	beginIsolatedTest(t)

	governedBody(t, portCodex, sampleCodexCatalog(t))

	out := governedBody(t, portOpenAI, []byte(`{"data":[{"id":"GPT-6-Sol","object":"model"}]}`))
	entry := decodeEntries(t, out)[0]
	if got := entry["context_length"]; got != float64(272000) {
		t.Fatalf("case-folded lookup failed, context_length = %v", got)
	}
}

// Learning is additive and idempotent: a second catalog pass must not multiply
// or corrupt what was already learned.
func TestCapabilityLearningIsIdempotent(t *testing.T) {
	beginIsolatedTest(t)

	governedBody(t, portCodex, sampleCodexCatalog(t))
	first, _ := capabilities.lookup("gpt-6-sol")
	governedBody(t, portCodex, sampleCodexCatalog(t))
	second, _ := capabilities.lookup("gpt-6-sol")

	if first.ContextLength != second.ContextLength || len(second.ReasoningLevels) != 3 {
		t.Fatalf("repeated learning changed the record: %+v -> %+v", first, second)
	}
}

// An entry that declares no reasoning levels still gets its context window; the
// two facts are independent.
func TestCapabilityBackfillWithOnlyContext(t *testing.T) {
	beginIsolatedTest(t)

	governedBody(t, portCodex, []byte(`{"models":[{"slug":"ctx-only","context_window":1000000}]}`))
	out := governedBody(t, portOpenAI, []byte(`{"data":[{"id":"ctx-only","object":"model"}]}`))

	entry := decodeEntries(t, out)[0]
	if got := entry["context_length"]; got != float64(1000000) {
		t.Fatalf("context_length = %v, want 1000000", got)
	}
	if _, exists := entry["reasoning_levels"]; exists {
		t.Fatal("reasoning_levels must not be invented for a model that declared none")
	}
}

// beginIsolatedTest gives a test the state it needs without borrowing it from
// whichever case happened to run first.
//
// Four package level stores outlive a single test: the active config, the two
// catalog snapshots, the capability records, and the refresh loop's last-outcome
// record. Resetting them on entry rather than deferring a reset keeps every case
// independent in both directions: a test neither inherits a neighbour's leftovers
// nor leaves any for the next one.
func beginIsolatedTest(t *testing.T) {
	t.Helper()
	// Stop any refresh loop a previous case started before clearing the state it
	// writes, or that goroutine races this test's assertions.
	stopCatalogRefresh()

	catalogFetch.reset()
	capabilities.reset()
	catalog.reset()
	fullCatalog.reset()
	if err := loadConfig(nil); err != nil {
		t.Fatalf("empty config must load cleanly: %v", err)
	}
}
