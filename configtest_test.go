package main

import (
	"strings"
	"testing"
)

// loadSuggested installs the recommended template as plugin config.
//
// The plugin deliberately ships no runtime default, so a test that asserts the
// grouped rule has to configure it exactly the way an operator does rather than
// leaning on an implicit fallback that no longer exists.
func loadSuggested(t *testing.T) {
	t.Helper()
	var b strings.Builder
	b.WriteString("strategy: grouped\norder:\n")
	for _, pattern := range suggestedOrder {
		b.WriteString("  - \"" + pattern + "\"\n")
	}
	if err := loadConfig([]byte(b.String())); err != nil {
		t.Fatalf("suggested rule must load cleanly: %v", err)
	}
}

// TestUnconfiguredOrderGroupsNothing pins the contract that replaced the old
// built-in default: with no `order` in config the plugin must not silently apply
// the recommended template. It may only sort alphabetically.
func TestUnconfiguredOrderGroupsNothing(t *testing.T) {
	if err := loadConfig(nil); err != nil {
		t.Fatalf("empty config must load cleanly: %v", err)
	}
	cfg := currentConfig()
	if len(cfg.matchers) != 0 {
		t.Fatalf("unconfigured plugin must have no matchers, got %d", len(cfg.matchers))
	}
	if orderConfigured() {
		t.Fatal("unconfigured plugin must report orderConfigured false")
	}
	if len(cfg.order) != 0 {
		t.Fatalf("unconfigured order must be empty, got %v", cfg.order)
	}

	// The template puts qoder-auto in the first bucket, so if it ever leaked into
	// the runtime path this comparison would fail rather than pass silently.
	less := comparer(cfg.strategy, cfg.matchers, cfg.caseSensitive)
	ids := []string{"workbuddy-hy3", "gpt-5.5", "qoder-auto"}
	sortIDs(ids, less)
	want := []string{"gpt-5.5", "qoder-auto", "workbuddy-hy3"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("want plain alphabetical %v, got %v", want, ids)
	}
}
