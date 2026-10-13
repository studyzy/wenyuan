"use client";

import { useState } from "react";

import { reflectApi } from "../../../../lib/client/endpoints";
import { useTranslations } from "../../../../i18n";
import { Button } from "../../../../components/ui/button";
import { Input } from "../../../../components/ui/input";
import { JsonViewer } from "../../../../components/ui/json-viewer";
import { Checkbox } from "../../../../components/ui/checkbox";
import { PanelHeader, PanelState } from "./panel-state";

/**
 * Reflect.
 *
 * The two legacy include booleans are offered as checkboxes and translated into
 * the include object the upstream expects, so the control matches what the
 * request actually carries instead of exposing the newer shape directly.
 */
export function ReflectPanel({ bankId }: { bankId: string }) {
  const t = useTranslations("panels.reflect");
  const [query, setQuery] = useState("");
  const [includeFacts, setIncludeFacts] = useState(true);
  const [includeToolCalls, setIncludeToolCalls] = useState(false);
  const [result, setResult] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState(false);

  const run = async () => {
    if (!query.trim()) return;
    setLoading(true);
    setError(false);
    try {
      setResult(
        await reflectApi.run({
          bank_id: bankId,
          query,
          include_facts: includeFacts,
          include_tool_calls: includeToolCalls,
        }),
      );
    } catch {
      setError(true);
      setResult(null);
    } finally {
      setLoading(false);
    }
  };

  return (
    <div>
      <PanelHeader title={t("title")} description={t("description")} />
      <div className="mb-3 flex gap-2">
        <Input
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter") void run();
          }}
          placeholder={t("placeholder")}
        />
        <Button variant="gradient" onClick={() => void run()} disabled={loading || !query.trim()}>
          {t("run")}
        </Button>
      </div>
      <div className="mb-4 flex items-center gap-5">
        <label className="flex items-center gap-2 text-[13px]">
          <Checkbox checked={includeFacts} onCheckedChange={(v) => setIncludeFacts(v === true)} />
          {t("includeFacts")}
        </label>
        <label className="flex items-center gap-2 text-[13px]">
          <Checkbox
            checked={includeToolCalls}
            onCheckedChange={(v) => setIncludeToolCalls(v === true)}
          />
          {t("includeToolCalls")}
        </label>
      </div>
      {result === null && !loading && !error ? (
        <div className="rounded-[16px] border border-border bg-card px-6 py-16 text-center">
          <p className="text-[13px] text-muted-foreground">{t("idle")}</p>
        </div>
      ) : (
        <PanelState loading={loading} error={error} onRetry={() => void run()}>
          <JsonViewer value={result} className="bg-muted" />
        </PanelState>
      )}
    </div>
  );
}
