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
	Existing map[string]int    `json:"existing,omitempty"`
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
// a model that already has a row arrives here already carrying its alias and is
// recognised by its prefix rather than by consulting the table again.
func buildAliasReport(port string, entries []catalogEntry, existing map[string]int) aliasReport {
	report := aliasReport{
		Port:     port,
		Captured: len(entries),
		Missing:  []missingAliasRow{},
		Existing: existing,
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

// aliasChannelStatus summarises the alias table per provider, for the panel.
type aliasChannelStatus struct {
	Channel string `json:"channel"`
	Count   int    `json:"count"`
}
