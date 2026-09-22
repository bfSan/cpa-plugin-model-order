package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The Codex client catalog is served from /v1/models?client_version=... as
// {"models":[...]} where each entry is keyed by "slug" and carries deeply nested
// template metadata. It is the shape Codex CLI and Codex++ actually read.
func TestCodexClientCatalogReordered(t *testing.T) {
	if err := loadConfig(nil); err != nil {
		t.Fatalf("defaults must load cleanly: %v", err)
	}
	body := []byte(`{"models":[` +
		`{"slug":"workbuddy-hy3","display_name":"Hy3","model_messages":{"instructions":"a } quoted, bracket [ here"},"supported_reasoning_levels":[{"effort":"low"}],"priority":3},` +
		`{"slug":"gpt-6-astra","display_name":"Astra","model_messages":{"instructions":"b"},"priority":1}` +
		`]}`)

	out, changed := orderBody(portCodex, body)
	if !changed {
		t.Fatal("expected the codex client catalog to be reordered")
	}

	var doc struct {
		Models []json.RawMessage `json:"models"`
	}
	if errUnmarshal := json.Unmarshal(out, &doc); errUnmarshal != nil {
		t.Fatalf("reordered catalog is invalid json: %v (%s)", errUnmarshal, out)
	}
	if len(doc.Models) != 2 {
		t.Fatalf("want 2 entries, got %d", len(doc.Models))
	}
	// gpt-* is a configured bucket, the hy3 preset is not, so gpt leads.
	var first struct {
		Slug         string `json:"slug"`
		Display_name string `json:"display_name"`
	}
	if errUnmarshal := json.Unmarshal(doc.Models[0], &first); errUnmarshal != nil {
		t.Fatalf("first entry unreadable: %v", errUnmarshal)
	}
	if first.Slug != "gpt-6-astra" {
		t.Fatalf("want gpt-6-astra first, got %q", first.Slug)
	}
	// Elements must be moved verbatim, nested strings and braces intact.
	var second struct {
		Slug          string         `json:"slug"`
		ModelMessages map[string]any `json:"model_messages"`
	}
	if errUnmarshal := json.Unmarshal(doc.Models[1], &second); errUnmarshal != nil {
		t.Fatalf("second entry unreadable: %v", errUnmarshal)
	}
	if second.Slug != "workbuddy-hy3" || second.ModelMessages["instructions"] != "a } quoted, bracket [ here" {
		t.Fatalf("nested content not preserved: %+v", second)
	}
}

// TestCatalogNumbersKeepExactFormat guards the splice design: because elements
// are moved as original bytes, float and large integer formatting from CPA is
// never re-rendered through Go's JSON encoder.
func TestCatalogNumbersKeepExactFormat(t *testing.T) {
	if err := loadConfig(nil); err != nil {
		t.Fatalf("defaults must load cleanly: %v", err)
	}
	body := []byte(`{"models":[` +
		`{"slug":"zeta","context_window":272000,"ratio":0.30,"exp":1e2},` +
		`{"slug":"alpha","context_window":272000,"ratio":0.30,"exp":1e2}` +
		`]}`)
	out, changed := orderBody(portCodex, body)
	if !changed {
		t.Fatal("expected reorder")
	}
	want := []byte(`{"models":[{"slug":"alpha","context_window":272000,"ratio":0.30,"exp":1e2},{"slug":"zeta","context_window":272000,"ratio":0.30,"exp":1e2}]}`)
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("number formatting changed\n got  %s\nwant %s", out, want)
	}
}
