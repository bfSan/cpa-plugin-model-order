package main

import "testing"

func skippedFor(report aliasReport, model string) (skippedAliasRow, bool) {
	for _, s := range report.Skipped {
		if s.Model == model {
			return s, true
		}
	}
	return skippedAliasRow{}, false
}

func proposedFor(report aliasReport, model string) (missingAliasRow, bool) {
	for _, m := range report.Missing {
		if m.Model == model {
			return m, true
		}
	}
	return missingAliasRow{}, false
}

// cline 的上游原名带斜杠（cline-free/*、x-ai/grok-4.7）。它们此前被判为
// "pass-through" 直接跳过，于是报告里一条 cline 建议都没有。
//
// 那个判定是错的：cline 是本部署的 provider，斜杠后面是 cline 下的来源，
// 所以 cline-x-ai/grok-4.7、cline-cline-free/solar-mini4 都是合法别名。
// 该不该加别名由操作者判断，插件只负责把清单摆出来。
func TestClineModelsAreProposedNotSkipped(t *testing.T) {
	models := []string{
		"cline-free/mimo-v2.6-flash",
		"cline-free/solar-mini4",
		"x-ai/grok-4.7",
	}
	report := buildAliasReport(
		map[string]bool{"cline": true},
		map[string]upstreamListing{"cline": pluginUpstream(models...)},
		nil,
	)

	if len(report.Missing) != len(models) {
		t.Fatalf("every cline model should be proposed, got %+v", report.Missing)
	}
	for _, model := range models {
		row, ok := proposedFor(report, model)
		if !ok {
			t.Fatalf("%s was not proposed", model)
		}
		if row.Alias != "cline-"+model {
			t.Fatalf("%s: alias = %q, want %q", model, row.Alias, "cline-"+model)
		}
		if row.Channel != "cline" {
			t.Fatalf("%s: channel = %q, want cline", model, row.Channel)
		}
	}
	// 既然都提议了，就不该再出现在跳过清单里 —— 两份清单必须互斥。
	for _, model := range models {
		if _, skipped := skippedFor(report, model); skipped {
			t.Fatalf("%s was proposed and skipped at the same time", model)
		}
	}
	if report.Ignored != 0 {
		t.Fatalf("Ignored = %d, want 0 when every model produced a row", report.Ignored)
	}
}

// 已经带 provider 前缀的上游名同样照常提议（除非表里已有行）。插件不做
// "这个名字看起来已经改好了" 的判断：漏报会让操作者以为没有缺口，多报一行随时可以忽略。
func TestPrefixedModelsAreStillProposed(t *testing.T) {
	report := buildAliasReport(
		map[string]bool{"workbuddy": true},
		map[string]upstreamListing{"workbuddy": pluginUpstream("workbuddy-hy4-preview")},
		nil,
	)

	row, ok := proposedFor(report, "workbuddy-hy4-preview")
	if !ok {
		t.Fatalf("a prefixed model must still be listed: %+v", report.Missing)
	}
	if row.Alias != "workbuddy-workbuddy-hy4-preview" {
		t.Fatalf("alias = %q, want the prefix applied verbatim", row.Alias)
	}
}

// 读不到上游名单的渠道，原因必须具名，不能只累加计数。
func TestNoUpstreamReasonIsNamed(t *testing.T) {
	report := buildAliasReport(map[string]bool{"cline": true}, nil, nil)

	if len(report.Missing) != 0 {
		t.Fatalf("nothing readable means no row to write: %+v", report.Missing)
	}
	row, ok := skippedFor(report, "")
	if !ok {
		t.Fatal("the unreadable channel must be listed with its reason")
	}
	if row.Reason != reasonNoUpstream {
		t.Fatalf("reason = %q, want %q", row.Reason, reasonNoUpstream)
	}
	if row.Provider != "cline" {
		t.Fatalf("provider = %q, want cline", row.Provider)
	}
	if report.SkippedByReason[reasonNoUpstream] != 1 {
		t.Fatalf("SkippedByReason = %v, want one entry", report.SkippedByReason)
	}
}

// 上游名单里出现空名字时，表里没有 `name` 可指向，只能具名跳过。
func TestEmptyModelNameReasonIsNamed(t *testing.T) {
	report := buildAliasReport(
		map[string]bool{"workbuddy": true},
		map[string]upstreamListing{"workbuddy": pluginUpstream("", "  ", "hy3")},
		nil,
	)

	row, ok := skippedFor(report, "")
	if !ok {
		t.Fatal("an empty upstream name must be recorded, not silently dropped")
	}
	if row.Reason != reasonNoModelName {
		t.Fatalf("reason = %q, want %q", row.Reason, reasonNoModelName)
	}
	if row.Provider != "workbuddy" {
		t.Fatalf("provider = %q, want workbuddy", row.Provider)
	}
	// 三条被服务的条目都参与判定（空名字也计入分母），所以 Captured=3；
	// 其中两条被拒，Missing + Ignored == Captured 仍然成立。
	if report.Captured != 3 {
		t.Fatalf("Captured = %d, want 3", report.Captured)
	}
	if report.Ignored != 2 {
		t.Fatalf("Ignored = %d, want 2 (the two blank names)", report.Ignored)
	}
	if _, ok := proposedFor(report, "hy3"); !ok {
		t.Fatalf("the named model must still be proposed: %+v", report.Missing)
	}
}

// Ignored 的语义是"参与判定但没生成建议的上游名数"，被隐藏的不计入 ——
// 面板那行概要按 Missing + Ignored == Captured 读。
func TestIgnoredStaysANameCount(t *testing.T) {
	report := buildAliasReport(
		map[string]bool{"workbuddy": true},
		map[string]upstreamListing{
			"workbuddy": {Models: []string{"a", "b"}, Hidden: []string{"c", "d"}, Origin: "plugin"},
		},
		nil,
	)
	// 两个被服务的都提议了，所以 Captured=2、Ignored=0。
	if report.Captured != 2 {
		t.Fatalf("Captured = %d, want 2", report.Captured)
	}
	if report.Ignored != 0 {
		t.Fatalf("Ignored = %d, want 0: both served names produced a row", report.Ignored)
	}
	// 两个隐藏的仍有记录，但不算进 Captured/Ignored。
	if len(report.Skipped) != 2 {
		t.Fatalf("Skipped = %+v, want the two hidden names", report.Skipped)
	}
	for _, name := range []string{"c", "d"} {
		if row, ok := skippedFor(report, name); !ok || row.Reason != reasonHidden {
			t.Fatalf("%s should be recorded as hidden, got %+v/%v", name, row, ok)
		}
	}
}

// 有上游模型但没有渠道时，整个 provider 只报一条，而不是把每个模型都变成一条抱怨。
func TestNoChannelIsReportedOncePerProvider(t *testing.T) {
	models := make([]string, 40)
	for i := range models {
		models[i] = "model-" + string(rune('a'+i%26))
	}
	report := buildAliasReport(
		map[string]bool{},
		map[string]upstreamListing{"openai": pluginUpstream(models...)},
		nil,
	)

	if len(report.NoChannel) != 1 {
		t.Fatalf("no_channel = %+v, want one entry for the provider", report.NoChannel)
	}
	if report.NoChannel[0].Served != len(models) {
		t.Errorf("served = %d, want %d", report.NoChannel[0].Served, len(models))
	}
	if len(report.Skipped) != 0 {
		t.Errorf("skipped = %+v, want none: no_channel is the reason, stated once", report.Skipped)
	}
}
