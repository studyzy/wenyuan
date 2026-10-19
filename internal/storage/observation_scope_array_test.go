package storage

import "testing"

// decodePostgresTextArray reads the text[] literal PostgreSQL renders for an
// array. Only the quotes fencing an element come off: an element that literally
// carries a quote (a tag like `say "hi"`) is rendered with that quote doubled,
// and stripping every quote would report a different tag — and so a different
// observation scope, since a scope is the observation's exact tag set.
func TestDecodePostgresTextArrayKeepsQuotesInsideAnElement(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"empty", "{}", nil},
		{"plain", "{a,b}", []string{"a", "b"}},
		{"quoted", `{"a","b"}`, []string{"a", "b"}},
		{"embedded quote", `{"say ""hi"""`, []string{`say "hi"`}},
		{"embedded quote beside a plain tag", `{"team:exec","say ""hi"""`, []string{"team:exec", `say "hi"`}},
		{"comma inside an element", `{"a,b"}`, []string{"a,b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decodePostgresTextArray([]byte(tc.raw))
			if len(got) != len(tc.want) {
				t.Fatalf("decode(%q) = %q, want %q", tc.raw, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("decode(%q) = %q, want %q", tc.raw, got, tc.want)
				}
			}
		})
	}
}
