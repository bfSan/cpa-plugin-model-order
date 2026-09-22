package main

import (
	"sort"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// isModelListing reports whether an intercepted response is a model listing.
//
// CPA serves model lists through WriteModelListResponse, which is the only call
// site that leaves Model, RequestedModel, OriginalRequest and RequestBody all
// empty. Chat responses always carry a model and a request body, so this check
// keeps ordinary traffic off the sorting path at negligible cost.
func isModelListing(req pluginapi.ResponseInterceptRequest) bool {
	if req.StatusCode != 200 || req.Stream {
		return false
	}
	if req.Model != "" || req.RequestedModel != "" {
		return false
	}
	if len(req.OriginalRequest) > 0 || len(req.RequestBody) > 0 {
		return false
	}
	return len(req.Body) > 0
}

// orderBody rewrites a model listing body into the configured order, and records
// what was served so the panel can show real ids rather than guesses.
// The second result reports whether anything changed; a false means the caller
// should return no body at all so CPA keeps its own bytes.
func orderBody(sourceFormat string, body []byte) ([]byte, bool) {
	list, errParse := parseModelList(body)
	if errParse != nil {
		return nil, false
	}
	cfg := currentConfig()
	less := comparer(cfg.strategy, cfg.matchers, cfg.caseSensitive)

	keys := make([]string, len(list.elements))
	for i, element := range list.elements {
		keys[i] = modelItemKey(element)
	}
	// CPA builds the list by ranging over a map, so the incoming order is
	// arbitrary. Always reorder rather than trusting "already sorted".
	order := make([]int, len(list.elements))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		return less(keys[order[i]], keys[order[j]])
	})

	reordered := make([][]byte, len(list.elements))
	changed := false
	for position, source := range order {
		reordered[position] = list.elements[source]
		if source != position {
			changed = true
		}
	}
	entries := readCatalogEntries(reordered)
	catalog.record(portFor(sourceFormat, entries), entries)

	if !changed {
		return nil, false
	}
	list.elements = reordered
	return list.render(), true
}
