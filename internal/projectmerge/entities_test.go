package projectmerge

import (
	"strings"
	"testing"
)

// TestEntityNameKey covers the boundary table the design states for a team
// entity's identity: the same table a member's bank intakes names through, plus
// the case folding only the team bank applies.
func TestEntityNameKey(t *testing.T) {
	tests := []struct {
		name    string
		left    string
		right   string
		sameKey bool
	}{
		{name: "case folds", left: "ABC", right: "abc", sameKey: true},
		{name: "mixed case folds", left: "Abc", right: "abc", sameKey: true},
		{name: "surrounding whitespace", left: " ABC ", right: "ABC", sameKey: true},
		{name: "internal whitespace run", left: "支付  网关", right: "支付 网关", sameKey: true},
		{name: "tab and newline", left: "支付\t网关", right: "支付\n网关", sameKey: true},
		{name: "full width space", left: "支付\u3000网关", right: "支付 网关", sameKey: true},
		{name: "full width letters differ", left: "ＡＢＣ", right: "ABC", sameKey: false},
		{name: "punctuation kept", left: "支付网关。", right: "支付网关", sameKey: false},
		{name: "different words", left: "支付网关", right: "支付系统", sameKey: false},
		{name: "no character folding", left: "①", right: "1", sameKey: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			left, leftOK := EntityNameKey(test.left)
			right, rightOK := EntityNameKey(test.right)
			if !leftOK || !rightOK {
				t.Fatalf("EntityNameKey rejected an acceptable name: %q=%v %q=%v",
					test.left, leftOK, test.right, rightOK)
			}
			if same := left == right; same != test.sameKey {
				t.Fatalf("keys equal = %v, want %v (%q -> %q, %q -> %q)",
					same, test.sameKey, test.left, left, test.right, right)
			}
		})
	}
}

// TestEntityNameKeyDropsUnusableNames pins the two names intake refuses. A name
// the member's bank never stored must not become a team entity either.
func TestEntityNameKeyDropsUnusableNames(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "blank", value: "   "},
		{name: "whitespace only", value: "\t\n\u3000"},
		{name: "past the cap", value: strings.Repeat("a", 513)},
		{name: "multi byte past the cap", value: strings.Repeat("文", 513)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if key, ok := EntityNameKey(test.value); ok {
				t.Fatalf("EntityNameKey(%q) = %q, true; want dropped", test.value, key)
			}
		})
	}
	// A name exactly at the cap is still accepted: the cap is a limit, not an
	// exclusive bound.
	if _, ok := EntityNameKey(strings.Repeat("文", 512)); !ok {
		t.Fatal("EntityNameKey rejected a name of exactly the cap")
	}
}

// TestEntitySetFoldsDuplicates covers a fact that names one thing twice: the
// shared bank holds one entity for it, spelled the way it first appeared.
func TestEntitySetFoldsDuplicates(t *testing.T) {
	set := NewEntitySet([]string{"ABC", "abc", " ABC ", "xyz"})
	if got := set.Len(); got != 2 {
		t.Fatalf("Len = %d, want 2", got)
	}
	key, ok := EntityNameKey("abc")
	if !ok {
		t.Fatal("EntityNameKey rejected a name it should accept")
	}
	if name, held := set.Name(key); !held || name != "ABC" {
		t.Fatalf("stored spelling = %q (held %v), want the first spelling %q", name, held, "ABC")
	}
	if got, want := strings.Join(set.Keys(), ","), key+",xyz"; got != want {
		t.Fatalf("Keys = %q, want %q", got, want)
	}
}

// TestEntitySetDropsUnusableNames keeps the set to identities the member's bank
// actually holds.
func TestEntitySetDropsUnusableNames(t *testing.T) {
	set := NewEntitySet([]string{"", "   ", strings.Repeat("a", 513), "kept"})
	if got := set.Len(); got != 1 {
		t.Fatalf("Len = %d, want 1", got)
	}
	if keys := set.Keys(); len(keys) != 1 || keys[0] != "kept" {
		t.Fatalf("Keys = %v, want [kept]", keys)
	}
}

// TestEntitySetNameReportsMembership keeps the lookup honest for a key the set
// does not hold.
func TestEntitySetNameReportsMembership(t *testing.T) {
	set := NewEntitySet([]string{"kept"})
	if _, ok := set.Name("absent"); ok {
		t.Fatal("Name reported a key the set does not hold")
	}
	if _, ok := set.Name("kept"); !ok {
		t.Fatal("Name missed a key the set holds")
	}
}

// TestEntitySetPairs pins the co-occurrence pairs: every unordered pair once, in
// a deterministic order, and none at all for a single entity.
func TestEntitySetPairs(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		want  [][2]string
	}{
		{name: "no entities", names: nil, want: nil},
		{name: "one entity", names: []string{"a"}, want: nil},
		{
			name:  "two entities",
			names: []string{"b", "a"},
			want:  [][2]string{{"a", "b"}},
		},
		{
			name:  "three entities",
			names: []string{"c", "a", "b"},
			want:  [][2]string{{"a", "b"}, {"a", "c"}, {"b", "c"}},
		},
		{
			// A duplicate collapses before pairing, so the fact witnesses one
			// co-occurrence rather than two.
			name:  "duplicate names pair once",
			names: []string{"a", "A", "b"},
			want:  [][2]string{{"a", "b"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := NewEntitySet(test.names).Pairs()
			if len(got) != len(test.want) {
				t.Fatalf("pairs = %v, want %v", got, test.want)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Fatalf("pairs = %v, want %v", got, test.want)
				}
			}
		})
	}
}

// TestEntitySetPairsAreOrderIndependent pins the ordering: a round that reads the
// same fact again must record the same pairs, whatever order the names arrive in.
func TestEntitySetPairsAreOrderIndependent(t *testing.T) {
	first := NewEntitySet([]string{"a", "b", "c"}).Pairs()
	second := NewEntitySet([]string{"c", "b", "a"}).Pairs()
	if len(first) != len(second) {
		t.Fatalf("pair counts differ: %v vs %v", first, second)
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("pairs differ at %d: %v vs %v", i, first[i], second[i])
		}
	}
}

// TestEntitySetKeysAreCopies keeps a caller from mutating the set through the
// slice it was handed.
func TestEntitySetKeysAreCopies(t *testing.T) {
	set := NewEntitySet([]string{"a", "b"})
	keys := set.Keys()
	keys[0] = "mutated"
	if got := set.Keys()[0]; got != "a" {
		t.Fatalf("Keys shares its backing array: first key is %q", got)
	}
}
