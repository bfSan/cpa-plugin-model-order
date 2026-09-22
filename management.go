package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type managementRoute struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Description string `json:"description"`
}

type resourceRoute struct {
	Path        string `json:"path"`
	Menu        string `json:"menu"`
	Description string `json:"description"`
}

type managementRegistrationResponse struct {
	Routes    []managementRoute `json:"routes,omitempty"`
	Resources []resourceRoute   `json:"resources,omitempty"`
}

// managementRequestWire carries the host injected callback id alongside the
// standard management request.
type managementRequestWire struct {
	pluginapi.ManagementRequest
	HostCallbackID string `json:"host_callback_id"`
}

var pathCache struct {
	mu       sync.RWMutex
	mana     string
	resource string
}

func setManagementBasePath(p string) {
	pathCache.mu.Lock()
	defer pathCache.mu.Unlock()
	pathCache.mana = strings.TrimRight(p, "/")
}

func setResourceBasePath(p string) {
	pathCache.mu.Lock()
	defer pathCache.mu.Unlock()
	pathCache.resource = strings.TrimRight(p, "/")
}

func managementBasePath() string {
	pathCache.mu.RLock()
	defer pathCache.mu.RUnlock()
	if pathCache.mana != "" {
		return pathCache.mana
	}
	return "/v0/management"
}

func resourceBasePath() string {
	pathCache.mu.RLock()
	defer pathCache.mu.RUnlock()
	if pathCache.resource != "" {
		return pathCache.resource
	}
	return "/v0/resource/plugins/" + pluginName
}

// managementRegistration declares the plugin's own API routes plus the browser
// panel resource. Routes under /v0/management are authenticated by CPA itself,
// so the plugin adds no key handling of its own; only the panel HTML is served
// unauthenticated, and it carries no secrets.
func managementRegistration() managementRegistrationResponse {
	base := "/plugins/" + pluginName
	return managementRegistrationResponse{
		Routes: []managementRoute{
			{Method: http.MethodGet, Path: base + "/status", Description: "Effective ordering rule and the model lists CPA last served per port."},
			{Method: http.MethodPost, Path: base + "/preview", Description: "Apply a candidate rule to a model list and return the resulting order with the rule each model matched."},
		},
		Resources: []resourceRoute{
			{Path: "/panel", Menu: "Model Order", Description: "Edit the model listing order rule and preview the result."},
		},
	}
}

func handleManagement(raw []byte) ([]byte, error) {
	var req managementRequestWire
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	path := strings.TrimRight(req.Path, "/")

	// Browser panel: a static resource, served ahead of any API handling.
	if req.Method == http.MethodGet && strings.HasPrefix(path, resourceBasePath()) {
		return okEnvelope(mgmtHTMLResponse(renderPanel()))
	}

	base := managementBasePath() + "/plugins/" + pluginName
	switch {
	case req.Method == http.MethodGet && path == base+"/status":
		return okEnvelope(mgmtJSONResponse(http.StatusOK, statusPayload()))
	case req.Method == http.MethodPost && path == base+"/preview":
		return handlePreview(req.Body)
	case req.Method == http.MethodGet && path == base:
		return okEnvelope(mgmtJSONResponse(http.StatusOK, statusPayload()))
	default:
		return okEnvelope(mgmtJSONResponse(http.StatusNotFound, map[string]any{
			"error": "not_found", "path": path,
		}))
	}
}

// statusPayload reports the effective rule plus whether it is configured at all.
// With no built-in fallback, an unconfigured plugin groups nothing, and the panel
// has to say so plainly rather than dress an empty list up as a default.
// suggested_order is the editor template and is never applied on its own.
func statusPayload() map[string]any {
	cfg := currentConfig()
	return map[string]any{
		"version":         version,
		"strategy":        cfg.strategy,
		"case_sensitive":  cfg.caseSensitive,
		"order":           cfg.order,
		"configured":      len(cfg.order) > 0,
		"suggested_order": append([]string(nil), suggestedOrder...),
		"catalogs":        catalog.list(),
	}
}

// previewRequest describes a candidate rule to evaluate without applying it.
type previewRequest struct {
	Strategy      string   `json:"strategy"`
	Order         []string `json:"order"`
	CaseSensitive bool     `json:"case_sensitive"`
	// Port selects a captured listing when IDs are omitted.
	Port string `json:"port"`
	// IDs overrides the captured list, which lets the panel preview a rule
	// against a hand written set.
	IDs []string `json:"ids"`
}

func handlePreview(body []byte) ([]byte, error) {
	var req previewRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return okEnvelope(mgmtJSONResponse(http.StatusBadRequest, map[string]any{"error": "invalid_body"}))
		}
	}

	ids := req.IDs
	port := req.Port
	if len(ids) == 0 {
		if port == "" {
			if listed := catalog.list(); len(listed) > 0 {
				port = listed[0].Port
			}
		}
		if snapshot, ok := catalog.get(port); ok {
			ids = idsFromEntries(snapshot.Entries)
		}
	}
	if len(ids) == 0 {
		return okEnvelope(mgmtJSONResponse(http.StatusOK, map[string]any{
			"port":    port,
			"ordered": []string{},
			"matched": map[string]string{},
			"note":    "no listing captured yet: pull /v1/models from any client first",
		}))
	}

	strategy := strings.TrimSpace(req.Strategy)
	if strategy == "" {
		strategy = StrategyGrouped
	}
	order := req.Order
	if len(order) == 0 && req.Strategy == "" {
		order = currentConfig().order
	}
	matchers := compileMatchers(order, req.CaseSensitive)
	less := comparer(strategy, matchers, req.CaseSensitive)

	ordered := append([]string(nil), ids...)
	sortIDs(ordered, less)

	matched := make(map[string]string, len(ordered))
	for _, id := range ordered {
		matched[id] = matchedPattern(id, matchers, req.CaseSensitive)
	}
	return okEnvelope(mgmtJSONResponse(http.StatusOK, map[string]any{
		"port":    port,
		"ordered": ordered,
		"matched": matched,
	}))
}

func idsFromEntries(entries []catalogEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.ID)
	}
	return out
}

// matchedPattern reports which configured rule claims a model, or "" when the
// model falls into the alphabetical tail. This is the answer the panel exists to
// give: it is the quickest way to see why one model sits where it does.
func matchedPattern(id string, matchers []matcher, caseSensitive bool) string {
	probe := id
	if !caseSensitive {
		probe = strings.ToLower(id)
	}
	for _, matcher := range matchers {
		if matcher.match(probe) {
			return matcher.raw
		}
	}
	return ""
}

// renderPanel stamps the host provided management base path into the page. CPA
// can mount the management API anywhere, so the panel must not assume it.
func renderPanel() string {
	return strings.ReplaceAll(panelHTML, "__MO_MANAGEMENT_BASE_PATH_JSON__", strconv.Quote(managementBasePath()))
}

func mgmtJSONResponse(status int, payload any) pluginapi.ManagementResponse {
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte(`{"error":"marshal_failed"}`)
		status = http.StatusInternalServerError
	}
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       raw,
	}
}

func mgmtHTMLResponse(html string) pluginapi.ManagementResponse {
	return pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:       []byte(html),
	}
}
