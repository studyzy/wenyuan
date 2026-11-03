import { describe, expect, it } from "vitest";
import {
  buildPageTrigger,
  codingBankManifest,
  CODING_BANK_TEMPLATE,
  DEFAULT_PAGE_TRIGGER_CRON,
  expandCronHash,
  isHashedCron,
  KNOWLEDGE_LABELS,
  PAGE_FACT_TYPES,
  PAGE_NAMES,
  pagesFor,
  pageScopeRule,
  pageTriggerDrifted,
  pageTriggerFor,
  pageTriggerPatch,
  parseHashedCron,
  REFLECT_MISSION,
  RETAIN_STRATEGIES,
} from "../../../src/core/missions";

/** This package owns WHEN a page refreshes (a cron on a hashed minute by default) and states only
 *  that: HOW a page refreshes is the server's own default, which this must not freeze a copy of. */
describe("buildPageTrigger", () => {
  it("defaults to the hourly staggered schedule", () => {
    expect(buildPageTrigger()).toMatchObject({
      fact_types: PAGE_FACT_TYPES,
      refresh_cron: DEFAULT_PAGE_TRIGGER_CRON,
    });
    expect(buildPageTrigger({})).toEqual(buildPageTrigger());
  });

  // The server defaults a tagged model to all_strict, which EXCLUDES untagged memories — and the
  // shared consolidation scope makes every observation in these banks untagged. Left at
  // all_strict, a page asking for observations could never retrieve a single one.
  it("admits the untagged shared observations these pages ask for", () => {
    expect(buildPageTrigger().tags_match).toBe("all");
    expect(PAGE_FACT_TYPES).toContain("observation");
    for (const pageTriggerType of ["auto-refresh", "cron", "manual"] as const) {
      expect(buildPageTrigger({ pageTriggerType }).tags_match).toBe("all");
    }
  });

  it("puts pages on a schedule", () => {
    const trigger = buildPageTrigger({ pageTriggerType: "cron", pageTriggerCron: "0 3 * * *" });
    expect(trigger.refresh_cron).toBe("0 3 * * *");
    // The API rejects a trigger carrying both — a page refreshes on one schedule or the other.
    expect(trigger.refresh_after_consolidation).toBeUndefined();
  });

  it("stops refreshing pages on request", () => {
    const trigger = buildPageTrigger({ pageTriggerType: "manual" });
    expect(trigger.refresh_after_consolidation).toBe(false);
    expect(trigger.refresh_cron).toBeUndefined();
  });

  it.each([
    ["auto-refresh", ["fact_types", "tags_match", "refresh_after_consolidation"]],
    ["cron", ["fact_types", "tags_match", "refresh_cron"]],
    ["manual", ["fact_types", "tags_match", "refresh_after_consolidation"]],
  ] as const)("states nothing the server owns under %s", (pageTriggerType, keys) => {
    const trigger = buildPageTrigger({ pageTriggerType, pageTriggerCron: "0 3 * * *" });
    expect(Object.keys(trigger).sort()).toEqual([...keys].sort());
  });
});

// One pageTriggerCron is shared by every page in every bank, copied verbatim — a literal
// expression would schedule ALL of them on the one minute it names. H hashes the field per page.
describe("hashed cron fields", () => {
  const cron = (raw: string, bank: string, page: string) =>
    pageTriggerFor(
      buildPageTrigger({ pageTriggerType: "cron", pageTriggerCron: raw }),
      bank,
      page
    ).refresh_cron;

  it("leaves an expression without H exactly as written", () => {
    expect(cron("0 3 * * *", "repo-a", "Component map")).toBe("0 3 * * *");
    expect(cron("0 3 * * *", "repo-b", "Core concepts")).toBe("0 3 * * *");
  });

  it("resolves H to an ordinary cron expression the server can parse", () => {
    // H never leaves this package — refresh_cron is standard 5-field cron server-side.
    expect(cron("H H * * *", "repo-a", "Component map")).toMatch(
      /^(?:[0-9]|[1-5][0-9]) (?:[0-9]|1[0-9]|2[0-3]) \* \* \*$/
    );
    expect(cron("H * * * *", "repo-a", "Component map")).toMatch(
      /^(?:[0-9]|[1-5][0-9]) \* \* \* \*$/
    );
  });

  it("keeps the fields the operator wrote and hashes only the H", () => {
    // "spread within 03:00" — the hour is a decision, the minute is not.
    const daily = cron("H 3 * * *", "repo-a", "Component map");
    expect(daily).toMatch(/^\d+ 3 \* \* \*$/);
    // A range bounds where the hash may land: spread across the night only.
    const hours = new Set(
      Array.from({ length: 60 }, (_, i) =>
        Number(cron("H H(0-5) * * *", `repo-${i}`, "Component map")!.split(" ")[1])
      )
    );
    expect(Math.min(...hours)).toBeGreaterThanOrEqual(0);
    expect(Math.max(...hours)).toBeLessThanOrEqual(5);
    expect(hours.size).toBeGreaterThan(1);
  });

  it("gives each page its own slot, stably", () => {
    const one = cron("H H * * *", "repo-a", "Component map");
    // Stable: a page keeps its slot across runs, machines and releases, or every session would
    // reschedule it — and the seed PATCH would report drift forever.
    expect(cron("H H * * *", "repo-a", "Component map")).toBe(one);
    expect(cron("H H * * *", "repo-a", "Core concepts")).not.toBe(one);
    expect(cron("H H * * *", "repo-b", "Component map")).not.toBe(one);
  });

  it("spreads a shared config across banks instead of piling them on one minute", () => {
    const slots = Array.from({ length: 50 }, (_, i) =>
      cron("H H * * *", `repo-${i}`, "Component map")
    );
    // Hashing distributes approximately — it does not partition — so a couple of collisions are
    // expected, not a bug.
    expect(new Set(slots).size).toBeGreaterThan(45);
  });

  it("does not derive minute and hour from the same number", () => {
    // Hashing the field index alongside the seed is what keeps "H H * * *" worth 1440 slots
    // rather than 60 correlated ones.
    const minutes = new Set<number>();
    const hours = new Set<number>();
    for (let i = 0; i < 200; i++) {
      const [m, h] = cron("H H * * *", `repo-${i}`, "Component map")!.split(" ");
      minutes.add(Number(m));
      hours.add(Number(h));
    }
    expect(minutes.size).toBeGreaterThan(40);
    expect(hours.size).toBe(24);
  });

  it("leaves a malformed expression for the server to reject", () => {
    // Rewriting it here would invent a schedule nobody asked for; config resolution refuses it
    // first — parseHashedCron is what lets it tell a malformed H from a plain cron.
    expect(expandCronHash("H(9-3) * * * *", "seed")).toBe("H(9-3) * * * *");
    expect(expandCronHash("H H", "seed")).toBe("H H");
    expect(parseHashedCron("H(0-99) * * * *")).toBeUndefined();
    expect(parseHashedCron("Hx * * * *")).toBeUndefined();
    for (const good of ["H H * * *", "H * * * *", "H 3 * * *", "H H(0-5) * * *"]) {
      expect(isHashedCron(good)).toBe(true);
      expect(parseHashedCron(good)).toBeDefined();
    }
    expect(isHashedCron("0 3 * * *")).toBe(false);
  });

  it("passes a trigger with no cron through untouched", () => {
    const auto = buildPageTrigger({ pageTriggerType: "auto-refresh" });
    expect(pageTriggerFor(auto, "repo-a", "Component map")).toBe(auto);
  });
});

// An existing page is re-synced to whatever the config says, so a changed default reaches a bank
// that was seeded under the old one.
describe("pageTriggerDrifted", () => {
  const settled = (name: string) => pageTriggerFor(buildPageTrigger(), "repo-a", name);

  it("sees no drift in the policy it just wrote", () => {
    const desired = settled("Component map");
    // What the server reports back: the effective policy, with the exclusive counterpart at its
    // default rather than absent.
    expect(pageTriggerDrifted({ ...desired, refresh_after_consolidation: false }, desired)).toBe(
      false
    );
  });

  it("sees a page still on the old auto-refresh default as drifted", () => {
    expect(
      pageTriggerDrifted(
        { tags_match: "all", refresh_after_consolidation: true, refresh_cron: null },
        settled("Component map")
      )
    ).toBe(true);
  });

  it("sees a different schedule as drifted", () => {
    const desired = settled("Component map");
    expect(pageTriggerDrifted({ tags_match: "all", refresh_cron: "0 3 * * *" }, desired)).toBe(
      true
    );
    // Compared against the page's OWN resolved cron, never the shared "H * * * *" — otherwise
    // every page would look drifted on every session.
    expect(desired.refresh_cron).not.toBe(DEFAULT_PAGE_TRIGGER_CRON);
    expect(
      pageTriggerDrifted({ ...desired, refresh_cron: DEFAULT_PAGE_TRIGGER_CRON }, desired)
    ).toBe(true);
  });

  it("still sees the tags_match drift it was originally written for", () => {
    const desired = settled("Component map");
    expect(pageTriggerDrifted({ ...desired, tags_match: "all_strict" }, desired)).toBe(true);
  });
});

// The server drops the unstated counterpart of a TRUTHY refresh field, so a cron patch clears
// auto-refresh by itself. "manual" is falsy and clears nothing — hence the explicit null.
describe("pageTriggerPatch", () => {
  it("leaves a cron or auto-refresh patch alone", () => {
    for (const pageTriggerType of ["cron", "auto-refresh"] as const) {
      const desired = buildPageTrigger({ pageTriggerType });
      expect(pageTriggerPatch(desired)).toEqual(desired);
    }
  });

  it("clears an existing schedule when moving a page to manual", () => {
    const patch = pageTriggerPatch(buildPageTrigger({ pageTriggerType: "manual" }));
    expect(patch.refresh_after_consolidation).toBe(false);
    expect(patch.refresh_cron).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// codingBankManifest
// ---------------------------------------------------------------------------
describe("codingBankManifest", () => {
  const bankOf = (overrides: Record<string, unknown> | undefined) =>
    codingBankManifest(overrides)?.bank;

  it("exposes the template the manifest is derived from", () => {
    expect(CODING_BANK_TEMPLATE.version).toBe("1");
    expect(CODING_BANK_TEMPLATE.bank.reflect_mission).toBe(REFLECT_MISSION);
    expect(CODING_BANK_TEMPLATE.bank.retain_default_strategy).toBe("git");
    expect(CODING_BANK_TEMPLATE.bank.entities_allow_free_form).toBe(true);
  });

  it("seeds the full template on a bank with no overrides of its own", () => {
    // Unreadable overrides (no bank yet, or the bank-config API switched off) seed the same lot:
    // nothing can have been customised through an API that is not there.
    for (const empty of [undefined, {}]) {
      expect(codingBankManifest(empty)).toEqual(CODING_BANK_TEMPLATE);
    }
  });

  it("adds a strategy a newer release introduced, keeping the ones already there", () => {
    // The reason the re-apply exists at all: a bank seeded before "survey" shipped must still get
    // it, or the survey's documents retain under a strategy the bank does not have.
    const { survey: _survey, ...seededByAnOlderRelease } = RETAIN_STRATEGIES;
    const bank = bankOf({
      reflect_mission: "seeded",
      retain_strategies: { ...seededByAnOlderRelease, mycustom: { retain_chunk_size: 500 } },
    });
    expect(Object.keys(bank!.retain_strategies as object).sort()).toEqual([
      "conversation",
      "document",
      "git",
      "gitlog",
      "mycustom",
      "survey",
    ]);
    expect((bank!.retain_strategies as Record<string, unknown>).survey).toEqual(
      RETAIN_STRATEGIES.survey
    );
  });

  it("never deletes a strategy the user defined, nor reverts their edits to ours", () => {
    const mine = {
      ...RETAIN_STRATEGIES,
      // The user rewrote the conversation strategy's mission; that is theirs to decide.
      conversation: { retain_mission: "MINE", retain_extraction_mode: "concise" },
      mycustom: { retain_chunk_size: 500 },
    };
    // Nothing missing => the field is not written at all, so no import can revert it.
    expect(
      bankOf({ reflect_mission: "seeded", retain_strategies: mine })
    ).not.toHaveProperty("retain_strategies");
  });

  it("re-syncs the extraction mode of the package's own strategies to the configured one", () => {
    // A bank seeded by a release that defaulted to verbose, plus a user tweak and a user strategy.
    const seeded = Object.fromEntries(
      Object.entries(RETAIN_STRATEGIES).map(([n, d]) => [
        n,
        d.retain_extraction_mode === "custom" ? d : { ...d, retain_extraction_mode: "verbose" },
      ])
    );
    const current = {
      ...seeded,
      conversation: { ...(seeded.conversation as object), retain_chunk_size: 500 },
      mycustom: { retain_extraction_mode: "verbose" },
    };
    const strategies = bankOf({ reflect_mission: "seeded", retain_strategies: current })!
      .retain_strategies as Record<string, Record<string, unknown>>;
    for (const name of ["git", "gitlog", "conversation", "document"]) {
      expect(strategies[name]!.retain_extraction_mode).toBe("concise");
    }
    // Only the mode moves: the user's other edits, their own strategy and the survey stay put.
    expect(strategies.conversation!.retain_chunk_size).toBe(500);
    expect(strategies.mycustom).toEqual({ retain_extraction_mode: "verbose" });
    expect(strategies.survey).toEqual(RETAIN_STRATEGIES.survey);

    // An explicit choice is honoured the same way, and seeds a new bank with it.
    const verbose = codingBankManifest(
      { reflect_mission: "seeded", retain_strategies: RETAIN_STRATEGIES },
      "verbose"
    )!.bank.retain_strategies as Record<string, Record<string, unknown>>;
    expect(verbose.git!.retain_extraction_mode).toBe("verbose");
    expect(codingBankManifest(undefined, "chunks")!.bank.retain_extraction_mode).toBe("chunks");
  });

  it("leaves a bank that already carries the whole structure completely alone", () => {
    // Every session start calls this. On a settled bank it must be a no-op — no manifest, no POST.
    expect(
      codingBankManifest({
        reflect_mission: "seeded",
        retain_default_strategy: "mycustom",
        entities_allow_free_form: false,
        retain_strategies: RETAIN_STRATEGIES,
        entity_labels: [KNOWLEDGE_LABELS],
      })
    ).toBeUndefined();
  });

  it("keeps retain_default_strategy pointing where the user aimed it", () => {
    // The map must not be replaced (deleting a user strategy) while the pointer to it survives,
    // leaving the bank naming a strategy that no longer existed.
    const bank = bankOf({
      retain_default_strategy: "mycustom",
      retain_strategies: { mycustom: { retain_chunk_size: 500 } },
    });
    expect(bank).not.toHaveProperty("retain_default_strategy");
    expect(bank!.retain_strategies).toHaveProperty("mycustom");
  });

  it("adds the knowledge label group alongside the user's own, and only once", () => {
    const mine = { key: "audience", type: "multi-values", values: [] };
    expect(bankOf({ reflect_mission: "seeded", entity_labels: [mine] })!.entity_labels).toEqual([
      mine,
      KNOWLEDGE_LABELS,
    ]);
    // Already present — including a version the user reworded — is left as it is.
    expect(
      bankOf({
        reflect_mission: "seeded",
        entity_labels: [{ ...KNOWLEDGE_LABELS, description: "my wording" }],
      })
    ).not.toHaveProperty("entity_labels");
  });

  it("still seeds the missions as a group, once", () => {
    // Spelled out rather than imported: the contract is these three fields, whatever the
    // implementation happens to call its list of them.
    const missions = ["reflect_mission", "retain_mission", "observations_mission"] as const;
    expect(bankOf({})!.reflect_mission).toBe(REFLECT_MISSION);
    for (const field of missions) {
      // Any one mission present means the bank has been through here; none of the three is
      // rewritten.
      const bank = bankOf({ [field]: "MY OWN MISSION" })!;
      for (const f of missions) expect(bank).not.toHaveProperty(f);
    }
    // A blank override is not a choice.
    expect(bankOf({ reflect_mission: "   " })!.reflect_mission).toBe(REFLECT_MISSION);
  });
});

// The user's own additions to the template: a bank created by this package is otherwise born with
// the server's defaults for everything the template does not name — which are exactly the
// expensive settings.
describe("codingBankManifest defaultBankConfig", () => {
  const cheap = {
    enable_observations: false,
    enable_auto_consolidation: false,
    mental_model_min_refresh_interval_seconds: 21600,
  };
  const withDefaults = (
    overrides: Record<string, unknown> | undefined,
    defaults: Record<string, unknown> = cheap
  ) => codingBankManifest(overrides, "concise", defaults)?.bank;

  it("seeds a new bank with the defaults, which win the keys the template also names", () => {
    const bank = withDefaults(undefined)!;
    expect(bank.enable_auto_consolidation).toBe(false);
    expect(bank.mental_model_min_refresh_interval_seconds).toBe(21600);
    // The template says enable_observations: true; the user's default is the more specific word.
    expect(bank.enable_observations).toBe(false);
    // Everything else the template seeds is still there.
    expect(bank.reflect_mission).toBe(REFLECT_MISSION);
    expect(bank.retain_strategies).toEqual(CODING_BANK_TEMPLATE.bank.retain_strategies);
  });

  it("never overwrites a value the bank already holds — even one equal to the server default", () => {
    // The operator turned auto-consolidation ON in the control plane for this one bank. A cheap
    // default must not flip it back on the next session start.
    const bank = withDefaults({ reflect_mission: "seeded", enable_auto_consolidation: true })!;
    expect(bank).not.toHaveProperty("enable_auto_consolidation");
    // The keys the bank is silent on are still filled in.
    expect(bank.mental_model_min_refresh_interval_seconds).toBe(21600);
    expect(bank.enable_observations).toBe(false);
  });

  it("is a no-op on a bank that already carries the structure and every default", () => {
    expect(
      codingBankManifest(
        {
          reflect_mission: "seeded",
          retain_default_strategy: "git",
          entities_allow_free_form: true,
          retain_strategies: RETAIN_STRATEGIES,
          entity_labels: [KNOWLEDGE_LABELS],
          ...cheap,
        },
        "concise",
        cheap
      )
    ).toBeUndefined();
  });

  it("skips the fields the package governs itself, and blank values", () => {
    // retain_strategies is merged per entry and retain_extraction_mode follows the configured
    // mode — a default naming either would replace a merged map wholesale or fight the re-sync.
    // Config resolution already warns and drops them; this is the backstop.
    const bank = withDefaults(undefined, {
      retain_strategies: { mine: {} },
      entity_labels: [],
      retain_extraction_mode: "verbose",
      reflect_source_facts_max_tokens: null,
      recall_budget_function: "",
      enable_auto_consolidation: false,
    })!;
    expect(bank.retain_strategies).toEqual(CODING_BANK_TEMPLATE.bank.retain_strategies);
    expect(bank.entity_labels).toEqual(CODING_BANK_TEMPLATE.bank.entity_labels);
    expect(bank.retain_extraction_mode).toBe("concise");
    expect(bank).not.toHaveProperty("reflect_source_facts_max_tokens");
    expect(bank).not.toHaveProperty("recall_budget_function");
    expect(bank.enable_auto_consolidation).toBe(false);
  });

  it("changes nothing when there are no defaults", () => {
    expect(codingBankManifest(undefined, "concise", {})).toEqual(CODING_BANK_TEMPLATE);
  });
});

// ---------------------------------------------------------------------------
// pagesFor / pageScopeRule
// ---------------------------------------------------------------------------
describe("pagesFor page configuration", () => {
  const scope = pageScopeRule("repo-a");

  it("seeds the whole taxonomy with its built-in queries when no config is given", () => {
    const pages = pagesFor("repo-a");
    expect(pages.map((p) => p.name)).toEqual([...PAGE_NAMES]);
    for (const page of pages) expect(page.source_query.endsWith(scope)).toBe(true);
  });

  it("scopes every page in the taxonomy to the named repository", () => {
    for (const page of pagesFor("dotfiles")) {
      expect(page.source_query).toContain("Scope this page to dotfiles ITSELF");
      expect(page.source_query).toMatch(/external tools, libraries and services/);
      expect(page.source_query).toMatch(/dependency/);
    }
  });

  it("gives a shared bank the clause that names no project", () => {
    // A bank several repos share has no repo to name; naming the BANK scoped every page to a
    // project no memory was about, leaving them all empty.
    expect(pageScopeRule()).toContain("shared by several projects");
    expect(pageScopeRule()).not.toContain("Scope this page to");
    for (const page of pagesFor(undefined)) {
      expect(page.source_query).toContain(pageScopeRule());
    }
  });

  it("is a pure function of the project, so a re-seed does not PATCH the same query back", () => {
    // The seeder compares the live description against this text on every run; anything varying
    // per call would re-PATCH all five pages forever.
    expect(pagesFor("dotfiles")).toEqual(pagesFor("dotfiles"));
    expect(pagesFor("dotfiles")).not.toEqual(pagesFor("other-repo"));
  });

  it("keeps the taxonomy's names and tier tags untouched", () => {
    expect(pagesFor("dotfiles").map((p) => p.name)).toEqual([
      "Component map",
      "Core concepts",
      "Conventions and patterns",
      "Key decisions and rationale",
      "Initiatives and enhancements",
    ]);
    expect(pagesFor("dotfiles").flatMap((p) => p.tags)).toEqual([
      "knowledge:component",
      "knowledge:concept",
      "knowledge:convention",
      "knowledge:decision",
      "knowledge:feature-work",
    ]);
  });

  it("false skips a page and leaves the rest in taxonomy order", () => {
    const pages = pagesFor("repo-a", { "Component map": false });
    expect(pages.map((p) => p.name)).toEqual(PAGE_NAMES.filter((n) => n !== "Component map"));
  });

  it("a custom source_query replaces the built-in one, scoping clause still appended", () => {
    const pages = pagesFor("repo-a", { "Key decisions and rationale": { source_query: "why?" } });
    const page = pages.find((p) => p.name === "Key decisions and rationale")!;
    // The clause rides along with a reworded query: it is what keeps a dependency's decisions off
    // this project's page, which rewording the question is not a choice to take on.
    expect(page.source_query).toBe("why?" + scope);
    // The tier tag is the taxonomy's — it selects the facts the synthesis reads, so a reworded
    // query must not drop it.
    expect(page.tags).toEqual(["knowledge:decision"]);
    expect(pages.find((p) => p.name === "Core concepts")!.source_query).toContain("core concepts");
  });

  it("matches names case- and whitespace-insensitively, as the seeder matches live pages", () => {
    expect(
      pagesFor("repo-a", { "  component MAP  ": false }).map((p) => p.name)
    ).not.toContain("Component map");
  });

  it("customPages are appended after the taxonomy, scoped, with their own tags", () => {
    const pages = pagesFor("repo-a", {}, {
      "Security posture": {
        source_query: "what are our security decisions?",
        tags: ["knowledge:decision"],
      },
    });
    expect(pages).toHaveLength(PAGE_NAMES.length + 1);
    const custom = pages[pages.length - 1]!;
    expect(custom.name).toBe("Security posture");
    expect(custom.source_query).toBe("what are our security decisions?" + scope);
    expect(custom.tags).toEqual(["knowledge:decision"]);
  });

  it("a custom page without tags gets none — all-match then puts no tag constraint on it", () => {
    const pages = pagesFor("repo-a", {}, { Roadmap: { source_query: "where is this going?" } });
    expect(pages.find((p) => p.name === "Roadmap")!.tags).toEqual([]);
  });

  it("disabling a taxonomy page and adding one of your own compose", () => {
    const pages = pagesFor(
      "repo-a",
      { "Component map": false },
      { Roadmap: { source_query: "where is this going?" } }
    );
    expect(pages.map((p) => p.name)).toEqual([
      ...PAGE_NAMES.filter((n) => n !== "Component map"),
      "Roadmap",
    ]);
  });
});

