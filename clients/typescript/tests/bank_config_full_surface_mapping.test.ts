/**
 * The full bank-config request mapping.
 *
 * `updateBankConfig` enumerates config fields rather than passing a dict through,
 * so a field accepted as an option but never written into `updates` is silently
 * dropped: the caller gets a 200 and no change. The recall-budget group, memory
 * defence and the consolidation knobs were all unreachable from TypeScript that
 * way.
 */
import { describe, expect, it } from "vitest";

import { WenYuanClient } from "../src/index";
import { call, jsonBody, jsonObject, stubFetch, type Stub } from "./helpers";

function client() {
  const stub = stubFetch(() => ({ json: { bank_id: "bank", config: {}, overrides: {} } }));
  return {
    stub,
    client: new WenYuanClient({ baseUrl: "http://api.test", fetch: stub.fetch }),
  };
}

function sentUpdates(stub: Stub): Record<string, unknown> {
  return jsonObject(jsonBody(call(stub)).updates, "updates");
}

describe("full bank config surface mapping", () => {
  it("maps the recall budget group onto the updates map", async () => {
    const { client: api, stub } = client();

    await api.updateBankConfig("bank", {
      recallMaxTokens: 4096,
      recallIncludeChunks: false,
      recallChunksMaxTokens: 500,
      recallBudgetFunction: "adaptive",
      recallBudgetFixedLow: 50,
      recallBudgetFixedMid: 150,
      recallBudgetFixedHigh: 500,
      recallBudgetAdaptiveLow: 0.01,
      recallBudgetAdaptiveMid: 0.05,
      recallBudgetAdaptiveHigh: 0.2,
      recallBudgetMin: 10,
      recallBudgetMax: 1000,
    });

    expect(sentUpdates(stub)).toEqual({
      recall_max_tokens: 4096,
      recall_include_chunks: false,
      recall_chunks_max_tokens: 500,
      recall_budget_function: "adaptive",
      recall_budget_fixed_low: 50,
      recall_budget_fixed_mid: 150,
      recall_budget_fixed_high: 500,
      recall_budget_adaptive_low: 0.01,
      recall_budget_adaptive_mid: 0.05,
      recall_budget_adaptive_high: 0.2,
      recall_budget_min: 10,
      recall_budget_max: 1000,
    });
  });

  it("maps the consolidation, retain and security groups onto the updates map", async () => {
    const { client: api, stub } = client();

    await api.updateBankConfig("bank", {
      enableAutoConsolidation: false,
      consolidationLlmBatchSize: 4,
      consolidationLlmParallelism: 2,
      consolidationMaxMemoriesPerRound: 50,
      consolidationSourceFactsMaxTokens: 2048,
      consolidationSourceFactsMaxTokensPerObservation: 128,
      mentalModelMinRefreshIntervalSeconds: 3600,
      knowledgePageDefaultTrigger: { refresh_cron: "0 * * * *" },
      reflectSourceFactsMaxTokens: 1024,
      maxObservationsPerScope: 25,
      observationScopeLimits: [{ scope: "team", limit: 10 }],
      consolidationStrategies: [
        { scopes: [{ tags: ["company:*"] }], observations_mission: "Trends only." },
      ],
      retainDefaultStrategy: "fast",
      retainStrategies: { fast: { retain_extraction_mode: "concise" } },
      retainChunkBatchSize: 64,
      storeDocumentText: false,
      mcpEnabledTools: ["recall", "reflect"],
      llmGeminiSafetySettings: [{ category: "HARM_CATEGORY_HARASSMENT", threshold: "BLOCK_NONE" }],
      memoryDefense: { redact_secrets: true },
      auditLogEnabled: true,
    });

    expect(sentUpdates(stub)).toEqual({
      enable_auto_consolidation: false,
      consolidation_llm_batch_size: 4,
      consolidation_llm_parallelism: 2,
      consolidation_max_memories_per_round: 50,
      consolidation_source_facts_max_tokens: 2048,
      consolidation_source_facts_max_tokens_per_observation: 128,
      mental_model_min_refresh_interval_seconds: 3600,
      knowledge_page_default_trigger: { refresh_cron: "0 * * * *" },
      reflect_source_facts_max_tokens: 1024,
      max_observations_per_scope: 25,
      observation_scope_limits: [{ scope: "team", limit: 10 }],
      consolidation_strategies: [
        { scopes: [{ tags: ["company:*"] }], observations_mission: "Trends only." },
      ],
      retain_default_strategy: "fast",
      retain_strategies: { fast: { retain_extraction_mode: "concise" } },
      retain_chunk_batch_size: 64,
      store_document_text: false,
      mcp_enabled_tools: ["recall", "reflect"],
      llm_gemini_safety_settings: [
        { category: "HARM_CATEGORY_HARASSMENT", threshold: "BLOCK_NONE" },
      ],
      memory_defense: { redact_secrets: true },
      audit_log_enabled: true,
    });
  });

  it("sends only the fields that were set", async () => {
    const { client: api, stub } = client();

    await api.updateBankConfig("bank", { recallMaxTokens: 100 });

    expect(sentUpdates(stub)).toEqual({ recall_max_tokens: 100 });
  });

  it("maps the disposition traits", async () => {
    const { client: api, stub } = client();

    await api.updateBankConfig("bank", {
      dispositionSkepticism: 4,
      dispositionLiteralism: 2,
      dispositionEmpathy: 5,
      reflectMission: "Track the fleet.",
    });

    expect(sentUpdates(stub)).toEqual({
      disposition_skepticism: 4,
      disposition_literalism: 2,
      disposition_empathy: 5,
      reflect_mission: "Track the fleet.",
    });
  });

  it("routes the update to the bank's config route", async () => {
    const { client: api, stub } = client();

    await api.updateBankConfig("bank", { retainChunkSize: 512 });

    expect(call(stub).method).toBe("PATCH");
    expect(call(stub).path).toBe("/v1/default/banks/bank/config");
  });
});