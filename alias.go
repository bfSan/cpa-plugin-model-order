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
//
// # Where the model list comes from
//
// The alias table's `name` column holds the UPSTREAM model name, so the report
// has to be judged against upstream names. It must not be judged against
// /v1/models: that listing has CPA's aliases already substituted, so comparing
// it against the table is circular -- a model with a correct row arrives looking
// like a bare name that has no row, and a model whose alias is stale arrives
// looking fine.
//
// /v1/models is also not live. CPA serves it from whatever the last client
// request produced, so a report built on it can describe a model list that no
// longer exists while looking exactly like a fresh one.
//
// The live upstream names are available from two places the panel can read:
// each provider plugin's own /models route (what that plugin will actually
// offer, in upstream naming), and, for providers without such a route, the
// per-credential catalog. The panel collects them and passes them in, because
// the plugin hosting this report cannot make those calls itself: the host
// exposes no RPC for either.

// aliasReport is the missing-alias view across every alias channel.
type aliasReport struct {
	// Channels echoes the alias channels the report was judged against. A row can
	// only be written to a channel that exists, so this drives the whole report.
	Channels []string `json:"channels"`
	// Captured counts the served upstream names that were judged, including the
	// ones that produced no row. It is the denominator of the summary, so
	// Missing + Ignored always equals it. Names the provider hides are excluded:
	// they never reach a client and are counted per channel in Sources instead.
	Captured int               `json:"captured"`
	Missing  []missingAliasRow `json:"missing"`
	// Ignored is how many of the Captured names produced no row, i.e. how many
	// already have an alias. It is a subset of Captured, never larger.
	Ignored int `json:"ignored"`
	// Skipped lists every upstream name that produced no alias row, with the
	// reason, so "why is this provider missing" always has an answer beyond a
	// bare count.
	Skipped []skippedAliasRow `json:"skipped,omitempty"`
	// SkippedByReason counts Skipped per reason for a one-line summary.
	SkippedByReason map[string]int `json:"skipped_by_reason,omitempty"`
	// Sources says, per channel, where its upstream list came from and how much
	// the provider's own visibility rules removed before judging. Without it a
	// provider whose whole catalog is hidden would look like a provider with
	// nothing to alias.
	Sources []aliasSourceStatus `json:"sources,omitempty"`
	// NoChannel lists providers that do have upstream models but no alias channel,
	// so their models cannot be given a row. They are reported rather than
	// silently dropped: the panel offers to create the channel.
	NoChannel []aliasSourceStatus `json:"no_channel,omitempty"`
	Note      string              `json:"note,omitempty"`
}

// aliasSourceStatus describes one provider's upstream list as it was judged.
type aliasSourceStatus struct {
	Channel string `json:"channel"`
	// Origin is where the names came from: "plugin" for a provider plugin's own
	// model route, "auth" for the per-credential catalog, "none" when neither
	// could be read.
	Origin string `json:"origin"`
	// Total is what the source offered, before the provider's own hiding.
	Total int `json:"total"`
	// Served is how many names were judged, i.e. Total minus Hidden.
	Served int `json:"served"`
	// Hidden is how many the provider's own overlay removes from the listing, so
	// they never reach a client and need no alias.
	Hidden int `json:"hidden"`
}

// skippedAliasRow is one upstream name that no alias row was proposed for.
type skippedAliasRow struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model"`
	Reason   string `json:"reason"`
}

// missingAliasRow is one upstream model that reaches clients under a bare name,
// with the alias it would be given. Channel is the oauth-model-alias channel the
// row belongs to, which is the provider name.
type missingAliasRow struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Alias    string `json:"alias"`
	Reason   string `json:"reason"`
	Channel  string `json:"channel"`
}

// upstreamListing is one provider's live upstream models as the panel collected
// them.
type upstreamListing struct {
	// Models are the upstream names this provider currently offers to clients.
	Models []string `json:"models"`
	// Hidden are upstream names the provider's own rules remove from the listing.
	// They are carried separately instead of being dropped by the panel so the
	// report can say why they were not proposed.
	Hidden []string `json:"hidden,omitempty"`
	// Origin is "plugin" or "auth", echoed back into Sources.
	Origin string `json:"origin,omitempty"`
}

// reasonMissing labels a row that would be added to oauth-model-alias.
const (
	// reasonMissing marks a genuine gap: an upstream name with no alias row.
	reasonMissing = "missing alias"
	// reasonAlreadyAliased marks an upstream model the alias table already covers.
	// It is decided from the table itself, never from the shape of the name:
	// "qoder-auto" is an alias CPA already applied while cline's
	// "cline-free/..." is an upstream name that merely looks prefixed, and no
	// string test separates the two.
	reasonAlreadyAliased = "already aliased"
	// reasonHidden marks an upstream model the provider itself hides, so it never
	// reaches a client and needs no alias.
	reasonHidden = "hidden by the provider"
	// reasonNoModelName marks an upstream row with an empty name.
	reasonNoModelName = "empty model name"
	// reasonNoUpstream marks a channel whose upstream models could not be read, so
	// the report has nothing to judge it against.
	reasonNoUpstream = "no upstream model list"
)

// buildAliasReport compares every alias channel's live upstream models against
// the alias table and returns the rows that are missing.
//
// channels is the set of alias channels that exist, and it is the outer loop
// because a row can only be written to a channel. A provider with upstream
// models but no channel is reported in NoChannel instead of being dropped: CPA's
// built-in openai serves gpt-6.1-sol and friends under their intended names and
// has no channel, so proposing openai-gpt-6.1-sol for all of them was the single
// largest source of noise in the report's first live run. Being explicit about
// the missing channel says the same thing without inventing rows.
//
// upstream holds each provider's live upstream names, keyed by provider.
// existing maps a channel to the names it already covers; the panel passes both
// the table's `name` and `alias` values, because depending on whether an alias is
// in effect a covered model can appear under either.
func buildAliasReport(channels map[string]bool, upstream map[string]upstreamListing, existing map[string]map[string]bool) aliasReport {
	report := aliasReport{
		Channels:        sortedKeys(channels),
		Missing:         []missingAliasRow{},
		Skipped:         []skippedAliasRow{},
		SkippedByReason: map[string]int{},
		Sources:         []aliasSourceStatus{},
		NoChannel:       []aliasSourceStatus{},
	}
	// Normalise the upstream keys here rather than trusting the caller: the panel
	// builds this map from provider names that come from several places with
	// inconsistent case, and a mismatched key would silently look like a provider
	// with no upstream list at all.
	normalised := make(map[string]upstreamListing, len(upstream))
	for provider, listing := range upstream {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider == "" {
			continue
		}
		normalised[provider] = listing
	}
	upstream = normalised
	// skip records why an upstream name produced no row. It deliberately does not
	// touch Ignored: that counter means "served names that produced no row", while
	// Skipped also covers names that were never judged at all (hidden ones).
	skip := func(provider, model, reason string) {
		report.Skipped = append(report.Skipped, skippedAliasRow{
			Provider: provider, Model: model, Reason: reason,
		})
		report.SkippedByReason[reason]++
	}

	// Providers with an upstream list but no channel cannot be written to.
	// Reporting them once per provider keeps a 71-model catalog from producing 71
	// identical complaints.
	for _, provider := range sortedUpstreamKeys(upstream) {
		if channels[provider] {
			continue
		}
		listing := upstream[provider]
		if len(listing.Models) == 0 && len(listing.Hidden) == 0 {
			continue
		}
		report.NoChannel = append(report.NoChannel, aliasSourceStatus{
			Channel: provider,
			Origin:  originOf(listing),
			Total:   len(listing.Models) + len(listing.Hidden),
			Served:  len(listing.Models),
			Hidden:  len(listing.Hidden),
		})
	}

	for _, channel := range report.Channels {
		listing, ok := upstream[channel]
		if !ok || (len(listing.Models) == 0 && len(listing.Hidden) == 0) {
			// A channel with no readable upstream list has nothing to judge. Saying
			// so is better than reporting "no missing aliases", which would read as
			// a clean bill of health.
			report.Sources = append(report.Sources, aliasSourceStatus{
				Channel: channel, Origin: "none",
			})
			skip(channel, "", reasonNoUpstream)
			continue
		}
		report.Sources = append(report.Sources, aliasSourceStatus{
			Channel: channel,
			Origin:  originOf(listing),
			Total:   len(listing.Models) + len(listing.Hidden),
			Served:  len(listing.Models),
			Hidden:  len(listing.Hidden),
		})

		// Hidden names are recorded, not proposed: the provider removes them from
		// the listing, so no alias can reach a client through them. They are
		// deliberately kept out of Captured and Ignored -- both of those describe
		// the served names, and the panel reads "of N judged, M need no row" out of
		// them. Hidden names are counted in Sources instead.
		for _, hidden := range listing.Hidden {
			name := strings.TrimSpace(hidden)
			if name == "" {
				continue
			}
			skip(channel, name, reasonHidden)
		}

		for _, raw := range listing.Models {
			name := strings.TrimSpace(raw)
			// Every served entry is judged, so it counts toward the denominator even
			// when it is rejected. That keeps Missing + Ignored == Captured, which is
			// what the panel's summary line is built from.
			report.Captured++
			if name == "" {
				// A row with no model name has no `name` field for the alias table
				// to point at, so there is nothing to propose.
				report.Ignored++
				skip(channel, name, reasonNoModelName)
				continue
			}
			if existing[channel][name] {
				// Covered by the table already, so no row to propose. The table is
				// the authority here rather than the shape of the name.
				report.Ignored++
				skip(channel, name, reasonAlreadyAliased)
				continue
			}
			report.Missing = append(report.Missing, missingAliasRow{
				Provider: channel,
				Model:    name,
				Alias:    providerPrefix(channel) + name,
				Reason:   reasonMissing,
				Channel:  channel,
			})
		}
	}

	sort.SliceStable(report.Missing, func(i, j int) bool {
		if report.Missing[i].Channel != report.Missing[j].Channel {
			return report.Missing[i].Channel < report.Missing[j].Channel
		}
		return report.Missing[i].Model < report.Missing[j].Model
	})
	if len(report.Missing) == 0 && report.Captured > 0 {
		report.Note = "every upstream model in these channels already has an alias row"
	}
	return report
}

// originOf names where a provider's upstream list came from.
func originOf(listing upstreamListing) string {
	origin := strings.ToLower(strings.TrimSpace(listing.Origin))
	if origin == "" {
		return "plugin"
	}
	return origin
}

// sortedUpstreamKeys turns the upstream map into a stable, sorted key slice.
func sortedUpstreamKeys(upstream map[string]upstreamListing) []string {
	out := make([]string, 0, len(upstream))
	for key := range upstream {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
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
