import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

/**
 * The memory curation family is client-side, so these assertions cover the
 * behaviour that would otherwise only be visible in a browser: what the form
 * sends, and which controls exist next to each other.
 */
const MEMORY_DIR = join(process.cwd(), "components/memory");

describe("memory editor family", () => {
  const detail = readFileSync(join(MEMORY_DIR, "memory-detail-modal.tsx"), "utf8");
  const edit = readFileSync(join(MEMORY_DIR, "edit-memory-form.tsx"), "utf8");
  const invalidate = readFileSync(join(MEMORY_DIR, "invalidate-memory-dialog.tsx"), "utf8");

  it("declares the three modules", () => {
    for (const file of ["edit-memory-form.tsx", "invalidate-memory-dialog.tsx", "memory-detail-modal.tsx"]) {
      expect(readFileSync(join(MEMORY_DIR, file), "utf8").length).toBeGreaterThan(0);
    }
  });

  it("offers both a memory tab and a history tab", () => {
    expect(detail).toContain('value="memory"');
    expect(detail).toContain('value="history"');
  });

  it("treats invalidation as a soft retire, not a delete", () => {
    // The control toggles state rather than removing the record, and the copy the
    // reader sees says so as well.
    expect(detail).toContain('state: invalidating ? "invalidated" : "valid"');
    expect(detail).not.toContain("apiDelete");

    const catalog = JSON.parse(
      readFileSync(join(process.cwd(), "messages/en.json"), "utf8"),
    ) as { memoryPanel: { curation: Record<string, string> } };
    expect(catalog.memoryPanel.curation.invalidateExplain).toContain("not deleted");
  });

  it("asks for a reason before retiring and skips it when restoring", () => {
    expect(detail).toContain("setAskingReason(true)");
    expect(detail).toContain("void curate()");
  });

  it("re-reads the memory after a change rather than patching local state", () => {
    // The upstream normalises what is sent, so the stored copy is authoritative.
    expect(detail).toContain("setNonce((value) => value + 1)");
    expect(detail).toContain("refresh()");
  });

  it("sends the bank id with a memory update so the request routes", () => {
    expect(detail).toContain("memoriesApi.update(bankId");
  });

  it("normalises entities from either shape the endpoint may return", () => {
    // An array from the detail endpoint, or a comma-joined string from a node.
    expect(edit).toContain("Array.isArray(memory.entities)");
    expect(edit).toContain('String(memory.entities).split(", ")');
  });

  it("commits an unsubmitted entity draft instead of dropping it", () => {
    expect(edit).toContain("entityDraft.trim()");
    expect(edit).toContain("finalEntities");
  });

  it("clears the retire reason when the dialog closes", () => {
    expect(invalidate).toContain("if (!next) setReason(\"\")");
  });

  it("refuses to save a memory with no text", () => {
    expect(edit).toContain("disabled={busy || !text.trim()}");
  });
});
