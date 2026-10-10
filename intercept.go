package main

import (
	"net/http"
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

// governBody applies the two list transformations in the order they must happen:
// visibility first, then ordering, and finally the catalog record.
//
// Filtering cannot run after the sort. The sort's job is to make every captured
// model reachable, and a caller-restricted listing must not put a hidden model
// back into the response by way of the alphabetical tail. Running filter first
// also means the catalog records what the most restrictive caller was served,
// which is the useful thing to show in the panel.
func governBody(sourceFormat string, headers http.Header, body []byte) ([]byte, bool) {
	list, errParse := parseModelList(body)
	if errParse != nil {
		return nil, false
	}
	port := portFor(sourceFormat, readCatalogEntries(list.elements))
	changed := false

	// The pre-filter catalog is recorded separately from the one the callers see.
	//
	// catalog.record below stores what this caller was served, which is the right
	// thing to display but the wrong thing to edit visibility against: once a model
	// is denied it stops appearing here, so the panel would lose the row and with it
	// the only way to un-deny it. fullCatalog keeps CPA's own pre-policy listing, so
	// a denied model stays listed and restorable.
	fullCatalog.record(port, readCatalogEntries(list.elements))

	if policy, ok := resolvePolicy(headers, port); ok {
		kept := make([][]byte, 0, len(list.elements))
		for _, element := range list.elements {
			if policy.allows(modelItemKey(element)) {
				kept = append(kept, element)
			}
		}
		// The removal itself is a change to the body. Without this, a listing
		// that filtered successfully but happened to already be in the
		// configured order would report "unchanged" and hand the caller back
		// CPA's original bytes, silently reinstating the hidden models.
		changed = changed || len(kept) != len(list.elements)
		list.elements = kept
	}

	_, reordered := reorder(list)
	changed = changed || reordered
	entries := readCatalogEntries(list.elements)
	catalog.record(port, entries)
	return list.render(), changed
}

// resolvePolicy finds the visibility rule for this request.
//
// The port check is part of the lookup rather than a separate gate so that
// "no policy for this caller" and "this port is not filtered" return the same
// thing: nothing to do. Both leave the body untouched, and neither can narrow an
// unrestricted key, because an unrestricted key has no entry in the table.
func resolvePolicy(headers http.Header, port string) (*accessPolicy, bool) {
	if !filterPort(port) {
		return nil, false
	}
	scope := callerScope(callerCredential(headers))
	if scope == "" {
		return nil, false
	}
	return currentConfig().access.lookup(scope)
}

// reorder sorts the list elements into the configured order and reports whether
// the order actually differs from what arrived. A false means the caller should
// return no body at all so CPA keeps its own bytes.
//
// CPA builds the list by ranging over a map, so the incoming order is
// arbitrary. The list is therefore always reordered rather than trusted to be
// sorted already.
func reorder(list *modelList) ([]byte, bool) {
	cfg := currentConfig()
	less := comparer(cfg.strategy, cfg.matchers, cfg.caseSensitive)

	keys := make([]string, len(list.elements))
	for i, element := range list.elements {
		keys[i] = modelItemKey(element)
	}
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
	list.elements = reordered
	return nil, changed
}
