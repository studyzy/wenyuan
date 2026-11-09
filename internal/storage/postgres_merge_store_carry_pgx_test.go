//go:build pgx

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// What a round carries besides the text: the columns a team fact needs to answer
// recall, the identity that lets an edited fact replace its row, and the two
// things a rebuild has to retire — a legacy text-keyed row and a copied
// observation.

// teamRow is one team fact as the tests read it back.
type teamRow struct {
	ID           string
	Text         string
	FactType     string
	SourceUnitID string
	SourceTenant string
	Context      string
	Scopes       string
	EventDate    *time.Time
	OccurredEnd  *time.Time
	MentionedAt  *time.Time
	HasEmbedding bool
	HasSearchVec bool
}

// teamRows reads the team bank's facts in text order.
func (f *mergeFixture) teamRows(t *testing.T) []teamRow {
	t.Helper()
	ctx := f.teamSchemaCtx(t)
	rows, err := f.db.QueryContext(ctx, `
		SELECT id::text, text, fact_type, COALESCE(metadata->>'source_unit_id',''),
		       COALESCE(metadata->>'source_tenant',''), COALESCE(context,''),
		       COALESCE(observation_scopes::text,''), event_date, occurred_end, mentioned_at,
		       embedding IS NOT NULL, search_vector IS NOT NULL
		FROM `+quoteIdent(f.teamID)+`.memory_units
		WHERE bank_id = $1 ORDER BY text, id`, tenant.ProjectBankID(f.teamID, f.projectID))
	if err != nil {
		t.Fatalf("read team rows: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []teamRow
	for rows.Next() {
		var row teamRow
		if err := rows.Scan(&row.ID, &row.Text, &row.FactType, &row.SourceUnitID, &row.SourceTenant,
			&row.Context, &row.Scopes, &row.EventDate, &row.OccurredEnd, &row.MentionedAt,
			&row.HasEmbedding, &row.HasSearchVec); err != nil {
			t.Fatalf("scan team row: %v", err)
		}
		out = append(out, row)
	}
	return out
}

// insertTeamRow writes a team fact directly, bypassing the round — the shape a
// hand-written fact, or one an older deployment wrote, has in the bank.
func (f *mergeFixture) insertTeamRow(t *testing.T, factType, text, metadata string) string {
	t.Helper()
	var id string
	if err := f.db.QueryRowContext(f.teamSchemaCtx(t),
		"INSERT INTO "+quoteIdent(f.teamID)+".memory_units (bank_id, text, fact_type, metadata) VALUES ($1,$2,$3,$4::jsonb) RETURNING id::text",
		tenant.ProjectBankID(f.teamID, f.projectID), text, factType, metadata).Scan(&id); err != nil {
		t.Fatalf("insert team row: %v", err)
	}
	return id
}

// TestMergeStoreReplacesAnEditedSource is the identity rule: an edited member
// fact keeps its unit id, so the round replaces its team row rather than leaving
// both versions standing.
func TestMergeStoreReplacesAnEditedSource(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	firstText := "the deploy uses port 15432"
	editedText := "the deploy uses port 15433"
	unit := f.writeMemberFact(t, f.memberA, firstText)
	if !f.runRound(t,
		[]TeamFactRow{{Text: firstText, MergeKey: tenant.MergeKey(firstText), SourceUnitID: unit, SourceTenant: f.memberA}},
		map[string][]MergeSourceRow{f.memberA: {{UnitID: unit, MergeKey: tenant.MergeKey(firstText)}}}) {
		t.Fatal("first round did not run")
	}
	before := f.teamRows(t)
	if len(before) != 1 {
		t.Fatalf("team rows = %v, want 1", before)
	}

	// Edit the member's text, the way curation does.
	if err := f.storeMemberText(t, f.memberA, unit, editedText); err != nil {
		t.Fatalf("edit member fact: %v", err)
	}
	// The row the round now reads back for that source unit.
	existing, err := f.rowsFor(t, unit)
	if err != nil {
		t.Fatalf("read existing row: %v", err)
	}
	if existing == nil || existing.Text != firstText {
		t.Fatalf("existing row = %+v, want the old text", existing)
	}
	if !f.runReplace(t,
		[]TeamFactRow{{ID: existing.ID}},
		[]TeamFactRow{{Text: editedText, MergeKey: tenant.MergeKey(editedText), SourceUnitID: unit, SourceTenant: f.memberA}},
		map[string][]MergeSourceRow{f.memberA: {{UnitID: unit, MergeKey: tenant.MergeKey(editedText)}}}) {
		t.Fatal("second round did not run")
	}
	after := f.teamRows(t)
	if len(after) != 1 {
		t.Fatalf("team rows after the edit = %+v, want the one replaced row", after)
	}
	if after[0].Text != editedText || after[0].ID == before[0].ID {
		t.Fatalf("row = %+v, want a new row carrying %q", after[0], editedText)
	}
}

// TestMergeStoreDoesNotCopyMemberObservations pins the input scope end to end: a
// member's observation produces no team row, and the link into it is not carried
// as a dangling edge.
func TestMergeStoreDoesNotCopyMemberObservations(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	world := f.writeMemberFact(t, f.memberA, "the deploy uses port 15432")
	observation := f.writeMemberFactOfType(t, f.memberA, "observation", "the deployment migrated ports")
	f.linkMemberFacts(t, f.memberA, world, observation)

	seen := map[string]bool{}
	var carriedLinks []MergeLink
	if _, err := f.store.BeginMergeStanza(context.Background(), f.teamID, f.projectID,
		mergeRoundOpts(100, time.Minute), func(stanza MergeStanza) error {
			for _, member := range stanza.Input().Members {
				for _, fact := range member.Fresh {
					seen[fact.UnitID] = true
				}
				for _, link := range member.Links {
					// A link is carried when both ends are facts this round read;
					// an endpoint outside the input means the link points at
					// something the team bank will never hold.
					if !seen[link.FromMergeKey] && !seen[link.ToMergeKey] {
						continue
					}
					carriedLinks = append(carriedLinks, link)
				}
			}
			return nil
		}); err != nil {
		t.Fatalf("BeginMergeStanza: %v", err)
	}
	if seen[observation] {
		t.Fatal("a member observation reached the round")
	}
	if !seen[world] {
		t.Fatal("the member's world fact did not reach the round")
	}
	// The one link in the member's bank touches the observation, so the round
	// carries none: an endpoint that is not input has no team row to attach to.
	if len(carriedLinks) != 0 {
		t.Fatalf("carried %d links, want none: %+v", len(carriedLinks), carriedLinks)
	}
}

// TestMergeStoreFinalizeRetiresLegacyRows pins the migration path: after a
// rebuild completes, a row an older deployment keyed on its text — which carries
// no `origin` — is gone, and so is the observation copied from a member's bank.
func TestMergeStoreFinalizeRetiresLegacyRows(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	unit := f.writeMemberFact(t, f.memberA, "the deploy uses port 15432")
	text := "the deploy uses port 15432"

	legacy := f.insertTeamRow(t, "world", "a fact only the legacy scheme wrote",
		`{"merge_key":"legacy-key","merged_at":"2026-01-01T00:00:00Z"}`)
	copied := f.insertTeamRow(t, "observation", "a conclusion copied from a member",
		`{"merge_key":"observation-key","merged_at":"2026-01-01T00:00:00Z"}`)
	// A team fact the team's own consolidation synthesised must survive: it is
	// not the round's, but it is not a copy either. Its evidence is the replayed
	// source, so retirement never makes it stale.
	//
	// It is written after the replay, because a consolidation run reads the rows
	// the replay wrote — citing the replayed row is exactly the state a real one
	// leaves behind.
	f.linkTeamRows(t, legacy, copied)

	f.finishRoundAs(t, MergeReplaceResult{
		Insert:  []TeamFactRow{{Text: text, MergeKey: tenant.MergeKey(text), SourceUnitID: unit, SourceTenant: f.memberA}},
		Sources: map[string][]MergeSourceRow{f.memberA: {{UnitID: unit, MergeKey: tenant.MergeKey(text)}}},
	}, true, true)

	replayed, err := f.rowsFor(t, unit)
	if err != nil || replayed == nil {
		t.Fatalf("read replayed row: %v %+v", err, replayed)
	}
	teamMade := f.insertTeamRow(t, "observation", "the team's own conclusion", `{}`)
	if _, err := f.db.ExecContext(f.teamSchemaCtx(t),
		"UPDATE "+quoteIdent(f.teamID)+".memory_units SET source_memory_ids = $2::uuid[] WHERE id = $1::uuid",
		teamMade, []string{replayed.ID}); err != nil {
		t.Fatalf("give the team observation its evidence: %v", err)
	}
	// A second pass retires what the first left; the team's own conclusion is now
	// the only row that is not the round's, and it must stay.
	f.finishRoundAs(t, MergeReplaceResult{}, false, true)

	rows := f.teamRows(t)
	if len(rows) != 2 {
		t.Fatalf("team rows after finalize = %+v, want the replayed row and the team's own observation", rows)
	}
	if len(rows) != 2 || rows[0].SourceUnitID != unit {
		t.Fatalf("team rows after finalize = %+v, want the replayed row and the team's own observation", rows)
	}
	var left int
	if err := f.db.QueryRowContext(context.Background(),
		"SELECT count(*) FROM "+quoteIdent(f.teamID)+".memory_units WHERE bank_id = $1 AND id = ANY($2::uuid[])",
		tenant.ProjectBankID(f.teamID, f.projectID), []string{legacy, copied, teamMade}).Scan(&left); err != nil {
		t.Fatalf("count retired rows: %v", err)
	}
	// Only the legacy pair is retired; the team's own conclusion is content the
	// round never wrote and must not delete.
	if left != 1 {
		t.Fatalf("retired rows left = %d, want the team's own observation to survive", left)
	}
	var links int
	if err := f.db.QueryRowContext(context.Background(),
		"SELECT count(*) FROM "+quoteIdent(f.teamID)+".memory_links WHERE from_unit_id = $1::uuid", legacy).Scan(&links); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if links != 0 {
		t.Fatalf("a link into a retired row survived: %d", links)
	}
}

// TestMergeStoreCarriesRetrievalColumns pins the columns a team fact needs to be
// found: the member's vector byte for byte, a text search vector, the dates, the
// context and the scopes.
func TestMergeStoreCarriesRetrievalColumns(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	unit := f.writeMemberFact(t, f.memberA, "the deploy uses port 15432")
	vector := []float32{1}
	for len(vector) < EmbeddingDimensions {
		vector = append(vector, 0)
	}
	occurred := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if _, err := f.db.ExecContext(context.Background(), `
		UPDATE `+quoteIdent(f.memberA)+`.memory_units
		SET embedding = $2::vector, context = 'a runbook line', observation_scopes = '["project"]'::jsonb,
		    occurred_start = $3, occurred_end = $3, mentioned_at = $3, event_date = $3, text_signals = 'deploy port'
		WHERE id = $1::uuid`, unit, EncodeEmbedding(vector), occurred); err != nil {
		t.Fatalf("decorate member fact: %v", err)
	}

	facts := make([]MergeFact, 0, 1)
	if _, err := f.store.BeginMergeStanza(context.Background(), f.teamID, f.projectID,
		mergeRoundOpts(100, time.Minute), func(stanza MergeStanza) error {
			for _, input := range stanza.Input().Members {
				if input.Member.TenantID == f.memberA {
					facts = append(facts, input.Fresh...)
				}
			}
			return nil
		}); err != nil {
		t.Fatalf("BeginMergeStanza: %v", err)
	}
	if len(facts) != 1 || len(facts[0].Embedding) != EmbeddingDimensions {
		t.Fatalf("facts = %+v, want one embedded fact", facts)
	}
	if err := f.store.AbandonMergeStanza(context.Background(), f.teamID, f.projectID, MergeStateFailure{}); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	if !f.runRound(t,
		[]TeamFactRow{{
			Text: facts[0].Text, MergeKey: tenant.MergeKey(facts[0].Text),
			SourceUnitID: facts[0].UnitID, SourceTenant: f.memberA, FactType: facts[0].FactType,
			Context: facts[0].Context, EventDate: facts[0].EventDate,
			OccurredStart: facts[0].OccurredStart, OccurredEnd: facts[0].OccurredEnd,
			MentionedAt: facts[0].MentionedAt, ObservationScopes: facts[0].ObservationScopes,
			TextSignals: facts[0].TextSignals, Embedding: facts[0].Embedding,
		}},
		map[string][]MergeSourceRow{f.memberA: {{UnitID: unit, MergeKey: tenant.MergeKey(facts[0].Text)}}}) {
		t.Fatal("round did not run")
	}

	rows := f.teamRows(t)
	if len(rows) != 1 {
		t.Fatalf("team rows = %+v, want 1", rows)
	}
	row := rows[0]
	if !row.HasEmbedding || !row.HasSearchVec {
		t.Fatalf("row has neither vector nor search vector: %+v", row)
	}
	if row.Context != "a runbook line" || row.Scopes != `["project"]` {
		t.Fatalf("context/scopes = %q/%q", row.Context, row.Scopes)
	}
	if row.EventDate == nil || row.OccurredEnd == nil || row.MentionedAt == nil {
		t.Fatalf("temporal bounds not carried: %+v", row)
	}
	var distance float64
	if err := f.db.QueryRowContext(f.teamSchemaCtx(t),
		"SELECT embedding <=> $2::vector FROM "+quoteIdent(f.teamID)+".memory_units WHERE bank_id = $1",
		tenant.ProjectBankID(f.teamID, f.projectID), EncodeEmbedding(vector)).Scan(&distance); err != nil {
		t.Fatalf("compare vectors: %v", err)
	}
	if distance > 1e-6 {
		t.Fatalf("the team vector differs from the member's by %v: it was not copied", distance)
	}
}

// TestMergeStoreSkipsEmbeddingWhenTheSourceHasNone pins that a merge still works
// with no embedder: a member row without a vector produces a team row without
// one, and the round succeeds.
func TestMergeStoreSkipsEmbeddingWhenTheSourceHasNone(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	unit := f.writeMemberFact(t, f.memberA, "the deploy uses port 15432")
	text := "the deploy uses port 15432"
	if !f.runRound(t,
		[]TeamFactRow{{Text: text, MergeKey: tenant.MergeKey(text), SourceUnitID: unit, SourceTenant: f.memberA}},
		map[string][]MergeSourceRow{f.memberA: {{UnitID: unit, MergeKey: tenant.MergeKey(text)}}}) {
		t.Fatal("round did not run")
	}
	rows := f.teamRows(t)
	if len(rows) != 1 || rows[0].HasEmbedding {
		t.Fatalf("team rows = %+v, want one unembedded row", rows)
	}
	if !rows[0].HasSearchVec {
		t.Fatal("the keyword index was dropped along with the vector")
	}
}
