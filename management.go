package main

import (
	"encoding/json"
	"net/http"
	"sort"
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
			{Method: http.MethodGet, Path: base + "/status", Description: "Effective ordering rule, visibility policies and the model lists CPA last served per port."},
			{Method: http.MethodPost, Path: base + "/preview", Description: "Apply a candidate rule to a model list and return the resulting order with the rule each model matched."},
			{Method: http.MethodGet, Path: base + "/alias-report", Description: "Models that reached a client under a bare name, judged from the listing alone when no channel list is given."},
			{Method: http.MethodPost, Path: base + "/alias-report", Description: "Models that reached a client under a bare name, given the alias channels they were judged against."},
		},
		Resources: []resourceRoute{
			{Path: "/panel", Menu: "Model Registry", Description: "Edit the model listing order and per key visibility, preview the result, and add the aliases CPA is missing."},
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
	case req.Method == http.MethodGet && path == base+"/alias-report":
		return handleAliasReport(nil)
	case req.Method == http.MethodPost && path == base+"/alias-report":
		return handleAliasReport(req.Body)
	case req.Method == http.MethodGet && path == base:
		return okEnvelope(mgmtJSONResponse(http.StatusOK, statusPayload()))
	default:
		return okEnvelope(mgmtJSONResponse(http.StatusNotFound, map[string]any{
			"error": "not_found", "path": path,
		}))
	}
}

// aliasReportRequest carries the alias channel names the report should judge
// against.
type aliasReportRequest struct {
	// Channels lists the providers that have a channel in CPA's
	// oauth-model-alias table. The panel reads that table anyway in order to
	// write it, so it passes the names through rather than the plugin guessing
	// which providers participate: CPA's built-in openai serves gpt-6.1-sol and
	// friends under their intended names and has no channel at all, and treating
	// its fourteen models as missing aliases buried the real report in noise.
	//
	// Empty is allowed and means "unknown": the report then judges from the
	// listing instead.
	Channels []string `json:"channels"`
	// ModelProviders maps a bare model id to the providers whose credentials
	// serve it, which the panel derives from the credential model catalogs.
	//
	// It exists because owned_by is not always set. trae reaches clients with an
	// empty owned_by on all twenty of its models, so the report dropped every one
	// of them and had nothing to say about a provider that was plainly in use.
	// The plugin cannot read the credential catalogs itself -- the host exposes no
	// RPC for them -- so the panel supplies the mapping.
	ModelProviders map[string][]string `json:"model_providers"`
}

// handleAliasReport answers "which models are leaking under a bare name".
//
// It reads only the catalog the plugin already recorded from real client
// traffic, so it needs no upstream call and cannot perturb the listing. When
// nothing has been captured the response says so instead of returning an empty
// report that reads like "no problems found".
func handleAliasReport(body []byte) ([]byte, error) {
	var req aliasReportRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return okEnvelope(mgmtJSONResponse(http.StatusBadRequest, map[string]any{"error": "invalid_body"}))
		}
	}
	channels := make(map[string]bool, len(req.Channels))
	for _, name := range req.Channels {
		if trimmed := strings.ToLower(strings.TrimSpace(name)); trimmed != "" {
			channels[trimmed] = true
		}
	}
	snapshots := catalog.list()
	if len(snapshots) == 0 {
		return okEnvelope(mgmtJSONResponse(http.StatusOK, map[string]any{
			"error":   "no_listing_captured",
			"note":    "pull /v1/models from any client first, then reopen this report",
			"reports": []aliasReport{},
		}))
	}
	reports := make([]aliasReport, 0, len(snapshots))
	for _, snapshot := range snapshots {
		reports = append(reports, buildAliasReportWithProviders(snapshot.Port, snapshot.Entries, channels, req.ModelProviders))
	}
	return okEnvelope(mgmtJSONResponse(http.StatusOK, map[string]any{"reports": reports}))
}

// statusPayload reports the effective rule plus whether it is configured at all.
// With no built-in fallback, an unconfigured plugin groups nothing, and the panel
// has to say so plainly rather than dress an empty list up as a default.
// suggested_order is the editor template and is never applied on its own.
func statusPayload() map[string]any {
	cfg := currentConfig()
	// policies are listed by scope, never by key. The panel needs to show that
	// a scope is constrained, and an operator who wrote the config can read the
	// label; the credential behind a scope is not recoverable from the digest and
	// is deliberately not echoed anywhere.
	policies := make([]map[string]any, 0, cfg.access.count)
	for scope, policy := range cfg.access.policies {
		policies = append(policies, map[string]any{
			"caller_scope":   scope,
			"label":          policy.Label,
			"allow_models":   policy.AllowModels,
			"deny_models":    policy.DenyModels,
			"case_sensitive": policy.CaseSensitive,
		})
	}
	sort.SliceStable(policies, func(i, j int) bool {
		left, _ := policies[i]["caller_scope"].(string)
		right, _ := policies[j]["caller_scope"].(string)
		return left < right
	})
	return map[string]any{
		"version":         version,
		"strategy":        cfg.strategy,
		"case_sensitive":  cfg.caseSensitive,
		"order":           cfg.order,
		"configured":      len(cfg.order) > 0,
		"suggested_order": append([]string(nil), suggestedOrder...),
		"policies":        policies,
		"policy_count":    cfg.access.count,
		"catalogs":        catalog.list(),
		// full_catalogs is the pre-policy listing. The panel's per-key view has to
		// offer a model the operator already denied, or the deny could never be
		// undone; catalogs above no longer contains those rows.
		"full_catalogs": fullCatalog.list(),
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
	// Scope previews the listing as one caller sees it. Empty means "no key
	// selected", which previews the unrestricted listing.
	//
	// It is a plain sha256 digest, never a credential: the panel derives it in the
	// browser (the same way key-model-access does) so the API key itself never
	// reaches this plugin.
	Scope string `json:"scope"`
	// Deny/Allow override the policy stored for Scope, so the panel can preview a
	// hide the operator has not saved yet. When both are nil the stored policy is
	// used as-is.
	Deny  *[]string `json:"deny"`
	Allow *[]string `json:"allow"`
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
		// A scoped preview reads the pre-policy catalog: the caller-visible one
		// no longer holds the models this key denied, so previewing against it
		// could never show a denied row as restorable.
		store := &catalog
		if strings.TrimSpace(req.Scope) != "" {
			store = &fullCatalog
		}
		if snapshot, ok := store.get(port); ok {
			ids = idsFromEntries(snapshot.Entries)
		} else if snapshot, ok := catalog.get(port); ok {
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

	// denied reports the ids the selected key cannot see. The ids stay in
	// `ordered` regardless: the panel renders them greyed in place so the operator
	// can restore one, and a preview that simply dropped them would hide the only
	// path back.
	denied := map[string]bool{}
	policy := previewPolicy(req, port)
	if policy != nil {
		for _, id := range ids {
			if !policy.allows(id) {
				denied[id] = true
			}
		}
	}

	ordered := append([]string(nil), ids...)
	sortIDs(ordered, less)

	matched := make(map[string]string, len(ordered))
	for _, id := range ordered {
		matched[id] = matchedPattern(id, matchers, req.CaseSensitive)
	}
	deniedIDs := make([]string, 0, len(denied))
	for _, id := range ordered {
		if denied[id] {
			deniedIDs = append(deniedIDs, id)
		}
	}
	return okEnvelope(mgmtJSONResponse(http.StatusOK, map[string]any{
		"port":    port,
		"ordered": ordered,
		"matched": matched,
		"denied":  deniedIDs,
		"scoped":  strings.TrimSpace(req.Scope) != "",
	}))
}

// previewPolicy builds the visibility rule a scoped preview should apply.
//
// The panel sends the deny list it is editing, so an unsaved hide is visible
// before it is written; the stored policy for that scope fills in what the panel
// did not send. A preview with no scope is unrestricted, which is what makes
// "no key selected" show the whole listing.
//
// port matters: filtering only runs on the ports filterPort accepts. A preview
// that marked models denied on the Claude port would show an operator a hide the
// runtime never applies.
func previewPolicy(req previewRequest, port string) *accessPolicy {
	scope := strings.TrimSpace(req.Scope)
	if scope == "" || !filterPort(port) {
		return nil
	}
	policy := &accessPolicy{CallerScope: scope}
	if stored, ok := currentConfig().access.lookup(scope); ok {
		policy.CaseSensitive = stored.CaseSensitive
		policy.AllowModels = append([]string(nil), stored.AllowModels...)
		policy.DenyModels = append([]string(nil), stored.DenyModels...)
	}
	if req.Allow != nil {
		policy.AllowModels = append([]string(nil), (*req.Allow)...)
	}
	if req.Deny != nil {
		policy.DenyModels = append([]string(nil), (*req.Deny)...)
	}
	return policy
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
