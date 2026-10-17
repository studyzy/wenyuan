package storage

import (
	"context"
	"testing"
	"time"
)

// testIncidentLinks returns the store's links touching one unit, both
// directions, so a test can assert what survives a sweep.
func testIncidentLinks(store *MemoryStore, unitID string) []MemoryLink {
	store.mu.RLock()
	defer store.mu.RUnlock()
	var out []MemoryLink
	for _, link := range store.links {
		if link.FromUnitID == unitID || link.ToUnitID == unitID {
			out = append(out, link)
		}
	}
	return out
}

// testHasLink reports whether a directed link of one type joins two units.
func testHasLink(store *MemoryStore, from, to string, kind MemoryLinkType) bool {
	for _, link := range testIncidentLinks(store, from) {
		if link.FromUnitID == from && link.ToUnitID == to && link.LinkType == kind {
			return true
		}
	}
	return false
}

// causalRetain writes three facts into one item: consecutive facts get derived
// temporal edges both ways through the middle unit, and the extraction declares
// a causal edge for each of the latter two.
func causalRetain(t *testing.T, store *MemoryStore, bank string) []string {
	t.Helper()
	content := "lost the job, then could not pay rent, then moved out"
	extractor := &stubExtraction{facts: map[string][]ExtractedFact{
		content: {
			{Text: "They lost the job."},
			{Text: "They could not pay rent.", CausedByIndexes: []int{0}},
			{Text: "They moved out.", CausedByIndexes: []int{1}},
		},
	}}
	outcome, err := store.RetainWrite(context.Background(), RetainRequest{
		BankID:     bank,
		Items:      []RetainItem{{Content: content, DocumentID: "doc-1"}},
		Extraction: extractor,
	})
	if err != nil {
		t.Fatal(err)
	}
	units := outcome.ItemUnitIDs[0]
	if len(units) != 3 {
		t.Fatalf("stored %d units, want 3", len(units))
	}
	return units
}

// An edit drops the derived links incident to the edited unit in both
// directions — the reference's apply_edit calls delete_unit_links, which
// matches from_unit_id and to_unit_id alike — while keeping the causal edges.
func TestUpdateMemoryEditDropsNonCausalLinks(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()
	units := causalRetain(t, store, bank)

	// The retain left the derived and causal edges the sweep must tell apart.
	if !testHasLink(store, units[0], units[1], LinkTemporal) {
		t.Fatalf("no temporal edge %s -> %s after retain", units[0], units[1])
	}
	if !testHasLink(store, units[1], units[2], LinkTemporal) {
		t.Fatalf("no temporal edge %s -> %s after retain", units[1], units[2])
	}
	if !testHasLink(store, units[1], units[0], LinkCausedBy) {
		t.Fatalf("no causal edge %s -> %s after retain", units[1], units[0])
	}
	if !testHasLink(store, units[2], units[1], LinkCausedBy) {
		t.Fatalf("no causal edge %s -> %s after retain", units[2], units[1])
	}

	newText := "a materially different fact"
	if _, err := store.UpdateMemory(ctx, bank, units[1], MemoryPatch{Text: &newText}); err != nil {
		t.Fatal(err)
	}

	if testHasLink(store, units[0], units[1], LinkTemporal) {
		t.Fatal("edit left the incoming temporal edge in place")
	}
	if testHasLink(store, units[1], units[2], LinkTemporal) {
		t.Fatal("edit left the outgoing temporal edge in place")
	}
	if !testHasLink(store, units[1], units[0], LinkCausedBy) {
		t.Fatal("edit dropped an outgoing causal edge")
	}
	if !testHasLink(store, units[2], units[1], LinkCausedBy) {
		t.Fatal("edit dropped an incoming causal edge")
	}
}

// A patch that does not touch the unit's content — a tag-only edit — is not the
// reference's doing_edit, so it leaves the links untouched.
func TestUpdateMemoryNonContentEditKeepsLinks(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()
	units := causalRetain(t, store, bank)

	tags := []string{"reviewed"}
	if _, err := store.UpdateMemory(ctx, bank, units[1], MemoryPatch{Tags: &tags}); err != nil {
		t.Fatal(err)
	}
	if !testHasLink(store, units[0], units[1], LinkTemporal) {
		t.Fatal("a tag-only edit dropped a temporal edge")
	}
	if !testHasLink(store, units[1], units[2], LinkTemporal) {
		t.Fatal("a tag-only edit dropped a temporal edge")
	}
}

// drainAndRelink runs the graph-maintenance drain the way the job does: claim
// the queued targets and relink each of them.
func drainAndRelink(t *testing.T, store *MemoryStore) {
	t.Helper()
	ctx := context.Background()
	targets, err := store.DrainGraphMaintenance(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) == 0 {
		t.Fatal("the edit queued no relink victims")
	}
	for _, target := range targets {
		if err := store.RelinkUnit(ctx, target.BankID, target.ID); err != nil {
			t.Fatal(err)
		}
	}
}

// An edit drops the edited unit's derived edges, and the graph-maintenance pass
// re-derives the temporal ones — the reference's relink tops up temporal as
// well as semantic links, so the edge comes back once the drain runs.
func TestGraphMaintenanceReDerivesEditedTemporalLinks(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()
	units := causalRetain(t, store, bank)

	newText := "a materially different fact"
	if _, err := store.UpdateMemory(ctx, bank, units[1], MemoryPatch{Text: &newText}); err != nil {
		t.Fatal(err)
	}
	if testHasLink(store, units[1], units[2], LinkTemporal) {
		t.Fatal("edit left the outgoing temporal edge in place")
	}

	drainAndRelink(t, store)

	if !testHasLink(store, units[1], units[2], LinkTemporal) {
		t.Fatal("relink did not re-derive the dropped outgoing temporal edge")
	}
	if !testHasLink(store, units[0], units[1], LinkTemporal) {
		t.Fatal("relink did not re-derive the dropped incoming temporal edge")
	}
	if !testHasLink(store, units[1], units[0], LinkCausedBy) {
		t.Fatal("relink dropped an outgoing causal edge")
	}
	if !testHasLink(store, units[2], units[1], LinkCausedBy) {
		t.Fatal("relink dropped an incoming causal edge")
	}
}

// A relink adds only the edges the window and the fact type admit: a neighbour
// 48h away is outside it, and one of another fact type never links by date.
func TestRelinkTemporalWindowAndFactType(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	now := time.Now().UTC()
	hourAgo := now.Add(-time.Hour)
	twoDaysAgo := now.Add(-48 * time.Hour)
	store.units["anchor"] = MemoryUnit{ID: "anchor", BankID: "b1", EventDate: &now, FactType: TypeWorld}
	store.units["close"] = MemoryUnit{ID: "close", BankID: "b1", EventDate: &hourAgo, FactType: TypeWorld}
	store.units["far"] = MemoryUnit{ID: "far", BankID: "b1", EventDate: &twoDaysAgo, FactType: TypeWorld}
	store.units["other-kind"] = MemoryUnit{ID: "other-kind", BankID: "b1", EventDate: &now, FactType: TypeExperience}

	if err := store.RelinkUnit(ctx, "b1", "anchor"); err != nil {
		t.Fatal(err)
	}
	if !testHasLink(store, "anchor", "close", LinkTemporal) {
		t.Fatal("an in-window same-kind neighbour was not linked")
	}
	if testHasLink(store, "anchor", "far", LinkTemporal) {
		t.Fatal("a unit 48h away must stay outside the window")
	}
	if testHasLink(store, "anchor", "other-kind", LinkTemporal) {
		t.Fatal("a unit of another fact type must not be linked by date")
	}
	// The edge is graded by proximity: one hour apart weighs far above the floor.
	for _, link := range store.links {
		if link.FromUnitID == "anchor" && link.ToUnitID == "close" {
			if link.Weight < 0.9 {
				t.Fatalf("temporal weight = %v, want ≈0.958", link.Weight)
			}
		}
	}
}

// A relink only adds: running it twice leaves the edge set unchanged, and it
// never deletes an incoming edge the unit's peers still hold.
func TestRelinkIsAdditive(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()
	units := causalRetain(t, store, bank)

	// An edge from a peer that is not being relinked must survive.
	before := len(testIncidentLinks(store, units[2]))
	if err := store.RelinkUnit(ctx, bank, units[1]); err != nil {
		t.Fatal(err)
	}
	afterFirst := len(store.links)
	if err := store.RelinkUnit(ctx, bank, units[1]); err != nil {
		t.Fatal(err)
	}
	if got := len(store.links); got != afterFirst {
		t.Fatalf("a second relink changed the link count: %d then %d", afterFirst, got)
	}
	if got := len(testIncidentLinks(store, units[2])); got < before {
		t.Fatalf("relinking a peer dropped this unit's incident edges: %d then %d", before, got)
	}
}
