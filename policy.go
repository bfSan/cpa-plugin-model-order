package main

import (
	"strings"
)

// accessPolicy is one caller's visibility rule. The shape mirrors the
// key-provider-access plugin already in this deployment (caller_scope plus
// allow/deny lists) so both policies for a key can be read side by side and a
// caller scope is spelled the same way in each file.
//
// The semantics are deliberately the ones CPA already uses for upstream profiles,
// because a model a key may not see and an upstream it may not reach are the same
// question asked at two different points in the request.
type accessPolicy struct {
	// CallerScope is the sha256 namespace from identity.go. It is the policy
	// key: there is no match by key text, so the table never holds a credential.
	CallerScope string `yaml:"caller_scope"`
	// Label is a free form note for whoever edits the file. It never affects
	// matching, and a policy without one is perfectly valid.
	Label string `yaml:"label,omitempty"`
	// AllowModels, when non empty, is the only set of models this caller may
	// see. An empty allow means "no allowlist", not "nothing visible": that
	// distinction is what keeps a deny-only policy from blanking the list.
	AllowModels []string `yaml:"allow_models"`
	// DenyModels is subtracted after the allowlist. It is applied on its own when
	// AllowModels is empty, which is the common shape for carving a few models out
	// of an otherwise full listing.
	DenyModels []string `yaml:"deny_models"`
	// CaseSensitive matches the ID exactly. It defaults to false so a policy
	// written against the lowercase ids clients actually send still works.
	CaseSensitive bool `yaml:"case_sensitive"`
}

// accessPolicyTable is the compiled, immutable policy set.
type accessPolicyTable struct {
	// policies is indexed by caller scope. A scope absent from the map has no
	// policy and is served the untouched listing.
	policies map[string]*accessPolicy
	// count is the number of configured policies, for the status route.
	count int
}

// lookup returns the policy for a caller scope. The second result is false when
// the scope is unknown, which is the signal to pass the body through untouched.
func (t *accessPolicyTable) lookup(scope string) (*accessPolicy, bool) {
	if t == nil || scope == "" {
		return nil, false
	}
	policy, ok := t.policies[scope]
	return policy, ok
}

// allows reports whether a model id is visible under this policy.
//
// A nil policy is the unrestricted case and is the reason the ALL key needs no
// configuration: the host never asks it to be restricted, and this function is
// not reached with a constraint it has to satisfy.
func (p *accessPolicy) allows(id string) bool {
	if p == nil {
		return true
	}
	if len(p.AllowModels) > 0 && !matchesAny(id, p.AllowModels, p.CaseSensitive) {
		return false
	}
	if len(p.DenyModels) > 0 && matchesAny(id, p.DenyModels, p.CaseSensitive) {
		return false
	}
	return true
}

// matchesAny reports whether an id matches any of the patterns. It reuses the
// ordering plugin's matcher so a filter rule and an order rule written in the
// same style behave the same way: "gpt-*", "*-preview", "*kimi*" and "auto" all
// mean what they look like they mean.
func matchesAny(id string, patterns []string, caseSensitive bool) bool {
	matchers := compileMatchers(patterns, caseSensitive)
	probe := id
	if !caseSensitive {
		probe = strings.ToLower(id)
	}
	for _, m := range matchers {
		if m.match(probe) {
			return true
		}
	}
	return false
}

// filterPort reports whether filtering applies to a listing port.
//
// Only the OpenAI port is filtered. The Claude port cloaks every id through CPA
// core's reversible encoding before the body reaches this plugin, so a policy
// written against a real model name would silently match nothing; the Gemini port
// is excluded for the same class of reason, its names carry a resource path
// prefix. Neither is in use by this deployment's clients, and guessing at their
// name mangling to filter them would risk hiding a model the operator expected to
// stay visible.
//
// The Codex catalog shares the OpenAI port and is filtered like it: its slugs are
// the same identities in a different envelope, and a model hidden from a key must
// not reappear merely because the client asked with ?client_version=.
func filterPort(port string) bool {
	return port == portOpenAI || port == portCodex
}

// providerPrefix is the alias convention CPA deployments use: every alias for a
// provider is its id with "<provider>-" in front. Detecting an already aliased id
// is what keeps the report down to genuinely missing entries.
//
// The provider is a plain name, not a path, so no path cleaning happens here.
// Doing that would rewrite "." to "" and turn a nonsense provider into a bare
// dash prefix, which then matches nothing.
func providerPrefix(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider)) + "-"
}
