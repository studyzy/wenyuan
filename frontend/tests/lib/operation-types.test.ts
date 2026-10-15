import { describe, it, expect } from "vitest";
import { readFileSync, readdirSync } from "node:fs";
import { join, resolve } from "node:path";

/**
 * Static guard for dataplane -> control-plane operation-type parity.
 *
 * An async operation type is declared once in the engine and then has to be
 * repeated in this UI: once in `OPERATION_TYPE_VALUES` so the operations list
 * can filter on it, and once in the `operationTypeLabels` map so it renders as a
 * name rather than a raw snake_case string. Nothing links the two sides, so the
 * failure mode is silent — the operations view keeps working, it just shows
 * `vector_index_maintenance` and offers no filter for it.
 *
 * Parsed rather than imported because the source of truth is a Go package and a
 * React component; a runtime import would need the whole app.
 *
 * The vocabulary is the reference's: the engine that writes an operation and the UI
 * that lists it name it the same string, and the guard parses both sides rather
 * than trusting either. Two groups are deliberately absent from the engine side —
 * operation types the reference submits but this service does not (its upload path
 * converts synchronously and queues `retain`, and graph/vector maintenance runs as
 * a periodic job rather than a queue row) — so those constants are declared for the
 * filter vocabulary and are never enqueued.
 */

const FRONTEND_ROOT = resolve(__dirname, "..", "..");
const VIEW = join(FRONTEND_ROOT, "components", "bank-operations-view.tsx");
const ENGINE_DIR = join(FRONTEND_ROOT, "..", "internal", "storage");

const ENGINE_OPERATION_TYPE_PATTERN = /OperationType[A-Za-z]*\s*=\s*"([a-z_]+)"/g;

/** Build a set from an iterable — the membership tests below need `.has`. */
function toSet(values: Iterable<string>): Set<string> {
  const set = new Set<string>();
  for (const value of values) set.add(value);
  return set;
}

/**
 * Operation types the UI carries that the engine never emits from
 * `_submit_async_operation`. These are real rows users can see, produced by
 * paths that build the operation record directly, so the UI must keep them —
 * the parity check only runs in the dataplane -> UI direction.
 */
const UI_ONLY = toSet(["all", "retain", "webhook_delivery"]);

function engineOperationTypes(): Set<string> {
  const types: string[] = [];
  for (const file of readdirSync(ENGINE_DIR)) {
    if (!file.endsWith(".go") || file.endsWith("_test.go")) continue;
    const src = readFileSync(join(ENGINE_DIR, file), "utf-8");
    for (const match of src.matchAll(ENGINE_OPERATION_TYPE_PATTERN)) types.push(match[1]);
  }
  return toSet(types);
}

function uiFilterValues(): Set<string> {
  const src = readFileSync(VIEW, "utf-8");
  const block = src.match(/const OPERATION_TYPE_VALUES = \[([\s\S]*?)\] as const/);
  expect(block, "OPERATION_TYPE_VALUES not found — did the constant get renamed?").toBeTruthy();
  return toSet([...block![1].matchAll(/"([a-z_]+)"/g)].map((m) => m[1]));
}

function uiLabelledTypes(): Set<string> {
  const src = readFileSync(VIEW, "utf-8");
  const block = src.match(/const operationTypeLabels: Record<string, string> = \{([\s\S]*?)\n {2}\}/);
  expect(block, "operationTypeLabels not found — did the map get renamed?").toBeTruthy();
  return toSet([...block![1].matchAll(/^\s+([a-z_]+):/gm)].map((m) => m[1]));
}

describe("operation-type parity between the engine and this UI", () => {
  it("every operation type the engine submits is filterable here", () => {
    const missing = [...engineOperationTypes()].filter((t) => !uiFilterValues().has(t)).sort();

    expect(
      missing,
      `Operation types ${JSON.stringify(missing)} are submitted by the engine but missing from ` +
        `OPERATION_TYPE_VALUES in bank-operations-view.tsx. Users cannot filter the operations list by them. ` +
        `Add them there (and to operationTypeLabels + every messages/*.json).`,
    ).toEqual([]);
  });

  it("every operation type the engine submits has a display label", () => {
    const missing = [...engineOperationTypes()].filter((t) => !uiLabelledTypes().has(t)).sort();

    expect(
      missing,
      `Operation types ${JSON.stringify(missing)} have no entry in operationTypeLabels, so they render as ` +
        `raw snake_case in the operations list. Add a t("operationType.<camelCase>") entry.`,
    ).toEqual([]);
  });

  it("the filter list and the label map stay in step", () => {
    // Two hand-maintained lists of the same thing: a type added to one and not
    // the other is filterable-but-unnamed, or named-but-unfilterable.
    expect([...uiFilterValues()].sort()).toEqual([...uiLabelledTypes()].sort());
  });

  it("declares which UI-only types are deliberate", () => {
    // Keeps the exemption list honest: an entry that the engine started
    // emitting, or that the UI dropped, should not sit here unnoticed.
    const unexplained = [...uiFilterValues()].filter(
      (t) => !engineOperationTypes().has(t) && !UI_ONLY.has(t),
    );

    expect(
      unexplained,
      `UI carries operation types the engine never emits: ${JSON.stringify(unexplained)}`,
    ).toEqual([]);
  });
});