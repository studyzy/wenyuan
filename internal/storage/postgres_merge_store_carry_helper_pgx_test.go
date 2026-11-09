//go:build pgx

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// The helpers the carry tests need: driving a round with a removal set, reading
// the team rows a source unit produced, and editing a member fact the way
// curation does.

// runReplace runs one round that adds `insert` and removes `remove`.
func (f *mergeFixture) runReplace(t *testing.T, remove, insert []TeamFactRow, sources map[string][]MergeSourceRow) bool {
	t.Helper()
	ctx := context.Background()
	var stamp time.Time
	ok, err := f.store.BeginMergeStanza(ctx, f.teamID, f.projectID, mergeRoundOpts(100, 5*time.Minute),
		func(stanza MergeStanza) error {
			stamp = stanza.(*mergeStanza).ClaimStamp()
			return nil
		})
	if err != nil {
		t.Fatalf("BeginMergeStanza: %v", err)
	}
	if !ok {
		return false
	}
	if err := f.store.FinishMergeStanza(ctx, FinishMergeRequest{
		TeamID: f.teamID, ProjectID: f.projectID, TeamSchema: f.teamID,
		BankID: tenant.ProjectBankID(f.teamID, f.projectID), Stamp: stamp,
		Replace: MergeReplaceResult{Remove: remove, Insert: insert, Sources: sources},
		Members: []string{f.memberA, f.memberB},
		Cursors: map[string]MergeCursor{},
	}); err != nil {
		t.Fatalf("FinishMergeStanza: %v", err)
	}
	return true
}

// rowsFor reads the team rows one source unit produced.
func (f *mergeFixture) rowsFor(t *testing.T, unitID string) (*TeamFactRow, error) {
	t.Helper()
	rows, err := f.store.rowsBySourceUnitsTx(context.Background(), f.newTx(t), &MergeProject{
		TeamID: f.teamID, ProjectID: f.projectID, TeamSchema: f.teamID,
		BankID: tenant.ProjectBankID(f.teamID, f.projectID),
	}, []string{unitID})
	if err != nil {
		return nil, err
	}
	row, ok := rows[unitID]
	if !ok {
		return nil, nil
	}
	return &row, nil
}

// newTx opens a transaction the test rolls back.
func (f *mergeFixture) newTx(t *testing.T) Tx {
	t.Helper()
	tx, err := f.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

// storeMemberText rewrites a member fact the way curation does: same id, new
// text, an advanced timestamp.
func (f *mergeFixture) storeMemberText(t *testing.T, member, unitID, text string) error {
	t.Helper()
	_, err := f.db.ExecContext(context.Background(),
		"UPDATE "+quoteIdent(member)+".memory_units SET text = $2, edited_at = now(), updated_at = now() WHERE id = $1::uuid",
		unitID, text)
	return err
}

// linkMemberFacts writes a link between two of a member's facts.
func (f *mergeFixture) linkMemberFacts(t *testing.T, member, from, to string) {
	t.Helper()
	if _, err := f.db.ExecContext(context.Background(),
		"INSERT INTO "+quoteIdent(member)+`.memory_links (from_unit_id, to_unit_id, link_type, weight, bank_id) VALUES ($1::uuid,$2::uuid,'temporal',0.5,$3)`,
		from, to, tenant.ProjectBankID(member, f.projectID)); err != nil {
		t.Fatalf("link member facts: %v", err)
	}
}

// linkTeamRows writes a link between two of the team's rows.
func (f *mergeFixture) linkTeamRows(t *testing.T, from, to string) {
	t.Helper()
	if _, err := f.db.ExecContext(f.teamSchemaCtx(t),
		"INSERT INTO "+quoteIdent(f.teamID)+`.memory_links (from_unit_id, to_unit_id, link_type, weight, bank_id) VALUES ($1::uuid,$2::uuid,'temporal',0.5,$3)`,
		from, to, tenant.ProjectBankID(f.teamID, f.projectID)); err != nil {
		t.Fatalf("link team rows: %v", err)
	}
}
