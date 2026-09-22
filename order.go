package main

import "strings"

// Strategy values accepted in plugin config.
const (
	// StrategyGrouped puts configured buckets first, everything else
	// alphabetically by model ID at the tail.
	StrategyGrouped = "grouped"
	// StrategyName sorts the whole list alphabetically by model ID.
	StrategyName = "name"
)

// defaultOrder is used when the config leaves `order` out. The rule is aggregate
// presets first, then the GPT/Codex families, then everything else alphabetically
// by model ID.
//
// Each pattern is its own bucket, so the tiers come out in the order written
// here rather than alphabetically. Both spellings are listed because CPA applies
// oauth-model-alias before the list reaches this plugin: a deployment that
// prefixes every alias per provider serves qoder-auto and workbuddy-balanced,
// while an unprefixed one serves bare auto and balanced.
//
// The suffix forms stay deliberately strict. "*-auto" matches qoder-auto but
// leaves codex-auto-review for the tail, which is the wanted behaviour because
// that entry is a review model, not a routing preset.
var defaultOrder = []string{
	"auto", "auto-*", "*-auto",
	"default", "default-*", "*-default",
	"balanced", "balanced-*", "*-balanced",
	"fast", "fast-*", "fast-model", "*-fast", "*-fast-model", "*-fast-*",
	"deep", "deep-*", "deep-model", "*-deep", "*-deep-model", "*-deep-*",
	"hybrid", "hybrid-*", "*-hybrid",
	"efficient", "*-efficient",
	"performance", "*-performance",
	"ultimate", "*-ultimate",
	"gpt-*",
	"codex-*",
}

// matcher is one compiled pattern from the `order` list.
type matcher struct {
	raw  string
	kind matcherKind
	lit  string
	// pre/suf are used by the wildcard kinds; segs by the general glob kind.
	pre  string
	suf  string
	segs []string
}

type matcherKind int

const (
	matcherExact matcherKind = iota
	matcherPrefix
	matcherSuffix
	matcherContains
	matcherGlob
)

// compileMatchers turns a configured pattern list into matchers, dropping blank
// entries. Patterns are matched against the model ID only.
//
//	Supported syntax:
//	  "gpt-5.6-*"   prefix
//	  "*-preview"   suffix
//	  "*kimi*"      contains
//	  "a*b*c"       general glob, segments in order
//	  "auto"        exact
func compileMatchers(patterns []string, caseSensitive bool) []matcher {
	out := make([]matcher, 0, len(patterns))
	for _, raw := range patterns {
		pattern := strings.TrimSpace(raw)
		if pattern == "" {
			continue
		}
		if !caseSensitive {
			pattern = strings.ToLower(pattern)
		}
		compiled, usable := classify(pattern)
		if usable {
			out = append(out, compiled)
		}
	}
	return out
}

// classify picks the cheapest matcher that is still faithful to the pattern.
// A pattern may only collapse into a prefix or suffix form when its single
// wildcard sits at that end: an interior wildcard makes it a general glob,
// otherwise "qoder-*-*" would be read as the literal prefix "qoder-*" and match
// nothing at all.
func classify(pattern string) (matcher, bool) {
	compiled := matcher{raw: pattern}
	stars := strings.Count(pattern, "*")
	switch {
	case stars == 0:
		compiled.kind = matcherExact
		compiled.lit = pattern
		return compiled, true
	case pattern == "*":
		// A bare "*" would swallow the whole list into one bucket, so it is
		// dropped instead of silently collapsing the configured order.
		return matcher{}, false
	case stars == 1 && strings.HasSuffix(pattern, "*"):
		compiled.kind = matcherPrefix
		compiled.pre = strings.TrimSuffix(pattern, "*")
		return compiled, true
	case stars == 1 && strings.HasPrefix(pattern, "*"):
		compiled.kind = matcherSuffix
		compiled.suf = strings.TrimPrefix(pattern, "*")
		return compiled, true
	case stars == 2 && strings.HasPrefix(pattern, "*") && strings.HasSuffix(pattern, "*"):
		lit := strings.TrimSuffix(strings.TrimPrefix(pattern, "*"), "*")
		if lit == "" {
			return matcher{}, false
		}
		compiled.kind = matcherContains
		compiled.lit = lit
		return compiled, true
	default:
		compiled.kind = matcherGlob
		compiled.segs = strings.Split(pattern, "*")
		return compiled, true
	}
}

func (m matcher) match(id string) bool {
	switch m.kind {
	case matcherExact:
		return id == m.lit
	case matcherContains:
		return strings.Contains(id, m.lit)
	case matcherPrefix:
		return strings.HasPrefix(id, m.pre)
	case matcherSuffix:
		return strings.HasSuffix(id, m.suf)
	case matcherGlob:
		return globMatch(m.segs, id)
	}
	return false
}

// globMatch checks that segments appear in order, the first one anchored at the
// start and the last one anchored at the end.
func globMatch(segs []string, id string) bool {
	if len(segs) == 0 {
		return false
	}
	if len(segs) == 1 {
		return id == segs[0]
	}
	rest := id
	for i, seg := range segs {
		if seg == "" {
			continue
		}
		at := strings.Index(rest, seg)
		if at < 0 {
			return false
		}
		if i == 0 && at != 0 {
			return false
		}
		if i == len(segs)-1 && at+len(seg) != len(rest) {
			return false
		}
		rest = rest[at+len(seg):]
	}
	return true
}

// comparer yields the less function used to order model IDs. Bucket index wins,
// then the ID itself, so a bucket is always internally alphabetical.
func comparer(strategy string, matchers []matcher, caseSensitive bool) func(a, b string) bool {
	if strategy == StrategyName || len(matchers) == 0 {
		return func(a, b string) bool {
			return compareIDs(a, b, caseSensitive) < 0
		}
	}
	rank := func(id string) int {
		probe := id
		if !caseSensitive {
			probe = strings.ToLower(id)
		}
		for i := range matchers {
			if matchers[i].match(probe) {
				return i
			}
		}
		return len(matchers)
	}
	return func(a, b string) bool {
		rankA, rankB := rank(a), rank(b)
		if rankA != rankB {
			return rankA < rankB
		}
		return compareIDs(a, b, caseSensitive) < 0
	}
}

// compareIDs orders two model IDs, case insensitive by default. Equal ignoring
// case still falls back to a byte comparison so the result stays deterministic.
func compareIDs(a, b string, caseSensitive bool) int {
	if !caseSensitive {
		lowerA, lowerB := strings.ToLower(a), strings.ToLower(b)
		if lowerA != lowerB {
			return strings.Compare(lowerA, lowerB)
		}
	}
	return strings.Compare(a, b)
}
