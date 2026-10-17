package memoryops

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// An entity is an object with a name and an optional type. The reference's
// entities list takes objects only, so a bare string, a JSON null or a number is
// recorded as invalid (reported at its own path by the write validation) rather
// than decoded or silently dropped.
func TestEntityInputDecode(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    EntityInputList
		invalid []bool
	}{
		{
			name: "object form with a type",
			body: `[{"text":"Alice","type":"PERSON"}]`,
			want: EntityInputList{{Text: "Alice", Type: "PERSON"}},
		},
		{
			name: "object form without a type",
			body: `[{"text":"Alice"}]`,
			want: EntityInputList{{Text: "Alice"}},
		},
		{
			name:    "bare string form is invalid",
			body:    `["Alice"]`,
			want:    EntityInputList{{}},
			invalid: []bool{true},
		},
		{
			name:    "mixed forms keep object elements and flag the string",
			body:    `["Alice",{"text":"Google","type":"ORG"}]`,
			want:    EntityInputList{{}, {Text: "Google", Type: "ORG"}},
			invalid: []bool{true, false},
		},
		{
			name:    "null element is invalid",
			body:    `[null]`,
			want:    EntityInputList{{}},
			invalid: []bool{true},
		},
		{
			name:    "a number is invalid",
			body:    `[42]`,
			want:    EntityInputList{{}},
			invalid: []bool{true},
		},
		{
			name: "empty list",
			body: `[]`,
			want: EntityInputList{},
		},
		{
			name: "null is an empty list",
			body: `null`,
			want: nil,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var item struct {
				Entities EntityInputList `json:"entities"`
			}
			if err := json.Unmarshal([]byte(`{"entities":`+test.body+`}`), &item); err != nil {
				t.Fatal(err)
			}
			if len(item.Entities) != len(test.want) {
				t.Fatalf("decoded %d entities, want %d: %+v", len(item.Entities), len(test.want), item.Entities)
			}
			for i, entity := range item.Entities {
				if entity.Text != test.want[i].Text || entity.Type != test.want[i].Type {
					t.Fatalf("entity %d = %+v, want %+v", i, entity, test.want[i])
				}
				wantInvalid := len(test.invalid) > i && test.invalid[i]
				if entity.Invalid != wantInvalid {
					t.Fatalf("entity %d invalid = %v, want %v", i, entity.Invalid, wantInvalid)
				}
			}
		})
	}
}

// An entity sent as a bare name carries no type, so it is stored as the default
// concept rather than with an empty label.
func TestEntityDefaultType(t *testing.T) {
	tests := []struct {
		name string
		in   EntityInputList
		want []storage.RetainEntity
	}{
		{
			name: "explicit type is kept",
			in:   EntityInputList{{Text: "Alice", Type: "PERSON"}},
			want: []storage.RetainEntity{{Text: "Alice", Kind: "PERSON"}},
		},
		{
			name: "absent type defaults to concept",
			in:   EntityInputList{{Text: "Alice"}},
			want: []storage.RetainEntity{{Text: "Alice", Kind: EntityDefaultType}},
		},
		{
			name: "blank name is dropped",
			in:   EntityInputList{{Text: "  ", Type: "PERSON"}, {Text: "Alice"}},
			want: []storage.RetainEntity{{Text: "Alice", Kind: EntityDefaultType}},
		},
		{
			name: "name is trimmed",
			in:   EntityInputList{{Text: "  Alice  ", Type: " PERSON "}},
			want: []storage.RetainEntity{{Text: "Alice", Kind: "PERSON"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := toStorageEntities(test.in)
			if len(got) != len(test.want) {
				t.Fatalf("mapped %d entities, want %d: %+v", len(got), len(test.want), got)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Fatalf("entity %d = %+v, want %+v", i, got[i], test.want[i])
				}
			}
		})
	}
}

// A write carrying the typed entity form stores an entity per supplied name, so
// the type survives the wire rather than being silently dropped.
func TestRetainAcceptsTypedEntities(t *testing.T) {
	mux, store := newServer(t)
	body := `{"items":[{"content":"Alice works at Google.",
		"entities":[{"text":"Alice","type":"PERSON"},{"text":"Google","type":"ORG"}]}]}`
	rec := post(t, mux, "/v1/default/banks/b1/memories", body)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	page, err := store.ListEntities(context.TODO(), storage.EntityQuery{BankID: "b1", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool, len(page.Entities))
	for _, entity := range page.Entities {
		names[entity.CanonicalName] = true
	}
	for _, want := range []string{"Alice", "Google"} {
		if !names[want] {
			t.Fatalf("entity %q was not stored: %+v", want, names)
		}
	}
}
