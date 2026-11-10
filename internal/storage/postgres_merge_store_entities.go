//go:build pgx

package storage

import (
	"context"
	"sort"

	"github.com/studyzy/wenyuan/internal/projectmerge"
)

// Writing the team bank's own entities from the entity names a round carries.
//
// A team fact that answers no entity posting is invisible to recall's
// entity-mediated graph expansion, so half of the graph arm goes quiet on the
// project's bank. This is what fills that in: every carried fact's entity names
// become team entity rows, and the new team unit is posted to them.
//
// The identity is the name, and it is the same identity a member's bank uses:
// idx_entities_bank_lower_name is UNIQUE on (bank_id, lower(canonical_name)), so
// "ABC" and "abc" are one entity on both sides of the merge. Resolution here is
// therefore not a second judgement about a name — it is the same one.

// mergeEntityOrigin marks the entity rows a merge round wrote, so an operator can
// tell them from rows a client created in the team bank directly. Nothing reads
// it: it is a label, not a rule.
const mergeEntityOrigin = "merge"

// mergeEntityTarget is one carried team fact the round must post entity rows for.
type mergeEntityTarget struct {
	// UnitID is the team unit the round just inserted.
	UnitID string
	// Names are the member fact's entity names, as the member bank spelled them.
	Names []string
}

// mergeEntityCarried is one carried fact with its names folded to team
// identities.
type mergeEntityCarried struct {
	unitID string
	set    projectmerge.EntitySet
}

// applyEntities writes the team entities and postings one round's inserted facts
// need.
//
// targets are the facts this round inserted, in write order. A fact the round
// kept rather than inserted is absent: its postings already exist, so it needs
// nothing.
func (s *PostgresMergeStore) applyEntities(
	ctx context.Context, tx Tx, qualified, bankID string, targets []mergeEntityTarget,
) error {
	if len(targets) == 0 {
		return nil
	}
	carried := make([]mergeEntityCarried, 0, len(targets))
	display := make(map[string]string)
	keys := make([]string, 0, len(targets))
	seen := make(map[string]bool)
	for _, target := range targets {
		set := projectmerge.NewEntitySet(target.Names)
		if set.Len() == 0 {
			continue
		}
		carried = append(carried, mergeEntityCarried{unitID: target.UnitID, set: set})
		for _, key := range set.Keys() {
			if seen[key] {
				continue
			}
			seen[key] = true
			keys = append(keys, key)
			if name, ok := set.Name(key); ok {
				display[key] = name
			}
		}
	}
	if len(keys) == 0 {
		return nil
	}
	// A deterministic statement order is what keeps two rounds writing the same
	// names from taking their row locks in opposite orders.
	sort.Strings(keys)
	if err := insertTeamEntities(ctx, tx, qualified, bankID, keys, display); err != nil {
		return err
	}
	entityIDs, err := lookUpTeamEntities(ctx, tx, qualified, bankID, keys)
	if err != nil {
		return err
	}
	if err := postTeamUnitEntities(ctx, tx, qualified, carried, entityIDs); err != nil {
		return err
	}
	return recordTeamCooccurrences(ctx, tx, qualified, carried, entityIDs)
}

// insertTeamEntities creates the team entity rows the round's names need, leaving
// the rows that already exist alone.
//
// The conflict target is the unique index's own expression, so a name this round
// has already written, or one a previous round wrote, is not duplicated. The
// stored spelling is the member's: the index decides identity, not the stored
// text.
func insertTeamEntities(
	ctx context.Context, tx Tx, qualified, bankID string, keys []string, display map[string]string,
) error {
	lowered := make([]string, len(keys))
	names := make([]string, len(keys))
	for i, key := range keys {
		lowered[i] = key
		names[i] = display[key]
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO `+qualified+`.entities (bank_id, canonical_name, entity_kind, metadata)
		SELECT $1::text, name, 'regular', jsonb_build_object('origin', $4::text)
		FROM unnest($2::text[], $3::text[]) AS t(key, name)
		ON CONFLICT (bank_id, lower(canonical_name)) DO NOTHING`,
		bankID, lowered, names, mergeEntityOrigin)
	return err
}

// lookUpTeamEntities resolves the round's identities to the team bank's rows.
//
// It runs after the insert instead of returning ids from it, because
// ON CONFLICT DO NOTHING returns nothing for a row that already existed and a
// round needs the id either way. The predicate is spelled exactly as the index
// is, so this read uses it.
func lookUpTeamEntities(
	ctx context.Context, tx Tx, qualified, bankID string, keys []string,
) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id::text, lower(canonical_name)
		FROM `+qualified+`.entities
		WHERE bank_id = $1 AND lower(canonical_name) = ANY($2::text[])`,
		bankID, keys)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]string, len(keys))
	for rows.Next() {
		var id, key string
		if err := rows.Scan(&id, &key); err != nil {
			return nil, err
		}
		out[key] = id
	}
	return out, rows.Err()
}

// postTeamUnitEntities posts each carried team unit to its entities.
//
// A unit is posted to one entity once: the set has already folded a duplicate
// name, and the schema's primary key would reject a second posting rather than
// ignore it.
func postTeamUnitEntities(
	ctx context.Context, tx Tx, qualified string, carried []mergeEntityCarried, entityIDs map[string]string,
) error {
	var unitIDs, entityIDList []string
	for _, fact := range carried {
		for _, key := range fact.set.Keys() {
			id, ok := entityIDs[key]
			if !ok {
				continue
			}
			unitIDs = append(unitIDs, fact.unitID)
			entityIDList = append(entityIDList, id)
		}
	}
	if len(unitIDs) == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO `+qualified+`.unit_entities (unit_id, entity_id)
		SELECT * FROM unnest($1::uuid[], $2::uuid[])
		ON CONFLICT DO NOTHING`,
		unitIDs, entityIDList)
	return err
}

// recordTeamCooccurrences records, for every carried fact, each pair of entities
// it mentions together.
//
// The pair is unordered and ordered lexicographically by id, which is the order
// the member side's own upsert takes them in. A fact whose pair is already
// recorded increments the count: this is the same unit-interval bookkeeping the
// member bank keeps, so the team bank's graph reads the same way.
func recordTeamCooccurrences(
	ctx context.Context, tx Tx, qualified string, carried []mergeEntityCarried, entityIDs map[string]string,
) error {
	cooccurrences := qualified + `.entity_cooccurrences`
	// A pair is counted once per round, not once per fact that names it: two
	// facts of one batch may both mention the same two entities, and a statement
	// cannot upsert the same row twice ("ON CONFLICT DO UPDATE command cannot
	// affect row a second time"). Counting per batch also keeps the count
	// meaningful — it is how many rounds saw the pair, which is the same unit the
	// member bank's own upsert accumulates in.
	counts := make(map[[2]string]int, len(carried))
	order := make([][2]string, 0, len(carried))
	for _, fact := range carried {
		for _, pair := range fact.set.Pairs() {
			first, firstOK := entityIDs[pair[0]]
			second, secondOK := entityIDs[pair[1]]
			if !firstOK || !secondOK {
				continue
			}
			if first > second {
				first, second = second, first
			}
			key := [2]string{first, second}
			if counts[key] == 0 {
				order = append(order, key)
			}
			counts[key]++
		}
	}
	if len(order) == 0 {
		return nil
	}
	firsts := make([]string, len(order))
	seconds := make([]string, len(order))
	increments := make([]int, len(order))
	for i, key := range order {
		firsts[i] = key[0]
		seconds[i] = key[1]
		increments[i] = counts[key]
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO `+cooccurrences+` (entity_id_1, entity_id_2, cooccurrence_count)
		SELECT * FROM unnest($1::uuid[], $2::uuid[], $3::int[])
		ON CONFLICT (entity_id_1, entity_id_2)
		DO UPDATE SET cooccurrence_count = `+cooccurrences+`.cooccurrence_count + EXCLUDED.cooccurrence_count`,
		firsts, seconds, increments)
	return err
}
