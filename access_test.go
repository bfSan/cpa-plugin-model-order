package main

import (
	"net/http"
	"testing"
)

// The four real caller scopes in this deployment, reproduced from
// sha256("cli-proxy-api:caller-scope:v1\x00" + key). The ALL key is the hard
// constraint of this plugin: it must never be restricted, and the test below
// pins that against the table it would be in if someone ever added it.
const (
	allKeyScope       = "8f9ea72d3446ec36051b029d6e3b8d051be0021df49b4d72f2db52444ff04abb"
	workbuddyKeyScope = "d66e70bffd410d852091e8987d47db72dadb15bd5ff3adf79be21afcd18228b2"
)

func TestCallerScopeMatchesCPA(t *testing.T) {
	// A regression here would silently stop matching every policy in the table,
	// so this asserts against the digests CPA itself produces for the real keys.
	cases := map[string]string{
		"wba-e66ecbf14973708213ff5c8b7eadd3c6f560476de08b3c9a":           allKeyScope,
		"wba-workbuddy-f27a552fca4d2d0a00a3001073b2d291c0e264bdc3a81eda": workbuddyKeyScope,
	}
	for key, want := range cases {
		if got := callerScope(key); got != want {
			t.Errorf("callerScope(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestCallerScopeTrimsAndRejectsEmpty(t *testing.T) {
	if got := callerScope("   "); got != "" {
		t.Errorf("blank credential should yield no scope, got %q", got)
	}
	if got := callerScope("  spaced-key  "); got != callerScope("spaced-key") {
		t.Error("credential should be trimmed before hashing")
	}
}

func TestCallerCredentialReadsBothHeaderForms(t *testing.T) {
	cases := []struct {
		name    string
		headers http.Header
		want    string
	}{
		{"bearer", http.Header{"Authorization": {"Bearer sk-test"}}, "sk-test"},
		{"bearer lowercase", http.Header{"Authorization": {"bearer sk-test"}}, "sk-test"},
		{"bearer mixed case", http.Header{"Authorization": {"BeArEr sk-test"}}, "sk-test"},
		{"x-api-key", http.Header{"X-Api-Key": {"sk-test"}}, "sk-test"},
		{"bearer wins over x-api-key", http.Header{"Authorization": {"Bearer sk-a"}, "X-Api-Key": {"sk-b"}}, "sk-a"},
		{"raw token without scheme", http.Header{"Authorization": {"sk-test"}}, "sk-test"},
		{"no headers", nil, ""},
		{"empty bearer", http.Header{"Authorization": {"Bearer   "}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := callerCredential(tc.headers); got != tc.want {
				t.Errorf("callerCredential = %q, want %q", got, tc.want)
			}
		})
	}
}

// The core hard constraint. An unrestricted key must see the whole listing even
// when policies for other keys exist, and it must see it byte for byte as CPA
// produced it, because "no policy" has to mean "this plugin is not involved".
func TestUnrestrictedKeySeesEverything(t *testing.T) {
	loadPolicyConfig(t, `
strategy: name
access:
  - caller_scope: `+workbuddyKeyScope+`
    label: workbuddy
    allow_models: ["workbuddy-*"]
`)

	headers := http.Header{"Authorization": {"Bearer wba-e66ecbf14973708213ff5c8b7eadd3c6f560476de08b3c9a"}}
	if _, ok := resolvePolicy(headers, portOpenAI); ok {
		t.Fatal("the ALL key must not resolve to any policy; it has no table entry")
	}

	// The input is already in alphabetical order, so the "name" strategy
	// reproduces it exactly. That isolates the claim being made: with no policy
	// resolved, nothing is removed and the plugin reports no change, so the
	// handler returns no body and CPA's own bytes stand.
	body := listingBody([]string{"gpt-6.1-sol", "qoder-auto", "workbuddy-space-bunny"})
	out, changed := governBody(portOpenAI, headers, body)
	if changed {
		t.Error("an unrestricted key must be served CPA's own bytes, so nothing changed")
	}
	if got, want := listedIDs(t, out), []string{"gpt-6.1-sol", "qoder-auto", "workbuddy-space-bunny"}; len(got) != len(want) {
		t.Errorf("the unrestricted listing lost models: got %v, want %v", got, want)
	}
}

// The same key with no policies configured at all is the shipped default state.
func TestEmptyPolicyTableServesEveryone(t *testing.T) {
	loadPolicyConfig(t, "strategy: name\n")
	headers := http.Header{"Authorization": {"Bearer any-key"}}
	if _, ok := resolvePolicy(headers, portOpenAI); ok {
		t.Fatal("an empty table must resolve no policy for anybody")
	}
}

func TestPolicyFiltersTheListing(t *testing.T) {
	loadPolicyConfig(t, `
strategy: name
access:
  - caller_scope: `+workbuddyKeyScope+`
    allow_models: ["workbuddy-*"]
`)
	headers := http.Header{"Authorization": {"Bearer wba-workbuddy-f27a552fca4d2d0a00a3001073b2d291c0e264bdc3a81eda"}}
	if _, ok := resolvePolicy(headers, portOpenAI); !ok {
		t.Fatal("a configured scope must resolve")
	}
	body := listingBody([]string{"workbuddy-auto", "workbuddy-space-bunny", "gpt-6.1-sol", "qoder-auto"})
	out, changed := governBody(portOpenAI, headers, body)
	if !changed {
		t.Fatal("filtering a listing is a change and must be reported as one")
	}
	got := listedIDs(t, out)
	want := []string{"workbuddy-auto", "workbuddy-space-bunny"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestPolicyWithoutAllowModelsIsNotBlank(t *testing.T) {
	// An empty allow list means "no allowlist", not "nothing visible". Getting
	// this backwards would hide every model from a deny-only policy.
	policy := &accessPolicy{DenyModels: []string{"gpt-*"}}
	for _, id := range []string{"workbuddy-auto", "qoder-auto"} {
		if !policy.allows(id) {
			t.Errorf("%q should be allowed under a deny-only policy", id)
		}
	}
	if policy.allows("gpt-6.1-sol") {
		t.Error("gpt-6.1-sol should be denied by gpt-*")
	}
}

func TestDenyOnlyPolicySubtracts(t *testing.T) {
	loadPolicyConfig(t, `
strategy: name
access:
  - caller_scope: `+workbuddyKeyScope+`
    deny_models: ["*-auto"]
`)
	headers := http.Header{"Authorization": {"Bearer wba-workbuddy-f27a552fca4d2d0a00a3001073b2d291c0e264bdc3a81eda"}}
	out, changed := governBody(portOpenAI, headers, listingBody([]string{"qoder-auto", "workbuddy-space-bunny", "gpt-6.1-sol"}))
	if !changed {
		t.Fatal("denying an entry is a change")
	}
	got := listedIDs(t, out)
	if len(got) != 2 || got[0] != "gpt-6.1-sol" || got[1] != "workbuddy-space-bunny" {
		t.Errorf("got %v, want [gpt-6.1-sol workbuddy-space-bunny]", got)
	}
}

// Filtering that removes an entry must be reported as a change even when the
// survivors already sit in the configured order. Otherwise the response falls
// through to CPA's original bytes and the hidden models come back.
func TestFilteringAloneCountsAsChanged(t *testing.T) {
	loadPolicyConfig(t, `
strategy: name
order: ["a", "b"]
access:
  - caller_scope: `+workbuddyKeyScope+`
    allow_models: ["a", "b"]
`)
	headers := http.Header{"Authorization": {"Bearer wba-workbuddy-f27a552fca4d2d0a00a3001073b2d291c0e264bdc3a81eda"}}
	body := listingBody([]string{"a", "b", "secret"})
	out, changed := governBody(portOpenAI, headers, body)
	if !changed {
		t.Fatal("dropping secret must count as a change even though a,b were already ordered")
	}
	if got := listedIDs(t, out); len(got) != 2 {
		t.Errorf("secret leaked back into the response: %v", got)
	}
}

func TestFilteringSkipsNonOpenAIPorts(t *testing.T) {
	loadPolicyConfig(t, `
strategy: name
access:
  - caller_scope: `+workbuddyKeyScope+`
    allow_models: ["workbuddy-*"]
`)
	headers := http.Header{"Authorization": {"Bearer wba-workbuddy-f27a552fca4d2d0a00a3001073b2d291c0e264bdc3a81eda"}}
	for _, port := range []string{portClaude, portGemini, portUnknown} {
		if _, ok := resolvePolicy(headers, port); ok {
			t.Errorf("port %q must not be filtered: its ids are mangled by CPA core", port)
		}
	}
	// The Codex catalog shares the OpenAI port and is filtered like it, so a
	// model hidden from a key cannot reappear via ?client_version=.
	if _, ok := resolvePolicy(headers, portCodex); !ok {
		t.Error("the codex catalog must be filtered alongside the openai port")
	}
}

func TestUnidentifiedCallerIsUnrestricted(t *testing.T) {
	loadPolicyConfig(t, `
strategy: name
access:
  - caller_scope: `+workbuddyKeyScope+`
    allow_models: ["workbuddy-*"]
`)
	for _, headers := range []http.Header{nil, {}, {"Authorization": {"Bearer  "}}} {
		if _, ok := resolvePolicy(headers, portOpenAI); ok {
			t.Errorf("headers %v carry no identity and must not resolve a policy", headers)
		}
	}
}

func TestCompileAccessPolicies(t *testing.T) {
	table := compileAccessPolicies([]accessPolicy{
		{CallerScope: "  "}, // dropped: no scope
		{CallerScope: workbuddyKeyScope, Label: "first"},     // kept
		{CallerScope: workbuddyKeyScope, Label: "duplicate"}, // dropped: first wins
		{CallerScope: "abc"}, // kept
	})
	if table.count != 2 {
		t.Fatalf("count = %d, want 2", table.count)
	}
	policy, ok := table.lookup(workbuddyKeyScope)
	if !ok {
		t.Fatal("the workbuddy scope should be present")
	}
	if policy.Label != "first" {
		t.Errorf("label = %q, want %q: a duplicate scope must not override the first", policy.Label, "first")
	}
	if _, ok := table.lookup(""); ok {
		t.Error("an empty scope must never resolve")
	}
	if _, ok := table.lookup("nosuchscope"); ok {
		t.Error("an unknown scope must not resolve")
	}
}

// A table entry with no scope must never be treated as "constrain everything":
// that would restrict keys the operator never meant to touch.
func TestScopeLessPolicyIsNotGlobal(t *testing.T) {
	table := compileAccessPolicies([]accessPolicy{{AllowModels: []string{"only-this"}}})
	if table.count != 0 {
		t.Fatalf("a policy without a scope must be dropped, got %d entries", table.count)
	}
	if _, ok := table.lookup(allKeyScope); ok {
		t.Error("a dropped policy must not constrain the unrestricted key")
	}
}

func loadPolicyConfig(t *testing.T, yaml string) {
	t.Helper()
	if err := loadConfig([]byte(yaml)); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	t.Cleanup(func() {
		if err := loadConfig(nil); err != nil {
			t.Fatalf("reset config: %v", err)
		}
	})
}
