package main

import (
	"bytes"
	"encoding/json"
	"errors"
)

// listKeys are the top level JSON keys that hold the model array in the three
// listing formats CPA serves: /v1/models and the Anthropic port use "data",
// the Gemini port uses "models".
var listKeys = [...]string{"data", "models"}

// modelList is the byte spans around the model array plus the array elements.
// head ends with the opening bracket and tail starts at the closing bracket, so
// rejoining is head + elements + tail and every byte outside the array is kept
// exactly as CPA produced it.
type modelList struct {
	head     []byte
	tail     []byte
	elements [][]byte
}

// parseModelList decodes a model listing body into its array elements.
// It reports an error for anything that is not a recognisable model list, and
// the caller must then leave the body untouched: a listing endpoint must never
// be broken by an ordering plugin.
func parseModelList(body []byte) (*modelList, error) {
	if len(body) == 0 {
		return nil, errors.New("empty body")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	first, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := first.(json.Delim)
	if !ok || delim != '{' {
		return nil, errors.New("body is not a JSON object")
	}

	for dec.More() {
		keyToken, errKey := dec.Token()
		if errKey != nil {
			return nil, errKey
		}
		key, okString := keyToken.(string)
		if !okString {
			return nil, errors.New("malformed object key")
		}
		// Capture the stream position while the decoder still sits right after the
		// key token. The value start is then found by walking forward, which avoids
		// depending on how much whitespace InputOffset has already absorbed.
		pos := int(dec.InputOffset())

		// Every value is consumed, list key or not, so the token stream stays
		// aligned. json.RawMessage captures the value bytes verbatim, which means
		// the array text needs no re-encoding and its length bounds the span.
		var raw json.RawMessage
		if errDecode := dec.Decode(&raw); errDecode != nil {
			return nil, errDecode
		}
		if !isListKey(key) {
			continue
		}
		cursor := skipSeparators(body, pos)
		if cursor >= len(body) {
			return nil, errors.New("cannot locate model array")
		}
		if len(raw) < 2 || raw[0] != '[' || raw[len(raw)-1] != ']' {
			// Not an array under a list key; keep looking at other keys.
			continue
		}
		// The offsets only exist to splice around the array, so any disagreement
		// between the decoded value and the raw body aborts instead of risking a
		// mangled listing reaching the client.
		if cursor+len(raw) > len(body) || !bytes.Equal(raw, body[cursor:cursor+len(raw)]) {
			return nil, errors.New("model array offset disagrees with body")
		}
		elements, errSplit := splitArrayElements(raw[1 : len(raw)-1])
		if errSplit != nil {
			return nil, errSplit
		}
		if len(elements) < 2 {
			return nil, errors.New("nothing to order")
		}
		if !allModelItems(elements) {
			return nil, errors.New("array items are not models")
		}
		return &modelList{
			head:     body[:cursor+1],
			tail:     body[cursor+len(raw)-1:],
			elements: elements,
		}, nil
	}
	return nil, errors.New("no model array found")
}

// skipSeparators advances over the colon and surrounding whitespace that sit
// between an object key and its value.
func skipSeparators(body []byte, pos int) int {
	for pos < len(body) {
		switch body[pos] {
		case ' ', '\t', '\n', '\r', ':':
			pos++
			continue
		}
		return pos
	}
	return pos
}

func isListKey(key string) bool {
	for _, candidate := range listKeys {
		if key == candidate {
			return true
		}
	}
	return false
}

// splitArrayElements walks the inner text of a JSON array and cuts it into
// top level elements. Commas inside strings or nested values never split.
func splitArrayElements(inner []byte) ([][]byte, error) {
	if len(bytes.TrimSpace(inner)) == 0 {
		return nil, nil
	}
	var (
		out     [][]byte
		depth   int
		inStr   bool
		escaped bool
		start   = 0
	)
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if inStr {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth < 0 {
				return nil, errors.New("unbalanced array")
			}
		case ',':
			if depth == 0 {
				out = append(out, bytes.TrimSpace(inner[start:i]))
				start = i + 1
			}
		}
	}
	last := bytes.TrimSpace(inner[start:])
	if len(last) == 0 {
		return nil, errors.New("trailing comma in model array")
	}
	return append(out, last), nil
}

// allModelItems guards against reordering some unrelated array that happens to
// be called data or models: every element must carry a string id or name.
func allModelItems(items [][]byte) bool {
	for _, item := range items {
		if modelItemKey(item) == "" {
			return false
		}
	}
	return true
}

// modelItemKey returns the identity an entry is listed under. The OpenAI and
// Anthropic ports use "id", the Gemini port uses "name" (for example
// "models/gemini-2.5-pro").
func modelItemKey(item []byte) string {
	var entry struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(item, &entry); err != nil {
		return ""
	}
	if entry.ID != "" {
		return entry.ID
	}
	return entry.Name
}

// render rejoins the elements back into the document.
func (m *modelList) render() []byte {
	total := len(m.head) + len(m.tail)
	for _, element := range m.elements {
		total += len(element) + 1
	}
	out := make([]byte, 0, total)
	out = append(out, m.head...)
	for i, element := range m.elements {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, element...)
	}
	return append(out, m.tail...)
}
