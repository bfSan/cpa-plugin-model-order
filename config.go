package main

import (
	"fmt"
	"sync/atomic"

	"gopkg.in/yaml.v3"
)

// rawConfig mirrors the plugin section under plugins.configs.model-order.
// `enabled` and `priority` belong to CPA and are read by the host, not here.
type rawConfig struct {
	Strategy      string   `yaml:"strategy"`
	CaseSensitive bool     `yaml:"case_sensitive"`
	Order         []string `yaml:"order"`
}

// activeConfig is the compiled, immutable snapshot used by the interceptor.
type activeConfig struct {
	strategy      string
	caseSensitive bool
	matchers      []matcher
	// order echoes the effective pattern list, for the status route.
	order []string
	// fromConfig records whether the pattern list came from plugin config rather
	// than the built-in defaults. The panel shows the difference so a user can
	// tell their own rule apart from the shipped one.
	fromConfig bool
}

var configStore atomic.Pointer[activeConfig]

// loadConfig installs a configuration snapshot, falling back to the built-in
// defaults when the host supplies nothing usable. A broken config must not take
// the listing down, so parse failures return an error and keep the previous
// snapshot in place.
func loadConfig(yamlBytes []byte) error {
	next := &activeConfig{
		strategy: StrategyGrouped,
		order:    append([]string(nil), defaultOrder...),
	}
	if len(yamlBytes) > 0 {
		var parsed rawConfig
		if err := yaml.Unmarshal(yamlBytes, &parsed); err != nil {
			return fmt.Errorf("model-order: invalid config yaml: %w", err)
		}
		switch parsed.Strategy {
		case "":
			// keep default
		case StrategyGrouped, StrategyName:
			next.strategy = parsed.Strategy
		default:
			return fmt.Errorf("model-order: unknown strategy %q, want grouped or name", parsed.Strategy)
		}
		next.caseSensitive = parsed.CaseSensitive
		if len(parsed.Order) > 0 {
			next.order = append([]string(nil), parsed.Order...)
			next.fromConfig = true
		}
	}
	next.matchers = compileMatchers(next.order, next.caseSensitive)
	configStore.Store(next)
	return nil
}

// orderConfigured reports whether the active pattern list came from config.
func orderConfigured() bool {
	return currentConfig().fromConfig
}

// currentConfig returns the effective snapshot, initialising defaults if the
// host never called plugin.register with a config.
func currentConfig() *activeConfig {
	if loaded := configStore.Load(); loaded != nil {
		return loaded
	}
	if err := loadConfig(nil); err != nil {
		// loadConfig only fails on bad input; the nil input cannot fail.
		panic(err)
	}
	return configStore.Load()
}
