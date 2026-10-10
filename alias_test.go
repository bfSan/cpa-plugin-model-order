package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// realChannels is the alias table as CPA actually holds it in this deployment:
// qoder and workbuddy have channels, openai and cline do not.
func realChannels() map[string]bool {
	return map[string]bool{"qoder": true, "workbuddy": true}
}

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
	report := buildAliasReport(portOpenAI, realCatalogEntries(), realChannels())
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
	}, realChannels())
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
	}, realChannels())
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
	}, realChannels())
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
	}, realChannels())
	if len(report.Missing) != 1 {
		t.Errorf("a repeated id must be reported once, got %d", len(report.Missing))
	}
}

// Provider names are compared case insensitively, because owned_by casing is a
// property of the provider plugin rather than something the operator controls.
func TestAliasReportNormalisesProviderCase(t *testing.T) {
	report := buildAliasReport(portOpenAI, []catalogEntry{{ID: "space-bunny", OwnedBy: "WorkBuddy"}}, map[string]bool{"workbuddy": true})
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
	report := buildAliasReport(snapshot.Port, snapshot.Entries, realChannels())
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

// CPA's built-in openai provider serves fourteen models as gpt-5.5, gpt-6.1-sol
// and so on. Those names are correct, not missing aliases: the first live run of
// this report proposed openai-gpt-6.1-sol for every one of them, which buried the
// three real rows under fourteen rows of noise. A provider that carries no
// <provider>- prefixed model at all is not participating in aliasing.
func TestAliasReportSkipsProvidersNotUsingAliases(t *testing.T) {
	entries := []catalogEntry{
		{ID: "gpt-5.5", OwnedBy: "openai"},
		{ID: "gpt-5.6-luna", OwnedBy: "openai"},
		{ID: "gpt-6-sol", OwnedBy: "openai"},
		{ID: "gpt-6.1-sol", OwnedBy: "openai"},
		// The one real gap, on a provider that has adopted the convention.
		{ID: "workbuddy-auto", OwnedBy: "workbuddy"},
		{ID: "space-bunny", OwnedBy: "workbuddy"},
	}
	report := buildAliasReport(portOpenAI, entries, nil)
	if len(report.Missing) != 1 {
		t.Fatalf("missing = %+v, want only the workbuddy bare name", report.Missing)
	}
	if report.Missing[0].Model != "space-bunny" {
		t.Errorf("model = %q, want space-bunny", report.Missing[0].Model)
	}
	if report.Ignored != 5 {
		t.Errorf("ignored = %d, want 5", report.Ignored)
	}
}

// The channel set is the authority on which providers participate. A provider
// absent from it is skipped even when its models look bare, which is what keeps
// CPA's built-in openai out of the report: gpt-5.5 and gpt-6.1-sol are the
// intended names and there is no openai alias channel to add them to.
func TestAliasReportSkipsProvidersWithoutAChannel(t *testing.T) {
	entries := []catalogEntry{
		{ID: "gpt-5.5", OwnedBy: "openai"},
		{ID: "gpt-6.1-sol", OwnedBy: "openai"},
		{ID: "anthropic/claude-opus-5.5", OwnedBy: "cline"},
		{ID: "workbuddy-auto", OwnedBy: "workbuddy"},
		{ID: "space-bunny", OwnedBy: "workbuddy"},
	}
	report := buildAliasReport(portOpenAI, entries, map[string]bool{"qoder": true, "workbuddy": true})
	if len(report.Missing) != 1 {
		t.Fatalf("missing = %+v, want only the workbuddy bare name", report.Missing)
	}
	if report.Missing[0].Model != "space-bunny" {
		t.Errorf("model = %q, want space-bunny", report.Missing[0].Model)
	}
	if report.Ignored != 4 {
		t.Errorf("ignored = %d, want 4", report.Ignored)
	}
	if got := report.Channels; len(got) != 2 || got[0] != "qoder" || got[1] != "workbuddy" {
		t.Errorf("channels = %v, want [qoder workbuddy]", got)
	}
}

// With no channel list the report still has to work, so it falls back to reading
// adoption off the listing: a provider whose models mostly carry the prefix is
// using the convention, and its bare names are the gap.
func TestAliasReportFallsBackToTheListing(t *testing.T) {
	entries := []catalogEntry{
		// workbuddy: 3 prefixed of 4, so the majority signals adoption.
		{ID: "workbuddy-auto", OwnedBy: "workbuddy"},
		{ID: "workbuddy-fast", OwnedBy: "workbuddy"},
		{ID: "workbuddy-kimi-k3", OwnedBy: "workbuddy"},
		{ID: "space-bunny", OwnedBy: "workbuddy"},
		// openai: none prefixed, so it is not participating.
		{ID: "gpt-6.1-sol", OwnedBy: "openai"},
		{ID: "gpt-6-sol", OwnedBy: "openai"},
	}
	report := buildAliasReport(portOpenAI, entries, nil)
	if len(report.Missing) != 1 || report.Missing[0].Model != "space-bunny" {
		t.Errorf("missing = %+v, want only space-bunny", report.Missing)
	}
	if len(report.Channels) != 0 {
		t.Errorf("channels = %v, want empty when no list was supplied", report.Channels)
	}
}

func TestSortedKeys(t *testing.T) {
	got := sortedKeys(map[string]bool{"workbuddy": true, "qoder": true, "openai": false})
	if len(got) != 2 || got[0] != "qoder" || got[1] != "workbuddy" {
		t.Errorf("sortedKeys = %v, want [qoder workbuddy]", got)
	}
}

// trae is the case this fallback exists for. Every one of its models reaches
// clients with an empty owned_by -- measured on the live deployment -- so before
// the credential catalog was supplied, the report dropped all twenty and said
// nothing about a provider that was plainly in use.
func TestAliasReportAttributesEntriesFromCredentialCatalog(t *testing.T) {
	entries := []catalogEntry{
		{ID: "Doubao-Seed-Evolving"},           // owned_by empty, catalog knows trae
		{ID: "glm-5.3"},                        // owned_by empty, catalog knows trae
		{ID: "hy3"},                            // owned_by empty, catalog knows workbuddy
		{ID: "gpt-6.1-sol", OwnedBy: "openai"}, // owned_by wins
	}
	providers := map[string][]string{
		"Doubao-Seed-Evolving": {"trae"},
		"glm-5.3":              {"trae"},
		"hy3":                  {"workbuddy"},
		"gpt-6.1-sol":          {"trae"}, // must be ignored: owned_by is set
	}
	report := buildAliasReportWithProviders(portOpenAI, entries,
		map[string]bool{"trae": true, "workbuddy": true}, providers)

	want := []struct{ model, alias, channel string }{
		{"Doubao-Seed-Evolving", "trae-Doubao-Seed-Evolving", "trae"},
		{"glm-5.3", "trae-glm-5.3", "trae"},
		{"hy3", "workbuddy-hy3", "workbuddy"},
	}
	if len(report.Missing) != len(want) {
		t.Fatalf("missing = %d rows, want %d: %+v", len(report.Missing), len(want), report.Missing)
	}
	for i, expect := range want {
		got := report.Missing[i]
		if got.Model != expect.model || got.Alias != expect.alias || got.Channel != expect.channel {
			t.Errorf("row %d = %s/%s/%s, want %s/%s/%s",
				i, got.Channel, got.Model, got.Alias, expect.channel, expect.model, expect.alias)
		}
	}
	// gpt-6.1-sol stays attributed to openai, which has no channel, so it is
	// skipped rather than proposed for trae.
	if report.Ignored != 1 {
		t.Errorf("ignored = %d, want 1", report.Ignored)
	}
}

// A provider that only the credential catalog knows still has to clear the
// channel gate: a catalog entry is not by itself permission to invent an alias.
func TestAliasReportCatalogProviderNeedsAChannel(t *testing.T) {
	entries := []catalogEntry{{ID: "glm-5.3"}}
	providers := map[string][]string{"glm-5.3": {"trae"}}
	report := buildAliasReportWithProviders(portOpenAI, entries, map[string]bool{"workbuddy": true}, providers)
	if len(report.Missing) != 0 {
		t.Fatalf("missing = %+v, want none: trae has no channel", report.Missing)
	}
	if report.Ignored != 1 {
		t.Errorf("ignored = %d, want 1", report.Ignored)
	}
}

// One bare name served by two channels needs a row in each, so dedup has to key
// on the channel as well as the id. kimi-k3 is exactly this on the live
// deployment: trae and workbuddy both serve it with an empty owned_by.
func TestAliasReportProposesOneRowPerServingChannel(t *testing.T) {
	entries := []catalogEntry{{ID: "kimi-k3"}}
	providers := map[string][]string{"kimi-k3": {"trae", "workbuddy"}}
	report := buildAliasReportWithProviders(portOpenAI, entries,
		map[string]bool{"trae": true, "workbuddy": true}, providers)
	if len(report.Missing) != 2 {
		t.Fatalf("missing = %d rows, want 2: %+v", len(report.Missing), report.Missing)
	}
	got := []string{report.Missing[0].Channel, report.Missing[1].Channel}
	if got[0] != "trae" || got[1] != "workbuddy" {
		t.Errorf("channels = %v, want [trae workbuddy]", got)
	}
	if report.Missing[0].Alias != "trae-kimi-k3" || report.Missing[1].Alias != "workbuddy-kimi-k3" {
		t.Errorf("aliases = %q/%q, want trae-kimi-k3/workbuddy-kimi-k3",
			report.Missing[0].Alias, report.Missing[1].Alias)
	}
}

// The mapping is a fallback, not an override: without it behavior is unchanged,
// which is what keeps the openai models from coming back as noise.
func TestAliasReportWithoutCatalogMappingIsUnchanged(t *testing.T) {
	entries := realCatalogEntries()
	withFallback := buildAliasReportWithProviders(portOpenAI, entries, realChannels(), nil)
	without := buildAliasReport(portOpenAI, entries, realChannels())
	if len(withFallback.Missing) != len(without.Missing) {
		t.Fatalf("nil mapping changed the report: %d vs %d rows",
			len(withFallback.Missing), len(without.Missing))
	}
	if withFallback.Ignored != without.Ignored {
		t.Errorf("ignored = %d with nil mapping, want %d", withFallback.Ignored, without.Ignored)
	}
}

// An id the credential catalog does not know stays unattributed, so it is
// skipped rather than guessed at.
func TestAliasReportSkipsIDsAbsentFromTheCatalog(t *testing.T) {
	entries := []catalogEntry{{ID: "mystery-model"}}
	report := buildAliasReportWithProviders(portOpenAI, entries,
		map[string]bool{"trae": true}, map[string][]string{"other": {"trae"}})
	if len(report.Missing) != 0 {
		t.Fatalf("missing = %+v, want none", report.Missing)
	}
	if report.Ignored != 1 {
		t.Errorf("ignored = %d, want 1", report.Ignored)
	}
}
