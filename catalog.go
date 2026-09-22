package main

import (
	"encoding/json"
	"sort"
	"sync"
	"time"
)

// Listing ports CPA serves models through. The OpenAI port answers twice with
// different shapes: the plain listing keys entries by id, while the Codex client
// catalog (the ?client_version= form that Codex CLI and Codex++ read) keys them by
// slug. They are tracked apart because the Anthropic port cloaks ids and the
// Codex catalog carries its own names, so a rule that reads well on one port may
// not match on another.
const (
	portOpenAI  = "openai"
	portCodex   = "codex"
	portClaude  = "claude"
	portGemini  = "gemini"
	portUnknown = "unknown"
)

// catalogEntry is one model as it actually reached a client.
type catalogEntry struct {
	ID          string `json:"id"`
	OwnedBy     string `json:"owned_by,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	// slugForm marks an entry out of the Codex client catalog, which identifies
	// models with "slug" instead of "id". Internal only: the panel gets the
	// resolved ID, this just tells the two OpenAI-shaped ports apart.
	slugForm bool
}

// catalogSnapshot is the last ordered listing CPA served for one port.
type catalogSnapshot struct {
	Port    string         `json:"port"`
	Count   int            `json:"count"`
	SeenAt  time.Time      `json:"seen_at"`
	Entries []catalogEntry `json:"entries"`
}

type catalogStore struct {
	mu        sync.RWMutex
	snapshots map[string]catalogSnapshot
}

var catalog catalogStore

func (s *catalogStore) record(port string, entries []catalogEntry) {
	if len(entries) == 0 {
		return
	}
	snapshot := catalogSnapshot{
		Port:    port,
		Count:   len(entries),
		SeenAt:  time.Now().UTC(),
		Entries: entries,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshots == nil {
		s.snapshots = make(map[string]catalogSnapshot)
	}
	s.snapshots[port] = snapshot
}

func (s *catalogStore) get(port string) (catalogSnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot, ok := s.snapshots[port]
	return snapshot, ok
}

// list returns one snapshot per port, newest first, so the panel can default to
// whatever the user most recently asked a client for.
func (s *catalogStore) list() []catalogSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]catalogSnapshot, 0, len(s.snapshots))
	for _, snapshot := range s.snapshots {
		out = append(out, snapshot)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].SeenAt.Equal(out[j].SeenAt) {
			return out[i].SeenAt.After(out[j].SeenAt)
		}
		return out[i].Port < out[j].Port
	})
	return out
}

// portFor names the listing port an intercepted body came from. Only the OpenAI
// port answers in two shapes, so the slug field is what separates a Codex client
// catalog from a plain listing.
func portFor(sourceFormat string, entries []catalogEntry) string {
	switch sourceFormat {
	case portClaude:
		return portClaude
	case portGemini:
		return portGemini
	}
	for _, entry := range entries {
		if entry.slugForm {
			return portCodex
		}
	}
	return portOpenAI
}

// readCatalogEntries pulls the display fields out of already parsed elements.
// The identity follows modelItemKey so what the panel shows is what the ordering
// actually compared on.
func readCatalogEntries(elements [][]byte) []catalogEntry {
	out := make([]catalogEntry, 0, len(elements))
	for _, element := range elements {
		var raw struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			Slug          string `json:"slug"`
			OwnedBy       string `json:"owned_by"`
			DisplayName   string `json:"display_name"`
			DisplayNameUC string `json:"displayName"`
		}
		if err := json.Unmarshal(element, &raw); err != nil {
			continue
		}
		entry := catalogEntry{
			ID:          modelItemKey(element),
			OwnedBy:     raw.OwnedBy,
			DisplayName: raw.DisplayName,
			slugForm:    raw.ID == "" && raw.Name == "" && raw.Slug != "",
		}
		if entry.DisplayName == "" {
			entry.DisplayName = raw.DisplayNameUC
		}
		if entry.ID == "" {
			continue
		}
		out = append(out, entry)
	}
	return out
}
