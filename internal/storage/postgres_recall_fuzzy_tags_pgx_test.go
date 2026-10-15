//go:build pgx

package storage

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// A fuzzy query resolves against the bank's tag vocabulary once and then matches
// rows exactly, so the SQL backend does not widen an exact-hit token to its
// similar neighbours the way a per-row similarity predicate would.
func TestPostgresRecallExactHitTokenDoesNotWiden(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()

	for _, unit := range []struct{ id, tag string }{
		{"exact", "name:alexandria"},
		{"near", "name:alexandrio"},
	} {
		if _, err := store.UpsertUnit(ctx, MemoryUnit{
			ID: pgUUID(unit.id), BankID: "vec-bank", Text: "alpha",
			FactType: TypeWorld, Tags: []string{unit.tag},
		}); err != nil {
			t.Fatalf("upsert %s: %v", unit.id, err)
		}
	}

	result, err := store.Recall(ctx, RecallQuery{
		BankID: "vec-bank", Query: "alpha", Limit: 10,
		TagGroups: []TagGroup{{
			Resolve: TagResolveFuzzy, Match: TagsMatchAnyStrict,
			Tags: []string{"name:alexandria"},
		}},
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(result.Matches) != 1 || result.Matches[0].ID != pgUUID("exact") {
		t.Fatalf("recall = %v, want only the exact-tag unit", idsOf(result.Matches))
	}
}

// A bank whose distinct tag count exceeds the resolver's cap is refused before
// any retrieval runs: matching against a truncated vocabulary would silently
// change which memories match.
func TestPostgresRecallRefusesOversizedTagVocabulary(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()

	tags := make([]string, 0, MaxTagVocabulary+1)
	for i := 0; i <= MaxTagVocabulary; i++ {
		tags = append(tags, fmt.Sprintf("tag-%d", i))
	}
	if _, err := store.UpsertUnit(ctx, MemoryUnit{
		ID: pgUUID("big"), BankID: "vec-bank", Text: "alpha",
		FactType: TypeWorld, Tags: tags,
	}); err != nil {
		t.Fatal(err)
	}

	_, err := store.Recall(ctx, RecallQuery{
		BankID: "vec-bank", Query: "alpha",
		TagGroups: []TagGroup{{Resolve: TagResolveFuzzy, Match: TagsMatchAny, Tags: []string{"tag-1"}}},
	})
	if !errors.Is(err, ErrTagVocabularyTooLarge) {
		t.Fatalf("recall error = %v, want ErrTagVocabularyTooLarge", err)
	}
}

// An `exact` fuzzy leaf whose per-token expansions multiply past the branch
// ceiling is refused rather than enumerated.
func TestPostgresRecallRefusesCombinationExplosion(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()

	vocabulary := []string{
		"user:alexandria", "user:alexandrio", "user:alexandrie", "user:alexandriu",
		"user:alexandrea", "user:alexandre", "user:alexandro",
	}
	if _, err := store.UpsertUnit(ctx, MemoryUnit{
		ID: pgUUID("fan"), BankID: "vec-bank", Text: "alpha",
		FactType: TypeWorld, Tags: vocabulary,
	}); err != nil {
		t.Fatal(err)
	}

	_, err := store.Recall(ctx, RecallQuery{
		BankID: "vec-bank", Query: "alpha",
		TagGroups: []TagGroup{{
			Resolve: TagResolveFuzzy, Match: TagsMatchExact,
			Tags: []string{"user:alexandria", "user:alexandrio"},
		}},
	})
	if !errors.Is(err, ErrTagExactCombinationsTooLarge) {
		t.Fatalf("recall error = %v, want ErrTagExactCombinationsTooLarge", err)
	}
}
