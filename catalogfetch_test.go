package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The capability catalog is fetched on load rather than learned from inbound
// traffic. These tests pin the fetch contract: what it asks for, what it accepts,
// and how a failure is reported instead of silently leaving an empty catalog.

func sampleCatalogServer(t *testing.T, status int, body string, seen *http.Request) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = *r.Clone(r.Context())
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

// A configured catalog is read with the configured credential, and the facts land
// in the capability store without any client having touched the listing.
func TestCatalogFetchLearnsOnDemand(t *testing.T) {
	beginIsolatedTest(t)
	server := sampleCatalogServer(t, http.StatusOK,
		`{"models":[{"slug":"gpt-6-sol","context_window":272000,"max_context_window":872000,`+
			`"input_modalities":["text","image"],`+
			`"supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]}]}`, nil)

	if err := loadConfig([]byte(
		"catalog_base: " + server.URL + "\n" +
			"catalog_api_key: test-key\n" +
			"catalog_refresh: 1h\n")); err != nil {
		t.Fatalf("config must load: %v", err)
	}
	startFastCatalogRefresh()
	defer stopCatalogRefresh()

	// The goroutine fetches immediately; wait for the store to fill.
	deadline := time.Now().Add(2 * time.Second)
	for capabilities.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	facts, ok := capabilities.lookup("gpt-6-sol")
	if !ok {
		t.Fatal("the fetcher must learn models without any inbound catalog traffic")
	}
	if facts.ContextLength != 272000 || facts.MaxContextLength != 872000 {
		t.Fatalf("facts = %+v", facts)
	}
}

// The fetch must send the configured key: an anonymous request is rejected by CPA
// before it reaches the listing, so a keyless fetch would always come back 401.
func TestCatalogFetchSendsCredential(t *testing.T) {
	beginIsolatedTest(t)
	var seen *http.Request = &http.Request{}
	server := sampleCatalogServer(t, http.StatusOK,
		`{"models":[{"slug":"m","context_window":1000}]}`, seen)

	if err := loadConfig([]byte(
		"catalog_base: " + server.URL + "\n" +
			"catalog_api_key: secret-key\n")); err != nil {
		t.Fatalf("config must load: %v", err)
	}
	startFastCatalogRefresh()
	defer stopCatalogRefresh()

	deadline := time.Now().Add(2 * time.Second)
	for capabilities.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := seen.Header.Get("Authorization"); got != "Bearer secret-key" {
		t.Fatalf("Authorization = %q, want the configured key", got)
	}
	// The Codex catalog shape is selected by this query parameter; fetching the
	// plain listing would carry no capability fields at all.
	if !strings.Contains(seen.URL.RawQuery, "client_version=") {
		t.Fatalf("fetch must request the Codex catalog shape, got query %q", seen.URL.RawQuery)
	}
}

// Without a key the loop never starts, and the reason is recorded rather than
// retried forever against an endpoint that will only answer 401.
func TestCatalogFetchWithoutKeyRecordsWhy(t *testing.T) {
	beginIsolatedTest(t)
	if err := loadConfig([]byte("catalog_base: http://127.0.0.1:9\n")); err != nil {
		t.Fatalf("config must load: %v", err)
	}
	startFastCatalogRefresh()
	defer stopCatalogRefresh()

	state := catalogFetch.fetchState()
	msg, _ := state["error"].(string)
	if !strings.Contains(msg, "catalog_api_key is not configured") {
		t.Fatalf("a missing key must be reported, got %q", msg)
	}
	if capabilities.count() != 0 {
		t.Fatal("nothing may be learned without a working fetch")
	}
}

// A failing endpoint is surfaced through the status state, so "the catalog is
// empty" is never mistaken for "nothing has been configured".
func TestCatalogFetchReportsUpstreamFailure(t *testing.T) {
	beginIsolatedTest(t)
	server := sampleCatalogServer(t, http.StatusUnauthorized, `{"error":"Missing API key"}`, nil)
	if err := loadConfig([]byte(
		"catalog_base: " + server.URL + "\n" +
			"catalog_api_key: wrong\n")); err != nil {
		t.Fatalf("config must load: %v", err)
	}
	startFastCatalogRefresh()
	defer stopCatalogRefresh()

	// loadConfig fires the fetch from a goroutine, so wait for the outcome of THIS
	// config rather than reading the record left by the previous one.
	catalogFetch.reset()
	deadline := time.Now().Add(2 * time.Second)
	var msg string
	for time.Now().Before(deadline) {
		msg, _ = catalogFetch.fetchState()["error"].(string)
		if msg != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(msg, "401") {
		t.Fatalf("a rejected fetch must be reported with its status, got %q", msg)
	}
}

// A body that is not a catalog is rejected instead of being recorded as zero
// models, which would look like a healthy but empty registry.
func TestCatalogFetchRejectsNonCatalog(t *testing.T) {
	beginIsolatedTest(t)
	if _, err := learnCatalogBody([]byte(`{"hello":"world"}`)); err == nil {
		t.Fatal("a body without a model array must be an error")
	}
}

// A bad interval is a configuration error and must fail the load, so the operator
// sees it at PATCH time rather than as a loop that silently never fires.
func TestCatalogRefreshIntervalValidated(t *testing.T) {
	beginIsolatedTest(t)
	if err := loadConfig([]byte("catalog_refresh: nonsense\n")); err == nil {
		t.Fatal("an unparseable catalog_refresh must be rejected")
	}
	if err := loadConfig([]byte("catalog_refresh: -5m\n")); err == nil {
		t.Fatal("a non-positive catalog_refresh must be rejected")
	}
}

// startFastCatalogRefresh starts the loop with the startup settle delay removed.
// In production the delay exists so provider plugins can finish registering their
// models before the first read; a unit test has no such startup, and waiting for
// it would only make the suite slow.
func startFastCatalogRefresh() {
	startCatalogRefresh(currentConfig(), 0, 5*time.Millisecond)
}
