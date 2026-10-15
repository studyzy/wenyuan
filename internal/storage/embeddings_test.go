package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// stubEmbedder returns a fixed-width vector per input, so the retain write
// path's embedding step can be exercised without a provider.
type stubEmbedder struct {
	dim  int
	err  error
	sent [][]string
}

func (s *stubEmbedder) EmbedPassages(_ context.Context, texts []string) ([][]float32, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.sent = append(s.sent, texts)
	out := make([][]float32, len(texts))
	for i := range texts {
		vec := make([]float32, s.dim)
		vec[0] = float32(i + 1)
		out[i] = vec
	}
	return out, nil
}

func (s *stubEmbedder) EmbedQuery(_ context.Context, _ string) ([]float32, error) {
	if s.err != nil {
		return nil, s.err
	}
	return make([]float32, s.dim), nil
}

// A write with an embedding generator attaches a vector to every stored unit,
// so the vector recall arm has something to rank.
func TestRetainWriteEmbedsUnits(t *testing.T) {
	store := NewMemoryStore("public")
	embedder := &stubEmbedder{dim: 4}
	if _, err := store.RetainWrite(context.Background(), RetainRequest{
		BankID:     "b1",
		Items:      []RetainItem{{Content: "the sky is blue"}},
		Embeddings: embedder,
	}); err != nil {
		t.Fatal(err)
	}
	units := store.UnitsForTest("b1")
	if len(units) == 0 {
		t.Fatal("the write stored no units")
	}
	for _, unit := range units {
		if len(unit.Embedding) != 4 {
			t.Fatalf("unit %s embedding width = %d, want 4", unit.ID, len(unit.Embedding))
		}
	}
}

// A write with no generator leaves the units unembedded, which is the offline
// deployment's behaviour.
func TestRetainWriteWithoutGeneratorLeavesUnitsUnembedded(t *testing.T) {
	store := NewMemoryStore("public")
	if _, err := store.RetainWrite(context.Background(), RetainRequest{
		BankID: "b1",
		Items:  []RetainItem{{Content: "the sky is blue"}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, unit := range store.UnitsForTest("b1") {
		if len(unit.Embedding) != 0 {
			t.Fatalf("unit %s unexpectedly embedded", unit.ID)
		}
	}
}

// A failing embedding generator fails the write rather than silently storing
// unembedded units.
func TestRetainWritePropagatesEmbeddingFailure(t *testing.T) {
	store := NewMemoryStore("public")
	_, err := store.RetainWrite(context.Background(), RetainRequest{
		BankID:     "b1",
		Items:      []RetainItem{{Content: "the sky is blue"}},
		Embeddings: &stubEmbedder{err: errors.New("provider down")},
	})
	if err == nil {
		t.Fatal("a failing embedder must fail the write")
	}
}

// A unit longer than the embedding input bound is truncated on the wire rather
// than rejected by the provider, which would fail the whole write. The stored
// unit keeps its full text.
func TestEmbedContentsTruncatesLongInput(t *testing.T) {
	embedder := &stubEmbedder{dim: 4}
	long := strings.Repeat("x", MaxEmbeddingInputChars+100)
	contents := []itemContent{{unit: MemoryUnit{ID: "u1", Text: long}}}
	if err := embedContents(context.Background(), embedder, contents); err != nil {
		t.Fatalf("embed: %v", err)
	}
	if len(embedder.sent) != 1 || len(embedder.sent[0]) != 1 {
		t.Fatalf("provider batch = %v, want one input", embedder.sent)
	}
	if got := len(embedder.sent[0][0]); got != MaxEmbeddingInputChars {
		t.Fatalf("provider input length = %d, want %d", got, MaxEmbeddingInputChars)
	}
	if len(contents[0].unit.Embedding) != 4 {
		t.Fatalf("a truncated input must still embed: %+v", contents[0].unit.Embedding)
	}
	if contents[0].unit.Text != long {
		t.Fatal("the stored text must not be truncated")
	}
}

// A long unit survives a retain write: the provider sees a bounded input and
// the unit is still embedded with its whole text.
func TestRetainWriteTruncatesLongEmbeddingInput(t *testing.T) {
	store := NewMemoryStore("public")
	embedder := &stubEmbedder{dim: 4}
	long := strings.Repeat("y", MaxEmbeddingInputChars+250)
	extractor := &stubExtraction{facts: map[string][]ExtractedFact{
		"content": {{Text: long}},
	}}
	if _, err := store.RetainWrite(context.Background(), RetainRequest{
		BankID:     "b1",
		Items:      []RetainItem{{Content: "content"}},
		Extraction: extractor,
		Embeddings: embedder,
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(embedder.sent) == 0 {
		t.Fatal("the write must reach the provider")
	}
	for _, batch := range embedder.sent {
		for _, text := range batch {
			if len(text) > MaxEmbeddingInputChars {
				t.Fatalf("provider input length = %d, want <= %d", len(text), MaxEmbeddingInputChars)
			}
		}
	}
	embedded := false
	for _, unit := range store.UnitsForTest("b1") {
		if unit.Text == long && len(unit.Embedding) == 4 {
			embedded = true
		}
	}
	if !embedded {
		t.Fatal("the long unit must still be embedded")
	}
}

// A replace-mode re-retain against the same document id rebuilds the document's
// units instead of accumulating duplicates.
func TestRetainReplaceModeRebuildsUnits(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	write := func(content string) {
		t.Helper()
		if _, err := store.RetainWrite(ctx, RetainRequest{
			BankID: "b1",
			Items:  []RetainItem{{Content: content, DocumentID: "doc-1", UpdateMode: "replace"}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	write("first version of the notes")
	first := len(store.UnitsForTest("b1"))
	write("second version of the notes")
	second := len(store.UnitsForTest("b1"))
	if first == 0 || second != first {
		t.Fatalf("replace must rebuild, not accumulate: first=%d second=%d", first, second)
	}
}

// The chunk id mapping is injective, so two different triples never collide.
func TestBuildChunkIDInjective(t *testing.T) {
	if BuildChunkID("a", "b_c", 0) == BuildChunkID("a_b", "c", 0) {
		t.Fatalf("distinct triples must not collide")
	}
	if got := BuildChunkID("bank", "doc", 3); got != "bank_doc_3" {
		t.Fatalf("unseparated ids keep the plain form, got %q", got)
	}
	if got := BuildChunkID("a", "b_c", 0); got != "a_b~5Fc_0" {
		t.Fatalf("separator must be escaped, got %q", got)
	}
}

// Two embedded units with similar vectors are linked semantically, distinct
// from the temporal adjacency links.
func TestRetainCreatesSemanticLinks(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.RetainWrite(ctx, RetainRequest{
		BankID:     "b1",
		Items:      []RetainItem{{Content: "alpha"}},
		Embeddings: &stubEmbedder{dim: 4},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetainWrite(ctx, RetainRequest{
		BankID:     "b1",
		Items:      []RetainItem{{Content: "beta"}},
		Embeddings: &stubEmbedder{dim: 4},
	}); err != nil {
		t.Fatal(err)
	}
	semantic := 0
	for _, link := range store.links {
		if link.LinkType == LinkSemantic {
			semantic++
		}
	}
	if semantic == 0 {
		t.Fatal("similar embedded units must be linked semantically")
	}
}

// A reranker batch already in [0, 1] is passed through; a logits batch is
// squashed rather than stretched to its own range.
func TestNormalizeRerankScoreModes(t *testing.T) {
	if got := normalizeRerankScore(0.007, []float64{0.007, 0.9}); got != 0.007 {
		t.Fatalf("a calibrated [0,1] score must pass through, got %v", got)
	}
	if got := normalizeRerankScore(3, []float64{-2, 3}); got <= 0.9 {
		t.Fatalf("a logit of 3 must squash high, got %v", got)
	}
	if got := normalizeRerankScore(-3, []float64{-3, 2}); got >= 0.1 {
		t.Fatalf("a logit of -3 must squash low, got %v", got)
	}
}

// An experience fact is stored as an experience unit and carries its where and
// kind back in metadata.
func TestRetainStoresFactTypeAndMetadata(t *testing.T) {
	store := NewMemoryStore("public")
	extractor := &stubExtraction{facts: map[string][]ExtractedFact{
		"Alice moved to Berlin.": {{Text: "Alice moved to Berlin.", Where: "Berlin", Kind: "event", Type: "experience"}},
	}}
	if _, err := store.RetainWrite(context.Background(), RetainRequest{
		BankID:     "b1",
		Items:      []RetainItem{{Content: "Alice moved to Berlin."}},
		Extraction: extractor,
	}); err != nil {
		t.Fatal(err)
	}
	units := store.UnitsForTest("b1")
	if len(units) != 1 {
		t.Fatalf("stored %d units", len(units))
	}
	if units[0].FactType != TypeExperience {
		t.Fatalf("fact type = %q, want experience", units[0].FactType)
	}
	// The fact's own dimensions (where, kind) drive its type and rendered text
	// but are not merged into the document metadata: the metadata column carries
	// what the caller stamped on the write, and only that.
	if units[0].Metadata["where"] != nil || units[0].Metadata["fact_kind"] != nil {
		t.Fatalf("metadata = %v, want no fact dimensions merged in", units[0].Metadata)
	}
}

// Fuzzy tag matching uses namespace-stripped trigram similarity within a
// namespace, not substring containment and not the whole namespaced tag.
func TestSimilarTagTrigram(t *testing.T) {
	// A near-miss value in the same namespace matches on its stripped value.
	if !similarTag("user:alexandria", "user:alexandrio") {
		t.Fatal("a near-miss value in the same namespace must match")
	}
	if similarTag("user:alice", "run:alice") {
		t.Fatal("differing namespaces must not match")
	}
	// A token with no namespace only matches other namespaceless tags: the
	// containment shortcut used to let it reach into a namespace.
	if similarTag("alice", "user:alice") {
		t.Fatal("a namespaceless token must not match a namespaced tag")
	}
	// Similarity is measured on the value, not the whole tag: scored whole,
	// "name:the" and "name:ts" share the namespace prefix and clear the floor.
	if !similarTag("name:the", "name:thee") {
		t.Fatal("a longer near-miss value in the same namespace must match")
	}
	if similarTag("name:the", "name:ts") {
		t.Fatal("the shared namespace must not carry an unrelated short value")
	}
	if similarTag("user:alice", "user:zzzz") {
		t.Fatal("unrelated values must not match")
	}
}

// An unchanged document is not re-extracted: a second retain of identical
// content returns the units already on record.
func TestRetainSkipsUnchangedDocument(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	first, err := store.RetainWrite(ctx, RetainRequest{
		BankID: "b1",
		Items:  []RetainItem{{Content: "stable content", DocumentID: "doc-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.RetainWrite(ctx, RetainRequest{
		BankID: "b1",
		Items:  []RetainItem{{Content: "stable content", DocumentID: "doc-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.ItemUnitIDs[0]) != len(first.ItemUnitIDs[0]) {
		t.Fatalf("delta-skipped retain returned %d units, want %d", len(second.ItemUnitIDs[0]), len(first.ItemUnitIDs[0]))
	}
	if len(store.UnitsForTest("b1")) != len(first.ItemUnitIDs[0]) {
		t.Fatal("an unchanged document must not add units")
	}
}

// A bank's config disables the arms it turned off and leaves the rest on.
func TestBankRecallDisabledArms(t *testing.T) {
	bank := Bank{Config: map[string]any{"enable_text_search": false, "enable_graph_retrieval": false}}
	arms := bank.RecallDisabledArms()
	if len(arms) != 2 {
		t.Fatalf("disabled arms = %v, want keyword and graph", arms)
	}
	off := map[string]bool{}
	for _, arm := range arms {
		off[arm] = true
	}
	if !off[ArmKeyword] || !off[ArmGraph] || off[ArmTemporal] {
		t.Fatalf("disabled arms = %v", arms)
	}
	if len((Bank{}).RecallDisabledArms()) != 0 {
		t.Fatal("a bank with no config must not disable any arm")
	}
	// Turning reranking off travels in the same list, as the cross-encoder pass
	// rather than a retrieval arm.
	noRerank := Bank{Config: map[string]any{"enable_reranking": false}}.RecallDisabledArms()
	if len(noRerank) != 1 || noRerank[0] != ArmReranker {
		t.Fatalf("disabled passes = %v, want just %q", noRerank, ArmReranker)
	}
}

// Entity pruning removes an entity no unit mentions and keeps a mentioned one.
func TestPruneEntity(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	store.entities["e1"] = Entity{ID: "e1", BankID: "b1", CanonicalName: "alice"}
	store.entities["e2"] = Entity{ID: "e2", BankID: "b1", CanonicalName: "bob"}
	store.unitEntities["u1"] = []string{"e2"}
	if err := store.PruneEntity(ctx, "b1", "e1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.entities["e1"]; ok {
		t.Fatal("an unreferenced entity must be pruned")
	}
	if _, ok := store.entities["e2"]; !ok {
		t.Fatal("a referenced entity must survive")
	}
}

// The in-process store has no relink/prune queues, so draining is a no-op.
func TestMemoryMaintenanceQueuesAreEmpty(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	graph, err := store.DrainGraphMaintenance(ctx, 10)
	if err != nil || len(graph) != 0 {
		t.Fatalf("graph drain = %v (err %v)", graph, err)
	}
	entities, err := store.DrainEntityMaintenance(ctx, 10)
	if err != nil || len(entities) != 0 {
		t.Fatalf("entity drain = %v (err %v)", entities, err)
	}
}

// The indexed text is the reference's augmentation, field for field. Each case
// here is a way the augmentation is a *retrieval* signal rather than cosmetic:
// the date a fact names has to be the date embedded, a span has to be reachable
// from either end, and a fact with no date must not claim one.
func TestIndexedEmbeddingTextMatchesTheReferenceAugmentation(t *testing.T) {
	jan := time.Date(2021, 1, 4, 0, 0, 0, 0, time.UTC)
	mar := time.Date(2021, 3, 9, 0, 0, 0, 0, time.UTC)
	entities := []RetainEntity{{Text: "Alice"}, {Text: "Berlin"}}

	cases := []struct {
		name string
		unit MemoryUnit
		want string
	}{
		{
			name: "a point in time names the month it happened",
			unit: MemoryUnit{Text: "Alice moved to Berlin", OccurredStart: &jan},
			want: "Alice moved to Berlin (happened in January 2021)",
		},
		{
			name: "a span reads as a range",
			unit: MemoryUnit{Text: "Alice lived in Berlin", OccurredStart: &jan, OccurredEnd: &mar},
			want: "Alice lived in Berlin (happened from January 2021 to March 2021)",
		},
		{
			name: "a span whose end equals its start is a point",
			unit: MemoryUnit{Text: "Alice visited", OccurredStart: &jan, OccurredEnd: &jan},
			want: "Alice visited (happened in January 2021)",
		},
		{
			name: "no occurred start falls back to the mention time, never the clock",
			unit: MemoryUnit{Text: "Alice moved", MentionedAt: &mar},
			want: "Alice moved (happened in March 2021)",
		},
		{
			name: "no date at all leaves the text unaugmented",
			unit: MemoryUnit{Text: "Alice likes cellos"},
			want: "Alice likes cellos",
		},
		{
			name: "an end without a start still reads as a range",
			unit: MemoryUnit{Text: "Alice stayed", MentionedAt: &jan, OccurredEnd: &mar},
			want: "Alice stayed (happened from January 2021 to March 2021)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := indexedEmbeddingText(tc.unit, nil); got != tc.want {
				t.Fatalf("indexedEmbeddingText = %q, want %q", got, tc.want)
			}
		})
	}

	// Entity names are a bracketed, comma-joined field: they are how a label like
	// "kind:rule" reaches the index without being read as part of the sentence.
	got := indexedEmbeddingText(MemoryUnit{Text: "Alice moved to Berlin", OccurredStart: &jan}, entities)
	want := "Alice moved to Berlin (happened in January 2021) [Alice, Berlin]"
	if got != want {
		t.Fatalf("indexedEmbeddingText with entities = %q, want %q", got, want)
	}
}
