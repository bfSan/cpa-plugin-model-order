package main

import (
	"sort"
	"strings"
	"time"
)

// CPA's oauth-model-alias matches exactly and has no wildcard syntax, so a new
// upstream model reaches clients under its bare name until someone adds a row
// for it. This file works out which rows are missing.
//
// The plugin does not write the table. The host exposes no RPC for writing
// arbitrary CPA configuration, and a plugin that held the management key to do
// it would be a much larger thing than the one that reports a gap. Instead the
// report is served here and the edit is made by the panel, in the browser,
// where the operator's management key already lives. The aliases stay CPA's.

// aliasReport is the missing-alias view for one listing port.
type aliasReport struct {
	Port     string            `json:"port"`
	Captured int               `json:"captured"`
	Missing  []missingAliasRow `json:"missing"`
	Ignored  int               `json:"ignored"`
	Note     string            `json:"note,omitempty"`
	// Channels echoes the alias channels the report was judged against, so the
	// panel can say which providers were considered at all. Empty means the
	// caller supplied no channel list and the report fell back to the listing.
	Channels []string `json:"channels,omitempty"`
	// Skipped lists every captured model that produced no alias row, with the
	// reason. A bare Ignored count cannot answer "why does this provider have no
	// suggestions": cline's five visible models all carry a slash, so they are
	// all dismissed as pass-through, and the count alone left no way to see that.
	Skipped []skippedAliasRow `json:"skipped,omitempty"`
	// SkippedByReason counts Skipped per reason, so the panel can summarise
	// without walking the list.
	SkippedByReason map[string]int `json:"skipped_by_reason,omitempty"`
	// CapturedAt is when the listing this report judges was captured. The report
	// reads the last listing a client actually pulled, never a live /v1/models
	// call, so its contents can be older than the catalog CPA currently serves.
	// Without this timestamp a stale report is indistinguishable from a fresh
	// one — which is exactly how "the plugin says five models, the report lists
	// four" happens: the report was reading the previous snapshot.
	CapturedAt time.Time `json:"captured_at,omitempty"`
	// ListingCount is the number of entries in that snapshot, so the panel can
	// contrast it with what the catalog shows now.
	ListingCount int `json:"listing_count,omitempty"`
}

// skippedAliasRow is one captured model that no alias row was proposed for.
type skippedAliasRow struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model"`
	Reason   string `json:"reason"`
}

// missingAliasRow is one model that reaches clients under a bare name, with the
// alias it would be given. Channel is the oauth-model-alias channel the row
// belongs to, which is the provider name.
type missingAliasRow struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Alias    string `json:"alias"`
	Reason   string `json:"reason"`
	Channel  string `json:"channel"`
}

// reasonMissing labels a row that would be added to oauth-model-alias. The
// other reasons below exist for the report's skipped count: an operator who
// expected a row for a provider/model name should be able to see why it is not
// proposed.
const (
	// reasonMissing marks a genuine gap: a bare id with no alias row.
	reasonMissing = "missing alias"
	// reasonAlreadyAliased marks a model the alias table already has a row for.
	// It is decided from the table itself, never from the shape of the name.
	reasonAlreadyAliased = "already aliased"
	// reasonNoProvider marks an entry that neither owned_by nor the credential
	// catalog can attribute to a provider.
	reasonNoProvider = "no provider"
	// reasonNoChannel marks a model whose provider has no oauth-model-alias
	// channel at all, so there is nowhere to write a row.
	reasonNoChannel = "provider has no alias channel"
	// reasonNoModelName marks a listing row with an empty model id.
	reasonNoModelName = "empty model name"
)

// buildAliasReport compares a captured listing against the alias table CPA
// currently holds and returns the rows that are missing.
//
// existing is the provider -> alias count summary from the alias table, used
// only for display. The decision to report a model is made from the captured
// listing alone: CPA substitutes aliases before the body reaches this plugin, so
// a model that already has a row arrives here already carrying its alias.
//
// A provider only participates in aliasing if it actually has a channel in
// CPA's oauth-model-alias table, and that table is the authority. CPA's built-in
// providers are the case that makes this necessary: the openai provider serves
// fourteen models as gpt-5.5, gpt-6.1-sol and so on, and those names are correct
// rather than missing. Proposing openai-gpt-6.1-sol for all fourteen was the
// single largest source of noise in the first live run of this report.
//
// The channel list is supplied by the caller because the plugin cannot read the
// alias table itself: the host only exposes OAuthModelAlias through
// StaticModelRequest, which a pure response interceptor never receives. The panel
// already fetches the table in order to write it, so it passes the channel names
// through rather than the plugin guessing.
//
// A provider with no channel, or a channel that already covers the model, is
// skipped. An empty channel set means "unknown", and the report then falls back to
// judging from the listing, because reporting the openai models would be worse
// than reporting a few extra rows.
func buildAliasReport(port string, entries []catalogEntry, channels map[string]bool) aliasReport {
	return buildAliasReportWithProviders(port, entries, channels, nil)
}

// buildAliasReportWithProviders is buildAliasReport plus a credential catalog.
//
// modelProviders maps a bare model id to the providers whose credentials serve
// it, for entries the listing cannot attribute on its own. It is a fallback and
// never an override: owned_by stays authoritative wherever it is set.
//
// trae is why this exists. All twenty of its models reach clients with an empty
// owned_by, so the first check dropped them and no alias was proposed no matter
// which channels the operator had added -- the report looked like it had nothing
// to say about a provider that was plainly in use. The panel already reads the
// credential catalogs, so it passes the mapping through rather than the plugin
// guessing a provider from the shape of a model name.
func buildAliasReportWithProviders(port string, entries []catalogEntry, channels map[string]bool, modelProviders map[string][]string) aliasReport {
	return buildAliasReportWithAliasTable(port, entries, channels, modelProviders, nil)
}

// buildAliasReportWithAliasTable is the full form: it also takes the current
// oauth-model-alias table so a model that already has a row is not proposed
// again.
//
// The table, not the shape of the name, is the authority. Deciding by prefix
// cannot work: "qoder-auto" is the result of an alias CPA already applied,
// while cline's "cline-free/..." is an upstream name that merely looks
// prefixed, and no string test separates the two. The table simply lists which
// (channel, name) rows exist.
//
// existing maps a channel to the model names it already covers.
func buildAliasReportWithAliasTable(port string, entries []catalogEntry, channels map[string]bool, modelProviders map[string][]string, existing map[string]map[string]bool) aliasReport {
	report := aliasReport{
		Port:            port,
		Captured:        len(entries),
		Missing:         []missingAliasRow{},
		Channels:        sortedKeys(channels),
		Skipped:         []skippedAliasRow{},
		SkippedByReason: map[string]int{},
	}
	// skip records why a captured model produced no row. It deliberately does not
	// touch Ignored: that counter has always meant "models that produced no row",
	// while Skipped is per (model, reason). One model can be skipped for two
	// providers, so driving both from here would inflate the model count.
	skip := func(provider, model, reason string) {
		report.Skipped = append(report.Skipped, skippedAliasRow{
			Provider: provider, Model: model, Reason: reason,
		})
		report.SkippedByReason[reason]++
	}
	// resolve attributes one entry to the providers that serve it. owned_by wins
	// when present; otherwise the credential catalog supplies the providers,
	// which is the only way a provider that omits owned_by gets a row.
	resolve := func(entry catalogEntry) []string {
		if provider := strings.ToLower(strings.TrimSpace(entry.OwnedBy)); provider != "" {
			return []string{provider}
		}
		out := make([]string, 0, 2)
		for _, provider := range modelProviders[strings.TrimSpace(entry.ID)] {
			if trimmed := strings.ToLower(strings.TrimSpace(provider)); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	}

	// With no channel list the report still has to work, so fall back to reading
	// adoption off the listing: a provider whose models mostly carry the
	// <provider>- prefix is using the convention, and its bare names are the gap.
	// The tally follows the resolved provider, so a catalog-only provider is
	// measured on the same footing as one that sets owned_by.
	total := make(map[string]int, 8)
	prefixed := make(map[string]int, 8)
	for _, entry := range entries {
		id := strings.TrimSpace(entry.ID)
		for _, provider := range resolve(entry) {
			total[provider]++
			if strings.HasPrefix(id, providerPrefix(provider)) {
				prefixed[provider]++
			}
		}
	}
	participates := func(provider string) bool {
		if len(channels) > 0 {
			return channels[provider]
		}
		count := total[provider]
		return count > 0 && prefixed[provider]*2 >= count
	}

	// seen guards the report against a listing that repeats a model, which a
	// multi channel deployment can produce when two providers expose the same
	// upstream model. It is keyed by channel as well, because one bare name
	// served by two channels needs a row in each of them.
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			// A row with no model name has no "name" field for the alias table to
			// point at. Proposing "<provider>-" would add a nameless row.
			report.Ignored++
			skip("", id, reasonNoModelName)
			continue
		}
		providers := resolve(entry)
		if len(providers) == 0 {
			// Neither owned_by nor the credential catalog knows who serves this,
			// so there is no channel to write the row to.
			report.Ignored++
			skip("", id, reasonNoProvider)
			continue
		}
		proposed := false
		// recordedReason tracks whether a per-provider reason was already logged,
		// so the no_channel fallback cannot double-count the same model.
		recordedReason := false
		for _, provider := range providers {
			if !participates(provider) {
				// openai and cline: no alias channel, and their names are intended.
				continue
			}
			key := provider + "\x00" + id
			if _, dup := seen[key]; dup {
				continue
			}
			// A model that already has a row is not a gap. This is the one
			// judgment that belongs to the plugin: it reads the table instead of
			// guessing from the name, so it cannot mistake an upstream name that
			// merely looks prefixed for an existing alias.
			if existing[provider][id] {
				// Covered by the table, so no row to propose. recordedReason keeps
				// the no_channel fallback from adding a second, vaguer reason for
				// the same model; Ignored is still incremented once by the block
				// below, because this model produced no proposal.
				skip(provider, id, reasonAlreadyAliased)
				recordedReason = true
				continue
			}
			seen[key] = struct{}{}
			report.Missing = append(report.Missing, missingAliasRow{
				Provider: provider,
				Model:    id,
				Alias:    providerPrefix(provider) + id,
				Reason:   reasonMissing,
				Channel:  provider,
			})
			proposed = true
		}
		if !proposed {
			// This model produced no row: count it once, and if no more specific
			// reason was recorded above, note that its providers have no channel.
			report.Ignored++
			if !recordedReason {
				skip(strings.Join(providers, ","), id, reasonNoChannel)
			}
		}
	}
	sort.SliceStable(report.Missing, func(i, j int) bool {
		if report.Missing[i].Channel != report.Missing[j].Channel {
			return report.Missing[i].Channel < report.Missing[j].Channel
		}
		return report.Missing[i].Model < report.Missing[j].Model
	})
	if len(report.Missing) == 0 {
		report.Note = "every captured model already carries a provider prefixed alias"
	}
	return report
}

// sortedKeys turns a channel set into a stable, sorted slice for display.
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key, on := range set {
		if on {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// aliasChannelStatus summarises the alias table per provider, for the panel.
type aliasChannelStatus struct {
	Channel string `json:"channel"`
	Count   int    `json:"count"`
}
