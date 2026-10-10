package main

import (
	"strings"
	"testing"
)

// The panel is one embedded document and the Go tests only assert on its HTML
// shape, so a broken expression inside it ships silently: it parses, it renders,
// and it only fails when the operator clicks the button that reaches that line.
//
// That is exactly what happened with the alias scan. `Array.from(new Set(x).map(...))`
// puts `.map` on the Set rather than on the array, which is valid syntax and a
// runtime TypeError -- "补齐建议" failed with "(intermediate value).map is not a
// function" while every test stayed green.
//
// jsArrayMethodOnSetOrMap is the detector for that class: Set and Map share only a
// few method names with Array, so any array-only method chained directly onto
// `new Set(...)` or `new Map(...)` is a bug rather than a style question.

// jsArrayOnlyMethods are the methods that exist on Array but not on Set or Map.
// Methods both types share (forEach, keys, values, entries) are deliberately
// absent: chaining those is legitimate.
var jsArrayOnlyMethods = map[string]bool{
	"map": true, "filter": true, "reduce": true, "reduceRight": true,
	"some": true, "every": true, "find": true, "findIndex": true,
	"flat": true, "flatMap": true, "join": true, "concat": true,
	"sort": true, "reverse": true, "slice": true, "splice": true,
	"push": true, "pop": true, "shift": true, "unshift": true,
	"indexOf": true, "lastIndexOf": true, "includes": true,
}

// stripJSForScanning removes comments and string bodies so a detector cannot fire
// on an example in a comment or on text inside a literal. Over-stripping only
// costs a missed detection, never a false alarm.
func stripJSForScanning(src string) string {
	var out strings.Builder
	out.Grow(len(src))

	// blank replaces a slice with spaces, keeping newlines and the exact byte
	// length. Length matters: the scan reports offsets, and it reports them as
	// line numbers in the ORIGINAL source. A blank that dropped bytes would shift
	// every later offset and point the operator at the wrong line.
	blank := func(s string) {
		for i := 0; i < len(s); i++ {
			if s[i] == '\n' {
				out.WriteByte('\n')
			} else {
				out.WriteByte(' ')
			}
		}
	}

	for i := 0; i < len(src); {
		switch {
		case strings.HasPrefix(src[i:], "//"):
			end := strings.IndexByte(src[i:], '\n')
			if end < 0 {
				blank(src[i:])
				i = len(src)
				continue
			}
			blank(src[i : i+end])
			i += end
		case strings.HasPrefix(src[i:], "/*"):
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				blank(src[i:])
				i = len(src)
				continue
			}
			blank(src[i : i+2+end+2])
			i += 2 + end + 2
		case src[i] == '"' || src[i] == '\'' || src[i] == '`':
			quote := src[i]
			j := i + 1
			for j < len(src) {
				if src[j] == '\\' && quote != '`' {
					j += 2
					continue
				}
				if src[j] == quote {
					j++
					break
				}
				j++
			}
			blank(src[i:min(j, len(src))])
			i = j
		default:
			out.WriteByte(src[i])
			i++
		}
	}
	return out.String()
}

// matchParen returns the index of the ')' matching the '(' at open, or -1.
func matchParen(src string, open int) int {
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// arrayMethodOnSetOrMapOffences reports every `new Set(...)` / `new Map(...)` that
// has an array-only method chained directly onto it, as line numbers.
func arrayMethodOnSetOrMapOffences(src string) []string {
	code := stripJSForScanning(src)

	var found []string
	for _, ctor := range []string{"new Set(", "new Map("} {
		for offset := 0; ; {
			at := strings.Index(code[offset:], ctor)
			if at < 0 {
				break
			}
			start := offset + at
			open := start + len(ctor) - 1
			close := matchParen(code, open)
			if close < 0 {
				break
			}
			// Look just past the closing paren for ".method(".
			rest := code[close+1:]
			trimmed := strings.TrimLeft(rest, " \t\r\n")
			if strings.HasPrefix(trimmed, ".") {
				name := trimmed[1:]
				end := 0
				for end < len(name) && (isJSIdentByte(name[end])) {
					end++
				}
				method := name[:end]
				afterMethod := strings.TrimLeft(name[end:], " \t\r\n")
				if jsArrayOnlyMethods[method] && strings.HasPrefix(afterMethod, "(") {
					line := strings.Count(src[:start], "\n") + 1
					found = append(found, ctor[:len(ctor)-1]+" → ."+method+" (line "+itoa(line)+")")
				}
			}
			offset = close + 1
		}
	}
	return found
}

func isJSIdentByte(b byte) bool {
	return b == '_' || b == '$' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// The detector has to actually fire on the shape that broke the panel, or the
// guard below is decoration.
func TestAliasScanGuardDetectsTheReportedFailure(t *testing.T) {
	broken := `
const channelNames = Array.from(new Set(
  Object.keys(aliasDraft || {}).concat(credProviders || [])
).map(s => String(s || "").trim().toLowerCase()).filter(Boolean));
`
	got := arrayMethodOnSetOrMapOffences(broken)
	if len(got) == 0 {
		t.Fatal("the detector must flag .map chained onto new Set(...)")
	}

	// A comment mentioning the broken shape is not a defect.
	inComment := "// Array.from(new Set(x).map(...)) 会报错\n" + "const ok = 1;\n"
	if got := arrayMethodOnSetOrMapOffences(inComment); len(got) != 0 {
		t.Errorf("comments must not be flagged: %v", got)
	}

	// The corrected shape -- normalise after Array.from -- is fine.
	fixed := `
const channelNames = Array.from(new Set(
  Object.keys(aliasDraft || {}).concat(credProviders || [])
)).map(s => String(s || "").trim().toLowerCase()).filter(Boolean);
`
	if got := arrayMethodOnSetOrMapOffences(fixed); len(got) != 0 {
		t.Errorf("the corrected shape must pass: %v", got)
	}

	// forEach exists on Set, so it is legitimate and must not be flagged.
	shared := "new Set(list).forEach(fn);\n"
	if got := arrayMethodOnSetOrMapOffences(shared); len(got) != 0 {
		t.Errorf("Set.forEach is legal: %v", got)
	}
}

// The reported line has to point at the offending source line, or the guard sends
// the operator somewhere else in a 1900-line document. This pins the offset maths
// against a known line number rather than trusting it.
func TestAliasScanGuardReportsTheCorrectLine(t *testing.T) {
	// Four lines of prose ahead of the defect, so the defect is on line 5.
	src := "// one\n// two\n// three\n// four\nconst x = new Set(a).map(f);\n"
	got := arrayMethodOnSetOrMapOffences(src)
	if len(got) != 1 {
		t.Fatalf("want exactly one offence, got %v", got)
	}
	if !strings.Contains(got[0], "line 5") {
		t.Errorf("offence should be reported on line 5, got %q", got[0])
	}

	// Multi-line comments and literals must not shift the numbering either.
	src2 := "/* a\nb\nc */\nconst y = \"new Set(a).map(f)\";\nnew Set(b).filter(g);\n"
	got2 := arrayMethodOnSetOrMapOffences(src2)
	if len(got2) != 1 {
		t.Fatalf("want one offence (the literal is text, not code), got %v", got2)
	}
	if !strings.Contains(got2[0], "line 5") {
		t.Errorf("offence should be reported on line 5, got %q", got2[0])
	}
}

// The real panel must be free of the shape, so this cannot regress unnoticed.
func TestPanelChainsNoArrayMethodOntoSetOrMap(t *testing.T) {
	if got := arrayMethodOnSetOrMapOffences(renderPanel()); len(got) != 0 {
		t.Fatalf("panel calls an array-only method on a Set/Map, which throws at the click that reaches it:\n  %s",
			strings.Join(got, "\n  "))
	}
}
