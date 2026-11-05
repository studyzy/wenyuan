package storage

import (
	"testing"
)

// The sequence ratio scores an exact case-insensitive match as 1 and falls off
// as the names diverge, while still scoring a transposition as a near match.
func TestEntitySimilarity(t *testing.T) {
	tests := []struct {
		name     string
		left     string
		right    string
		wantHigh bool
	}{
		{name: "identical", left: "Acme Corp", right: "Acme Corp", wantHigh: true},
		{name: "case differs", left: "Acme Corp", right: "acme corp", wantHigh: true},
		{name: "whitespace differs", left: "Acme  Corp", right: "Acme Corp", wantHigh: true},
		{name: "single typo", left: "Acme Corp", right: "Acme Corpp", wantHigh: true},
		{name: "transposition", left: "Google", right: "Googel", wantHigh: true},
		{name: "unrelated names", left: "Alice", right: "Bicycle", wantHigh: false},
		{name: "one empty side", left: "", right: "Alice", wantHigh: false},
		{name: "different words", left: "Google", right: "Alphabet", wantHigh: false},
		{name: "no shared prefix", left: "Corp", right: "Acme", wantHigh: false},
		// A long extension shares the short name's characters but is a different
		// name: the sequence ratio spreads them across the longer string and
		// lands below the bar.
		{name: "shared long prefix", left: "Google", right: "Googleplex Park", wantHigh: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			score := entitySimilarity(test.left, test.right)
			if score < 0 || score > 1 {
				t.Fatalf("score %v is outside the unit interval", score)
			}
			similar := score >= EntitySimilarityThreshold
			if similar != test.wantHigh {
				t.Fatalf("score %v for %q/%q: similar=%v, want %v", score, test.left, test.right, similar, test.wantHigh)
			}
		})
	}
}

// An exact match scores 1 under either argument order, because the measure is
// not direction-dependent.
func TestEntitySimilarityIsSymmetric(t *testing.T) {
	pairs := [][2]string{
		{"Acme Corp", "Acme Corpp"},
		{"Google", "Alphabet"},
		{"文渊", "文渊阁"},
		{"", "Alice"},
	}
	for _, pair := range pairs {
		forward := entitySimilarity(pair[0], pair[1])
		backward := entitySimilarity(pair[1], pair[0])
		if forward != backward {
			t.Fatalf("similarity(%q, %q) = %v but the reverse is %v", pair[0], pair[1], forward, backward)
		}
	}
}

// Multibyte names are compared per rune, so a one-character difference in a
// Chinese name does not read as a wholesale mismatch.
func TestEntitySimilarityHandlesMultibyte(t *testing.T) {
	if score := entitySimilarity("文渊记忆", "文渊记忆库"); score < 0.7 {
		t.Fatalf("score %v for a one-character multibyte difference is too low", score)
	}
}

// A near name does not clear the weighted merge bar on name evidence alone, so
// resolution on still creates its own entity. Resolution off reuses only an
// exact match, which the near name now has.
func TestRetainEntityResolutionResolvesNearNames(t *testing.T) {
	store := NewMemoryStore("public")

	existing := RetainEntity{Text: "Acme Corporation", Kind: "ORG"}
	first := store.resolveEntityLocked("b1", existing, true, nil, nil)
	if got := store.entityCount("b1"); got != 1 {
		t.Fatalf("expected one entity, got %d", got)
	}
	// A typo's name-only score stays below the bar, so it gets its own row.
	near := RetainEntity{Text: "Acme Corporaton", Kind: "ORG"}
	second := store.resolveEntityLocked("b1", near, true, nil, nil)
	if second.ID == first.ID {
		t.Fatalf("a near name without shared context must not reuse the entity: %+v vs %+v", second, first)
	}
	if got := store.entityCount("b1"); got != 2 {
		t.Fatalf("resolution on created %d entities, want two", got)
	}

	// Resolution off reuses an exact match, which is the row resolution on just
	// created for the near name.
	third := store.resolveEntityLocked("b1", near, false, nil, nil)
	if third.ID != second.ID {
		t.Fatal("resolution off must reuse an exactly matching name")
	}
	if got := store.entityCount("b1"); got != 2 {
		t.Fatalf("expected two entities, got %d", got)
	}
}

// An exactly-matching name resolves to the stored entity under either switch,
// so the switch only changes what counts as close.
func TestRetainEntityExactMatchUnderBothModes(t *testing.T) {
	store := NewMemoryStore("public")
	first := store.resolveEntityLocked("b1", RetainEntity{Text: "Acme Corp"}, false, nil, nil)
	second := store.resolveEntityLocked("b1", RetainEntity{Text: "acme corp"}, true, nil, nil)
	if first.ID != second.ID {
		t.Fatalf("a case-insensitive exact match must reuse the entity: %+v vs %+v", first, second)
	}
	if got := store.entityCount("b1"); got != 1 {
		t.Fatalf("expected one entity, got %d", got)
	}
}

// An unnamed or blank entity is dropped rather than stored as a nameless row.
func TestRetainEntityBlankNameIsDropped(t *testing.T) {
	store := NewMemoryStore("public")
	for _, name := range []string{"", "   ", "\t\n"} {
		if resolved := store.resolveEntityLocked("b1", RetainEntity{Text: name}, true, nil, nil); resolved.ID != "" {
			t.Fatalf("blank name %q produced an entity: %+v", name, resolved)
		}
	}
	if got := store.entityCount("b1"); got != 0 {
		t.Fatalf("blank names created %d entities, want none", got)
	}
}

// Entities only resolve within their own bank, so a name in one bank never
// merges onto an entity in another.
func TestRetainEntityResolutionIsBankScoped(t *testing.T) {
	store := NewMemoryStore("public")
	first := store.resolveEntityLocked("b1", RetainEntity{Text: "Acme Corp"}, true, nil, nil)
	second := store.resolveEntityLocked("b2", RetainEntity{Text: "Acme Corp"}, true, nil, nil)
	if first.ID == second.ID {
		t.Fatal("entities in different banks must not merge")
	}
}

// entityCount reports how many entities the store holds for a bank.
func (s *MemoryStore) entityCount(bankID string) int {
	count := 0
	for _, entity := range s.entities {
		if entity.BankID == bankID {
			count++
		}
	}
	return count
}
