package personalrollup

import (
	"testing"
	"time"
)

func projectionSource(bank, unit, text, kind string) ProjectionFact {
	return ProjectionFact{
		Key: ProjectionKey(bank, unit), Text: text, FactType: kind,
		BankID: bank, UnitID: unit,
	}
}

func TestProjectionKeyIsPerSourceUnit(t *testing.T) {
	world := ProjectionKey("alice::orders", "u1")
	experience := ProjectionKey("alice::orders", "u1")
	if world != experience {
		t.Fatal("the key must not depend on the fact kind")
	}
	if ProjectionKey("alice::orders", "u1") == ProjectionKey("alice::billing", "u1") {
		t.Fatal("two banks' facts must stay distinct")
	}
	if ProjectionKey("alice::orders", "u1") == ProjectionKey("alice::orders", "u2") {
		t.Fatal("two units of one bank must stay distinct")
	}
}

func TestProjectionIsOneRowPerSourceFact(t *testing.T) {
	// Two banks happen to hold the same sentence: a verbatim projection keeps
	// both rows, exactly as one bank holding both facts would.
	fresh := []ProjectionFact{
		projectionSource("alice::orders", "u1", "on port 15432", "world"),
		projectionSource("alice::billing", "u2", "on port 15432", "experience"),
	}
	diff := DiffProjection(nil, fresh, false)
	if len(diff.Insert) != 2 || len(diff.Remove) != 0 {
		t.Fatalf("diff = %+v, want two inserts", diff)
	}
	if len(diff.Sources) != 2 {
		t.Fatalf("ledger rows = %d, want one per fact", len(diff.Sources))
	}
	if diff.Insert[0].FactType != "world" || diff.Insert[1].FactType != "experience" {
		t.Fatalf("kinds = %q/%q, want world/experience", diff.Insert[0].FactType, diff.Insert[1].FactType)
	}
}

func TestProjectionKeepsAnUnchangedRow(t *testing.T) {
	fresh := []ProjectionFact{projectionSource("alice::orders", "u1", "on port 15432", "world")}
	stored := fresh[0]
	stored.ID = "row-1"
	diff := DiffProjection([]ProjectionFact{stored}, fresh, false)
	if len(diff.Insert) != 0 || len(diff.Remove) != 0 {
		t.Fatalf("diff = %+v, want the row kept", diff)
	}
}

func TestProjectionReplacesAnEditedFact(t *testing.T) {
	stored := projectionSource("alice::orders", "u1", "on port 15432", "world")
	stored.ID = "row-1"
	fresh := []ProjectionFact{projectionSource("alice::orders", "u1", "on port 15433", "world")}
	diff := DiffProjection([]ProjectionFact{stored}, fresh, false)
	if len(diff.Remove) != 1 || diff.Remove[0].ID != "row-1" {
		t.Fatalf("remove = %+v, want the old row id", diff.Remove)
	}
	if len(diff.Insert) != 1 || diff.Insert[0].Text != "on port 15433" {
		t.Fatalf("insert = %+v, want the edited text", diff.Insert)
	}
}

func TestProjectionReplacesAFactThatChangedKind(t *testing.T) {
	stored := projectionSource("alice::orders", "u1", "on port 15432", "world")
	stored.ID = "row-1"
	fresh := []ProjectionFact{projectionSource("alice::orders", "u1", "on port 15432", "experience")}
	diff := DiffProjection([]ProjectionFact{stored}, fresh, false)
	if len(diff.Remove) != 1 || len(diff.Insert) != 1 {
		t.Fatalf("diff = %+v, want the row replaced", diff)
	}
}

func TestProjectionReplacesAFactThatChangedDates(t *testing.T) {
	stored := projectionSource("alice::orders", "u1", "on port 15432", "world")
	stored.ID = "row-1"
	first := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)
	stored.MentionedAt = &first
	fresh := []ProjectionFact{projectionSource("alice::orders", "u1", "on port 15432", "world")}
	fresh[0].MentionedAt = &second
	diff := DiffProjection([]ProjectionFact{stored}, fresh, false)
	if len(diff.Remove) != 1 || len(diff.Insert) != 1 {
		t.Fatalf("diff = %+v, want the row replaced", diff)
	}
}

func TestIncrementalRoundLeavesStoredRowsAlone(t *testing.T) {
	old := projectionSource("alice::orders", "u1", "old fact", "world")
	old.ID = "row-1"
	fresh := []ProjectionFact{projectionSource("alice::billing", "u2", "new fact", "experience")}
	diff := DiffProjection([]ProjectionFact{old}, fresh, false)
	if len(diff.Remove) != 0 {
		t.Fatalf("remove = %+v, an incremental round must not drop an unread row", diff.Remove)
	}
	if len(diff.Insert) != 1 {
		t.Fatalf("insert = %+v, want the one new fact", diff.Insert)
	}
}

func TestRebuildDropsRowsWhoseSourceIsGone(t *testing.T) {
	kept := projectionSource("alice::orders", "u1", "kept", "world")
	kept.ID = "row-1"
	gone := projectionSource("alice::orders", "u2", "gone", "world")
	gone.ID = "row-2"
	diff := DiffProjection([]ProjectionFact{kept, gone}, []ProjectionFact{kept}, true)
	if len(diff.Remove) != 1 || diff.Remove[0].ID != "row-2" {
		t.Fatalf("remove = %+v, want only the deleted source's row", diff.Remove)
	}
	if len(diff.Insert) != 0 {
		t.Fatalf("insert = %+v, the surviving row must be kept", diff.Insert)
	}
}
