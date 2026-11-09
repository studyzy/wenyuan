# Project bank knowledge pages — design

Status: 2026-11-10, approved. Scope: the administration plane's project
creation. This is our own addition, not part of the reference's compatibility
surface: no `/v1/...` route, JSON field, header or schema changes.

## The problem

A project's bank is created by `POST /admin/v1/teams/{id}/projects`
(`internal/adminapi/teams_projects.go`). It starts empty of pages and stays
that way.

The coding-agents package seeds five knowledge pages into every bank it owns
(`integrations/coding-agents/src/core/missions/pages.ts`, `PAGE_TAXONOMY`:
Component map, Core concepts, Conventions and patterns, Key decisions and
rationale, Initiatives and enhancements), but only into the banks its own
sessions address. A team's project banks are created and filled by
`project.merge`, which never runs that package, so they never get the pages.

The facts the pages read are already there. `project.merge` copies each member
fact verbatim, tags included, so the project bank carries the same
`knowledge:component` / `knowledge:concept` / `knowledge:convention` /
`knowledge:decision` / `knowledge:feature-work` labels the members' retains
projected. Measured on the three local project banks, each tier holds between
40 and 296 facts. What is missing is only the pages.

## The decision

Seed the five pages when a project's bank is created.

| Question | Answer |
|---|---|
| When | Only when this request created the bank. `adopt_bank` hands over a bank that is somebody else's; nothing is written into it. |
| Where the questions come from | A copy of the coding-agents taxonomy in Go, not a call into the package. A team bank must get its pages without a coding-agent session having run. |
| Failure | Logged, not fatal. The project is still `201 Created`. |
| Refresh | Hourly, each page on its own hashed minute. |
| Who implements it | A page seeder behind a narrow interface, backed by the same create core the HTTP route and the MCP tool use. |

## Architecture

### `adminapi.PageSeeder`

A narrow interface beside `BankAdmin` and `MergeAdmin`
(`internal/adminapi/team_config.go`), reached through `TeamConfig.Pages`:

```go
type PageSeeder interface {
    SeedKnowledgePages(ctx context.Context, bankID, project string) error
}
```

It takes an already-open tenant context, like the other team handles. A nil
value skips the step, which is what the in-memory backend and a single-tenant
deployment get.

### `reporting.PageBuilder`

The page-create body currently lives in
`(*reporting.Handlers).CreateKnowledgePageNode`, which already exists as the
one implementation behind both the HTTP route and the MCP tools. It moves into
a `PageBuilder` holding only what the create needs — `Store`, `Embeddings`,
`ProcessDefaults` — and `Handlers` keeps a `PageBuilder` and forwards, so a
route, a tool and the seeder cannot drift apart. The trigger layering, the
`max_tokens` default and the embedding of the page title stay where they are;
the builder exposes the same entry point `Handlers` had.

### `reporting.KnowledgePageSeed`

`internal/reporting/knowledge_seed.go` implements `adminapi.PageSeeder` on top
of `PageBuilder`. It holds the five definitions and the scope clause.

## Data flow

1. `createTeamProject` validates the request and creates the bank
   (`CreateBank(teamID, bankID, name)`), then the registry row.
2. `created == true` and `TeamConfig.Pages != nil`: call
   `Pages.SeedKnowledgePages(ctx, project.BankID, project.Name)`.
3. The seeder lists the bank's existing nodes and skips any page whose name
   already exists, so a retry, a concurrent run or a re-seed adds nothing.
4. Each missing page is created through `PageBuilder`, which stores the backing
   mental model, stores the tree node and enqueues the first refresh.
5. A page that fails is logged and the next one proceeds; the aggregate error
   is returned to the caller, which logs it and answers `201` regardless.

## The five pages

Name, question and layer tag are copied from the coding-agents taxonomy. The
scope clause (`pageScopeRule`) is appended to every question and names the
project: a bank collects everything said *in* a repository, including facts
about dependencies it merely uses, and the clause is what lets the synthesizer
tell the two apart.

| Name | Tag |
|---|---|
| Component map | `knowledge:component` |
| Core concepts | `knowledge:concept` |
| Conventions and patterns | `knowledge:convention` |
| Key decisions and rationale | `knowledge:decision` |
| Initiatives and enhancements | `knowledge:feature-work` |

Per page:

```json
{
  "name": "<taxonomy name>",
  "source_query": "<taxonomy question> + <scope clause naming the project>",
  "tags": ["<tier tag>"],
  "max_tokens": 4096,
  "trigger": {
    "fact_types": ["world", "experience", "observation"],
    "tags_match": "all",
    "refresh_cron": "<minute> * * * *"
  }
}
```

`tags_match` is `all`, not the server's `all_strict` default for a tagged
model: `all` keeps the tier discrimination (a `knowledge:decision` fact still
cannot reach Component map) while letting untagged facts in, and the
`source_query` does the selecting. `all_strict` would drop every untagged
world/experience fact that reached the project bank without a knowledge label.

`fact_types` names all three because a project bank holds all three: the merge
round copies world and experience, and the bank's own consolidation derives
its observations.

## Refresh

`refresh_cron` is hourly with the minute hashed from the page's identity:
`sha256(bankID + "\x00" + pageName)` is cut to its first 32 bits and taken
modulo 60, which is the coding-agents package's own `cron-hash` rule. Five
pages therefore fire on five spread minutes instead of one wave, and each keeps
its slot across restarts because the seed is a pure function of the pair.

**No model call is spent on an idle bank.** The scheduled-refresh job
(`internal/app/maintenance_jobs.go`, `refreshOneDueModel`) already gates every
tick on `IsStale`: the bank's write watermark is compared against the model's
`last_memory_seen_at` first, and only a write inside the page's scope — a new
`knowledge:<tier>` fact from a merge round — makes the page stale enough to
enqueue a refresh. A tick with nothing new costs one cheap query per page.

## Error handling

- Bank creation fails: unchanged, `500`.
- Registry row refused: unchanged, the bank is dropped and the registry error
  is the answer.
- Seeding fails: `slog` records team, project and bank ids with the error; the
  response is still `201 Created` with the project. Pages are a synthesized
  artifact of the bank, not a precondition for it, and the first build is
  already asynchronous.
- `adopt_bank`: no seeding at all.

## Testing

- `internal/reporting/knowledge_seed_test.go` (in-memory store): five root
  pages named after the taxonomy; each carries its tier tag, `fact_types`,
  `tags_match`, a valid five-field cron with a minute in `[0, 59]`, and a
  `source_query` ending in the scope clause naming the project; the five
  minutes are distinct for one bank; a second run adds nothing.
- `internal/adminapi/teams_test.go`: a fake seeder is called once with the
  project's bank id and name; not called on the `adopt_bank` path; a seeder
  error still answers `201` and leaves the project row in place.
- `internal/app/`: end-to-end through the served handler — create a team and a
  project, then read `GET /v1/default/banks/{bank}/knowledge-base/tree` with
  the team resolved and find five root nodes.

## Documentation

`AGENTS.md` gains a paragraph in the admin-plane section: what is seeded, that
it happens only for a bank this request created, that a failure is logged and
not fatal, and that the five questions are a copy of the coding-agents
taxonomy which must be changed in both places when either moves.