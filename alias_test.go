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

// 报告不再替操作者筛掉"看起来已经改好"的名字，而是全部列出并标记。
// cline 的可见模型全是上游原名（cline-free/*、x-ai/grok-4.7），它们在旧规则下
// 被整组跳过，报告里一条 cline 建议都没有 —— 但 cline-anthropic/claude-opus-5.5
// 这类别名是合法的（cline 是本部署的 provider，斜杠后是 cline 下的来源）。
//
// 反过来 qoder-auto 这种确实是 CPA 别名替换后的结果，套前缀会变成
// qoder-qoder-auto。两者从名字上分不出来，所以标记出来交给操作者判断。
func TestAliasReportListsEverythingAndFlagsPrefixed(t *testing.T) {
	report := buildAliasReport(portOpenAI, realCatalogEntries(), realChannels())
	// 每一个能归属到 provider 的模型都应出现在清单里（qoder 2 + workbuddy 6 +
	// cline 5），不再有"看起来改好了就跳过"的静默丢弃。
	if len(report.Missing) != 13 {
		t.Fatalf("missing = %d rows, want every attributable model: %+v", len(report.Missing), report.Missing)
	}
	// 本身已带 provider 前缀的，必须标出来。
	flagged := map[string]bool{}
	for _, row := range report.Missing {
		flagged[row.Model] = row.AlreadyPrefixed
	}
	for _, model := range []string{"qoder-auto", "workbuddy-auto", "workbuddy-space-bunny"} {
		if !flagged[model] {
			t.Errorf("%s should be flagged as already prefixed", model)
		}
	}
	for _, model := range []string{"space-bunny", "hy4-preview-dev", "x-ai/grok-4.7"} {
		if flagged[model] {
			t.Errorf("%s must not be flagged as already prefixed", model)
		}
	}
	// cline-free/* 以 "cline-" 开头，所以会被标记 —— 这正是要摆给操作者看的信息：
	// 名字带前缀不等于已经配过别名。cline 的上游原名就该套上 cline- 前缀。
	if !flagged["cline-free/solar-mini4"] {
		t.Error("cline-free/* starts with the cline prefix and must be flagged for review")
	}
	// cline 的上游原名确实会进来，且别名就是 cline- 加原名。
	if row, ok := proposedFor(report, "x-ai/grok-4.7"); !ok || row.Alias != "cline-x-ai/grok-4.7" {
		t.Errorf("cline upstream name should be proposed as cline-x-ai/grok-4.7, got %+v", row)
	}
	if row, ok := proposedFor(report, "cline-free/solar-mini4"); !ok || row.Alias != "cline-cline-free/solar-mini4" {
		t.Errorf("cline-free/* should be proposed too, got %+v", row)
	}

	// 三条真正的裸名建议依然正确（按模型查找，行按 channel+model 排序）。
	want := []struct{ model, alias string }{
		{"hy4-preview-dev", "workbuddy-hy4-preview-dev"},
		{"hy4-preview-x", "workbuddy-hy4-preview-x"},
		{"space-bunny", "workbuddy-space-bunny"},
	}
	for _, expect := range want {
		row, ok := proposedFor(report, expect.model)
		if !ok {
			t.Fatalf("%s missing from the report", expect.model)
		}
		if row.Alias != expect.alias {
			t.Errorf("%s alias = %q, want %q", expect.model, row.Alias, expect.alias)
		}
		if row.Channel != "workbuddy" {
			t.Errorf("%s channel = %q, want workbuddy", expect.model, row.Channel)
		}
		if row.AlreadyPrefixed {
			t.Errorf("%s is a bare name and must not be flagged as prefixed", expect.model)
		}
	}
	// 只有三条真的没有 channel 可写：没有 owned_by 的一条、空 id 的一条，
	// 以及 codex 那条（codex 不在 channel 列表里，没有别名通道可写）。
	if report.Ignored != 3 {
		t.Errorf("ignored = %d, want 3", report.Ignored)
	}
}

// cline 的 provider/model 形式原名照样进清单。cline 是本部署的 provider，
// 斜杠后面是 cline 下的来源，所以 cline-anthropic/claude-opus-5.5 是合法别名。
func TestAliasReportProposesProviderModelNames(t *testing.T) {
	report := buildAliasReport(portOpenAI, []catalogEntry{
		{ID: "anthropic/claude-opus-5.5", OwnedBy: "cline"},
		{ID: "openai/gpt-6.1-sol", OwnedBy: "cline"},
	}, realChannels())
	if len(report.Missing) != 2 {
		t.Fatalf("provider/model names should be proposed: %+v", report.Missing)
	}
	row, ok := proposedFor(report, "anthropic/claude-opus-5.5")
	if !ok || row.Alias != "cline-anthropic/claude-opus-5.5" {
		t.Errorf("alias = %q, want cline-anthropic/claude-opus-5.5", row.Alias)
	}
}

// 一个已经带 provider 前缀的名字同样会被提议。插件不去判断"这个名字是不是
// 已经改好了" —— 操作者能在列表上看到它、也知道自己做过什么，比插件猜更可靠。
// 仍然提议的代价只是多一行可忽略的建议；漏报的代价是操作者以为没有缺口。
func TestAliasReportProposesAlreadyPrefixedModels(t *testing.T) {
	report := buildAliasReport(portOpenAI, []catalogEntry{
		{ID: "workbuddy-space-bunny", OwnedBy: "workbuddy"},
		{ID: "qoder-auto", OwnedBy: "qoder"},
	}, realChannels())
	if len(report.Missing) != 2 {
		t.Fatalf("every captured model should be proposed, got %+v", report.Missing)
	}
	for _, row := range report.Missing {
		if row.Alias != row.Channel+"-"+row.Model {
			t.Errorf("alias = %q, want %s-%s", row.Alias, row.Channel, row.Model)
		}
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
	// workbuddy-space-bunny 已带前缀，仍会列出并标记（codex 没有 channel，不列）。
	if len(report.Missing) != 1 {
		t.Fatalf("the workbuddy model should be listed: %+v", report.Missing)
	}
	if !report.Missing[0].AlreadyPrefixed {
		t.Errorf("%s carries its provider prefix and must be flagged: %+v",
			report.Missing[0].Model, report.Missing[0])
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
func TestAliasReportFlagsModelsAlreadyCarryingThePrefix(t *testing.T) {
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
	// openai 四个模型因为没有 channel 依旧不提议；workbuddy 的两个都会列出，
	// 其中 workbuddy-auto 由标记说明"它已经带前缀了"。
	if len(report.Missing) != 2 {
		t.Fatalf("missing = %+v, want both workbuddy names", report.Missing)
	}
	if row, ok := proposedFor(report, "space-bunny"); !ok || row.AlreadyPrefixed {
		t.Errorf("space-bunny is a bare name, want it proposed unflagged: %+v", row)
	}
	if row, ok := proposedFor(report, "workbuddy-auto"); !ok || !row.AlreadyPrefixed {
		t.Errorf("workbuddy-auto carries its prefix, want it proposed and flagged: %+v", row)
	}
	// openai 四个没有可写通道（无 channel 列表时它不参与），另加无 id 的一个。
	if report.Ignored != 4 {
		t.Errorf("ignored = %d, want 4", report.Ignored)
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
	// 显式 channel 列表是权威：openai 与 cline 不在其中就不提议。
	// workbuddy 的两个都列出，其中一个带标记。
	if len(report.Missing) != 2 {
		t.Fatalf("missing = %+v, want both workbuddy names", report.Missing)
	}
	if row, ok := proposedFor(report, "space-bunny"); !ok || row.AlreadyPrefixed {
		t.Errorf("space-bunny should be proposed unflagged: %+v", row)
	}
	// openai 两条 + cline 一条 + 无 id 一条 = 4（workbuddy-auto 已改为列出）
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
	// workbuddy 采用前缀约定因此参与，它的四个名字全部列出（三个带标记）；
	// openai 无前缀不参与，其两条不提议。
	if row, ok := proposedFor(report, "space-bunny"); !ok || row.AlreadyPrefixed {
		t.Fatalf("space-bunny should be proposed unflagged: %+v", row)
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
