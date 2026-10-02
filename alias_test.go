package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// aliasEntries mirrors the shape of the real captured listing. The fixture is the
// state measured on this deployment: every openai, qoder and codex model already
// carries its provider prefix, the workbuddy channel has three bare names, and
// cline's models are provider/model pass-throughs.
func realCatalogEntries() []catalogEntry {
	return []catalogEntry{
		// Already aliased: these arrive prefixed and must not be proposed again.
		{ID: "qoder-auto", OwnedBy: "qoder"},
		{ID: "qoder-balanced", OwnedBy: "qoder"},
		{ID: "codex-gpt-6.1-sol", OwnedBy: "codex"},
		{ID: "workbuddy-auto", OwnedBy: "workbuddy"},
		{ID: "workbuddy-balanced", OwnedBy: "workbuddy"},
		{ID: "workbuddy-space-bunny", OwnedBy: "workbuddy"},
		// The three measured bare names.
		{ID: "space-bunny", OwnedBy: "workbuddy"},
		{ID: "hy4-preview-dev", OwnedBy: "workbuddy"},
		{ID: "hy4-preview-x", OwnedBy: "workbuddy"},
		// cline pass-throughs: not missing aliases.
		{ID: "anthropic/claude-opus-5.5", OwnedBy: "cline"},
		{ID: "anthropic/claude-sonnet-5.5", OwnedBy: "cline"},
		{ID: "openai/gpt-6.1-sol", OwnedBy: "cline"},
		{ID: "spacexai/grok-4.7", OwnedBy: "cline"},
		// Not attributable: owned_by is the only source of the owning provider.
		{ID: "some-unowned-model", OwnedBy: ""},
		// No identity at all: readCatalogEntries drops these, but the report must
		// still be safe if one reaches it.
		{ID: "", OwnedBy: "workbuddy"},
	}
}

// The measured result: exactly three missing aliases. The cline provider/model
// names are the trap. Counting them would report seven and propose aliases such
// as "cline-anthropic/claude-opus-5.5", which is not a model any provider serves.
func TestAliasReportFindsExactlyThreeBareNames(t *testing.T) {
	report := buildAliasReport(portOpenAI, realCatalogEntries(), nil)
	if len(report.Missing) != 3 {
		t.Fatalf("missing = %d rows, want 3: %+v", len(report.Missing), report.Missing)
	}
	want := []struct{ model, alias string }{
		{"hy4-preview-dev", "workbuddy-hy4-preview-dev"},
		{"hy4-preview-x", "workbuddy-hy4-preview-x"},
		{"space-bunny", "workbuddy-space-bunny"},
	}
	for i, expect := range want {
		if report.Missing[i].Model != expect.model {
			t.Errorf("row %d model = %q, want %q", i, report.Missing[i].Model, expect.model)
		}
		if report.Missing[i].Alias != expect.alias {
			t.Errorf("row %d alias = %q, want %q", i, report.Missing[i].Alias, expect.alias)
		}
		if report.Missing[i].Channel != "workbuddy" {
			t.Errorf("row %d channel = %q, want workbuddy", i, report.Missing[i].Channel)
		}
	}
	// Nine skipped: four cline pass-throughs, six already aliased, one with no
	// owned_by and one with no id.
	if report.Ignored != 12 {
		t.Errorf("ignored = %d, want 12", report.Ignored)
	}
}

func TestAliasReportSkipsProviderModelNames(t *testing.T) {
	report := buildAliasReport(portOpenAI, []catalogEntry{
		{ID: "anthropic/claude-opus-5.5", OwnedBy: "cline"},
		{ID: "openai/gpt-6.1-sol", OwnedBy: "cline"},
	}, nil)
	if len(report.Missing) != 0 {
		t.Errorf("pass-through names must not be proposed for aliasing: %+v", report.Missing)
	}
	if report.Ignored != 2 {
		t.Errorf("ignored = %d, want 2", report.Ignored)
	}
}

func TestAliasReportSkipsAlreadyAliased(t *testing.T) {
	report := buildAliasReport(portOpenAI, []catalogEntry{
		{ID: "workbuddy-space-bunny", OwnedBy: "workbuddy"},
		{ID: "qoder-auto", OwnedBy: "qoder"},
	}, nil)
	if len(report.Missing) != 0 {
		t.Errorf("already aliased models must not be proposed again: %+v", report.Missing)
	}
	if report.Note == "" {
		t.Error("an empty report should say so plainly rather than look like a failure")
	}
}

func TestAliasReportSkipsEntriesWithoutProviderOrID(t *testing.T) {
	report := buildAliasReport(portOpenAI, []catalogEntry{
		{ID: "space-bunny", OwnedBy: ""},
		{ID: "", OwnedBy: "workbuddy"},
	}, nil)
	if len(report.Missing) != 0 {
		t.Errorf("entries with no provider or no id cannot be aliased: %+v", report.Missing)
	}
	if report.Ignored != 2 {
		t.Errorf("ignored = %d, want 2", report.Ignored)
	}
}

func TestAliasReportDeduplicatesRepeatedIDs(t *testing.T) {
	report := buildAliasReport(portOpenAI, []catalogEntry{
		{ID: "space-bunny", OwnedBy: "workbuddy"},
		{ID: "space-bunny", OwnedBy: "workbuddy"},
	}, nil)
	if len(report.Missing) != 1 {
		t.Errorf("a repeated id must be reported once, got %d", len(report.Missing))
	}
}

// Provider names are compared case insensitively, because owned_by casing is a
// property of the provider plugin rather than something the operator controls.
func TestAliasReportNormalisesProviderCase(t *testing.T) {
	report := buildAliasReport(portOpenAI, []catalogEntry{{ID: "space-bunny", OwnedBy: "WorkBuddy"}}, nil)
	if len(report.Missing) != 1 {
		t.Fatalf("missing = %d, want 1", len(report.Missing))
	}
	if report.Missing[0].Alias != "workbuddy-space-bunny" {
		t.Errorf("alias = %q, want workbuddy-space-bunny", report.Missing[0].Alias)
	}
	if report.Missing[0].Channel != "workbuddy" {
		t.Errorf("channel = %q, want the lowercased provider", report.Missing[0].Channel)
	}
}

func TestLooksLikeDirectPass(t *testing.T) {
	for _, id := range []string{"anthropic/claude-opus-5.5", "openai/gpt-6.1-sol", "a/b"} {
		if !looksLikeDirectPass(id) {
			t.Errorf("%q should read as a pass-through", id)
		}
	}
	for _, id := range []string{"space-bunny", "workbuddy-space-bunny", "", "nope"} {
		if looksLikeDirectPass(id) {
			t.Errorf("%q should not read as a pass-through", id)
		}
	}
}

// The report reads the catalog the interceptor recorded, so it must survive a
// listing that the plugin never touched: an unfiltered, already ordered body.
func TestAliasReportOnUnchangedListingStillRecords(t *testing.T) {
	loadPolicyConfig(t, "strategy: name\n")
	headers := http.Header{"Authorization": {"Bearer any-key"}}
	body := ownedListingBody([][2]string{
		{"codex-gpt-6.1-sol", "codex"},
		{"workbuddy-space-bunny", "workbuddy"},
	})
	if _, changed := governBody(portOpenAI, headers, body); changed {
		t.Fatal("expected an already ordered listing to be reported unchanged")
	}
	snapshot, ok := catalog.get(portOpenAI)
	if !ok {
		t.Fatal("the catalog must record the listing even when nothing changed, or the alias report has nothing to read")
	}
	report := buildAliasReport(snapshot.Port, snapshot.Entries, nil)
	if len(report.Missing) != 0 {
		t.Errorf("both captured models are aliased here, got %+v", report.Missing)
	}
}

// ownedListingBody builds a listing with a per model provider, which the shared
// listingBody helper cannot express.
func ownedListingBody(models [][2]string) []byte {
	var builder strings.Builder
	builder.WriteString(`{"object":"list","data":[`)
	for i, pair := range models {
		if i > 0 {
			builder.WriteByte(',')
		}
		entry, _ := json.Marshal(map[string]any{"id": pair[0], "object": "model", "owned_by": pair[1]})
		builder.Write(entry)
	}
	builder.WriteString(`]}`)
	return []byte(builder.String())
}

func TestProviderPrefix(t *testing.T) {
	if got := providerPrefix("workbuddy"); got != "workbuddy-" {
		t.Errorf("providerPrefix = %q, want workbuddy-", got)
	}
	if got := providerPrefix("  WorkBuddy  "); got != "workbuddy-" {
		t.Errorf("providerPrefix should normalise case and space, got %q", got)
	}
}
