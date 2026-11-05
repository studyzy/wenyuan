//go:build pgx

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// writeMemberLink inserts one directed link between two facts in a member bank.
func (f *mergeFixture) writeMemberLink(t *testing.T, member, from, to, linkType string, weight float64) {
	t.Helper()
	ctx, err := WithSchema(context.Background(), member)
	if err != nil {
		t.Fatalf("WithSchema: %v", err)
	}
	if _, err := f.db.ExecContext(ctx, `
		INSERT INTO `+quoteIdent(member)+`.memory_links (bank_id, from_unit_id, to_unit_id, link_type, weight)
		VALUES ($1, $2::uuid, $3::uuid, $4, $5)`,
		tenant.ProjectBankID(member, f.projectID), from, to, linkType, weight); err != nil {
		t.Fatalf("insert member link: %v", err)
	}
}

// TestMergeStoreCarriesFactLinks pins that a member link between two facts
// survives the carry: the team bank gets an edge between the two team rows,
// with the member's type and weight, and entity_id left NULL because an entity
// identity belongs to a bank.
func TestMergeStoreCarriesFactLinks(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	fromText := "Alice owns the deploy."
	toText := "The deploy moved to port 15432."
	from := f.writeMemberFact(t, f.memberA, fromText)
	to := f.writeMemberFact(t, f.memberA, toText)
	f.writeMemberLink(t, f.memberA, from, to, string(LinkCauses), 0.7)

	ctx := context.Background()
	var stamp time.Time
	var links []MergeLink
	ran, err := f.store.BeginMergeStanza(ctx, f.teamID, f.projectID, mergeRoundOpts(100, time.Minute),
		func(stanza MergeStanza) error {
			stamp = stanza.(*mergeStanza).ClaimStamp()
			for _, member := range stanza.Input().Members {
				links = append(links, member.Links...)
			}
			return nil
		})
	if err != nil {
		t.Fatalf("BeginMergeStanza: %v", err)
	}
	if !ran {
		t.Fatal("the round did not run")
	}
	fromKey, toKey := tenant.MergeKey(fromText), tenant.MergeKey(toText)
	if len(links) != 1 || links[0].FromMergeKey != fromKey || links[0].ToMergeKey != toKey ||
		links[0].LinkType != LinkCauses || links[0].Weight != 0.7 {
		t.Fatalf("member link read as %+v, want %s -> %s causes 0.7", links, fromKey, toKey)
	}
	if err := f.store.FinishMergeStanza(ctx, FinishMergeRequest{
		TeamID: f.teamID, ProjectID: f.projectID, TeamSchema: f.teamID,
		BankID: tenant.ProjectBankID(f.teamID, f.projectID), Stamp: stamp,
		Replace: MergeReplaceResult{
			Insert: []TeamFactRow{
				{Text: fromText, MergeKey: fromKey},
				{Text: toText, MergeKey: toKey},
			},
			Links: links,
		},
		Members: []string{f.memberA},
	}); err != nil {
		t.Fatalf("FinishMergeStanza: %v", err)
	}

	rows, err := f.db.QueryContext(f.teamSchemaCtx(t), `
		SELECT source.text, target.text, link.link_type, link.weight, COALESCE(link.entity_id::text, '')
		FROM `+quoteIdent(f.teamID)+`.memory_links link
		JOIN `+quoteIdent(f.teamID)+`.memory_units source ON source.id = link.from_unit_id
		JOIN `+quoteIdent(f.teamID)+`.memory_units target ON target.id = link.to_unit_id
		WHERE link.bank_id = $1`, tenant.ProjectBankID(f.teamID, f.projectID))
	if err != nil {
		t.Fatalf("read team links: %v", err)
	}
	defer func() { _ = rows.Close() }()
	type carried struct {
		from, to, kind, entity string
		weight                 float64
	}
	var got []carried
	for rows.Next() {
		var edge carried
		if err := rows.Scan(&edge.from, &edge.to, &edge.kind, &edge.weight, &edge.entity); err != nil {
			t.Fatalf("scan team link: %v", err)
		}
		got = append(got, edge)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read team links: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("team links = %+v, want the one member link", got)
	}
	if got[0].from != fromText || got[0].to != toText || got[0].kind != string(LinkCauses) ||
		got[0].weight != 0.7 || got[0].entity != "" {
		t.Fatalf("team link = %+v, want %q -> %q causes 0.7 with no entity", got[0], fromText, toText)
	}
}

// TestMergeStoreCarriesALinkWhenItsOtherFactArrives pins the batch boundary: a
// link read before its other endpoint is in the team bank is skipped, and the
// round that reads that endpoint writes it.
func TestMergeStoreCarriesALinkWhenItsOtherFactArrives(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	firstText := "Alice owns the deploy."
	secondText := "The deploy moved to port 15432."
	first := f.writeMemberFact(t, f.memberA, firstText)
	time.Sleep(10 * time.Millisecond)
	second := f.writeMemberFact(t, f.memberA, secondText)
	f.writeMemberLink(t, f.memberA, first, second, string(LinkCauses), 1)

	ctx := context.Background()
	run := func(cursor MergeCursor, text string) {
		var stamp time.Time
		var links []MergeLink
		ran, err := f.store.BeginMergeStanza(ctx, f.teamID, f.projectID, mergeRoundOpts(1, time.Minute),
			func(stanza MergeStanza) error {
				stamp = stanza.(*mergeStanza).ClaimStamp()
				for _, member := range stanza.Input().Members {
					links = append(links, member.Links...)
				}
				return nil
			})
		if err != nil {
			t.Fatalf("BeginMergeStanza: %v", err)
		}
		if !ran {
			t.Fatal("the round did not run")
		}
		key := tenant.MergeKey(text)
		if err := f.store.FinishMergeStanza(ctx, FinishMergeRequest{
			TeamID: f.teamID, ProjectID: f.projectID, TeamSchema: f.teamID,
			BankID: tenant.ProjectBankID(f.teamID, f.projectID), Stamp: stamp,
			Replace: MergeReplaceResult{
				Insert:  []TeamFactRow{{Text: text, MergeKey: key}},
				Links:   links,
				Sources: map[string][]MergeSourceRow{f.memberA: {{UnitID: cursor.UnitID, MergeKey: key}}},
			},
			Members: []string{f.memberA, f.memberB},
			Cursors: map[string]MergeCursor{f.memberA: cursor},
		}); err != nil {
			t.Fatalf("FinishMergeStanza: %v", err)
		}
	}
	var firstCursor MergeCursor
	if err := f.db.QueryRowContext(ctx,
		"SELECT updated_at FROM "+quoteIdent(f.memberA)+".memory_units WHERE id = $1::uuid", first).
		Scan(&firstCursor.UpdatedAt); err != nil {
		t.Fatalf("read first fact timestamp: %v", err)
	}
	firstCursor.UnitID = first
	run(firstCursor, firstText)
	if count := f.teamLinkCount(t); count != 0 {
		t.Fatalf("a link to an unmerged fact was written: %d links", count)
	}
	// The second round reads the second fact, and the link is incident to it.
	run(MergeCursor{UnitID: second}, secondText)
	if count := f.teamLinkCount(t); count != 1 {
		t.Fatalf("team links = %d, want the link written once its other fact arrived", count)
	}
}

// teamLinkCount counts the team bank links.
func (f *mergeFixture) teamLinkCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.db.QueryRowContext(f.teamSchemaCtx(t),
		"SELECT count(*) FROM "+quoteIdent(f.teamID)+".memory_links WHERE bank_id = $1",
		tenant.ProjectBankID(f.teamID, f.projectID)).Scan(&count); err != nil {
		t.Fatalf("count team links: %v", err)
	}
	return count
}
