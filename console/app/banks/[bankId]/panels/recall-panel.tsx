"use client";

import { useState } from "react";

import { recallApi } from "../../../../lib/client/endpoints";
import { useTranslations } from "../../../../i18n";
import { Button } from "../../../../components/ui/button";
import { Input } from "../../../../components/ui/input";
import { PanelHeader, PanelState } from "./panel-state";
import { RecallResults } from "./recall-results";

/**
 * Recall.
 *
 * The response is shown through the result view rather than projected here, so
 * the ranked list and the raw envelope stay in one place and both read the same
 * fetched payload.
 */
export function RecallPanel({ bankId }: { bankId: string }) {
  const t = useTranslations("panels.recall");
  const [query, setQuery] = useState("");
  const [result, setResult] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState(false);

  const run = async () => {
    if (!query.trim()) return;
    setLoading(true);
    setError(false);
    try {
      setResult(await recallApi.run({ bank_id: bankId, query }));
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
      <div className="mb-4 flex gap-2">
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
      {result === null && !loading && !error ? (
        <div className="rounded-[16px] border border-border bg-card px-6 py-16 text-center">
          <p className="text-[13px] text-muted-foreground">{t("idle")}</p>
        </div>
      ) : (
        <PanelState loading={loading} error={error} onRetry={() => void run()}>
          <RecallResults payload={result} />
        </PanelState>
      )}
    </div>
  );
}

