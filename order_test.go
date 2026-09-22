package main

import (
	"reflect"
	"sort"
	"testing"
)

func TestCompileMatchersDropsBlankAndBareStar(t *testing.T) {
	compiled := compileMatchers([]string{"", "  ", "*", "gpt-*"}, false)
	if len(compiled) != 1 {
		t.Fatalf("want 1 matcher, got %d: %+v", len(compiled), compiled)
	}
	if compiled[0].kind != matcherPrefix || compiled[0].pre != "gpt-" {
		t.Fatalf("unexpected matcher %+v", compiled[0])
	}
}

func TestMatcherKinds(t *testing.T) {
	cases := []struct {
		pattern string
		id      string
		want    bool
	}{
		{"auto", "auto", true},
		{"auto", "qoder-auto", false},
		{"*-auto", "qoder-auto", true},
		{"*-auto", "codex-auto-review", false},
		{"gpt-*", "gpt-5.6-sol", true},
		{"gpt-*", "openai-gpt-5", false},
		{"*kimi*", "workbuddy-kimi-k3", true},
		{"*kimi*", "kimi-k3", true},
		{"a*b*c", "abc", true},
		{"a*b*c", "xxabc", false},
		{"qoder-*-*", "qoder-glm-5.2", true},
	}
	for _, tc := range cases {
		matchers := compileMatchers([]string{tc.pattern}, false)
		if len(matchers) == 0 {
			t.Fatalf("pattern %q compiled to nothing", tc.pattern)
		}
		if got := matchers[0].match(tc.id); got != tc.want {
			t.Errorf("pattern %q vs id %q: got %v want %v", tc.pattern, tc.id, got, tc.want)
		}
	}
}

func TestMatchersAreCaseInsensitiveByDefault(t *testing.T) {
	matchers := compileMatchers([]string{"GPT-*"}, false)
	if !matchers[0].match("gpt-5.6-sol") {
		t.Fatal("expected case insensitive match")
	}
	strict := compileMatchers([]string{"GPT-*"}, true)
	if strict[0].match("gpt-5.6-sol") {
		t.Fatal("expected case sensitive miss")
	}
}

func TestComparerOrdersBucketsThenAlphabetical(t *testing.T) {
	cfg := currentConfig()
	less := comparer(cfg.strategy, cfg.matchers, cfg.caseSensitive)
	ids := []string{
		"z-ai/glm-5.3-flash",
		"workbuddy-balanced",
		"gpt-6-astra",
		"qoder-auto",
		"cline-free/kimi-k3",
		"codex-auto-review",
		"workbuddy-hy3",
		"gpt-5.5",
	}
	sort.SliceStable(ids, func(i, j int) bool { return less(ids[i], ids[j]) })

	want := []string{
		"qoder-auto",         // bucket *-auto
		"workbuddy-balanced", // bucket *-balanced
		"gpt-5.5",            // bucket gpt-*
		"gpt-6-astra",        // bucket gpt-*
		"codex-auto-review",  // bucket codex-*
		"cline-free/kimi-k3", // tail, alphabetical
		"workbuddy-hy3",      // tail
		"z-ai/glm-5.3-flash", // tail
	}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("unexpected order\n got %v\nwant %v", ids, want)
	}
}

func TestComparerNameStrategyIsPlainAlphabetical(t *testing.T) {
	less := comparer(StrategyName, nil, false)
	ids := []string{"workbuddy-auto", "gpt-5.5", "qoder-auto"}
	sort.SliceStable(ids, func(i, j int) bool { return less(ids[i], ids[j]) })
	want := []string{"gpt-5.5", "qoder-auto", "workbuddy-auto"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("got %v want %v", ids, want)
	}
}

func TestComparerEmptyMatchersFallsBackToAlphabetical(t *testing.T) {
	// grouped strategy but no usable patterns must not leave the map order intact.
	less := comparer(StrategyGrouped, nil, false)
	if !less("a", "b") {
		t.Fatal("expected alphabetical fallback")
	}
}
