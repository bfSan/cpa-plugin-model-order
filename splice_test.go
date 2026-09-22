package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestParseModelListOpenAIFormat(t *testing.T) {
	body := []byte(`{"object":"list","data":[{"id":"b","object":"model","created":1700000000,"owned_by":"x"},{"id":"a","object":"model","owned_by":"y"}]}`)
	list, errParse := parseModelList(body)
	if errParse != nil {
		t.Fatalf("parse failed: %v", errParse)
	}
	if len(list.elements) != 2 {
		t.Fatalf("want 2 elements, got %d", len(list.elements))
	}
	if string(list.head) != `{"object":"list","data":[` {
		t.Fatalf("unexpected head %q", list.head)
	}
	if string(list.tail) != `]}` {
		t.Fatalf("unexpected tail %q", list.tail)
	}
	if got := modelItemKey(list.elements[0]); got != "b" {
		t.Fatalf("want id b, got %q", got)
	}
	// render with the original order must reproduce the input byte for byte.
	if got := string(list.render()); got != string(body) {
		t.Fatalf("render is not byte faithful:\n got %s\nwant %s", got, body)
	}
}

func TestParseModelListGeminiFormatUsesName(t *testing.T) {
	body := []byte(`{"models":[{"name":"models/zeta","version":"v1"},{"name":"models/alpha","version":"v1"}]}`)
	list, errParse := parseModelList(body)
	if errParse != nil {
		t.Fatalf("parse failed: %v", errParse)
	}
	// The "models/" resource prefix is stripped so configured patterns behave the
	// same on this port as on the OpenAI one.
	if got := modelItemKey(list.elements[0]); got != "zeta" {
		t.Fatalf("want zeta, got %q", got)
	}
}

// TestGeminiPortHonoursPrefixPatterns is the regression for the resource prefix:
// without stripping it, "gpt-*" only ever matched on the OpenAI port.
func TestGeminiPortHonoursPrefixPatterns(t *testing.T) {
	if err := loadConfig(nil); err != nil {
		t.Fatalf("defaults must load cleanly: %v", err)
	}
	body := []byte(`{"models":[` +
		`{"name":"models/workbuddy-hy3"},{"name":"models/gpt-6-astra"},{"name":"models/qoder-auto"}` +
		`]}`)
	out, changed := orderBody(portGemini, body)
	if !changed {
		t.Fatal("expected gemini listing to be reordered")
	}
	want := []byte(`{"models":[{"name":"models/qoder-auto"},{"name":"models/gpt-6-astra"},{"name":"models/workbuddy-hy3"}]}`)
	if string(out) != string(want) {
		t.Fatalf("got  %s\nwant %s", out, want)
	}
}

func TestReorderPreservesNestedValuesAndEscapes(t *testing.T) {
	// The second element carries a nested object, an array and an escaped quote,
	// none of which may be mistaken for array boundaries.
	body := []byte(`{"object":"list","data":[` +
		`{"id":"b","meta":{"deep":{"deeper":[1,2,3]}},"tags":["x","y"]},` +
		`{"id":"a","note":"has } [ , inside \" string"}` +
		`]}`)
	list, errParse := parseModelList(body)
	if errParse != nil {
		t.Fatalf("parse failed: %v", errParse)
	}
	if len(list.elements) != 2 {
		t.Fatalf("want 2 elements, got %d", len(list.elements))
	}
	list.elements[0], list.elements[1] = list.elements[1], list.elements[0]
	out := list.render()

	var doc struct {
		Object string            `json:"object"`
		Data   []json.RawMessage `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(out, &doc); errUnmarshal != nil {
		t.Fatalf("reordered body is invalid json: %v (%s)", errUnmarshal, out)
	}
	if doc.Object != "list" || len(doc.Data) != 2 {
		t.Fatalf("unexpected doc %+v", doc)
	}
	if string(doc.Data[0]) != `{"id":"a","note":"has } [ , inside \" string"}` {
		t.Fatalf("element not preserved verbatim: %s", doc.Data[0])
	}
}

func TestParseModelListRejectsNonListings(t *testing.T) {
	cases := map[string]string{
		"chat completion":  `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"content":"hi"}}]}`,
		"single model":     `{"object":"list","data":[{"id":"only"}]}`,
		"empty data":       `{"object":"list","data":[]}`,
		"data not array":   `{"data":{"id":"x"},"object":"list"}`,
		"items without id": `{"data":[{"foo":1},{"bar":2}]}`,
		"not json":         `{"object":"list","data":`,
		"top level array":  `[{"id":"a"},{"id":"b"}]`,
	}
	for name, raw := range cases {
		if _, errParse := parseModelList([]byte(raw)); errParse == nil {
			t.Errorf("%s: expected rejection, got no error", name)
		}
	}
}

func TestSplitArrayElementsHandlesWhitespace(t *testing.T) {
	body := []byte("{\n  \"object\": \"list\",\n  \"data\": [\n    {\"id\": \"b\"},\n    {\"id\": \"a\"}\n  ]\n}")
	list, errParse := parseModelList(body)
	if errParse != nil {
		t.Fatalf("parse failed: %v", errParse)
	}
	got := []string{string(list.elements[0]), string(list.elements[1])}
	want := []string{`{"id": "b"}`, `{"id": "a"}`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
