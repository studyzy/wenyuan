package storage

import (
	"strings"
	"testing"
)

// The index name is deterministic, carries the reference's shape, fits
// PostgreSQL's identifier limit, and stays distinct for two banks whose ids
// differ only past the truncation point. Deriving it by truncating the bank id
// instead of digesting it is what produced `invalid byte sequence` when the cut
// fell inside a multi-byte character, and what collided two such banks.
func TestBankVectorIndexName(t *testing.T) {
	id := "1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d"
	name := BankVectorIndexName("world", id)
	if !strings.HasPrefix(name, "idx_mu_emb_worl_") {
		t.Fatalf("name = %q, want the reference's prefix", name)
	}
	if name != BankVectorIndexName("world", id) {
		t.Fatal("the name must be deterministic")
	}
	if got := BankVectorIndexName("observation", "b1"); !strings.HasPrefix(got, "idx_mu_emb_obsv_") {
		t.Fatalf("short id name = %q", got)
	}
	if len(BankVectorIndexName("experience", strings.Repeat("a", 64))) > 63 {
		t.Fatal("name must fit PostgreSQL's identifier limit")
	}
	// Two banks sharing a long common prefix must not share an index name, and
	// a name with a multi-byte character must be usable at all.
	base := strings.Repeat("b", 40)
	if BankVectorIndexName("world", base+"-one") == BankVectorIndexName("world", base+"-two") {
		t.Fatal("distinct banks must not collide on one index name")
	}
	if got := BankVectorIndexName("world", base+"-日本語"); got == "" || len(got) > 63 {
		t.Fatalf("a multi-byte bank id must yield a valid name, got %q", got)
	}
}

// With the threshold at zero nothing is planned, which is the reference's
// default and its bank-lifecycle behaviour.
func TestPlanBankVectorIndexesDisabled(t *testing.T) {
	states := []BankVectorIndexState{{FactType: "world", Rows: 1000, IndexName: "idx_mu_emb_worl_b1"}}
	if plan := PlanBankVectorIndexes(states, 0); plan != nil {
		t.Fatalf("plan = %v, want none", plan)
	}
}

// An index that exists but is unusable is treated as absent for coverage, so a
// partition that still qualifies is rebuilt — and the invalid leftover is
// dropped first, because CREATE ... IF NOT EXISTS would otherwise accept it and
// leave the partition uncovered forever.
func TestPlanBankVectorIndexesTreatsInvalidAsAbsent(t *testing.T) {
	states := []BankVectorIndexState{
		{FactType: "world", Rows: 500, IndexName: "idx_worl", Invalid: true},
	}
	plan := PlanBankVectorIndexes(states, 10)
	if len(plan) != 2 {
		t.Fatalf("plan = %+v, want a drop and a rebuild", plan)
	}
	if plan[0].Create || plan[0].IndexName != "idx_worl" {
		t.Fatalf("first action = %+v, want the drop of the invalid index", plan[0])
	}
	if !plan[1].Create || plan[1].IndexName != "idx_worl" {
		t.Fatalf("second action = %+v, want the rebuild", plan[1])
	}
}

// An invalid index over a partition that no longer qualifies is only dropped:
// there is nothing to rebuild.
func TestPlanBankVectorIndexesDropsInvalidBelowThreshold(t *testing.T) {
	states := []BankVectorIndexState{
		{FactType: "observation", Rows: 3, IndexName: "idx_obsv", Invalid: true},
	}
	plan := PlanBankVectorIndexes(states, 10)
	if len(plan) != 1 || plan[0].Create {
		t.Fatalf("plan = %+v, want a single drop", plan)
	}
}

// A partition above the threshold earns an index, one below it loses one, and a
// healthy partition is left alone.
func TestPlanBankVectorIndexesCoverage(t *testing.T) {
	states := []BankVectorIndexState{
		{FactType: "world", Rows: 10, IndexName: "idx_worl", Present: false},
		{FactType: "experience", Rows: 1, IndexName: "idx_expr", Present: true},
		{FactType: "observation", Rows: 500, IndexName: "idx_obsv", Present: true},
	}
	plan := PlanBankVectorIndexes(states, 10)
	if len(plan) != 2 {
		t.Fatalf("plan = %+v, want two actions", plan)
	}
	if plan[0].Create || plan[0].FactType != "experience" {
		t.Fatalf("first action = %+v, want the drop", plan[0])
	}
	if !plan[1].Create || plan[1].FactType != "world" {
		t.Fatalf("second action = %+v, want the create", plan[1])
	}
}

// The DDL is CONCURRENTLY, partial on the bank and fact type, and matches the
// configured access method.
func TestBankVectorIndexDDL(t *testing.T) {
	create := BankVectorIndexDDL("public", "pgvector", "b1", BankVectorIndexAction{
		FactType: "world", IndexName: "idx_mu_emb_worl_b1", Create: true,
	})
	for _, want := range []string{
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS",
		"ON \"public\".memory_units USING hnsw (embedding vector_cosine_ops)",
		"WHERE fact_type = 'world' AND bank_id = 'b1'",
	} {
		if !strings.Contains(create, want) {
			t.Fatalf("create DDL missing %q: %s", want, create)
		}
	}
	// The operator class belongs to the USING clause alone: repeating it would
	// emit "USING hnsw (...) (...)", which PostgreSQL rejects.
	if want := `ON "public".memory_units USING hnsw (embedding vector_cosine_ops) WHERE`; !strings.Contains(create, want) {
		t.Fatalf("create DDL = %s, want the access method and operator class exactly once", create)
	}
	if strings.Count(create, "vector_cosine_ops") != 1 {
		t.Fatalf("create DDL repeats the operator class: %s", create)
	}
	drop := BankVectorIndexDDL("public", "pgvector", "b1", BankVectorIndexAction{
		FactType: "world", IndexName: "idx_mu_emb_worl_b1", Create: false,
	})
	if drop != `DROP INDEX CONCURRENTLY IF EXISTS "public"."idx_mu_emb_worl_b1"` {
		t.Fatalf("drop DDL = %s", drop)
	}
	vchord := BankVectorIndexDDL("s", "vchord", "b1", BankVectorIndexAction{
		FactType: "world", IndexName: "i", Create: true,
	})
	if !strings.Contains(vchord, "USING vchordrq (embedding vector_cosine_ops)") {
		t.Fatalf("vchord DDL = %s", vchord)
	}
}

// A bank identifier containing a quote cannot escape the predicate.
func TestBankVectorIndexDDLQuotesBankID(t *testing.T) {
	ddl := BankVectorIndexDDL("public", "pgvector", "b'1", BankVectorIndexAction{
		FactType: "world", IndexName: "i", Create: true,
	})
	if !strings.Contains(ddl, "bank_id = 'b''1'") {
		t.Fatalf("bank id not escaped: %s", ddl)
	}
}
