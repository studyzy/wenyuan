package memoryops

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// An entity may be sent as a bare name or as an object with a name and a type,
// and both forms decode into the same shape.
func TestEntityInputDecode(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		want     EntityInputList
		wantFail bool
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
			name: "bare string form",
			body: `["Alice"]`,
			want: EntityInputList{{Text: "Alice"}},
		},
		{
			name: "mixed forms in one list",
			body: `["Alice",{"text":"Google","type":"ORG"}]`,
			want: EntityInputList{{Text: "Alice"}, {Text: "Google", Type: "ORG"}},
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
		{
			name:     "a number is rejected",
			body:     `[42]`,
			wantFail: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var item struct {
				Entities EntityInputList `json:"entities"`
			}
			err := json.Unmarshal([]byte(`{"entities":`+test.body+`}`), &item)
			if test.wantFail {
				if err == nil {
					t.Fatal("expected a decode error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(item.Entities) != len(test.want) {
				t.Fatalf("decoded %d entities, want %d: %+v", len(item.Entities), len(test.want), item.Entities)
			}
			for i, entity := range item.Entities {
				if entity.Text != test.want[i].Text || entity.Type != test.want[i].Type {
					t.Fatalf("entity %d = %+v, want %+v", i, entity, test.want[i])
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
	page, err := store.ListEntities(context.TODO(), storage.EntityQuery{BankID: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool, len(page.Entities))
	for _, entity := range page.Entities {
		names[entity.Name] = true
	}
	for _, want := range []string{"Alice", "Google"} {
		if !names[want] {
			t.Fatalf("entity %q was not stored: %+v", want, names)
		}
	}
}
