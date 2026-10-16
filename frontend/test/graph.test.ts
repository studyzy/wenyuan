import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

import { convertGraphPayload } from "../components/graph/graph-data";

/**
 * The graph layer is a canvas renderer, so its drawing cannot be asserted
 * without a DOM. What can be asserted is the data contract the renderer depends
 * on and the prop surface the views are written against.
 */
const GRAPH_DIR = join(process.cwd(), "components/graph");

describe("graph data conversion", () => {
  it("labels a node from its own label, its memory text, or a short id", () => {
    const converted = convertGraphPayload({
      nodes: [
        { data: { id: "a", label: "explicit" } },
        { data: { id: "b" } },
        { data: { id: "c0123456789" } },
      ],
      table_rows: [{ id: "b", text: "memory text for b" }],
    });
    expect(converted.nodes[0].label).toBe("explicit");
    expect(converted.nodes[1].label).toBe("memory text for b");
    // No label and no row: falls back to a short id prefix so nothing is blank.
    expect(converted.nodes[2].label).toBe("c0123456");
  });

  it("elides a long memory text used as a label", () => {
    const long = "x".repeat(80);
    const converted = convertGraphPayload({
      nodes: [{ data: { id: "a" } }],
      table_rows: [{ id: "a", text: long }],
    });
    expect(converted.nodes[0].label).toHaveLength(43);
    expect(converted.nodes[0].label?.endsWith("...")).toBe(true);
  });

  it("classifies an edge from its link type, falling back to its line style", () => {
    const converted = convertGraphPayload({
      nodes: [
        { data: { id: "a" } },
        { data: { id: "b" } },
        { data: { id: "c" } },
      ],
      edges: [
        { data: { source: "a", target: "b", linkType: "entity" } },
        { data: { source: "b", target: "c", lineStyle: "dashed" } },
        { data: { source: "a", target: "c" } },
      ],
    });
    expect(converted.links.map((link) => link.type)).toEqual([
      "entity",
      "temporal",
      "semantic",
    ]);
  });

  it("prefers an explicit weight over similarity", () => {
    const converted = convertGraphPayload({
      nodes: [{ data: { id: "a" } }, { data: { id: "b" } }],
      edges: [
        { data: { source: "a", target: "b", weight: 3, similarity: 0.4 } },
        { data: { source: "a", target: "b", similarity: 0.4 } },
      ],
    });
    expect(converted.links[0].weight).toBe(3);
    expect(converted.links[1].weight).toBe(0.4);
  });

  it("tolerates a payload with no nodes or edges at all", () => {
    expect(convertGraphPayload({})).toEqual({ nodes: [], links: [] });
  });
});

describe("constellation renderer surface", () => {
  const source = readFileSync(join(GRAPH_DIR, "constellation.tsx"), "utf8");

  it("declares the prop surface the views depend on", () => {
    for (const prop of [
      "data",
      "height",
      "onNodeClick",
      "nodeColorFn",
      "linkColorFn",
      "nodeSizeFn",
      "nodeHeatFn",
      "compactLabels",
      "clusterKeyFn",
      "nodeGroupsFn",
    ]) {
      expect(source, `prop ${prop} missing`).toContain(prop);
    }
  });

  it("layouts deterministically rather than by a force simulation", () => {
    // Position comes from a hash of the id, so a re-render never reshuffles.
    expect(source).toContain("hashStr(node.id)");
    expect(source).not.toContain("Math.random");
  });

  it("keeps per-frame interaction state out of React state", () => {
    expect(source).toContain("stateRef");
    expect(source).toContain("requestAnimationFrame");
    configureAnimations();
  });

  it("remembers the ambient-motion preference globally", () => {
    expect(source).toContain("MOTION_STORAGE_KEY");
    expect(source).toContain("localStorage");
  });

  it("binds the dark theme to the class the toggle writes", () => {
    expect(source).toContain('classList.contains("dark")');
  });
});

/** The loop must be cancellable or a route change leaks a frame callback. */
function configureAnimations(): void {
  const source = readFileSync(join(GRAPH_DIR, "constellation.tsx"), "utf8");
  expect(source).toContain("cancelAnimationFrame");
  expect(source).toContain("ResizeObserver");
}
