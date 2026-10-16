import { describe, expect, it } from "vitest";

import { recallResponseToPromptString } from "../src/index";

describe("recallResponseToPromptString", () => {
  it("renders each fact with its temporal fields and context", () => {
    const prompt = recallResponseToPromptString({
      results: [
        {
          id: "1",
          text: "Alice works at Google",
          context: "work",
          occurred_start: "2024-01-15T10:00:00Z",
          occurred_end: "2024-06-15T10:00:00Z",
          mentioned_at: "2024-03-01T09:00:00Z",
        },
        { id: "2", text: "The sky is blue" },
      ],
    });

    expect(prompt.startsWith("FACTS:\n")).toBe(true);
    const facts = JSON.parse(prompt.slice("FACTS:\n".length));
    expect(facts).toEqual([
      {
        text: "Alice works at Google",
        context: "work",
        occurred_start: "2024-01-15T10:00:00Z",
        occurred_end: "2024-06-15T10:00:00Z",
        mentioned_at: "2024-03-01T09:00:00Z",
      },
      { text: "The sky is blue" },
    ]);
  });

  it("inlines the source chunk a fact came from", () => {
    const prompt = recallResponseToPromptString({
      results: [{ id: "1", text: "Alice works at Google", chunk_id: "chunk_1" }],
      chunks: {
        chunk_1: {
          id: "chunk_1",
          text: "Alice works at Google on the AI team since 2020.",
          chunk_index: 0,
        },
      },
    });

    const facts = JSON.parse(prompt.slice("FACTS:\n".length));
    expect(facts[0].source_chunk).toBe("Alice works at Google on the AI team since 2020.");
  });

  it("renders entity sections from the response's observations", () => {
    const prompt = recallResponseToPromptString({
      results: [{ id: "1", text: "Alice works at Google" }],
      entities: {
        Alice: {
          entity_id: "e1",
          canonical_name: "Alice",
          observations: [{ text: "Alice is a senior engineer at Google working on AI." }],
        },
      },
    });

    expect(prompt).toContain("ENTITIES:");
    expect(prompt).toContain("## Alice");
    expect(prompt).toContain("Alice is a senior engineer at Google working on AI.");
  });

  it("keeps the facts section ahead of the entities section", () => {
    const prompt = recallResponseToPromptString({
      results: [{ id: "1", text: "Alice works at Google", chunk_id: "c1" }],
      chunks: { c1: { id: "c1", text: "Full conversation about Alice at Google.", chunk_index: 0 } },
      entities: {
        Alice: {
          entity_id: "e1",
          canonical_name: "Alice",
          observations: [{ text: "Alice is a senior engineer." }],
        },
      },
    });

    const [factsSection] = prompt.split("ENTITIES:");
    const facts = JSON.parse(factsSection.trim().slice("FACTS:\n".length));
    expect(facts[0].source_chunk).toBe("Full conversation about Alice at Google.");
    expect(prompt).toContain("## Alice\nAlice is a senior engineer.");
  });

  it("renders an empty facts list when there are no results", () => {
    expect(recallResponseToPromptString({ results: [] })).toBe("FACTS:\n[]");
  });

  it("ignores a chunk_id the response does not carry", () => {
    const prompt = recallResponseToPromptString({
      results: [{ id: "1", text: "Some fact", chunk_id: "missing_chunk" }],
    });

    const facts = JSON.parse(prompt.slice("FACTS:\n".length));
    expect(facts[0]).not.toHaveProperty("source_chunk");
  });
});