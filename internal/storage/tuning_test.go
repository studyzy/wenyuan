package storage

import (
	"context"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
)

// The store's defaults and the configuration table's defaults describe the same
// reference deployment. They are written in two places because storage cannot
// import the configuration surface without inverting the dependency, so this
// test holds them together: a drift here would make a store built without
// tuning behave differently from one the composition root configured.
func TestTuningDefaultsMatchTheConfigurationDefaults(t *testing.T) {
	cfg := config.FromEnv(func(string) (string, bool) { return "", false })
	tuning := DefaultTuning()

	pairs := []struct {
		name     string
		store    any
		expected any
	}{
		{"graph seed floor", tuning.GraphSeedMinSimilarity, cfg.RecallTuning.GraphSeedMinSimilarity},
		{"semantic link floor", tuning.SemanticLinkMinSimilarity, cfg.RecallTuning.SemanticLinkMinSimilarity},
		{"bm25 term cap", tuning.BM25MaxQueryTerms, cfg.RecallTuning.BM25MaxQueryTerms},
		{"budget low", tuning.RecallBudgetLow, cfg.ProcessDefaults.RecallBudgetFixedLow},
		{"budget mid", tuning.RecallBudgetMid, cfg.ProcessDefaults.RecallBudgetFixedMid},
		{"budget high", tuning.RecallBudgetHigh, cfg.ProcessDefaults.RecallBudgetFixedHigh},
		{"reflect iterations", tuning.ReflectMaxIterations, cfg.ReflectTuning.MaxIterations},
		{"reflect context budget", tuning.ReflectContextBudget, cfg.ReflectTuning.MaxContextTokens},
	}
	for _, pair := range pairs {
		if pair.store != pair.expected {
			t.Errorf("%s: store default %v, configuration default %v", pair.name, pair.store, pair.expected)
		}
	}
}

// The two history switches default on, and their bounds match the configuration
// defaults, so a store built without tuning records the same history the
// reference does.
func TestHistoryTuningDefaults(t *testing.T) {
	cfg := config.FromEnv(func(string) (string, bool) { return "", false })
	tuning := DefaultTuning()
	if !tuning.ObservationHistory || !tuning.MentalModelHistory {
		t.Fatalf("history defaults = observation %v, mental model %v; want both on",
			tuning.ObservationHistory, tuning.MentalModelHistory)
	}
	if tuning.ObservationHistoryMax != cfg.ConsolidationTuning.HistoryMaxEntries {
		t.Errorf("observation history bound = %d, configuration default %d",
			tuning.ObservationHistoryMax, cfg.ConsolidationTuning.HistoryMaxEntries)
	}
	if tuning.MentalModelHistoryMax != cfg.ConsolidationTuning.MentalModelHistoryEntries {
		t.Errorf("mental-model history bound = %d, configuration default %d",
			tuning.MentalModelHistoryMax, cfg.ConsolidationTuning.MentalModelHistoryEntries)
	}
}

// With the switch off the record is not written, which is what the reference's
// disable does: the read route answers with no entries.
func TestMentalModelHistorySwitchOffSkipsTheRecord(t *testing.T) {
	store := NewMemoryStore("public")
	tuning := DefaultTuning()
	tuning.MentalModelHistory = false
	store.SetTuning(tuning)
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, Bank{BankID: "b1", Name: "One"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutMentalModel(ctx, MentalModel{BankID: "b1", ID: "model-1", Subtype: SubtypePinned, Name: "model-1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordMentalModelHistory(ctx, "b1", "model-1", MentalModelHistoryEntry{ChangedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	entries, err := store.MentalModelHistory(ctx, "b1", "model-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("history recorded while switched off: %+v", entries)
	}
}

// A query with no floor of its own runs under the deployment's per-arm floors,
// and a caller's own floor is never overridden.
func TestDefaultFloorsApplyOnlyWhereTheCallerSetNone(t *testing.T) {
	tuning := DefaultTuning()
	tuning.SemanticMinSimilarity = 0.5
	tuning.TemporalSemanticMinSimilarity = 0.2
	tuning.BM25MinScore = 0.1

	seeded := tuning.withDefaultFloors(RecallQuery{})
	for arm, want := range map[string]float64{ArmSemantic: 0.5, ArmTemporal: 0.2, ArmKeyword: 0.1} {
		if seeded.MinScores[arm] != want {
			t.Errorf("%s floor = %v, want the deployment default %v", arm, seeded.MinScores[arm], want)
		}
	}

	explicit := tuning.withDefaultFloors(RecallQuery{MinScores: map[string]float64{ArmSemantic: 0.9}})
	if explicit.MinScores[ArmSemantic] != 0.9 {
		t.Errorf("the caller's semantic floor was overridden: %v", explicit.MinScores[ArmSemantic])
	}
	if explicit.MinScores[ArmTemporal] != 0.2 {
		t.Errorf("the unfloored temporal arm did not take the default: %v", explicit.MinScores[ArmTemporal])
	}
}

// A zero deployment floor leaves the arm unfloored, which is what the
// reference's zero means.
func TestZeroDefaultFloorLeavesTheArmUnfloored(t *testing.T) {
	tuning := DefaultTuning()
	tuning.BM25MinScore = 0
	seeded := tuning.withDefaultFloors(RecallQuery{})
	if _, set := seeded.MinScores[ArmKeyword]; set {
		t.Fatalf("a zero floor was recorded as %v", seeded.MinScores[ArmKeyword])
	}
}

// Seeding a floor must not write into the caller's map: the query a caller
// built is theirs.
func TestDefaultFloorSeedingCopiesTheCallersMap(t *testing.T) {
	tuning := DefaultTuning()
	caller := map[string]float64{ArmSemantic: 0.9}
	seeded := tuning.withDefaultFloors(RecallQuery{MinScores: caller})
	if len(caller) != 1 {
		t.Fatalf("the caller's map grew: %v", caller)
	}
	if seeded.MinScores[ArmTemporal] != tuning.TemporalSemanticMinSimilarity {
		t.Fatalf("the seeded query lost its default: %v", seeded.MinScores)
	}
}

// The graph arm's per-entity cap and timeout are the deployment's: a store
// built without tuning keeps the reference's defaults.
func TestLinkExpansionTuningDefaults(t *testing.T) {
	cfg := config.FromEnv(func(string) (string, bool) { return "", false })
	tuning := DefaultTuning()
	if tuning.LinkExpansionPerEntityLimit != cfg.RecallTuning.LinkExpansionPerEntityLimit {
		t.Errorf("per-entity limit = %d, configuration default %d",
			tuning.LinkExpansionPerEntityLimit, cfg.RecallTuning.LinkExpansionPerEntityLimit)
	}
	if tuning.LinkExpansionTimeout != cfg.RecallTuning.LinkExpansionTimeout {
		t.Errorf("expansion timeout = %s, configuration default %s",
			tuning.LinkExpansionTimeout, cfg.RecallTuning.LinkExpansionTimeout)
	}
	if tuning.LinkExpansionTimeout <= 0 {
		t.Fatal("the graph arm must be bounded by default")
	}
}

// The semantic arm applies the deployment's ANN tuning, pgvector's own
// settings, and nothing at all on another vector backend.
func TestAnnSettingsFollowTheBackendAndTuning(t *testing.T) {
	store := NewPostgresStore(nil, "public")
	tuning := DefaultTuning()
	tuning.ANNIterativeScan = false
	tuning.ANNMaxScanTuples = 1234
	store.SetTuning(tuning)

	settings := store.annSettings()
	byName := map[string]string{}
	for _, setting := range settings {
		byName[setting.name] = setting.value
	}
	if byName["hnsw.iterative_scan"] != "off" {
		t.Errorf("iterative_scan = %q, want off when the switch is off", byName["hnsw.iterative_scan"])
	}
	if byName["hnsw.max_scan_tuples"] != "1234" {
		t.Errorf("max_scan_tuples = %q, want the configured 1234", byName["hnsw.max_scan_tuples"])
	}
	if byName["hnsw.ef_search"] != "200" {
		t.Errorf("ef_search = %q, want the recall profile's 200", byName["hnsw.ef_search"])
	}

	// The default is on, and the scan tuple ceiling is the reference's.
	def := NewPostgresStore(nil, "public")
	for _, setting := range def.annSettings() {
		if setting.name == "hnsw.iterative_scan" && setting.value != "strict_order" {
			t.Errorf("default iterative_scan = %q, want strict_order", setting.value)
		}
	}

	other := NewPostgresStore(nil, "public")
	other.vectorExtension = "vchord"
	if got := other.annSettings(); len(got) != 0 {
		t.Fatalf("a non-pgvector deployment got pgvector settings: %+v", got)
	}
}
