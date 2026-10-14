"use client";

import { banksApi, observationsApi, webhooksApi } from "../../../../lib/client/endpoints";
import { auditLogsApi, llmRequestsApi } from "../../../../lib/client/endpoints";
import { readItems, readString, useAsyncData } from "../../../../lib/use-async-data";
import { formatRelativeTime } from "../../../../lib/relative-time";
import { useTranslations } from "../../../../i18n";
import { IdChip, TagList } from "../../../../components/ui/facet-chip";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "../../../../components/ui/table";
import { DetailRow, PanelHeader, PanelState } from "./panel-state";
import { BankConfigEditor } from "./bank-config-editor";
import { DirectivesView } from "./directives-view";
import { LlmRequestStats } from "./llm-request-stats";
import { ObservationsList } from "./observations-list";
import type { ConfigurationSubTab } from "../../../../lib/bank-detail";

/**
 * Bank profile and its configuration sub-tabs.
 *
 * Each sub-tab loads only its own data, so opening the profile does not fan out
 * into six requests the reader may never look at. Anything with an unstable
 * shape is shown through the JSON viewer rather than projected into rows that
 * would quietly drop fields the upstream added.
 */
export function BankProfilePanel({
  bankId,
  subTab,
}: {
  bankId: string;
  subTab: ConfigurationSubTab;
}) {
  const t = useTranslations("panels.profile");

  const bank = useAsyncData(() => banksApi.get(bankId), [bankId]);
  const general = bank;

  const config = useAsyncData(
    () => (subTab === "configuration" ? banksApi.config(bankId) : Promise.resolve(null)),
    [bankId, subTab],
  );
  const aliases = useAsyncData(
    () => (subTab === "general" ? banksApi.aliases(bankId) : Promise.resolve(null)),
    [bankId, subTab],
  );
  const webhooks = useAsyncData(
    () => (subTab === "webhooks" ? webhooksApi.list(bankId) : Promise.resolve(null)),
    [bankId, subTab],
  );
  const logs = useAsyncData(
    () => (subTab === "audit-logs" ? auditLogsApi.list(bankId) : Promise.resolve(null)),
    [bankId, subTab],
  );
  const llm = useAsyncData(
    () => (subTab === "llm-requests" ? llmRequestsApi.list(bankId) : Promise.resolve(null)),
    [bankId, subTab],
  );
  const observations = useAsyncData(
    () => (subTab === "memory-defense" ? observationsApi.list(bankId) : Promise.resolve(null)),
    [bankId, subTab],
  );

  return (
    <div>
      {subTab === "general" && (
        <div>
          <PanelHeader title={t("generalTitle")} description={t("generalDescription")} />
          <PanelState loading={general.loading} error={general.error} onRetry={general.reload}>
            <dl className="rounded-[16px] border border-border bg-card px-4 py-2">
              <DetailRow label={t("fieldBankId")}>
                <IdChip id={readString(bank.data, "bank_id", bankId)} />
              </DetailRow>
              <DetailRow label={t("fieldName")}>
                {readString(bank.data, "name") || t("unset")}
              </DetailRow>
              <DetailRow label={t("fieldMission")}>
                {readString(bank.data, "mission") || t("unset")}
              </DetailRow>
              <DetailRow label={t("fieldCreated")}>
                {readString(bank.data, "created_at")
                  ? formatRelativeTime(readString(bank.data, "created_at"))
                  : t("unset")}
              </DetailRow>
            </dl>
            <PanelState loading={aliases.loading} error={aliases.error} onRetry={aliases.reload}>
              <AliasList payload={aliases.data} />
            </PanelState>
          </PanelState>
        </div>
      )}

      {subTab === "configuration" && (
        <div>
          <PanelHeader title={t("configurationTitle")} description={t("configurationDescription")} />
          <PanelState loading={config.loading} error={config.error} onRetry={config.reload}>
            {config.data && (
              <BankConfigEditor
                bankId={bankId}
                initial={config.data}
                onSaved={(next) => config.replace(next)}
              />
            )}
          </PanelState>
          <div className="mt-6">
            <p className="text-[12px] text-muted-foreground">{t("modelsMovedHint")}</p>
          </div>
        </div>
      )}

      {subTab === "webhooks" && (
        <div>
          <PanelHeader title={t("webhooksTitle")} description={t("webhooksDescription")} />
          <PanelState loading={webhooks.loading} error={webhooks.error} onRetry={webhooks.reload}>
            <RecordTable payload={webhooks.data} emptyLabel={t("noWebhooks")} />
          </PanelState>
        </div>
      )}

      {subTab === "audit-logs" && (
        <div>
          <PanelHeader title={t("auditLogsTitle")} description={t("auditLogsDescription")} />
          <PanelState loading={logs.loading} error={logs.error} onRetry={logs.reload}>
            <RecordTable payload={logs.data} emptyLabel={t("noLogs")} />
          </PanelState>
        </div>
      )}

      {subTab === "llm-requests" && (
        <div>
          <PanelHeader title={t("llmRequestsTitle")} description={t("llmRequestsDescription")} />
          <PanelState loading={llm.loading} error={llm.error} onRetry={llm.reload}>
            <LlmRequestStats payload={llm.data} />
          </PanelState>
        </div>
      )}

      {subTab === "memory-defense" && (
        <div>
          <PanelHeader title={t("memoryDefenseTitle")} description={t("memoryDefenseDescription")} />
          <h3 className="mb-2 text-[13px] font-semibold">{t("directivesTitle")}</h3>
          <DirectivesView bankId={bankId} />
          <div className="mt-6">
            <h3 className="mb-2 text-[13px] font-semibold">{t("observationsTitle")}</h3>
            <PanelState
              loading={observations.loading}
              error={observations.error}
              onRetry={observations.reload}
            >
              <ObservationsList payload={observations.data} />
            </PanelState>
          </div>
        </div>
      )}
    </div>
  );
}

function AliasList({ payload }: { payload: unknown }) {
  const t = useTranslations("panels.profile");
  const rows = readItems<{ alias?: string; primary?: boolean }>(payload, "aliases");
  if (rows.length === 0) {
    return <p className="mt-4 text-[13px] text-muted-foreground">{t("noAliases")}</p>;
  }
  return (
    <div className="mt-4">
      <h3 className="mb-2 text-[13px] font-semibold">{t("aliasesTitle")}</h3>
      <div className="flex flex-wrap gap-1.5">
        {rows.map((row, index) => (
          <IdChip
            key={row.alias ?? index}
            id={row.alias ?? ""}
            active={row.primary === true}
          />
        ))}
      </div>
    </div>
  );
}

/**
 * A minimal table for the log-style payloads.
 *
 * These responses vary between deployments, so only the fields the shared shape
 * guarantees are given columns; the rest stays reachable through the row's
 * title.
 */
function RecordTable({ payload, emptyLabel }: { payload: unknown; emptyLabel: string }) {
  const t = useTranslations("panels.profile");
  const rows = readItems<Record<string, unknown>>(payload);
  if (rows.length === 0) {
    return <p className="text-[13px] text-muted-foreground">{emptyLabel}</p>;
  }
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>{t("columns.id")}</TableHead>
          <TableHead>{t("columns.summary")}</TableHead>
          <TableHead>{t("columns.tags")}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.slice(0, 50).map((row, index) => {
          const id = String(row.id ?? row.operation_id ?? index);
          const summary = String(row.operation_type ?? row.action ?? row.type ?? "");
          const tags = Array.isArray(row.tags) ? (row.tags as string[]) : [];
          return (
            <TableRow key={id} title={JSON.stringify(row)}>
              <TableCell className="font-mono text-[11px] text-muted-foreground">{id}</TableCell>
              <TableCell className="text-[13px]">{summary || "—"}</TableCell>
              <TableCell>
                <TagList tags={tags.slice(0, 3)} size="xs" />
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}
