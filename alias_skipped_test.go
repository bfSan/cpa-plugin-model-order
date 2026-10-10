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

// cline 的可见模型全是上游原名（cline-free/*、x-ai/grok-4.7）。它们此前被判为
// "pass-through" 直接跳过，于是报告里一条 cline 建议都没有。
//
// 那个判定是错的：cline 是本部署的 provider，斜杠后面是 cline 下的来源，
// 所以 cline-x-ai/grok-4.7、cline-cline-free/solar-mini4 都是合法别名。
// 该不该加别名由操作者判断，插件只负责把清单摆出来。
func TestClineModelsAreProposedNotSkipped(t *testing.T) {
	entries := []catalogEntry{
		{ID: "cline-free/mimo-v2.6-flash", OwnedBy: "cline"},
		{ID: "cline-free/solar-mini4", OwnedBy: "cline"},
		{ID: "x-ai/grok-4.7", OwnedBy: "cline"},
	}
	report := buildAliasReportWithProviders("openai", entries, map[string]bool{"cline": true}, nil)

	if len(report.Missing) != len(entries) {
		t.Fatalf("every cline model should be proposed, got %+v", report.Missing)
	}
	for _, entry := range entries {
		row, ok := proposedFor(report, entry.ID)
		if !ok {
			t.Fatalf("%s was not proposed", entry.ID)
		}
		if row.Alias != "cline-"+entry.ID {
			t.Fatalf("%s: alias = %q, want %q", entry.ID, row.Alias, "cline-"+entry.ID)
		}
		if row.Channel != "cline" {
			t.Fatalf("%s: channel = %q, want cline", entry.ID, row.Channel)
		}
	}
	// 既然都提议了，就不该再出现在跳过清单里 —— 两份清单必须互斥。
	for _, entry := range entries {
		if _, skipped := skippedFor(report, entry.ID); skipped {
			t.Fatalf("%s was proposed and skipped at the same time", entry.ID)
		}
	}
	if report.Ignored != 0 {
		t.Fatalf("Ignored = %d, want 0 when every model produced a row", report.Ignored)
	}
}

// 已经带 provider 前缀的名字同样照常提议。插件不做"这个名字看起来已经改好了"
// 的判断：漏报会让操作者以为没有缺口，多报一行随时可以忽略。
func TestPrefixedModelsAreStillProposed(t *testing.T) {
	entries := []catalogEntry{{ID: "workbuddy-hy4-preview", OwnedBy: "workbuddy"}}
	report := buildAliasReportWithProviders("openai", entries, map[string]bool{"workbuddy": true}, nil)

	row, ok := proposedFor(report, "workbuddy-hy4-preview")
	if !ok {
		t.Fatalf("a prefixed model must still be listed: %+v", report.Missing)
	}
	if row.Alias != "workbuddy-workbuddy-hy4-preview" {
		t.Fatalf("alias = %q, want the prefix applied verbatim", row.Alias)
	}
}

// 没有 channel 要写时，原因必须具名，不能只累加计数。
func TestNoChannelReasonIsNamed(t *testing.T) {
	entries := []catalogEntry{{ID: "mystery-model", OwnedBy: "ghost"}}
	report := buildAliasReportWithProviders("openai", entries, map[string]bool{"cline": true}, nil)

	if len(report.Missing) != 0 {
		t.Fatalf("no channel means no row to write: %+v", report.Missing)
	}
	row, ok := skippedFor(report, "mystery-model")
	if !ok {
		t.Fatal("the skipped model must be listed with its reason")
	}
	if row.Reason != reasonNoChannel {
		t.Fatalf("reason = %q, want %q", row.Reason, reasonNoChannel)
	}
	if report.SkippedByReason[reasonNoChannel] != 1 {
		t.Fatalf("SkippedByReason = %v, want one entry", report.SkippedByReason)
	}
}

// 没有 owned_by 也认不出 provider 时，原因要指名是"无 provider"。
func TestNoProviderReasonIsNamed(t *testing.T) {
	entries := []catalogEntry{{ID: "mystery-model"}}
	report := buildAliasReportWithProviders("openai", entries, map[string]bool{"cline": true}, nil)
	row, ok := skippedFor(report, "mystery-model")
	if !ok {
		t.Fatal("no skip record for an unattributable model")
	}
	if row.Reason != reasonNoProvider {
		t.Fatalf("reason = %q, want %q", row.Reason, reasonNoProvider)
	}
}

// Ignored 的语义是"没生成建议的模型数"，不按原因重复计数 —— 面板概要直接读它。
func TestIgnoredStaysAModelCount(t *testing.T) {
	entries := []catalogEntry{
		{ID: "mystery-a"},
		{ID: "mystery-b"},
	}
	report := buildAliasReportWithProviders("openai", entries, map[string]bool{"cline": true}, nil)
	if report.Ignored != len(entries) {
		t.Fatalf("Ignored = %d, want %d (one per model)", report.Ignored, len(entries))
	}
	if report.Captured != len(entries) {
		t.Fatalf("Captured = %d, want %d", report.Captured, len(entries))
	}
}
