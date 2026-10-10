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

// cline 的五个可见模型全带斜杠，此前被判为 pass-through 后只累加一个
// Ignored 计数，报告里什么都不显示 —— 操作者看到"cline 没有建议"，
// 却无从知道判定主动跳过了它们。这条测试要求原因必须可查。
func TestSkippedModelsRecordTheirReason(t *testing.T) {
	entries := []catalogEntry{
		{ID: "cline-free/mimo-v2.6-flash", OwnedBy: "cline"},
		{ID: "cline-free/solar-mini4", OwnedBy: "cline"},
		{ID: "x-ai/grok-4.7", OwnedBy: "cline"},
	}
	channels := map[string]bool{"cline": true}
	report := buildAliasReportWithProviders("openai", entries, channels, nil)

	if len(report.Missing) != 0 {
		t.Fatalf("these models are pass-through, no row should be proposed: %v", report.Missing)
	}
	if len(report.Skipped) != len(entries) {
		t.Fatalf("every skipped model must be listed: got %d, want %d", len(report.Skipped), len(entries))
	}
	for _, entry := range entries {
		row, ok := skippedFor(report, entry.ID)
		if !ok {
			t.Fatalf("no skip record for %s", entry.ID)
		}
		if row.Reason != reasonDirectPass {
			t.Fatalf("%s: reason = %q, want %q", entry.ID, row.Reason, reasonDirectPass)
		}
		if row.Provider != "cline" {
			t.Fatalf("%s: provider = %q, want cline", entry.ID, row.Provider)
		}
	}
	// 汇总必须与明细一致，否则面板的概要会跟下面的清单打架。
	if got := report.SkippedByReason[reasonDirectPass]; got != len(entries) {
		t.Fatalf("SkippedByReason[%s] = %d, want %d", reasonDirectPass, got, len(entries))
	}
}

// Ignored 的既有语义是"没生成建议的模型数"，不能因为明细改成按(模型,原因)
// 记录就被乘上原因条数 —— 面板的概要直接读它。
func TestIgnoredStaysAModelCount(t *testing.T) {
	entries := []catalogEntry{
		{ID: "cline-free/a", OwnedBy: "cline"},
		{ID: "cline-free/b", OwnedBy: "cline"},
	}
	report := buildAliasReportWithProviders("openai", entries, map[string]bool{"cline": true}, nil)
	if report.Ignored != len(entries) {
		t.Fatalf("Ignored = %d, want %d (one per model)", report.Ignored, len(entries))
	}
	if report.Captured != len(entries) {
		t.Fatalf("Captured = %d, want %d", report.Captured, len(entries))
	}
}

// 已经带 provider 前缀的名字走 already-aliased，而不是 pass-through：
// 两条原因必须能区分，否则操作者会以为前缀命名没生效。
func TestAlreadyAliasedReportedSeparately(t *testing.T) {
	entries := []catalogEntry{{ID: "workbuddy-hy4-preview", OwnedBy: "workbuddy"}}
	report := buildAliasReportWithProviders("openai", entries, map[string]bool{"workbuddy": true}, nil)
	row, ok := skippedFor(report, "workbuddy-hy4-preview")
	if !ok {
		t.Fatal("no skip record for an already-prefixed model")
	}
	if row.Reason != reasonAlreadyAliased {
		t.Fatalf("reason = %q, want %q", row.Reason, reasonAlreadyAliased)
	}
	if report.SkippedByReason[reasonAlreadyAliased] != 1 {
		t.Fatalf("SkippedByReason = %v, want one already-aliased entry", report.SkippedByReason)
	}
}

// 没有 owned_by 也认不出 provider 时，原因要指名是"无 provider"，
// 而不是笼统地算作没生成建议。
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

// 一个模型正常生成建议时不得同时出现在跳过清单里，否则计数会自相矛盾。
func TestProposedModelsAreNotListedAsSkipped(t *testing.T) {
	entries := []catalogEntry{{ID: "hy4-preview", OwnedBy: "workbuddy"}}
	report := buildAliasReportWithProviders("openai", entries, map[string]bool{"workbuddy": true}, nil)
	if len(report.Missing) != 1 {
		t.Fatalf("expected one proposed row, got %v", report.Missing)
	}
	if _, ok := skippedFor(report, "hy4-preview"); ok {
		t.Fatal("a proposed model must not also be reported as skipped")
	}
	if report.Ignored != 0 {
		t.Fatalf("Ignored = %d, want 0 when a row was proposed", report.Ignored)
	}
}
