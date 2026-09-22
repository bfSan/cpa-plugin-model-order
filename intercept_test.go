package main

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// serverOrder is the real /v1/models order captured from the deployment. CPA
// builds this list by ranging over a map keyed by model ID, so it arrives
// shuffled with providers interleaved. That is the regression the plugin fixes.
var serverOrder = []string{
	"qoder-qwen3.8-flash", "qoder-efficient", "workbuddy-minimax-m3", "workbuddy-deep",
	"spacexai/grok-4.7", "gpt-5.6-luna", "gpt-5.6-sol", "workbuddy-glm-5.3",
	"workbuddy-balanced", "gpt-5.3-codex", "qoder-deepseek-v4.1-flash", "qoder-minimax-m2.7",
	"cline-free/solar-pro4", "qoder-ultimate", "workbuddy-deepseek-v4.1-flash", "workbuddy-deepseek-v4-flash",
	"gpt-5.6-terra", "codex-auto-review", "gpt-image-1.5", "workbuddy-glm-5.3-flash",
	"workbuddy-kimi-k3", "workbuddy-fast", "gpt-6-astra", "gpt-image-2.5",
	"cline-free/deepseek-v4.1-flash", "cline-free/kimi-k3", "workbuddy-hy4-preview-f",
	"cline-pass/mimo-v2.6-flash", "cline-pass/mimo-v2.6-pro", "gpt-5.5", "gpt-image-2",
	"qoder-glm-5.2", "qoder-glm-5.3-flash", "workbuddy-hy3", "workbuddy-hy3-x",
	"workbuddy-hy4-preview", "workbuddy-minimax-m2.7", "gpt-image-2.5-sunburst", "qoder-auto",
	"qoder-qwen3.8-max", "z-ai/glm-5.3-flash", "qoder-performance", "workbuddy-kimi-k2.8-preview",
	"gpt-image-2.5-flare", "qoder-qwen3.7-flash",
}

// expectedOrder is the agreed rule applied to serverOrder: aggregate presets
// first, then GPT, then Codex, then the rest alphabetically by model ID.
var expectedOrder = []string{
	"qoder-auto",
	"workbuddy-balanced",
	"workbuddy-fast",
	"workbuddy-deep",
	"qoder-efficient",
	"qoder-performance",
	"qoder-ultimate",
	"gpt-5.3-codex", "gpt-5.5", "gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra",
	"gpt-6-astra", "gpt-image-1.5", "gpt-image-2", "gpt-image-2.5",
	"gpt-image-2.5-flare", "gpt-image-2.5-sunburst",
	"codex-auto-review",
	"cline-free/deepseek-v4.1-flash", "cline-free/kimi-k3", "cline-free/solar-pro4",
	"cline-pass/mimo-v2.6-flash", "cline-pass/mimo-v2.6-pro",
	"qoder-deepseek-v4.1-flash", "qoder-glm-5.2", "qoder-glm-5.3-flash",
	"qoder-minimax-m2.7", "qoder-qwen3.7-flash", "qoder-qwen3.8-flash", "qoder-qwen3.8-max",
	"spacexai/grok-4.7",
	"workbuddy-deepseek-v4-flash", "workbuddy-deepseek-v4.1-flash",
	"workbuddy-glm-5.3", "workbuddy-glm-5.3-flash",
	"workbuddy-hy3", "workbuddy-hy3-x", "workbuddy-hy4-preview", "workbuddy-hy4-preview-f",
	"workbuddy-kimi-k2.8-preview", "workbuddy-kimi-k3",
	"workbuddy-minimax-m2.7", "workbuddy-minimax-m3",
	"z-ai/glm-5.3-flash",
}

func listingBody(ids []string) []byte {
	var builder strings.Builder
	builder.WriteString(`{"object":"list","data":[`)
	for i, id := range ids {
		if i > 0 {
			builder.WriteByte(',')
		}
		entry, _ := json.Marshal(map[string]any{"id": id, "object": "model", "owned_by": "p"})
		builder.Write(entry)
	}
	builder.WriteString(`]}`)
	return []byte(builder.String())
}

func listedIDs(t *testing.T, body []byte) []string {
	t.Helper()
	var doc struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("body is not valid json: %v (%s)", err, body)
	}
	out := make([]string, 0, len(doc.Data))
	for _, entry := range doc.Data {
		out = append(out, entry.ID)
	}
	return out
}

func TestIsModelListing(t *testing.T) {
	listing := pluginapi.ResponseInterceptRequest{
		StatusCode: 200,
		Body:       listingBody([]string{"a", "b"}),
	}
	if !isModelListing(listing) {
		t.Fatal("expected a model listing to be recognised")
	}
	cases := map[string]pluginapi.ResponseInterceptRequest{
		"error status":   {StatusCode: 500, Body: listing.Body},
		"has model":      {StatusCode: 200, Model: "gpt-5.5", Body: listing.Body},
		"has request":    {StatusCode: 200, RequestBody: []byte(`{"model":"x"}`), Body: listing.Body},
		"original bytes": {StatusCode: 200, OriginalRequest: []byte(`{}`), Body: listing.Body},
		"stream":         {StatusCode: 200, Stream: true, Body: listing.Body},
		"empty body":     {StatusCode: 200},
	}
	for name, req := range cases {
		if isModelListing(req) {
			t.Errorf("%s: expected the request to be skipped", name)
		}
	}
}

func TestOrderBodyMatchesAgreedRule(t *testing.T) {
	loadSuggested(t)
	out, changed := orderBody(portOpenAI, listingBody(serverOrder))
	if !changed {
		t.Fatal("expected the listing to be reordered")
	}
	got := listedIDs(t, out)
	if len(got) != len(serverOrder) {
		t.Fatalf("model count changed: got %d want %d", len(got), len(serverOrder))
	}
	if !reflect.DeepEqual(got, expectedOrder) {
		t.Fatalf("unexpected order\n got %v\nwant %v", got, expectedOrder)
	}
}

func TestOrderBodyIsIdempotent(t *testing.T) {
	loadSuggested(t)
	once, changed := orderBody(portOpenAI, listingBody(serverOrder))
	if !changed {
		t.Fatal("expected first pass to reorder")
	}
	// CPA may serve the cached snapshot repeatedly; a second pass must report
	// "nothing to do" so the body is never rewritten twice.
	if _, changedAgain := orderBody(portOpenAI, once); changedAgain {
		t.Fatal("second pass must be a no-op on an already ordered list")
	}
}

func TestOrderBodyKeepsMembershipIntact(t *testing.T) {
	loadSuggested(t)
	out, _ := orderBody(portOpenAI, listingBody(serverOrder))
	got := listedIDs(t, out)
	sort.Strings(got)
	want := append([]string(nil), serverOrder...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("membership changed\n got %v\nwant %v", got, want)
	}
}

func TestOrderBodyHonoursConfiguredOrder(t *testing.T) {
	t.Cleanup(func() { _ = loadConfig(nil) })
	if err := loadConfig([]byte("strategy: grouped\norder:\n  - \"qoder-*\"\n  - \"gpt-*\"\n")); err != nil {
		t.Fatalf("config load failed: %v", err)
	}
	out, changed := orderBody(portOpenAI, listingBody([]string{"workbuddy-hy3", "gpt-5.5", "qoder-auto"}))
	if !changed {
		t.Fatal("expected reorder")
	}
	got := listedIDs(t, out)
	want := []string{"qoder-auto", "gpt-5.5", "workbuddy-hy3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestOrderBodyNameStrategy(t *testing.T) {
	t.Cleanup(func() { _ = loadConfig(nil) })
	if err := loadConfig([]byte("strategy: name\n")); err != nil {
		t.Fatalf("config load failed: %v", err)
	}
	out, changed := orderBody(portOpenAI, listingBody([]string{"workbuddy-hy3", "gpt-5.5", "qoder-auto"}))
	if !changed {
		t.Fatal("expected reorder")
	}
	got := listedIDs(t, out)
	want := []string{"gpt-5.5", "qoder-auto", "workbuddy-hy3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestOrderBodyLeavesNonListingsAlone(t *testing.T) {
	body := []byte(`{"id":"c1","object":"chat.completion","choices":[{"message":{"content":"hi"}}]}`)
	if out, changed := orderBody(portOpenAI, body); changed {
		t.Fatalf("chat completion must not be rewritten, got %s", out)
	}
}

func TestLoadConfigRejectsUnknownStrategyAndKeepsPrevious(t *testing.T) {
	loadSuggested(t)
	before := currentConfig()
	if err := loadConfig([]byte("strategy: sideways\n")); err == nil {
		t.Fatal("expected unknown strategy to be rejected")
	}
	if currentConfig() != before {
		t.Fatal("a rejected config must leave the active snapshot untouched")
	}
}

// TestGeminiListingReordered covers the /v1beta/models shape, which keys the
// array as "models" and identifies entries with "name".
func TestGeminiListingReordered(t *testing.T) {
	loadSuggested(t)
	body := []byte(`{"models":[{"name":"models/zeta","version":"v1"},{"name":"models/alpha","version":"v1"}]}`)
	out, changed := orderBody(portGemini, body)
	if !changed {
		t.Fatal("expected gemini listing to be reordered")
	}
	want := []byte(`{"models":[{"name":"models/alpha","version":"v1"},{"name":"models/zeta","version":"v1"}]}`)
	if string(out) != string(want) {
		t.Fatalf("got  %s\nwant %s", out, want)
	}
}
