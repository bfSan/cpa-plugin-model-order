package main

import (
	"fmt"
	"strings"
	"sync/atomic"

	"gopkg.in/yaml.v3"
)

// rawConfig mirrors the plugin section under plugins.configs.model-registry.
// `enabled` and `priority` belong to CPA and are read by the host, not here.
type rawConfig struct {
	Strategy      string         `yaml:"strategy"`
	CaseSensitive bool           `yaml:"case_sensitive"`
	Order         []string       `yaml:"order"`
	Access        []accessPolicy `yaml:"access"`
}

// activeConfig is the compiled, immutable snapshot used by the interceptor.
type activeConfig struct {
	strategy      string
	caseSensitive bool
	matchers      []matcher
	// order echoes the effective pattern list, for the status route. It is empty
	// unless config supplied one: there is no built-in fallback.
	order []string
	// fromConfig records whether the pattern list came from plugin config. Since
	// config is the only source, this is effectively "is grouping enabled", which
	// is what the panel's status chip reports.
	fromConfig bool
	// access is the compiled per caller visibility table. An empty table means
	// every caller is served the untouched listing, which is the state an
	// unrestricted key depends on.
	access *accessPolicyTable
}

var configStore atomic.Pointer[activeConfig]

// loadConfig installs a configuration snapshot. Ordering is config only: no
// `order` key means no grouping, and the listing falls back to CPA's own order
// rather than to a rule baked into the plugin. A broken config must not take the
// listing down, so parse failures return an error and keep the previous snapshot
// in place.
func loadConfig(yamlBytes []byte) error {
	next := &activeConfig{
		strategy: StrategyGrouped,
	}
	if len(yamlBytes) > 0 {
		var parsed rawConfig
		if err := yaml.Unmarshal(yamlBytes, &parsed); err != nil {
			return fmt.Errorf("model-registry: invalid config yaml: %w", err)
		}
		switch parsed.Strategy {
		case "":
			// keep default
		case StrategyGrouped, StrategyName:
			next.strategy = parsed.Strategy
		default:
			return fmt.Errorf("model-registry: unknown strategy %q, want grouped or name", parsed.Strategy)
		}
		next.caseSensitive = parsed.CaseSensitive
		if len(parsed.Order) > 0 {
			next.order = append([]string(nil), parsed.Order...)
			next.fromConfig = true
		}
		next.access = compileAccessPolicies(parsed.Access)
	}
	next.matchers = compileMatchers(next.order, next.caseSensitive)
	configStore.Store(next)
	return nil
}

// compileAccessPolicies indexes the policy list by caller scope.
//
// A policy without a scope is dropped rather than applied to everyone: an empty
// caller_scope in a table entry is far more likely to be a half written rule than
// a deliberate "constrain all keys", and guessing that reading would restrict the
// unrestricted key, which is precisely the mistake this plugin must not make.
// A duplicate scope keeps the first entry, matching how CPA resolves the first
// alias for a model, so the table reads the same way from either direction.
func compileAccessPolicies(policies []accessPolicy) *accessPolicyTable {
	table := &accessPolicyTable{policies: make(map[string]*accessPolicy, len(policies))}
	for i := range policies {
		policy := policies[i]
		scope := strings.TrimSpace(policy.CallerScope)
		if scope == "" {
			continue
		}
		if _, exists := table.policies[scope]; exists {
			continue
		}
		copied := policy
		copied.CallerScope = scope
		table.policies[scope] = &copied
		table.count++
	}
	return table
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
