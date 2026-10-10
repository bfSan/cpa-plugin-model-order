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
	return map[string]bool{"qoder": true, "workbuddy": true, "cline": true}
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
		// cline: the upstream names themselves. "cline-free/..." starts with the
		// cline prefix without having been aliased, so it still wants a row.
		{ID: "anthropic/claude-opus-5.5", OwnedBy: "cline"},
		{ID: "anthropic/claude-sonnet-5.5", OwnedBy: "cline"},
		{ID: "openai/gpt-6.1-sol", OwnedBy: "cline"},
		{ID: "x-ai/grok-4.7", OwnedBy: "cline"},
		{ID: "cline-free/solar-mini4", OwnedBy: "cline"},
		// Not attributable: owned_by is the only source of the owning provider.
		{ID: "some-unowned-model", OwnedBy: ""},
		// No identity at all: readCatalogEntries drops these, but the report must
		// still be safe if one reaches it.
		{ID: "", OwnedBy: "workbuddy"},
	}
}

// 别名表是判断"是否已经配过"的唯一权威。名字形态做不到这件事：
// listing 里出现的是上游名还是别名，取决于别名有没有生效 —— 两种都要算"已覆盖"。
func TestAliasReportSkipsModelsAlreadyInAliasTable(t *testing.T) {
	entries := []catalogEntry{
		// 表里有行（以别名形式出现在 listing 里）：不该再提议。
		{ID: "workbuddy-space-bunny", OwnedBy: "workbuddy"},
		{ID: "qoder-auto", OwnedBy: "qoder"},
		// 表里没有行：真缺口，照常提议。
		{ID: "hy4-preview-dev", OwnedBy: "workbuddy"},
		// cline 的上游原名以 cline- 开头，但表里没有行 —— 正是"该加前缀"的一类。
		{ID: "cline-free/solar-mini4", OwnedBy: "cline"},
		{ID: "x-ai/grok-4.7", OwnedBy: "cline"},
	}
	existing := map[string]map[string]bool{
		// 面板会同时送 name 与 alias：listing 里出现哪一个取决于别名是否已生效。
		"workbuddy": {"space-bunny": true, "workbuddy-space-bunny": true},
		"qoder":     {"auto": true, "qoder-auto": true},
		"cline":     {},
	}
	report := buildAliasReportWithAliasTable(portOpenAI, entries, realChannels(), nil, existing)

	// 表里已有行的，不再提议，并记明原因。
	for _, model := range []string{"workbuddy-space-bunny", "qoder-auto"} {
		if _, ok := proposedFor(report, model); ok {
			t.Errorf("%s already has an alias row and must not be proposed", model)
		}
		row, ok := skippedFor(report, model)
		if !ok {
			t.Errorf("%s should be listed as skipped", model)
			continue
		}
		if row.Reason != reasonAlreadyAliased {
			t.Errorf("%s reason = %q, want %q", model, row.Reason, reasonAlreadyAliased)
		}
	}

	// 表里没有的照常提议。
	for _, want := range []struct{ model, alias string }{
		{"hy4-preview-dev", "workbuddy-hy4-preview-dev"},
		{"x-ai/grok-4.7", "cline-x-ai/grok-4.7"},
		{"cline-free/solar-mini4", "cline-cline-free/solar-mini4"},
	} {
		row, ok := proposedFor(report, want.model)
		if !ok {
			t.Errorf("%s has no row in the table and must be proposed", want.model)
			continue
		}
		if row.Alias != want.alias {
			t.Errorf("%s alias = %q, want %q", want.model, row.Alias, want.alias)
		}
	}
}

// 面板没有送别名表时，报告退回"全部列出"：多列一行可以忽略，凭名字猜测而漏报
// 真正的缺口才是代价。
func TestAliasReportWithoutTableProposesEverything(t *testing.T) {
	report := buildAliasReport(portOpenAI, []catalogEntry{
		{ID: "workbuddy-space-bunny", OwnedBy: "workbuddy"},
		{ID: "qoder-auto", OwnedBy: "qoder"},
	}, realChannels())
	if len(report.Missing) != 2 {
		t.Fatalf("with no table every attributable model is a candidate: %+v", report.Missing)
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
	// 没有别名表信息时，能归属到 provider 的模型都会列出（codex 没有 channel，不列）。
	if len(report.Missing) != 1 {
		t.Fatalf("the workbuddy model should be listed: %+v", report.Missing)
	}
	if report.Missing[0].Model != "workbuddy-space-bunny" {
		t.Errorf("model = %q, want workbuddy-space-bunny", report.Missing[0].Model)
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
	// 显式 channel 列表是权威：openai 与 cline 不在其中，没有地方可写。
	if len(report.Missing) != 2 {
		t.Fatalf("missing = %+v, want both workbuddy names", report.Missing)
	}
	if _, ok := proposedFor(report, "space-bunny"); !ok {
		t.Error("space-bunny is a real gap and must be proposed")
	}
	// openai 两条 + cline 一条都不在 channels 里，没有地方可写。
	if report.Ignored != 3 {
		t.Errorf("ignored = %d, want 3", report.Ignored)
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
	// workbuddy 多数名字带前缀，采用约定因此参与，四个都列出；
	// openai 没有前缀，不参与，其两条不提议。
	if _, ok := proposedFor(report, "space-bunny"); !ok {
		t.Fatalf("space-bunny should be proposed: %+v", report.Missing)
	}
	if _, ok := proposedFor(report, "gpt-6.1-sol"); ok {
		t.Errorf("openai does not participate here and must not be proposed: %+v", report.Missing)
	}
	if len(report.Missing) != 4 {
		t.Errorf("missing = %+v, want the four workbuddy names", report.Missing)
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
