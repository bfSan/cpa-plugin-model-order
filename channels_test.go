package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Several provider plugins can offer the same public model with different
// capabilities, and CPA keeps only the last registered channel's opinion. These
// tests pin the two aggregation rules and the reason they are not symmetrical.

func facts(provider string, def, max int64, levels ...string) providerFacts {
	return providerFacts{Provider: provider, ContextDefault: def, ContextMax: max, Levels: levels}
}

// Context is the intersection of the channels' maxima: the advertised number must
// be one every channel can serve.
func TestAggregateContextTakesSmallestMaximum(t *testing.T) {
	got, _ := aggregateChannels([]providerFacts{
		facts("trae", 1000000, 1000000),
		facts("workbuddy-cn", 512000, 512000, "medium"),
	})
	if got != 512000 {
		t.Fatalf("context = %d, want the smaller channel maximum 512000", got)
	}
}

// A channel's default tier is not its limit. qoder defaults dfmodel to 200K but
// offers a 1M tier, so comparing defaults would understate it by 5x.
func TestAggregateContextUsesTierMaximumNotDefault(t *testing.T) {
	got, _ := aggregateChannels([]providerFacts{
		facts("qoder-cn", 200000, 1000000, "low", "high", "max"),
		facts("qoder-intl", 200000, 1000000, "low", "high", "max"),
	})
	if got != 1000000 {
		t.Fatalf("context = %d, want the tier maximum 1000000", got)
	}
}

// Reasoning is the union: a level a channel rejects is at worst one rejected
// parameter, while intersecting can remove thinking entirely.
func TestAggregateReasoningTakesUnion(t *testing.T) {
	_, levels := aggregateChannels([]providerFacts{
		facts("workbuddy-cn", 1000000, 1000000, "low", "high", "max"),
		facts("trae", 1000000, 1000000, "high", "low", "xhigh"),
	})
	want := []string{"low", "high", "xhigh", "max"}
	if len(levels) != len(want) {
		t.Fatalf("levels = %v, want %v", levels, want)
	}
	for i := range want {
		if levels[i] != want[i] {
			t.Fatalf("levels = %v, want %v (weakest to strongest)", levels, want)
		}
	}
}

// The case that rules out an intersection: one channel reports four levels and
// another reports none. Intersecting leaves nothing, which silently disables
// thinking for a model that clearly supports it.
func TestAggregateReasoningSurvivesAChannelReportingNothing(t *testing.T) {
	_, levels := aggregateChannels([]providerFacts{
		facts("workbuddy-global", 1000000, 1000000, "medium"),
		facts("trae", 1000000, 1000000),
	})
	if len(levels) != 1 || levels[0] != "medium" {
		t.Fatalf("levels = %v, want [medium] rather than an empty list", levels)
	}
}

// Levels come back in strength order regardless of the order channels listed
// them, because the panel renders the list as-is.
func TestAggregateReasoningOrdersLevels(t *testing.T) {
	_, levels := aggregateChannels([]providerFacts{
		facts("a", 1, 1, "max", "low"),
		facts("b", 1, 1, "xhigh", "medium", "high"),
	})
	want := []string{"low", "medium", "high", "xhigh", "max"}
	for i := range want {
		if levels[i] != want[i] {
			t.Fatalf("levels = %v, want %v", levels, want)
		}
	}
}

// A single channel needs no aggregation: its own numbers are the truth and the
// catalog entry already carries them.
func TestApplyChannelFactsSkipsSingleChannelModels(t *testing.T) {
	capabilities.reset()
	defer capabilities.reset()
	capabilities.byID = map[string]capability{"solo": {ContextLength: 111, MaxContextLength: 111}}
	capabilities.lower = map[string]string{"solo": "solo"}

	applied := capabilities.applyChannelFacts(map[string][]providerFacts{
		"solo": {facts("workbuddy-cn", 999, 999)},
	})
	if applied != 0 {
		t.Fatalf("applied = %d, want 0 for a single-channel model", applied)
	}
	if got, _ := capabilities.lookup("solo"); got.ContextLength != 111 {
		t.Fatalf("a single channel must not overwrite the catalog value, got %d", got.ContextLength)
	}
}

// A multi-channel model has its catalog-derived numbers replaced by the
// aggregated ones, and loses the single-channel default level because that
// default does not hold across channels.
func TestApplyChannelFactsOverridesMultiChannelModel(t *testing.T) {
	capabilities.reset()
	defer capabilities.reset()
	capabilities.byID = map[string]capability{
		"dfmodel": {ContextLength: 200000, MaxContextLength: 200000,
			ReasoningLevels: []string{"low"}, DefaultReasoningLevel: "high"},
	}
	capabilities.lower = map[string]string{"dfmodel": "dfmodel"}

	applied := capabilities.applyChannelFacts(map[string][]providerFacts{
		"dfmodel": {
			facts("qoder-cn", 200000, 1000000, "low", "high", "max"),
			facts("qoder-intl", 200000, 1000000, "low", "high", "max"),
		},
	})
	if applied != 1 {
		t.Fatalf("applied = %d, want 1", applied)
	}
	got, ok := capabilities.lookup("dfmodel")
	if !ok {
		t.Fatal("model must still be present")
	}
	if got.ContextLength != 1000000 {
		t.Fatalf("context = %d, want the 1M tier both channels offer", got.ContextLength)
	}
	if got.DefaultReasoningLevel != "" {
		t.Fatalf("default level = %q, want it cleared: one channel's default is not another's", got.DefaultReasoningLevel)
	}
}

// The channel table is exposed for the panel so an operator can see which
// channels produced an aggregated number.
func TestChannelReportExposesOnlyMultiChannelModels(t *testing.T) {
	capabilities.reset()
	defer capabilities.reset()
	capabilities.applyChannelFacts(map[string][]providerFacts{
		"solo":   {facts("a", 1, 1, "low")},
		"shared": {facts("a", 1, 1, "low"), facts("b", 2, 2, "high")},
	})
	report := capabilities.channelReport()
	if _, ok := report["solo"]; ok {
		t.Fatal("single-channel models must not appear in the channel report")
	}
	if entries, ok := report["shared"]; !ok || len(entries) != 2 {
		t.Fatalf("shared model must report both channels, got %v", report["shared"])
	}
}

// The parsers have to survive the shapes the plugins actually write, and an
// unreadable document must contribute nothing rather than fail the refresh.
func TestParseWorkbuddyCatalog(t *testing.T) {
	raw := []byte(`{"realm":"cn","models":[
		{"id":"deepseek-v4.1-flash","context_length":1000000,
		 "supported_context_lengths":[300000,600000,1000000],
		 "supported_efforts":["low","high","max"]}]}`)
	got := parseWorkbuddyCatalog(raw, "workbuddy")
	fact, ok := got["deepseek-v4.1-flash"]
	if !ok {
		t.Fatal("model must be parsed")
	}
	if fact.Provider != "workbuddy-cn" {
		t.Fatalf("provider = %q, want the realm appended", fact.Provider)
	}
	if fact.ContextMax != 1000000 || fact.ContextDefault != 1000000 {
		t.Fatalf("facts = %+v", fact)
	}
	if len(fact.Levels) != 3 {
		t.Fatalf("levels = %v", fact.Levels)
	}
}

func TestParseQoderCatalog(t *testing.T) {
	raw := []byte(`{"region":"intl","models":[{"ID":"dfmodel"}],
		"region_facts":{"dfmodel":{"context_length":200000,
		 "context_tiers":[{"tokens":200000},{"tokens":400000},{"tokens":1000000}],
		 "thinking":{"levels":["low","high","max"]}}}}`)
	got := parseQoderCatalog(raw, "qoder")
	fact, ok := got["dfmodel"]
	if !ok {
		t.Fatal("model must be parsed")
	}
	if fact.Provider != "qoder-intl" {
		t.Fatalf("provider = %q", fact.Provider)
	}
	// The default is 200K but the tiers reach 1M, and the intersection has to use
	// the tier maximum or it would understate the channel by 5x.
	if fact.ContextMax != 1000000 {
		t.Fatalf("context max = %d, want 1000000 from the tiers", fact.ContextMax)
	}
	if fact.ContextDefault != 200000 {
		t.Fatalf("context default = %d, want 200000", fact.ContextDefault)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if got := parseWorkbuddyCatalog([]byte("not json"), "workbuddy"); len(got) != 0 {
		t.Fatalf("garbage must yield nothing, got %v", got)
	}
	if got := parseQoderCatalog([]byte(`{"models":"wrong"}`), "qoder"); len(got) != 0 {
		t.Fatalf("malformed models must yield nothing, got %v", got)
	}
}

// One provider writes a catalog file per credential and per realm, so the same
// channel label arrives several times. Keeping each copy would list a provider
// twice and let it outvote the other channels.
func TestGatherKeepsOneEntryPerChannel(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "CLIProxyAPI", "workbuddy", "model-catalog", "models")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	doc := `{"realm":"cn","models":[{"id":"m","context_length":1000,"supported_context_lengths":[1000],"supported_efforts":["low"]}]}`
	for _, name := range []string{"a.json", "b.json"} {
		if err := os.WriteFile(filepath.Join(base, name), []byte(doc), 0o644); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	got := gatherProviderFacts(dir)["m"]
	if len(got) != 1 {
		t.Fatalf("channels = %d, want 1 after de-duplication: %+v", len(got), got)
	}
	if got[0].Provider != "workbuddy-cn" {
		t.Fatalf("channel = %q", got[0].Provider)
	}
}

// Trae keeps its catalog inside the credential file rather than a catalog
// directory, so it is read from the auth directory. A credential belonging to
// another provider must contribute nothing.
func TestParseTraeAuth(t *testing.T) {
	raw := []byte(`{"provider":"trae","auth":{"variant":"cn"},
		"model_cache":{"realm":"cn","models":[
			{"ID":"deepseek-v4.1-flash","ContextLength":1000000,
			 "Thinking":{"Levels":["high","low","xhigh"]}}]}}`)
	got := parseTraeAuth(raw, "trae")
	fact, ok := got["deepseek-v4.1-flash"]
	if !ok {
		t.Fatal("model must be parsed from the credential snapshot")
	}
	if fact.Provider != "trae-cn" {
		t.Fatalf("provider = %q", fact.Provider)
	}
	if fact.ContextMax != 1000000 || len(fact.Levels) != 3 {
		t.Fatalf("facts = %+v", fact)
	}

	if other := parseTraeAuth([]byte(`{"provider":"qoder","model_cache":{"models":[{"ID":"x"}]}}`), "trae"); len(other) != 0 {
		t.Fatalf("a non-Trae credential must contribute nothing, got %v", other)
	}
}

// The deployment-level check that the two rules compose: a model one channel
// caps at 512K and another serves at 1M advertises 512K, while its reasoning
// levels come from every channel that reports any.
func TestAggregateCombinesBothRules(t *testing.T) {
	ctx, levels := aggregateChannels([]providerFacts{
		facts("trae-cn", 1000000, 1000000),
		facts("workbuddy-cn", 512000, 512000, "medium"),
	})
	if ctx != 512000 {
		t.Fatalf("context = %d, want 512000 (intersection)", ctx)
	}
	if len(levels) != 1 || levels[0] != "medium" {
		t.Fatalf("levels = %v, want the union [medium]", levels)
	}
}

// A refresh rewrites every record from the catalog. Aggregating on write meant
// the next refresh replaced the merged levels with whichever single channel CPA
// served, which is exactly how deepseek-v4.1-flash lost its "max" level while
// trae happened to be the last registrant. Applying the table on read makes the
// result independent of refresh order.
func TestAggregationSurvivesCatalogRefresh(t *testing.T) {
	capabilities.reset()
	defer capabilities.reset()

	// First the catalog populates the store, as learnCatalogBody does.
	capabilities.recordCodexCatalog([][]byte{
		[]byte(`{"slug":"deepseek-v4.1-flash","context_window":1000000,` +
			`"supported_reasoning_levels":[{"effort":"high"},{"effort":"low"},{"effort":"xhigh"}]}`),
	})
	if got, _ := capabilities.lookup("deepseek-v4.1-flash"); len(got.ReasoningLevels) != 3 {
		t.Fatalf("precondition: want the 3 catalog levels, got %v", got.ReasoningLevels)
	}

	capabilities.applyChannelFacts(map[string][]providerFacts{
		"deepseek-v4.1-flash": {
			facts("trae-cn", 1000000, 1000000, "high", "low", "xhigh"),
			facts("workbuddy-cn", 1000000, 1000000, "low", "high", "max"),
		},
	})
	got, _ := capabilities.lookup("deepseek-v4.1-flash")
	want := []string{"low", "high", "xhigh", "max"}
	if len(got.ReasoningLevels) != len(want) {
		t.Fatalf("after aggregation levels = %v, want %v", got.ReasoningLevels, want)
	}

	// Now the next refresh writes the single-channel record again. The merged
	// result must still be what callers see.
	capabilities.recordCodexCatalog([][]byte{
		[]byte(`{"slug":"deepseek-v4.1-flash","context_window":1000000,` +
			`"supported_reasoning_levels":[{"effort":"high"},{"effort":"low"},{"effort":"xhigh"}]}`),
	})
	got, _ = capabilities.lookup("deepseek-v4.1-flash")
	if len(got.ReasoningLevels) != len(want) {
		t.Fatalf("a refresh must not undo aggregation: levels = %v, want %v", got.ReasoningLevels, want)
	}
	for i := range want {
		if got.ReasoningLevels[i] != want[i] {
			t.Fatalf("levels = %v, want %v", got.ReasoningLevels, want)
		}
	}
}

// Provider catalogs use internal IDs while /v1/models serves oauth aliases.
// Without applying that table first, qwen3.8-max flips between qoder's medium
// and Trae's high whenever registration order changes instead of exposing both.
func TestGatherAppliesOAuthAliasesBeforeUnion(t *testing.T) {
	dir := t.TempDir()
	oldAuthDir, oldConfigPath := authDir, cpaConfigPath
	authDir = filepath.Join(dir, "auths")
	cpaConfigPath = filepath.Join(dir, "config.yaml")
	t.Cleanup(func() {
		authDir = oldAuthDir
		cpaConfigPath = oldConfigPath
	})

	qoderDir := filepath.Join(dir, "CLIProxyAPI", "qoder", "model-catalog")
	if err := os.MkdirAll(qoderDir, 0o755); err != nil {
		t.Fatalf("setup qoder: %v", err)
	}
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("setup trae: %v", err)
	}
	qoder := `{"region":"cn","models":[{"ID":"qmodel_38max"}],"region_facts":{"qmodel_38max":{"context_length":1000000,"thinking":{"levels":["low","medium","xhigh"]}}}}`
	trae := `{"provider":"trae","auth":{"variant":"cn"},"model_cache":{"realm":"cn","models":[{"ID":"qwen3.8-max","ContextLength":1000000,"Thinking":{"Levels":["high","low","xhigh"]}}]}}`
	config := "oauth:\n  model-alias:\n    qoder:\n      - name: qmodel_38max\n        alias: qwen3.8-max\n"
	if err := os.WriteFile(filepath.Join(qoderDir, "catalog.json"), []byte(qoder), 0o644); err != nil {
		t.Fatalf("write qoder: %v", err)
	}
	if err := os.WriteFile(filepath.Join(authDir, "trae.json"), []byte(trae), 0o644); err != nil {
		t.Fatalf("write trae: %v", err)
	}
	if err := os.WriteFile(cpaConfigPath, []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	channels := gatherProviderFacts(dir)["qwen3.8-max"]
	if len(channels) != 2 {
		t.Fatalf("channels = %+v, want qoder and trae under the public alias", channels)
	}
	_, levels := aggregateChannels(channels)
	want := []string{"low", "medium", "high", "xhigh"}
	if len(levels) != len(want) {
		t.Fatalf("levels = %v, want %v", levels, want)
	}
	for i := range want {
		if levels[i] != want[i] {
			t.Fatalf("levels = %v, want %v", levels, want)
		}
	}
}
