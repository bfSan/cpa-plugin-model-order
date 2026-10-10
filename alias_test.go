package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// pluginUpstream builds a provider's upstream listing the way the panel sends
// it: names straight from the provider plugin's own model route.
func pluginUpstream(models ...string) upstreamListing {
	return upstreamListing{Models: models, Origin: "plugin"}
}

// authUpstream is the fallback shape, for providers with no model route.
func authUpstream(models ...string) upstreamListing {
	return upstreamListing{Models: models, Origin: "auth"}
}

// The upstream name is what the table's `name` column points at, so a name the
// table does not cover is the gap the report exists to find.
func TestAliasReportProposesUpstreamNamesMissingFromTheTable(t *testing.T) {
	channels := map[string]bool{"workbuddy": true, "qoder": true}
	upstream := map[string]upstreamListing{
		"workbuddy": pluginUpstream("hy3", "gemini-3.5-flash", "space-bunny"),
		"qoder":     pluginUpstream("auto"),
	}
	existing := map[string]map[string]bool{
		// Covered: these have rows already.
		"workbuddy": {"space-bunny": true, "workbuddy-space-bunny": true},
		"qoder":     {"auto": true, "qoder-auto": true},
	}
	report := buildAliasReport(channels, upstream, existing)

	want := []struct{ model, alias, channel string }{
		{"gemini-3.5-flash", "workbuddy-gemini-3.5-flash", "workbuddy"},
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
	// 两个已覆盖的名字不提议，但要记明原因，不能只是消失。
	for _, model := range []string{"space-bunny", "auto"} {
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
}

// 表里已覆盖的模型，可能以上游原名出现（行还没生效），也可能以别名出现
// （CPA 已替换过）。两种形状都要算"已覆盖"，否则同一行会被重复建议。
func TestAliasReportCoversBothNameAndAliasShapes(t *testing.T) {
	channels := map[string]bool{"workbuddy": true}
	upstream := map[string]upstreamListing{
		// 前者是上游原名，后者是别名已经生效时的形状。
		"workbuddy": pluginUpstream("space-bunny", "workbuddy-space-bunny", "brand-new"),
	}
	existing := map[string]map[string]bool{
		"workbuddy": {"space-bunny": true, "workbuddy-space-bunny": true},
	}
	report := buildAliasReport(channels, upstream, existing)

	if len(report.Missing) != 1 {
		t.Fatalf("only brand-new is a gap: %+v", report.Missing)
	}
	if report.Missing[0].Model != "brand-new" {
		t.Errorf("model = %q, want brand-new", report.Missing[0].Model)
	}
}

// 面板没送别名表时不猜：全部列出。多列一行可以忽略，凭名字猜测而漏掉真缺口才是代价。
func TestAliasReportWithoutTableProposesEverything(t *testing.T) {
	report := buildAliasReport(
		map[string]bool{"workbuddy": true, "qoder": true},
		map[string]upstreamListing{
			"workbuddy": pluginUpstream("space-bunny", "hy3"),
			"qoder":     pluginUpstream("auto"),
		},
		nil,
	)
	if len(report.Missing) != 3 {
		t.Fatalf("with no table every upstream model is a candidate: %+v", report.Missing)
	}
}

// 有上游模型但没有别名渠道时，行没地方可写。这要单独报出来（面板才好提示建渠道），
// 而不是把 71 个模型变成 71 条一模一样的抱怨。
func TestAliasReportNamesProvidersWithoutAChannel(t *testing.T) {
	channels := map[string]bool{"workbuddy": true}
	upstream := map[string]upstreamListing{
		"workbuddy": pluginUpstream("hy3"),
		// CPA 内置的 openai 就长这样：模型名是本来就想对外提供的名字，
		// 而且没有 openai 这个别名渠道，给它造行才是错的。
		"openai": pluginUpstream("gpt-5.5", "gpt-6.1-sol"),
	}
	report := buildAliasReport(channels, upstream, nil)

	if len(report.Missing) != 1 || report.Missing[0].Model != "hy3" {
		t.Fatalf("only the workbuddy model has a channel to write to: %+v", report.Missing)
	}
	if len(report.NoChannel) != 1 {
		t.Fatalf("no_channel = %+v, want one entry for openai", report.NoChannel)
	}
	got := report.NoChannel[0]
	if got.Channel != "openai" || got.Served != 2 {
		t.Errorf("no_channel entry = %+v, want openai with 2 served models", got)
	}
	// 没有渠道的 provider 不产生跳过记录：它的模型不是"被跳过"，是没地方写。
	if _, ok := skippedFor(report, "gpt-5.5"); ok {
		t.Error("a provider with no channel should be reported in no_channel, not as a skip row")
	}
}

// 渠道读不到上游名单时，"没有缺失别名"并不等于没问题 —— 是无从判定，必须说清楚。
func TestAliasReportNamesChannelsWithNoUpstreamList(t *testing.T) {
	channels := map[string]bool{"workbuddy": true, "trae": true}
	upstream := map[string]upstreamListing{
		"workbuddy": pluginUpstream("hy3"),
		// trae 没有任何条目：面板没能读到它的上游模型。
	}
	report := buildAliasReport(channels, upstream, nil)

	var trae *aliasSourceStatus
	for i := range report.Sources {
		if report.Sources[i].Channel == "trae" {
			trae = &report.Sources[i]
		}
	}
	if trae == nil {
		t.Fatalf("trae must appear in sources: %+v", report.Sources)
	}
	if trae.Origin != "none" {
		t.Errorf("origin = %q, want none", trae.Origin)
	}
	row, ok := skippedFor(report, "")
	if !ok {
		t.Fatal("an unreadable channel must be recorded, or it reads as a clean bill of health")
	}
	if row.Reason != reasonNoUpstream {
		t.Errorf("reason = %q, want %q", row.Reason, reasonNoUpstream)
	}
}

// 渠道自己隐藏的模型到不了客户端，不需要别名；但它们要出现在报告里，
// 否则操作者会以为这些模型凭空消失了。
func TestAliasReportRecordsHiddenModelsWithoutProposingThem(t *testing.T) {
	channels := map[string]bool{"cline": true}
	upstream := map[string]upstreamListing{
		"cline": {
			Models: []string{"cline-free/step-5-preview"},
			Hidden: []string{"cline-pass/glm-5.3", "cline-cloud/kimi-k3"},
			Origin: "plugin",
		},
	}
	report := buildAliasReport(channels, upstream, nil)

	if len(report.Missing) != 1 || report.Missing[0].Model != "cline-free/step-5-preview" {
		t.Fatalf("only the served model should be proposed: %+v", report.Missing)
	}
	for _, model := range []string{"cline-pass/glm-5.3", "cline-cloud/kimi-k3"} {
		if _, ok := proposedFor(report, model); ok {
			t.Errorf("%s is hidden by the provider and must not be proposed", model)
		}
		row, ok := skippedFor(report, model)
		if !ok {
			t.Errorf("%s should be recorded as skipped", model)
			continue
		}
		if row.Reason != reasonHidden {
			t.Errorf("%s reason = %q, want %q", model, row.Reason, reasonHidden)
		}
	}
	// 统计要能读出"总共看了多少、隐藏了多少、真判了多少"。
	if len(report.Sources) != 1 {
		t.Fatalf("sources = %+v, want one entry", report.Sources)
	}
	src := report.Sources[0]
	if src.Total != 3 || src.Served != 1 || src.Hidden != 2 {
		t.Errorf("source = %+v, want total 3 / served 1 / hidden 2", src)
	}
}

// 同一个上游名被两个渠道服务时，每个渠道各需要一行。
func TestAliasReportProposesOneRowPerChannel(t *testing.T) {
	channels := map[string]bool{"trae": true, "workbuddy": true}
	upstream := map[string]upstreamListing{
		"trae":      authUpstream("kimi-k3"),
		"workbuddy": pluginUpstream("kimi-k3"),
	}
	report := buildAliasReport(channels, upstream, nil)

	if len(report.Missing) != 2 {
		t.Fatalf("missing = %d rows, want 2: %+v", len(report.Missing), report.Missing)
	}
	if report.Missing[0].Channel != "trae" || report.Missing[1].Channel != "workbuddy" {
		t.Errorf("channels = %q/%q, want trae/workbuddy",
			report.Missing[0].Channel, report.Missing[1].Channel)
	}
	if report.Missing[0].Alias != "trae-kimi-k3" || report.Missing[1].Alias != "workbuddy-kimi-k3" {
		t.Errorf("aliases = %q/%q, want trae-kimi-k3/workbuddy-kimi-k3",
			report.Missing[0].Alias, report.Missing[1].Alias)
	}
}

// 来源要回显出来：只有插件路由给的上游名单才是"它真正会提供的名字"，
// 认证目录是退而求其次。操作者需要知道这次判定站在哪个来源上。
func TestAliasReportEchoesUpstreamOrigins(t *testing.T) {
	channels := map[string]bool{"workbuddy": true, "trae": true}
	upstream := map[string]upstreamListing{
		"workbuddy": pluginUpstream("hy3"),
		"trae":      authUpstream("glm-5.3"),
	}
	report := buildAliasReport(channels, upstream, nil)

	origins := map[string]string{}
	for _, src := range report.Sources {
		origins[src.Channel] = src.Origin
	}
	if origins["workbuddy"] != "plugin" {
		t.Errorf("workbuddy origin = %q, want plugin", origins["workbuddy"])
	}
	if origins["trae"] != "auth" {
		t.Errorf("trae origin = %q, want auth", origins["trae"])
	}
}

// Captured 是分母：参与判定的上游名数量（被渠道隐藏的不算，它们到不了客户端）。
// Ignored 是其中没生成建议的数量，所以 Missing + Ignored == Captured 必须成立，
// 否则面板那行概要会自相矛盾。
func TestAliasReportCountsAddUp(t *testing.T) {
	channels := map[string]bool{"workbuddy": true}
	upstream := map[string]upstreamListing{
		"workbuddy": {
			Models: []string{"hy3", "space-bunny", "already-good"},
			Hidden: []string{"hidden-model"},
			Origin: "plugin",
		},
	}
	existing := map[string]map[string]bool{
		"workbuddy": {"already-good": true},
	}
	report := buildAliasReport(channels, upstream, existing)

	// 三个被服务的名字参与判定；hidden-model 不算 Captured（它到不了客户端）。
	if report.Captured != 3 {
		t.Errorf("captured = %d, want 3", report.Captured)
	}
	// 只有已覆盖的那一个没生成建议；隐藏的不算进 Ignored。
	if report.Ignored != 1 {
		t.Errorf("ignored = %d, want 1", report.Ignored)
	}
	if len(report.Missing)+report.Ignored != report.Captured {
		t.Errorf("missing(%d) + ignored(%d) != captured(%d)",
			len(report.Missing), report.Ignored, report.Captured)
	}
	// 隐藏的那个仍然要有一条记录，否则它会凭空消失。
	if row, ok := skippedFor(report, "hidden-model"); !ok || row.Reason != reasonHidden {
		t.Errorf("hidden-model should be recorded as hidden, got %+v/%v", row, ok)
	}
}

// 面板送来的 provider 名大小写不统一，匹配前必须归一化，否则工作白做。
func TestAliasReportNormalisesProviderCase(t *testing.T) {
	channels := map[string]bool{"workbuddy": true}
	upstream := map[string]upstreamListing{
		"WorkBuddy": pluginUpstream("hy3"),
	}
	report := buildAliasReport(channels, upstream, nil)

	if len(report.Missing) != 1 {
		t.Fatalf("the listing's case must not change the result: %+v", report.Missing)
	}
	if report.Missing[0].Channel != "workbuddy" {
		t.Errorf("channel = %q, want workbuddy", report.Missing[0].Channel)
	}
	if len(report.NoChannel) != 0 {
		t.Errorf("no_channel = %+v, want none: the channel does exist", report.NoChannel)
	}
}

func TestProviderPrefix(t *testing.T) {
	if got := providerPrefix("workbuddy"); got != "workbuddy-" {
		t.Errorf("providerPrefix = %q, want workbuddy-", got)
	}
	if got := providerPrefix("  WorkBuddy  "); got != "workbuddy-" {
		t.Errorf("providerPrefix should normalise case and space, got %q", got)
	}
}

func TestSortedKeys(t *testing.T) {
	got := sortedKeys(map[string]bool{"workbuddy": true, "qoder": true, "openai": false})
	if len(got) != 2 || got[0] != "qoder" || got[1] != "workbuddy" {
		t.Errorf("sortedKeys = %v, want [qoder workbuddy]", got)
	}
}

func TestSortedUpstreamKeys(t *testing.T) {
	got := sortedUpstreamKeys(map[string]upstreamListing{"z": {}, "a": {}, "m": {}})
	if len(got) != 3 || got[0] != "a" || got[1] != "m" || got[2] != "z" {
		t.Errorf("sortedUpstreamKeys = %v, want [a m z]", got)
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
