package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
)

// Capability backfill exists because CPA answers model listings in more than one
// shape and only one of them carries capability facts.
//
// The Codex client catalog (GET /v1/models?client_version=...) is built from the
// model registry and emits roughly forty-five fields per entry, including
// context_window, max_context_window, input_modalities and the reasoning levels.
// The plain OpenAI listing (GET /v1/models) is produced by a different handler
// that deliberately projects every entry down to id/object/created/owned_by, so
// a client reading it learns nothing about what a model can do.
//
// Both listings describe the same models: this deployment serves the same 48 in
// each. The plugin already sees both bodies, so it can carry the facts across.
// It learns them from whatever catalog traffic CPA actually serves and replays
// them into the listing that dropped them.
//
// This only works because the response interceptor runs AFTER the handler has
// projected the body: whatever a plugin returns becomes the body. The projection
// is the input to this seam, not the final word on what a client receives.

// capability holds the facts carried from the Codex client catalog into the
// plain listing. Everything else in a catalog entry (instruction templates,
// plan lists, tool switches) describes client behaviour rather than the model's
// capabilities, and stays where it is.
type capability struct {
	ContextLength         int64
	MaxContextLength      int64
	InputModalities       []string
	ReasoningLevels       []string
	DefaultReasoningLevel string
}

// empty reports whether a capability carries nothing worth injecting.
func (c capability) empty() bool {
	return c.ContextLength <= 0 &&
		c.MaxContextLength <= 0 &&
		len(c.InputModalities) == 0 &&
		len(c.ReasoningLevels) == 0 &&
		c.DefaultReasoningLevel == ""
}

// capabilityStore maps a public model ID to the facts last learned for it.
//
// It is keyed by the client-facing model ID, not by a provider upstream name:
// the two listings this plugin bridges are both addressed by the public name, and
// an upstream name is only meaningful inside one channel.
type capabilityStore struct {
	mu   sync.RWMutex
	byID map[string]capability
	// channels holds every provider's opinion about a model, so the listing can
	// apply the intersection/union rules instead of showing whichever channel CPA
	// happened to register last.
	channels map[string][]providerFacts
	// lower indexes the same records under a case-folded key so a lookup that
	// misses exactly can still find a model whose casing differs between the two
	// listings. CPA resolves model availability with strings.EqualFold, so a
	// case-only difference must not silently drop a backfill.
	lower map[string]string
}

var capabilities capabilityStore

// recordCodexCatalog learns capability facts from a Codex client catalog body.
//
// Entries without a slug are skipped: the slug is the only identity that lines
// up with the plain listing, and guessing one from display_name would attach
// facts to the wrong model.
func (s *capabilityStore) recordCodexCatalog(elements [][]byte) int {
	learned := make(map[string]capability, len(elements))
	for _, element := range elements {
		var raw struct {
			Slug                     string   `json:"slug"`
			ContextWindow            int64    `json:"context_window"`
			MaxContextWindow         int64    `json:"max_context_window"`
			InputModalities          []string `json:"input_modalities"`
			DefaultReasoningLevel    string   `json:"default_reasoning_level"`
			SupportedReasoningLevels []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		}
		if errUnmarshal := json.Unmarshal(element, &raw); errUnmarshal != nil {
			continue
		}
		id := strings.TrimSpace(raw.Slug)
		if id == "" {
			continue
		}
		facts := capability{
			ContextLength:         raw.ContextWindow,
			MaxContextLength:      raw.MaxContextWindow,
			InputModalities:       compactStrings(raw.InputModalities),
			DefaultReasoningLevel: strings.TrimSpace(raw.DefaultReasoningLevel),
		}
		for _, level := range raw.SupportedReasoningLevels {
			if effort := strings.TrimSpace(level.Effort); effort != "" {
				facts.ReasoningLevels = append(facts.ReasoningLevels, effort)
			}
		}
		if facts.empty() {
			continue
		}
		learned[id] = facts
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byID == nil {
		s.byID = make(map[string]capability, len(learned))
		s.lower = make(map[string]string, len(learned))
	}
	for id, facts := range learned {
		s.byID[id] = facts
		// Index every record under its folded key, including IDs that are already
		// lower case. A lookup arrives with the caller's casing, which may be
		// upper case while the recorded ID is not; a folded index built only from
		// mixed-case IDs would miss exactly those.
		s.lower[strings.ToLower(id)] = id
	}
	return len(learned)
}

// lookup finds the facts recorded for one public model ID.
func (s *capabilityStore) lookup(id string) (capability, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return capability{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if facts, ok := s.byID[id]; ok {
		return facts, true
	}
	// Fall back to the folded index unconditionally: the caller's casing may match
	// the folded form while the recorded ID is mixed case, which is the mirror of
	// the case the exact lookup above handles.
	if original, ok := s.lower[strings.ToLower(id)]; ok {
		if facts, okFacts := s.byID[original]; okFacts {
			return facts, true
		}
	}
	return capability{}, false
}

// backfill injects learned capability facts into every entry of a plain listing
// that is missing them, and reports whether any entry changed.
//
// Entries are rewritten in place in the caller's slice so the surrounding
// ordering and catalog recording see the bodies that will actually be served.
func (s *capabilityStore) backfill(elements [][]byte) bool {
	s.mu.RLock()
	empty := len(s.byID) == 0
	s.mu.RUnlock()
	if empty {
		return false
	}

	changed := false
	for i, element := range elements {
		facts, ok := s.lookup(modelItemKey(element))
		if !ok {
			continue
		}
		if next, did := injectCapability(element, facts); did {
			elements[i] = next
			changed = true
		}
	}
	return changed
}

// count reports how many models currently have learned facts, for the panel.
func (s *capabilityStore) count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byID)
}

// reset drops every learned fact. Tests use it to start from a known state.
func (s *capabilityStore) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID = nil
	s.lower = nil
	s.channels = nil
}

// applyChannelFacts replaces the per-channel table and, for every model that
// more than one channel serves, overrides the catalog-derived facts with the
// aggregated result.
//
// The override is keyed by public model ID. Upstream names never appear here: a
// channel's internal name for a model is only meaningful inside that channel.
func (s *capabilityStore) applyChannelFacts(byModel map[string][]providerFacts) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channels = byModel

	applied := 0
	for id, channels := range byModel {
		if len(channels) < 2 {
			// A single channel already describes the model accurately; the catalog
			// entry is as good as this and may carry fields the store does not.
			continue
		}
		contextLength, levels := aggregateChannels(channels)
		key := s.resolveKey(id)
		if key == "" {
			continue
		}
		current := s.byID[key]
		if contextLength > 0 {
			current.ContextLength = contextLength
			current.MaxContextLength = contextLength
		}
		if len(levels) > 0 {
			current.ReasoningLevels = levels
			// The default a single channel reports is not a safe default across
			// channels: qoder defaults dfmodel to 200K while workbuddy defaults the
			// same model to a different tier. Drop it rather than mislead.
			current.DefaultReasoningLevel = ""
		}
		if current.empty() {
			continue
		}
		s.byID[key] = current
		applied++
	}
	return applied
}

// resolveKey returns the stored key matching id exactly or case-insensitively.
// Callers hold the lock.
func (s *capabilityStore) resolveKey(id string) string {
	if _, ok := s.byID[id]; ok {
		return id
	}
	if original, ok := s.lower[strings.ToLower(id)]; ok {
		return original
	}
	return ""
}

// channelReport returns the per-channel facts for the status route, so an
// operator can see why a model's advertised context is what it is.
func (s *capabilityStore) channelReport() map[string][]map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string][]map[string]any, len(s.channels))
	for id, channels := range s.channels {
		if len(channels) < 2 {
			continue
		}
		entries := make([]map[string]any, 0, len(channels))
		for _, channel := range channels {
			entries = append(entries, map[string]any{
				"provider":        channel.Provider,
				"context_default": channel.ContextDefault,
				"context_max":     channel.ContextMax,
				"levels":          channel.Levels,
			})
		}
		out[id] = entries
	}
	return out
}

// capabilityField is one value to append to a listing entry.
type capabilityField struct {
	name  string
	value any
}

// injectCapability appends the capability fields an entry is missing.
//
// The entry is edited at the byte level rather than round-tripped through a map:
// the listing is spliced from original bytes elsewhere in this plugin precisely
// so numbers keep the formatting CPA produced, and re-encoding a whole entry
// here would undo that. Only the missing fields are added; anything the entry
// already declares, from any source, wins.
func injectCapability(element []byte, facts capability) ([]byte, bool) {
	end := bytes.LastIndexByte(element, '}')
	if end < 0 {
		return element, false
	}

	var present map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(element, &present); errUnmarshal != nil {
		return element, false
	}

	fields := make([]capabilityField, 0, 5)
	if facts.ContextLength > 0 {
		fields = append(fields, capabilityField{"context_length", facts.ContextLength})
	}
	if facts.MaxContextLength > 0 {
		fields = append(fields, capabilityField{"max_context_length", facts.MaxContextLength})
	}
	if len(facts.InputModalities) > 0 {
		fields = append(fields, capabilityField{"input_modalities", facts.InputModalities})
	}
	if len(facts.ReasoningLevels) > 0 {
		fields = append(fields, capabilityField{"reasoning_levels", facts.ReasoningLevels})
	}
	if facts.DefaultReasoningLevel != "" {
		fields = append(fields, capabilityField{"default_reasoning_level", facts.DefaultReasoningLevel})
	}

	var additions []byte
	for _, field := range fields {
		if _, exists := present[field.name]; exists {
			continue
		}
		raw, errMarshal := json.Marshal(field.value)
		if errMarshal != nil {
			continue
		}
		additions = append(additions, ',')
		additions = append(additions, '"')
		additions = append(additions, field.name...)
		additions = append(additions, '"', ':')
		additions = append(additions, raw...)
	}
	if len(additions) == 0 {
		return element, false
	}

	// An entry that is an empty object must not receive a leading comma.
	if isEmptyJSONObject(element[:end]) {
		additions = additions[1:]
	}

	out := make([]byte, 0, len(element)+len(additions))
	out = append(out, element[:end]...)
	out = append(out, additions...)
	out = append(out, element[end:]...)
	return out, true
}

// isEmptyJSONObject reports whether head is an object with no members yet, so the
// caller knows not to emit a leading comma.
func isEmptyJSONObject(head []byte) bool {
	trimmed := bytes.TrimRight(head, " \t\r\n")
	if len(trimmed) == 0 {
		return false
	}
	return trimmed[len(trimmed)-1] == '{'
}

// compactStrings trims each value and drops the empty ones, returning nil when
// nothing survives so an empty list is never injected.
func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
