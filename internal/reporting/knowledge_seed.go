package reporting

// The five knowledge pages a project's bank starts with.
//
// A project bank is created by the administration plane and filled by the
// project merge job; neither ever runs the coding-agents package that seeds
// these pages into the banks its own sessions own. So the pages are seeded here,
// from a copy of that package's taxonomy — a team bank must get its pages
// without a coding-agent session having run.
//
// The copy is verbatim and the questions are the whole point of each page: if
// the taxonomy moves, both copies move together.
// (integrations/coding-agents/src/core/missions/pages.ts, PAGE_TAXONOMY.)

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"

	"github.com/studyzy/wenyuan/internal/storage"
)

// seededPage is one page of the taxonomy: its name, the question it answers and
// the knowledge tier that scopes it.
type seededPage struct {
	Name        string
	SourceQuery string
	Tags        []string
}

// seededPageMaxTokens is the content budget every seeded page gets: a document
// rather than an answer, which is why it is larger than a plain mental model's.
const seededPageMaxTokens = 4096

// seededPageTaxonomy is the coding-agents taxonomy, verbatim and before
// scoping. The tier tags are the labels the members' retains project, so a page
// draws only on the facts routed to its own tier.
var seededPageTaxonomy = []seededPage{
	{
		Name: "Component map",
		SourceQuery: "From this project's commit history and past discussions, what are the main " +
			"components/modules/subsystems, what is each responsible for, and how do they relate to or " +
			"depend on one another? Describe the structure and responsibilities.",
		Tags: []string{"knowledge:component"},
	},
	{
		Name: "Core concepts",
		SourceQuery: "What are the core concepts, domain abstractions, and key entities in this project — " +
			"the vocabulary a developer must understand? For each, explain what it represents and its role, " +
			"drawn from how they are introduced and discussed across the history and conversations.",
		Tags: []string{"knowledge:concept"},
	},
	{
		Name: "Conventions and patterns",
		SourceQuery: "What conventions, idioms, and recurring patterns does this project follow — its " +
			"approach to testing, error handling, naming, structure, and how changes are typically made? " +
			"Describe how THIS project does things, as evidenced across its history and discussions.",
		Tags: []string{"knowledge:convention"},
	},
	{
		Name: "Key decisions and rationale",
		SourceQuery: "What are the significant technical decisions made in this project and the rationale " +
			"behind them — the durable 'why we do it this way' a developer should know? Summarize the " +
			"decisions and their reasoning from the commit rationales and past conversations.",
		Tags: []string{"knowledge:decision"},
	},
	{
		Name: "Initiatives and enhancements",
		SourceQuery: "Based on this repository's commit history, what are the major initiatives, features, and " +
			"enhancements the project has worked on? Summarize the themes and notable changes over time. " +
			"When a source memory's context carries a `[[page:<id>]]` link, repeat that link in the " +
			"summary of what it describes, so each initiative links to its detailed page.",
		Tags: []string{"knowledge:feature-work"},
	},
}

// seededPageFactTypes is what a page synthesizes from. A project bank holds all
// three: the merge round copies the members' world and experience facts, and the
// bank's own consolidation derives its observations.
var seededPageFactTypes = []any{"world", "experience", "observation"}

// sededPageTagsMatch keeps tier discrimination while letting untagged facts
// reach the page. The server defaults a tagged model to "all_strict", which
// excludes untagged rows; world and experience facts reach a project bank
// without a knowledge label often enough that "all_strict" would starve the
// page. "all" still stops a knowledge:decision fact from reaching Component map.
const seededPageTagsMatch = "all"

// KnowledgePageSeeder seeds a project bank's knowledge pages.
//
// It is what the administration plane holds behind adminapi.PageSeeder. A nil
// builder means the deployment cannot make pages, and then nothing is seeded.
type KnowledgePageSeeder struct {
	Builder *PageBuilder
}

// SeedKnowledgePages creates the taxonomy's pages in bankID, skipping any page
// of that name the bank already holds.
//
// The bank lives in a tenant schema, and this runs from the administration
// plane, whose context carries none — so the schema the caller names is put on
// the context before any store call. Without it every call here fails with
// "tenant schema is required on the context" against PostgreSQL, while the
// in-memory backend, which has no schemas, is happy either way.
//
// Matching by name is what makes the call idempotent: a re-run, a concurrent
// run or a bank that already carries some of the pages adds only what is
// missing. A page that fails is logged and the next one proceeds; the returned
// error is the join of the failures, which the caller reports without failing
// whatever it was creating.
func (s KnowledgePageSeeder) SeedKnowledgePages(ctx context.Context, schema, bankID, project string) error {
	if s.Builder == nil {
		return nil
	}
	ctx, err := storage.WithSchema(ctx, schema)
	if err != nil {
		return err
	}
	existing, err := s.Builder.Store.ListKnowledgeNodes(ctx, bankID)
	if err != nil {
		return fmt.Errorf("list the bank's knowledge nodes: %w", err)
	}
	present := make(map[string]bool, len(existing))
	for _, node := range existing {
		if node.Kind == knowledgeKindPage {
			present[lowerName(node.Title)] = true
		}
	}
	var failures []error
	for _, page := range seededPageTaxonomy {
		if present[lowerName(page.Name)] {
			continue
		}
		params := CreateKnowledgePageParams{
			SourceQuery: page.SourceQuery + projectScopeRule(project),
			Tags:        page.Tags,
			MaxTokens:   seededPageMaxTokens,
			Trigger:     seededPageTrigger(bankID, page.Name),
		}
		if _, serviceErr := s.Builder.BuildPage(ctx, nil, bankID, nil, page.Name, params); serviceErr != nil {
			slog.Default().Error("seeding a project bank's knowledge page failed",
				"bank_id", bankID, "page", page.Name, "error", serviceErr.Error())
			failures = append(failures, fmt.Errorf("%s: %s", page.Name, serviceErr.Error()))
		}
	}
	return errors.Join(failures...)
}

// projectScopeRule is the clause appended to every seeded question, naming the
// subject the page is about.
//
// A bank collects everything said IN a repository, not everything said ABOUT it:
// facts about a dependency the repository merely uses land there too, and the
// attribution tags record where a fact arrived from, never what it is about.
// Stating the exclusion in the query is what lets the synthesizer tell the two
// apart while it still has the fact's text in front of it.
func projectScopeRule(project string) string {
	return " Scope this page to " + project + " ITSELF: the bank also holds facts about external tools, " +
		"libraries and services that " + project + " merely uses, configures, deploys or discusses, and " +
		"those belong to somebody else's codebase. Include something only when its subject is " +
		project + "'s own code, configuration or process; when it is about a dependency, leave it out " +
		"however well-evidenced it looks — including any commit SHA or identifier that belongs to that " +
		"dependency's repository rather than this one."
}

// seededPageTrigger is the refresh policy a seeded page is created with.
//
// The schedule is hourly, and the minute is hashed from the page's identity so
// the five pages of one bank fire on five different minutes instead of one wave
// against the worker pool that also serves retain. The hour is unhashed because
// an hourly schedule has one hour only.
//
// Nothing is spent on an idle bank regardless: the scheduled-refresh job gates
// every tick on the page's scope, so a tick with no new fact in it queues no
// refresh.
func seededPageTrigger(bankID, pageName string) map[string]any {
	return map[string]any{
		"fact_types":   seededPageFactTypes,
		"tags_match":   seededPageTagsMatch,
		"refresh_cron": fmt.Sprintf("%d * * * *", seededPageMinute(bankID, pageName)),
	}
}

// seededPageMinute is the page's own minute of the hour, in [0, 59].
//
// It is the first four bytes of SHA-256 over "bank\x00page", taken modulo 60 —
// the same seed and rule the coding-agents package resolves its "H" field with,
// so a page keeps its slot across restarts and two banks get different ones.
func seededPageMinute(bankID, pageName string) uint32 {
	digest := sha256.Sum256([]byte(bankID + "\x00" + pageName))
	return binary.BigEndian.Uint32(digest[:4]) % 60
}

// lowerName folds a page name for the presence check. Page names are unique in a
// folder case-insensitively, so `Deploy` and `deploy` must not seed twice.
func lowerName(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		out = append(out, r)
	}
	return string(out)
}
