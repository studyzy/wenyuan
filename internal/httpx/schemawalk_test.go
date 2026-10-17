package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A route whose body the reference's model validates in one pass wants every
// rejected field, not only the ones a body carrying a `null` reached: the
// unconditional walk reports them, and it reports them in declaration order.
func TestValidationItemsWalkWithoutNull(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/import", nil)
	r.Pattern = "POST /v1/default/banks/{bank_id}/import"
	body := `{"version":1,"mental_models":"x"}`
	items := ValidationItemsAgainstRequest(r, &struct{}{}, []byte(body))
	got, err := json.Marshal(ValidationFailure{Errors: items})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"detail":[` +
		`{"type":"string_type","loc":["body","version"],"msg":"Input should be a valid string","input":1},` +
		`{"type":"list_type","loc":["body","mental_models"],"msg":"Input should be a valid list","input":"x"}]}`
	if string(got) != want {
		t.Fatalf("items = %s, want %s", got, want)
	}
}

// A body that is not valid JSON is left to the caller's own decode, so the walk
// reports nothing for it.
func TestValidationItemsSkipUnparseableBodies(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/import", nil)
	r.Pattern = "POST /v1/default/banks/{bank_id}/import"
	if items := ValidationItemsAgainstRequest(r, &struct{}{}, []byte(`not json`)); len(items) != 0 {
		t.Fatalf("items = %+v", items)
	}
}

// Items built from more than one source are ordered by the request schema's
// field-declaration order, which is the order the reference reports them in.
func TestOrderItemsByDeclaration(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/import", nil)
	r.Pattern = "POST /v1/default/banks/{bank_id}/import"
	items := []ValidationError{
		Missing([]string{"body", "mental_models", "0", "name"}, nil),
		ValidationItem("value_error", []string{"body", "mental_models", "0", "id"}, "Value error, bad id", "X", nil),
		ValidationItem("int_parsing", []string{"body", "bank", "retain_chunk_size"}, "Input should be a valid integer", "x", nil),
	}
	OrderItemsByDeclaration(r, &struct{}{}, items)
	// The bank section is declared before the mental models, so its item comes
	// first even though it was appended last.
	want := []string{"retain_chunk_size", "id", "name"}
	for i, item := range items {
		if got := item.Loc[len(item.Loc)-1]; got != want[i] {
			t.Fatalf("item %d names %q, want %q (%+v)", i, got, want[i], items)
		}
	}
}

// Stripping the values the walk rejected leaves the members the reference's
// model accepted, so a later check on those members sees the values the caller
// sent rather than the zero values a failed decode left behind.
func TestStripRejectedValues(t *testing.T) {
	body := []byte(`{"version":"1","mental_models":[{"id":"x","name":"n","source_query":"q","tags":["a",1]}]}`)
	stripped := StripRejectedValues(body, [][]string{
		{"body", "mental_models", "0", "tags", "1"},
		{"body", "version"},
		{"body", "mental_models", "0", "absent_member"}, // names no value: a no-op
	})
	var document map[string]any
	if err := json.Unmarshal(stripped, &document); err != nil {
		t.Fatalf("stripped body is not JSON: %v (%s)", err, stripped)
	}
	if _, present := document["version"]; present {
		t.Fatalf("version was not removed: %s", stripped)
	}
	models, _ := document["mental_models"].([]any)
	if len(models) != 1 {
		t.Fatalf("models = %#v", document["mental_models"])
	}
	model, _ := models[0].(map[string]any)
	if model["id"] != "x" {
		t.Fatalf("a location naming no value changed the document: %s", stripped)
	}
	tags, _ := model["tags"].([]any)
	if len(tags) != 1 || tags[0] != "a" {
		t.Fatalf("the rejected element survived: %s", stripped)
	}
}

// The class-named shape message is available for the location the declaration
// puts a model at, which is what a route rendering the items itself serves.
func TestModelShapeMessage(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/import", nil)
	r.Pattern = "POST /v1/default/banks/{bank_id}/import"
	cases := map[string]string{
		"":                             "Input should be a valid dictionary or instance of BankTemplateManifest",
		"body":                         "Input should be a valid dictionary or instance of BankTemplateManifest",
		"body.bank":                    "Input should be a valid dictionary or instance of BankTemplateConfig",
		"body.mental_models.0.trigger": "Input should be a valid dictionary or instance of MentalModelTrigger",
		"body.version":                 "",
	}
	for loc, want := range cases {
		var segments []string
		if loc != "" {
			segments = splitLocation(loc)
		}
		if got := ModelShapeMessage(r, &struct{}{}, segments); got != want {
			t.Errorf("%q => %q, want %q", loc, got, want)
		}
	}
}

// splitLocation splits a dotted test location, dropping the first segment's
// leading empty string for a leading dot.
func splitLocation(loc string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(loc); i++ {
		if i == len(loc) || loc[i] == '.' {
			out = append(out, loc[start:i])
			start = i + 1
		}
	}
	return out
}
