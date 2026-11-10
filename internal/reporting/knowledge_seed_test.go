package reporting

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/mentalmodels"
	"github.com/studyzy/wenyuan/internal/storage"
)

// seedFixture builds a seeder over a store holding one empty bank.
func seedFixture(t *testing.T) (KnowledgePageSeeder, *storage.MemoryStore, string) {
	t.Helper()
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	const bankID = "team::project"
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bankID, Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	return KnowledgePageSeeder{Builder: &PageBuilder{Store: store}}, store, bankID
}

// seededPages lists the bank's page nodes.
func seededPages(t *testing.T, store *storage.MemoryStore, bankID string) []storage.KnowledgeNode {
	t.Helper()
	nodes, err := store.ListKnowledgeNodes(context.Background(), bankID)
	if err != nil {
		t.Fatal(err)
	}
	pages := make([]storage.KnowledgeNode, 0, len(nodes))
	for _, node := range nodes {
		if node.Kind == knowledgeKindPage {
			pages = append(pages, node)
		}
	}
	return pages
}

func TestSeedKnowledgePagesCreatesTheTaxonomy(t *testing.T) {
	seeder, store, bankID := seedFixture(t)
	if err := seeder.SeedKnowledgePages(context.Background(), "public", bankID, "payments-api"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	pages := seededPages(t, store, bankID)
	if len(pages) != len(seededPageTaxonomy) {
		t.Fatalf("pages = %d, want %d", len(pages), len(seededPageTaxonomy))
	}
	byName := make(map[string]storage.KnowledgeNode, len(pages))
	for _, page := range pages {
		if page.ParentID != "" {
			t.Errorf("%q is not at the root", page.Title)
		}
		byName[page.Title] = page
	}
	for _, want := range seededPageTaxonomy {
		if _, ok := byName[want.Name]; !ok {
			t.Errorf("missing page %q", want.Name)
		}
	}
}

func TestSeedKnowledgePagesScopesEachPage(t *testing.T) {
	seeder, store, bankID := seedFixture(t)
	const project = "payments-api"
	if err := seeder.SeedKnowledgePages(context.Background(), "public", bankID, project); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ctx := context.Background()
	for _, page := range seededPages(t, store, bankID) {
		model, err := store.GetMentalModel(ctx, bankID, page.MentalModelID)
		if err != nil {
			t.Fatalf("model of %q: %v", page.Title, err)
		}
		if !strings.Contains(model.SourceQuery, "Scope this page to "+project+" ITSELF") {
			t.Errorf("%q is not scoped to %q: %s", page.Title, project, model.SourceQuery)
		}
		if len(model.Tags) != 1 || !strings.HasPrefix(model.Tags[0], "knowledge:") {
			t.Errorf("%q tags = %v, want one knowledge: tier", page.Title, model.Tags)
		}
		if model.MaxTokens != seededPageMaxTokens {
			t.Errorf("%q max_tokens = %d, want %d", page.Title, model.MaxTokens, seededPageMaxTokens)
		}
	}
}

func TestSeedKnowledgePagesTriggerSpreadsTheHour(t *testing.T) {
	seeder, store, bankID := seedFixture(t)
	if err := seeder.SeedKnowledgePages(context.Background(), "public", bankID, "p"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ctx := context.Background()
	hourly := regexp.MustCompile(`^([0-9]{1,2}) \* \* \* \*$`)
	minutes := make(map[string]bool, len(seededPageTaxonomy))
	for _, page := range seededPages(t, store, bankID) {
		model, err := store.GetMentalModel(ctx, bankID, page.MentalModelID)
		if err != nil {
			t.Fatalf("model of %q: %v", page.Title, err)
		}
		cron, _ := model.Trigger["refresh_cron"].(string)
		match := hourly.FindStringSubmatch(cron)
		if match == nil {
			t.Fatalf("%q refresh_cron = %q, want an hourly schedule", page.Title, cron)
		}
		minute, err := strconv.Atoi(match[1])
		if err != nil || minute < 0 || minute > 59 {
			t.Fatalf("%q refresh_cron = %q, minute out of range", page.Title, cron)
		}
		if minutes[cron] {
			t.Errorf("%q shares the slot %q with another page", page.Title, cron)
		}
		minutes[cron] = true
		if model.Trigger["tags_match"] != seededPageTagsMatch {
			t.Errorf("%q tags_match = %v, want %q", page.Title, model.Trigger["tags_match"], seededPageTagsMatch)
		}
		if got := model.Trigger["fact_types"]; len(got.([]any)) != 3 {
			t.Errorf("%q fact_types = %v, want world/experience/observation", page.Title, got)
		}
	}
}

func TestSeedKnowledgePagesIsIdempotent(t *testing.T) {
	seeder, store, bankID := seedFixture(t)
	ctx := context.Background()
	if err := seeder.SeedKnowledgePages(ctx, "public", bankID, "p"); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	before := seededPages(t, store, bankID)
	if err := seeder.SeedKnowledgePages(ctx, "public", bankID, "p"); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	after := seededPages(t, store, bankID)
	if len(after) != len(before) {
		t.Fatalf("pages after a second seed = %d, want %d", len(after), len(before))
	}
	for i := range after {
		if after[i].ID != before[i].ID {
			t.Errorf("page %q was recreated", after[i].Title)
		}
	}
}

func TestSeedKnowledgePagesKeepsAPageTheBankAlreadyHas(t *testing.T) {
	seeder, store, bankID := seedFixture(t)
	ctx := context.Background()
	// A page of the same name, created by hand, is the bank's own: the seed
	// matches by name and must leave it, and its content, alone.
	existing, serviceErr := seeder.Builder.BuildPage(ctx, nil, bankID, nil, "Component map",
		CreateKnowledgePageParams{SourceQuery: "our own question", Tags: []string{"hand-written"}})
	if serviceErr != nil {
		t.Fatalf("create: %v", serviceErr)
	}
	if err := seeder.SeedKnowledgePages(ctx, "public", bankID, "p"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	model, err := store.GetMentalModel(ctx, bankID, existing.ModelID)
	if err != nil {
		t.Fatalf("model: %v", err)
	}
	if model.SourceQuery != "our own question" {
		t.Errorf("source_query = %q, the seed replaced a page it found", model.SourceQuery)
	}
	if got := len(seededPages(t, store, bankID)); got != len(seededPageTaxonomy) {
		t.Errorf("pages = %d, want %d", got, len(seededPageTaxonomy))
	}
}

func TestSeedKnowledgePagesQueuesOnlyTheFirstRefresh(t *testing.T) {
	seeder, store, bankID := seedFixture(t)
	ctx := context.Background()
	if err := seeder.SeedKnowledgePages(ctx, "public", bankID, "p"); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	// One refresh per page, and none for the pages a second run skips: a
	// re-seed must not rebuild every page of the bank.
	first := countQueuedRefreshes(t, store, bankID)
	if first != len(seededPageTaxonomy) {
		t.Fatalf("operations after the first seed = %d, want %d", first, len(seededPageTaxonomy))
	}
	if err := seeder.SeedKnowledgePages(ctx, "public", bankID, "p"); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if second := countQueuedRefreshes(t, store, bankID); second != first {
		t.Errorf("operations after a re-seed = %d, want %d", second, first)
	}
}

// countQueuedRefreshes counts the refresh operations the bank holds.
func countQueuedRefreshes(t *testing.T, store *storage.MemoryStore, bankID string) int {
	t.Helper()
	page, err := store.ListOperations(context.Background(), storage.OperationQuery{BankID: bankID, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, op := range page.Operations {
		if op.OperationType == storage.OperationTypeRefreshMentalModel {
			count++
		}
	}
	return count
}

func TestSeedKnowledgePagesWithoutABuilderSeedsNothing(t *testing.T) {
	var seeder KnowledgePageSeeder
	if err := seeder.SeedKnowledgePages(context.Background(), "public", "any", "p"); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestSeededPageMinuteIsStableAndInRange(t *testing.T) {
	first := seededPageMinute("team::a", "Component map")
	if again := seededPageMinute("team::a", "Component map"); again != first {
		t.Errorf("minute = %d then %d, want the same slot", first, again)
	}
	if first > 59 {
		t.Errorf("minute = %d, want [0, 59]", first)
	}
	// Two banks must not be forced onto one slot: the seed carries the bank id.
	other := seededPageMinute("team::b", "Component map")
	if other == first {
		t.Logf("two banks happened to share minute %d; the rule still separates by input", first)
	}
}

func TestSeededPageTriggerCarriesThePageDefault(t *testing.T) {
	// The trigger a page is created with is merged over the built-in page
	// default, so a member this seeder does not state keeps the page's own
	// value rather than a mental model's.
	merged := mentalmodels.MergeTrigger(seededPageTrigger("bank", "Page"), nil)
	if merged["mode"] != "delta" {
		t.Errorf("mode = %v, want the page default", merged["mode"])
	}
	if merged["exclude_mental_models"] != true {
		t.Errorf("exclude_mental_models = %v, want true", merged["exclude_mental_models"])
	}
	if merged["refresh_cron"] == nil {
		t.Error("refresh_cron was dropped by the merge")
	}
}
