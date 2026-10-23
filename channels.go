package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Provider channel aggregation.
//
// CPA serves one listing per model, but several provider plugins may each offer
// that model with different capabilities. CPA itself keeps only the last
// registered channel's ModelInfo, so the listing has always shown one channel's
// opinion as if it were the model's.
//
// Measured here (2026-10-23, 27 models served by more than one channel):
//
//	minimax-m3   trae 1000000 vs workbuddy 512000
//	dfmodel      qoder-CN and qoder-Intl both offer tiers up to 1000000,
//	             while the default tier each reports is 200000
//	kimi-k3      trae reports four reasoning levels, workbuddy one
//
// Two different rules follow from that, and they are not symmetrical:
//
//	context   -> intersection. Declaring more context than a channel can take is
//	             a silent truncation, which is unrecoverable and invisible.
//	reasoning -> union. A level a channel does not accept is at worst one rejected
//	             parameter. Intersecting instead can empty the list entirely:
//	             kimi-k3 has one channel reporting four levels and another
//	             reporting none, and a strict intersection would leave no level
//	             and quietly disable thinking.
//
// The facts are read from each provider plugin's own model catalog on disk. The
// host exposes no RPC that returns per-channel capability data: ModelsForAuth is
// only offered to provider plugins, and the registry keeps one ModelInfo per
// model. Reading those stores couples this plugin to their on-disk format, so a
// missing or unreadable store is skipped rather than reported as a failure, and
// the aggregation falls back to whatever the catalog fetch learned.

// providerCatalogDirs maps a provider key to the directory holding its model
// catalog. The shape is <config-dir>/CLIProxyAPI/<provider>/..., which both
// plugins derive from os.UserConfigDir.
type providerCatalogSource struct {
	// Provider is the channel label reported in the panel.
	Provider string
	// Dir is the catalog directory relative to <user-config>/CLIProxyAPI.
	Dir string
	// Kind selects the parser for that provider's document shape.
	Kind providerCatalogKind
}

type providerCatalogKind string

const (
	// kindWorkbuddy reads models/<hash>.json documents with a `models` array of
	// {id, context_length, supported_context_lengths, supported_efforts}.
	kindWorkbuddy providerCatalogKind = "workbuddy"
	// kindQoder reads <hash>.json documents with a `models` array plus a
	// `region_facts` map carrying thinking levels and context tiers.
	kindQoder providerCatalogKind = "qoder"
	// kindTraeAuth reads one credential document whose model_cache holds the
	// advertised list. Trae keeps no separate catalog directory: the snapshot
	// lives inside the credential file, so this source reads the auth directory
	// rather than the config directory.
	kindTraeAuth providerCatalogKind = "trae-auth"
)

// defaultProviderSources lists the provider catalogs this deployment publishes.
// Each entry is best-effort: an absent directory contributes nothing.
var defaultProviderSources = []providerCatalogSource{
	{Provider: "workbuddy", Dir: "workbuddy/model-catalog/models", Kind: kindWorkbuddy},
	{Provider: "qoder", Dir: "qoder/model-catalog", Kind: kindQoder},
	{Provider: "trae", Dir: "", Kind: kindTraeAuth},
}

// authDir is where credential documents live, resolved from CPA's config. Trae's
// catalog is embedded in its credential file, so the aggregator needs this
// directory in addition to the config directory.
var authDir = "/opt/cpa/auths"

// providerFacts is one channel's opinion about one model.
type providerFacts struct {
	Provider string
	// ContextDefault is the context window the provider serves by default.
	ContextDefault int64
	// ContextMax is the largest selectable context, or ContextDefault when the
	// provider advertises no tiers. This is what an intersection has to compare:
	// a provider that defaults to 200K but offers a 1M tier can serve 1M.
	ContextMax int64
	Levels     []string
}

// channelFacts holds every channel's opinion about one model.
type channelFacts struct {
	Model    string
	Channels []providerFacts
}

// gatherProviderFacts reads every configured provider catalog and groups the
// results by lowercase model ID.
func gatherProviderFacts(configDir string) map[string][]providerFacts {
	out := make(map[string][]providerFacts)
	for _, source := range defaultProviderSources {
		var dir string
		if source.Kind == kindTraeAuth {
			dir = authDir
		} else {
			dir = filepath.Join(configDir, "CLIProxyAPI", filepath.FromSlash(source.Dir))
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			raw, errRead := os.ReadFile(filepath.Join(dir, entry.Name()))
			if errRead != nil {
				continue
			}
			var facts map[string]providerFacts
			switch source.Kind {
			case kindWorkbuddy:
				facts = parseWorkbuddyCatalog(raw, source.Provider)
			case kindQoder:
				facts = parseQoderCatalog(raw, source.Provider)
			case kindTraeAuth:
				facts = parseTraeAuth(raw, source.Provider)
			}
			for id, fact := range facts {
				out[id] = append(out[id], fact)
			}
		}
	}
	// One provider writes several catalog files: one per credential, plus one per
	// realm. They describe the same channel, and keeping each copy would let a
	// duplicated channel outvote the others in the union and list the same
	// provider twice. Keep the first opinion per channel label.
	//
	// Sort first so "first" is deterministic: directory order is not guaranteed,
	// and a listing whose provenance reshuffles between reads is hard to reason
	// about. Sorting by provider label and then by maximum context keeps the most
	// capable copy, which is the one that constrains an intersection least.
	for id, channels := range out {
		sort.SliceStable(channels, func(i, j int) bool {
			if channels[i].Provider != channels[j].Provider {
				return channels[i].Provider < channels[j].Provider
			}
			return channels[i].ContextMax > channels[j].ContextMax
		})
		deduped := make([]providerFacts, 0, len(channels))
		for i, channel := range channels {
			if i > 0 && channel.Provider == channels[i-1].Provider {
				continue
			}
			deduped = append(deduped, channel)
		}
		out[id] = deduped
	}
	return out
}

// parseWorkbuddyCatalog reads one workbuddy catalog document.
func parseWorkbuddyCatalog(raw []byte, provider string) map[string]providerFacts {
	var doc struct {
		Realm  string `json:"realm"`
		Models []struct {
			ID                      string   `json:"id"`
			ContextLength           int64    `json:"context_length"`
			SupportedContextLengths []int64  `json:"supported_context_lengths"`
			SupportedEfforts        []string `json:"supported_efforts"`
		} `json:"models"`
	}
	if errUnmarshal := json.Unmarshal(raw, &doc); errUnmarshal != nil {
		return nil
	}
	label := provider
	if realm := strings.TrimSpace(doc.Realm); realm != "" {
		// The CN and Global realms price and cap models differently, so they are
		// distinct channels rather than one provider with two opinions.
		label = provider + "-" + realm
	}
	out := make(map[string]providerFacts, len(doc.Models))
	for _, model := range doc.Models {
		id := strings.ToLower(strings.TrimSpace(model.ID))
		if id == "" {
			continue
		}
		maxContext := model.ContextLength
		for _, tier := range model.SupportedContextLengths {
			if tier > maxContext {
				maxContext = tier
			}
		}
		out[id] = providerFacts{
			Provider:       label,
			ContextDefault: model.ContextLength,
			ContextMax:     maxContext,
			Levels:         compactStrings(model.SupportedEfforts),
		}
	}
	return out
}

// parseQoderCatalog reads one qoder catalog document, which keeps capability
// facts in a separate region_facts map keyed by upstream model name.
func parseQoderCatalog(raw []byte, provider string) map[string]providerFacts {
	var doc struct {
		Region string `json:"region"`
		Models []struct {
			ID string `json:"ID"`
		} `json:"models"`
		RegionFacts map[string]struct {
			ContextLength int64 `json:"context_length"`
			ContextTiers  []struct {
				Tokens int64 `json:"tokens"`
			} `json:"context_tiers"`
			Thinking struct {
				Levels []string `json:"levels"`
			} `json:"thinking"`
		} `json:"region_facts"`
	}
	if errUnmarshal := json.Unmarshal(raw, &doc); errUnmarshal != nil {
		return nil
	}
	label := provider
	if region := strings.TrimSpace(doc.Region); region != "" {
		label = provider + "-" + region
	}
	out := make(map[string]providerFacts, len(doc.Models))
	for _, model := range doc.Models {
		id := strings.ToLower(strings.TrimSpace(model.ID))
		if id == "" {
			continue
		}
		fact := doc.RegionFacts[model.ID]
		maxContext := fact.ContextLength
		for _, tier := range fact.ContextTiers {
			if tier.Tokens > maxContext {
				maxContext = tier.Tokens
			}
		}
		out[id] = providerFacts{
			Provider:       label,
			ContextDefault: fact.ContextLength,
			ContextMax:     maxContext,
			Levels:         compactStrings(fact.Thinking.Levels),
		}
	}
	return out
}

// aggregateChannels applies the two rules to one model's channel opinions.
//
// Context is the smallest maximum across channels: every channel must be able to
// serve the number, and a channel's default tier is not its limit. Reasoning is
// the union, ordered from weakest to strongest, because a level no channel
// rejects is strictly better than losing the ability to think.
func aggregateChannels(channels []providerFacts) (contextLength int64, levels []string) {
	var smallest int64
	seen := make(map[string]bool, 8)
	for _, channel := range channels {
		if channel.ContextMax > 0 && (smallest == 0 || channel.ContextMax < smallest) {
			smallest = channel.ContextMax
		}
		for _, level := range channel.Levels {
			seen[level] = true
		}
	}
	levels = orderLevels(seen)
	return smallest, levels
}

// levelOrder ranks reasoning levels from weakest to strongest. Levels outside
// this vocabulary keep their relative order at the end, so an upstream that
// invents a name still appears in the listing.
var levelOrder = map[string]int{
	"minimal": 0,
	"low":     1,
	"medium":  2,
	"high":    3,
	"xhigh":   4,
	"max":     5,
	"ultra":   6,
}

// orderLevels sorts a level set into the canonical weakest-to-strongest order.
func orderLevels(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for level := range set {
		out = append(out, level)
	}
	sort.Slice(out, func(i, j int) bool {
		ri, okI := levelOrder[out[i]]
		rj, okJ := levelOrder[out[j]]
		switch {
		case okI && okJ:
			return ri < rj
		case okI:
			return true
		case okJ:
			return false
		default:
			return out[i] < out[j]
		}
	})
	return out
}

// parseTraeAuth reads the model snapshot embedded in one Trae credential.
//
// A credential that is not Trae, or one whose snapshot has not been written yet,
// contributes nothing. The variant field separates the CN and Intl realms, which
// advertise different catalogs and are therefore distinct channels.
func parseTraeAuth(raw []byte, provider string) map[string]providerFacts {
	var doc struct {
		Provider string `json:"provider"`
		Auth     struct {
			Variant string `json:"variant"`
		} `json:"auth"`
		ModelCache struct {
			Realm  string `json:"realm"`
			Models []struct {
				ID            string `json:"ID"`
				ContextLength int64  `json:"ContextLength"`
				Thinking      struct {
					Levels []string `json:"Levels"`
				} `json:"Thinking"`
			} `json:"models"`
		} `json:"model_cache"`
	}
	if errUnmarshal := json.Unmarshal(raw, &doc); errUnmarshal != nil {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(doc.Provider), "trae") {
		return nil
	}
	label := provider
	realm := strings.TrimSpace(doc.ModelCache.Realm)
	if realm == "" {
		realm = strings.TrimSpace(doc.Auth.Variant)
	}
	if realm != "" {
		label = provider + "-" + realm
	}
	out := make(map[string]providerFacts, len(doc.ModelCache.Models))
	for _, model := range doc.ModelCache.Models {
		id := strings.ToLower(strings.TrimSpace(model.ID))
		if id == "" {
			continue
		}
		out[id] = providerFacts{
			Provider:       label,
			ContextDefault: model.ContextLength,
			// Trae advertises no selectable context tiers, so its current window is
			// also its maximum.
			ContextMax: model.ContextLength,
			Levels:     compactStrings(model.Thinking.Levels),
		}
	}
	return out
}
