import { describe, expect, it } from "vitest";
import {
  buildReflectQuery,
  buildSystemInjection,
  formatPageFallback,
  formatRecallFallback,
  PAGE_INJECT_LEAD,
  RECALL_INJECT_LEAD,
} from "../../../src/core/inject";
import { stripInjectedMemory } from "../../../src/core/transcript-util";

describe("buildReflectQuery", () => {
  it("wraps the session goal verbatim and constrains the rendering to past-tense fact", () => {
    const out = buildReflectQuery("fix the uploader retry loop");
    expect(out).toContain("<goal>\nfix the uploader retry loop\n</goal>");
    // The imperative ban is the point: reflect follows instructions in the query, and an
    // unconstrained one once re-issued completed work as a present directive.
    expect(out).toMatch(/NEVER phrase anything as an instruction/);
    expect(out).toMatch(/Declarative, past-tense, attributed facts only/);
    // A decided literal table must survive verbatim, and decisions beat code reading.
    expect(out).toMatch(/COMPLETELY and VERBATIM/);
    expect(out).toMatch(/the decision wins/);
  });
});

describe("formatPageFallback", () => {
  it("renders one bullet per hit as name (id): snippet, with whitespace runs collapsed", () => {
    const out = formatPageFallback([
      { id: "p1", name: "Uploader guide", snippet: "retries\n  with   backoff" },
      { id: "p2", name: "Auth notes", snippet: "   " },
    ]);
    expect(out).toContain("- Uploader guide (p1): retries with backoff");
    // A blank snippet drops the colon entirely rather than trailing as "p2: ".
    expect(out).toContain("- Auth notes (p2)");
    expect(out).not.toContain("(p2): ");
  });

  it("defaults to the failed-reflect lead and accepts the configured-injection lead", () => {
    const hits = [{ id: "p1", name: "Guide", snippet: "s" }];
    expect(formatPageFallback(hits).startsWith("(WenYuan's synthesis was unavailable")).toBe(true);
    expect(formatPageFallback(hits, PAGE_INJECT_LEAD).startsWith(PAGE_INJECT_LEAD)).toBe(true);
  });
});

describe("formatRecallFallback", () => {
  it("bullets each recalled memory on one line", () => {
    const out = formatRecallFallback(["a\n fact", "another   fact"]);
    expect(out).toContain("- a fact");
    expect(out).toContain("- another fact");
  });

  it("defaults to the failed-reflect lead and accepts the configured-injection lead", () => {
    expect(formatRecallFallback(["x"]).startsWith("(WenYuan's synthesis was unavailable")).toBe(true);
    expect(formatRecallFallback(["x"], RECALL_INJECT_LEAD).startsWith(RECALL_INJECT_LEAD)).toBe(true);
  });
});

describe("buildSystemInjection", () => {
  const block = buildSystemInjection("the decided rule is X=3");

  it("wraps the memory in the wenyuan_memory tag with honest provenance and an ignore authorization", () => {
    expect(block.startsWith("<wenyuan_memory>\n")).toBe(true);
    expect(block.endsWith("\n</wenyuan_memory>")).toBe(true);
    expect(block).toContain("Automatically retrieved by WenYuan");
    // Calibrated framing: retrieval can miss, and an overconfident wrapper reads like a prompt
    // injection that skeptical models rightly discard wholesale.
    expect(block).toMatch(/may or may not bear on the current task/);
    expect(block).toMatch(/ignore\nit entirely|ignore it entirely/);
  });

  it("never lets past record read as a present instruction, and names the attribution line", () => {
    expect(block).toMatch(/record of the PAST/);
    expect(block).toContain("> 🧠 **From WenYuan memory** —");
    expect(block).toContain("Never attribute memory that did not contribute");
  });

  it("routes corrections through wenyuan_ingest_document", () => {
    expect(block).toMatch(/wenyuan_ingest_document/);
    expect(block).toMatch(/Correction: <topic>/);
  });

  it("is stripped whole by the transcript readers — the write-back never re-ingests it", () => {
    // LOAD-BEARING pairing with transcript-util's tag set: if the wrapper name ever drifts from
    // the strip set, the session write-back feeds the injected synthesis back into the bank
    // (a retain→reflect feedback loop).
    expect(stripInjectedMemory(block)).toBe("");
  });
});
