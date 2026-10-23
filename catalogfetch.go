package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// The capability catalog has to be fetched, not waited for.
//
// Backfilling /v1/models needs per-model facts (context window, modalities,
// reasoning levels), and the only response shape that carries them is the Codex
// client catalog. An earlier version learned them by watching that catalog pass
// through the interceptor, which quietly made the whole feature depend on some
// client requesting it. In this deployment almost nothing does: over one day the
// catalog endpoint saw ten requests while /v1/models saw dozens, so after every
// restart the plugin learned nothing and the backfill silently did nothing until
// a human happened to warm it up.
//
// So the plugin asks for the catalog itself, on load and on a timer. That turns
// "eventually, if someone else asks" into "at startup", which is what the listing
// needs to be correct from the first request a client makes.
//
// The request goes back through CPA's own listener rather than into the registry,
// because a response interceptor is handed response bodies and nothing else: the
// host exposes no RPC for model metadata. Reading the same endpoint ordinary
// clients read also means the plugin learns exactly what CPA publishes, rather
// than a second opinion about it.

const (
	// defaultCatalogPath is the listing that carries capability fields. The
	// client_version parameter selects the Codex catalog shape; the value itself
	// only affects minor field naming, and "cpa" is CPA's own marker.
	defaultCatalogPath = "/v1/models?client_version=cpa"
	// defaultCatalogBase is where CPA listens when nothing else is configured.
	defaultCatalogBase = "http://127.0.0.1:8317"
	// catalogFetchTimeout bounds one refresh. It runs off the request path, so a
	// slow catalog delays nothing, but it must not hang a goroutine forever.
	catalogFetchTimeout = 30 * time.Second
	// defaultCatalogRefresh is how often the catalog is re-read. Credentials come
	// and go and models get added upstream, so a single startup read would drift.
	defaultCatalogRefresh = time.Hour
	// defaultStartupFetchDelay lets provider plugins finish registering before the
	// first read. Reading during plugin.register returns an empty catalog because
	// the registry has not been populated yet.
	defaultStartupFetchDelay = 20 * time.Second
	// emptyCatalogRetry is how soon to retry when the source answered but carried
	// no models, which during startup means "not ready yet" rather than "wrong".
	emptyCatalogRetry = 30 * time.Second
	// maxCatalogBodyBytes caps the response the plugin will read. A Codex catalog
	// is tens of KB; this is a backstop against a misconfigured URL pointing at
	// something enormous.
	maxCatalogBodyBytes = 8 << 20
)

// catalogFetcher owns the background refresh loop.
type catalogFetcher struct {
	mu       sync.Mutex
	cancel   context.CancelFunc
	interval time.Duration
	// lastErr and lastOK are the most recent outcome, for the status route. They
	// exist so an operator can tell "the catalog is empty" from "the fetch has
	// been failing since startup", which look identical from the listing alone.
	lastErr   string
	lastOK    time.Time
	lastCount int
}

var catalogFetch catalogFetcher

// catalogEndpoint resolves the URL and credential a refresh should use.
//
// The API key is read from plugin config because the plugin has no other way to
// authenticate to CPA: the host's own management credential is not exposed to
// plugins, and an unauthenticated request is rejected with 401 before it reaches
// the listing. A key is therefore required for the fetch to work at all, and its
// absence is reported rather than silently producing an empty catalog.
func catalogEndpoint(cfg *activeConfig) (string, string, error) {
	base := strings.TrimSpace(cfg.catalogBase)
	if base == "" {
		base = defaultCatalogBase
	}
	base = strings.TrimRight(base, "/")
	parsed, errParse := url.Parse(base)
	if errParse != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", "", fmt.Errorf("model-registry: catalog base %q is not an absolute URL", base)
	}
	path := strings.TrimSpace(cfg.catalogPath)
	if path == "" {
		path = defaultCatalogPath
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	key := strings.TrimSpace(cfg.catalogAPIKey)
	if key == "" {
		return "", "", fmt.Errorf("model-registry: catalog_api_key is not configured, so the capability catalog cannot be fetched")
	}
	return base + path, key, nil
}

// startCatalogRefresh begins the background loop, replacing any loop already
// running. It is called on every configure so a config change takes effect
// without a restart.
func startCatalogRefresh(cfg *activeConfig, startupDelay, emptyRetry time.Duration) {
	catalogFetch.stopAndWait()
	catalogFetch.mu.Lock()
	interval := cfg.catalogRefresh
	if interval <= 0 {
		interval = defaultCatalogRefresh
	}
	catalogFetch.interval = interval

	ctx, cancel := context.WithCancel(context.Background())
	catalogFetch.cancel = cancel
	catalogFetch.mu.Unlock()

	// A missing key is a configuration gap, not a crash. Record it once and leave
	// the loop off: retrying an unauthenticated request on a timer would just fill
	// the log with 401s.
	if _, _, errEndpoint := catalogEndpoint(cfg); errEndpoint != nil {
		catalogFetch.setError(errEndpoint.Error())
		return
	}

	go func() {
		// Wait before the first fetch. configure() runs during plugin registration,
		// which is before every provider plugin has registered its models: a fetch
		// at that moment gets an empty catalog back from CPA, not because the
		// source is wrong but because there is nothing in the registry yet. A short
		// delay puts the first read after startup has settled, and the retry loop
		// below covers the case where it has not.
		if !sleepCtx(ctx, startupDelay) {
			return
		}
		// Retry quickly while the catalog is still empty: during startup that means
		// the registry is not ready yet, and waiting a full refresh interval would
		// leave the listing without capability fields for that whole window.
		for !refreshCatalogOnce(ctx) {
			if !sleepCtx(ctx, emptyRetry) {
				return
			}
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refreshCatalogOnce(ctx)
			}
		}
	}()
}

// stopCatalogRefresh ends the loop. Used by tests and on shutdown.
func stopCatalogRefresh() {
	catalogFetch.stopAndWait()
}

// stopAndWait cancels the running loop. It does not block: the loop's only
// blocking points are context-aware, so cancellation is observed promptly, and
// waiting here would deadlock any caller that holds a lock the loop needs.
func (f *catalogFetcher) stopAndWait() {
	f.mu.Lock()
	cancel := f.cancel
	f.cancel = nil
	f.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (f *catalogFetcher) setError(msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastErr = msg
}

func (f *catalogFetcher) setSuccess(count int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastErr = ""
	f.lastOK = time.Now().UTC()
	f.lastCount = count
}

// fetchState reports the last outcome for the status route.
func (f *catalogFetcher) fetchState() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]any{
		"refreshed_at": f.lastOK,
		"model_count":  f.lastCount,
		"interval":     f.interval.String(),
	}
	if f.lastErr != "" {
		out["error"] = f.lastErr
	}
	return out
}

// refreshCatalogOnce performs one fetch and records what it learned.
func refreshCatalogOnce(ctx context.Context) bool {
	cfg := currentConfig()
	endpoint, key, errEndpoint := catalogEndpoint(cfg)
	if errEndpoint != nil {
		catalogFetch.setError(errEndpoint.Error())
		return false
	}

	reqCtx, cancel := context.WithTimeout(ctx, catalogFetchTimeout)
	defer cancel()

	req, errRequest := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if errRequest != nil {
		catalogFetch.setError(errRequest.Error())
		return false
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")

	resp, errDo := http.DefaultClient.Do(req)
	if errDo != nil {
		// A cancelled context is the loop shutting down, not a failure to report.
		if ctx.Err() != nil {
			return false
		}
		catalogFetch.setError("fetch catalog: " + errDo.Error())
		return false
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		catalogFetch.setError(fmt.Sprintf("catalog endpoint answered %d: %s", resp.StatusCode, strings.TrimSpace(string(body))))
		return false
	}

	body, errRead := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBodyBytes))
	if errRead != nil {
		catalogFetch.setError("read catalog: " + errRead.Error())
		return false
	}

	learned, errLearn := learnCatalogBody(body)
	if errLearn != nil {
		logCatalogf("refresh failed body=%dB: %v", len(body), errLearn)
		catalogFetch.setError(errLearn.Error())
		return false
	}
	// Provider catalogs are read on the same schedule: they are the only source of
	// per-channel facts, and a listing that reflects one channel is the bug this
	// aggregation exists to fix.
	cfg = currentConfig()
	if configDir, errDir := os.UserConfigDir(); errDir == nil {
		applied := capabilities.applyChannelFacts(gatherProviderFacts(configDir))
		logCatalogf("refresh ok body=%dB learned=%d aggregated=%d", len(body), learned, applied)
	}
	logCatalogf("refresh ok body=%dB learned=%d total=%d", len(body), learned, capabilities.count())
	catalogFetch.setSuccess(learned)
	return true
}

// learnCatalogBody records the capability facts carried by one catalog body.
//
// The body is parsed through the same splitter the interceptor uses, so the
// fetch path and the live path cannot disagree about what an element is.
func learnCatalogBody(body []byte) (int, error) {
	list, errParse := parseModelList(body)
	if errParse != nil {
		return 0, fmt.Errorf("catalog body is not a model listing: %w", errParse)
	}
	learned := capabilities.recordCodexCatalog(list.elements)
	if learned == 0 {
		return 0, fmt.Errorf("catalog body carried no capability facts")
	}
	return learned, nil
}

// reset clears the recorded outcome. Tests use it to start from a known state;
// the refresh loop itself never calls it.
func (f *catalogFetcher) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastErr = ""
	f.lastOK = time.Time{}
	f.lastCount = 0
}

// logCatalogf writes one line to stderr, which CPA captures into its log. The
// fetch runs in the background, so without this a misconfigured source is only
// visible through the status route an operator may not think to check.
// logCatalogf reports a refresh outcome. The fetch runs off the request path, so
// this is the only place a failure surfaces in the host log; the status route
// carries the same information for an operator who looks there instead.
func logCatalogf(format string, args ...any) {
	log.Printf("model-registry: "+format, args...)
}

// sleepCtx waits for d or until ctx is cancelled, reporting whether the wait
// completed. It exists so the startup delay stays interruptible: a reconfigure or
// shutdown must not be held for the full delay by a sleeping goroutine.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
