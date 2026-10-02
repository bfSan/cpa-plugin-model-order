package main

import (
	"sort"
	"strings"
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
	// reasonDirectPass marks a provider/model name such as cline's
	// "anthropic/claude-opus-5.5".
	reasonDirectPass = "provider/model pass-through"
	// reasonNoProvider marks an entry with no usable owned_by.
	reasonNoProvider = "no owned_by"
	// reasonAlreadyAliased marks an id that already carries the provider prefix.
	reasonAlreadyAliased = "already aliased"
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
	report := aliasReport{
		Port:     port,
		Captured: len(entries),
		Missing:  []missingAliasRow{},
		Channels: sortedKeys(channels),
	}
	// With no channel list the report still has to work, so fall back to reading
	// adoption off the listing: a provider whose models mostly carry the
	// <provider>- prefix is using the convention, and its bare names are the gap.
	total := make(map[string]int, 4)
	prefixed := make(map[string]int, 4)
	for _, entry := range entries {
		provider := strings.ToLower(strings.TrimSpace(entry.OwnedBy))
		if provider == "" {
			continue
		}
		total[provider]++
		if strings.HasPrefix(strings.TrimSpace(entry.ID), providerPrefix(provider)) {
			prefixed[provider]++
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
	// upstream model.
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		provider := strings.ToLower(strings.TrimSpace(entry.OwnedBy))
		id := strings.TrimSpace(entry.ID)
		switch {
		case provider == "":
			// owned_by is the only place the owning provider can come from on
			// this listing shape, so without it there is no channel to write to.
			report.Ignored++
			continue
		case id == "":
			// A row with no model name has no "name" field for the alias table to
			// point at. Proposing "<provider>-" would add a nameless row.
			report.Ignored++
			continue
		case looksLikeDirectPass(id):
			// cline lists "anthropic/claude-opus-5.5": the provider already
			// names it, and prefixing would give "cline-anthropic/claude-5.5".
			// These are not missing aliases, and the plan document records that
			// treating them as such is the mistake that inflates the report from
			// three rows to seven.
			report.Ignored++
			continue
		case !participates(provider):
			// openai and cline: no alias channel, and their names are intended.
			report.Ignored++
			continue
		case strings.HasPrefix(id, providerPrefix(provider)):
			report.Ignored++
			continue
		}
		if _, dup := seen[id]; dup {
			report.Ignored++
			continue
		}
		seen[id] = struct{}{}
		report.Missing = append(report.Missing, missingAliasRow{
			Provider: provider,
			Model:    id,
			Alias:    providerPrefix(provider) + id,
			Reason:   reasonMissing,
			Channel:  provider,
		})
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
