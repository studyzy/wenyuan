package wenyuan

import (
	"encoding/json"
	"strings"
	"testing"
)

// The two behaviours asserted here exist because the generator needs patching
// after it runs (scripts/generate_clients.sh documents each patch): union
// MarshalJSON is emitted with a pointer receiver, which makes a union used as a
// plain struct field serialize as its Go shape, and decoders are emitted with
// DisallowUnknownFields, which would reject a response the moment the server
// adds a field. Regeneration that loses a patch fails these tests.

func TestContentMarshalsAsItsWireForm(t *testing.T) {
	// The bare-string form is what almost every caller retains; it must reach
	// the wire as a JSON string rather than as an object with the union's Go
	// field names.
	item := MemoryItem{Content: TextContent("Alice joined Google")}
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(encoded); !strings.Contains(got, `"content":"Alice joined Google"`) {
		t.Fatalf("string content encoded as %s, want a bare JSON string", got)
	}
	if strings.Contains(string(encoded), "String") || strings.Contains(string(encoded), "ArrayOf") {
		t.Fatalf("union field names leaked into the wire form: %s", encoded)
	}

	// The block form travels as an array, so an inline image keeps its place in
	// the ordered content.
	blocks := []ContentAnyOfInner{{TextContentBlock: &TextContentBlock{Type: "text", Text: "click the button"}}}
	item = MemoryItem{Content: Content{ArrayOfContentAnyOfInner: &blocks}}
	encoded, err = json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal blocks: %v", err)
	}
	if got := string(encoded); !strings.Contains(got, `"content":[{"text":"click the button"`) {
		t.Fatalf("block content encoded as %s, want an array of blocks", got)
	}
}

func TestContentUnmarshalsEitherForm(t *testing.T) {
	var item MemoryItem
	if err := json.Unmarshal([]byte(`{"content":"a plain sentence"}`), &item); err != nil {
		t.Fatalf("decode string form: %v", err)
	}
	if item.Content.String == nil || *item.Content.String != "a plain sentence" {
		t.Fatalf("string form decoded as %+v", item.Content)
	}

	var blocks MemoryItem
	if err := json.Unmarshal([]byte(`{"content":[{"type":"text","text":"one"},{"type":"text","text":"two"}]}`), &blocks); err != nil {
		t.Fatalf("decode block form: %v", err)
	}
	if blocks.Content.ArrayOfContentAnyOfInner == nil || len(*blocks.Content.ArrayOfContentAnyOfInner) != 2 {
		t.Fatalf("block form decoded as %+v", blocks.Content)
	}
}

func TestResponsesIgnoreUnknownFields(t *testing.T) {
	// A newer server may send fields this client does not know. Rejecting them
	// would make every forward-compatible addition a breaking change for a
	// pinned client, so the decoder patch that removes DisallowUnknownFields is
	// pinned here.
	body := `{
		"results": [{"id": "m1", "text": "hello", "a_field_from_the_future": {"nested": [1,2,3]}}],
		"another_future_field": "surprise"
	}`
	var response RecallResponse
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("unknown fields must be ignored, got: %v", err)
	}
	if len(response.Results) != 1 || response.Results[0].Id != "m1" {
		t.Fatalf("decoded results = %+v", response.Results)
	}
}

func TestDecodingRequiresTheDeclaredRequiredProperties(t *testing.T) {
	// The generated decoder checks that every property the document marks
	// required is *present* — presence, not truthiness — and names the one that
	// is missing. Pinned so a regeneration that drops the check is noticed.
	var response RecallResponse
	err := json.Unmarshal([]byte(`{"results":[{"id":"m1"}]}`), &response)
	if err == nil {
		t.Fatalf("a result without its required text must be refused")
	}
	if !strings.Contains(err.Error(), "text") {
		t.Fatalf("error must name the missing property, got: %v", err)
	}
}

func TestNullAndAbsentOptionalFieldsAreDistinct(t *testing.T) {
	// Two states travel on the wire: a field the server never set (absent), and
	// a field it set to null. The Nullable wrappers keep them apart — decoding
	// null must not look like "absent" to a caller that distinguishes them.
	body := `{"results":[{"id":"m1","text":"hello","context":null,"mentioned_at":"2026-01-02T03:04:05Z"}]}`
	var response RecallResponse
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("null optionals must decode: %v", err)
	}
	result := response.Results[0]

	if !result.Context.IsSet() {
		t.Fatalf("an explicit null must decode as set: %+v", result.Context)
	}
	if got := result.Context.Get(); got != nil {
		t.Fatalf("an explicit null must decode to a nil value, got %q", *got)
	}
	if !result.MentionedAt.IsSet() || result.MentionedAt.Get() == nil {
		t.Fatalf("a present timestamp must decode to a value: %+v", result.MentionedAt)
	}

	var absent RecallResponse
	if err := json.Unmarshal([]byte(`{"results":[{"id":"m1","text":"hello"}]}`), &absent); err != nil {
		t.Fatalf("decoding an absent optional: %v", err)
	}
	if absent.Results[0].Context.IsSet() {
		t.Fatalf("an omitted field must decode as unset: %+v", absent.Results[0].Context)
	}
}
