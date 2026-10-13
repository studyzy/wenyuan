"use client";

import { useCallback, useState } from "react";
import { Pencil } from "lucide-react";

import { useTranslations } from "../../i18n";
import { chunksApi, documentsApi, memoriesApi } from "../../lib/client/endpoints";
import { useAsyncData, readItems, readString } from "../../lib/use-async-data";
import { formatAbsoluteDateTime, formatRelativeTime } from "../../lib/relative-time";
import { documentHarness } from "../../lib/harness-logo";
import { Button } from "../ui/button";
import { Spinner } from "../ui/spinner";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../ui/tabs";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "../ui/dialog";
import { EntityChip, MetadataChip, TagList } from "../ui/facet-chip";
import { HarnessLogo } from "../ui/harness-logo";
import { EditMemoryForm, type EditMemoryFields } from "./edit-memory-form";
import { InvalidateMemoryDialog } from "./invalidate-memory-dialog";

export interface MemoryDetail {
  id: string;
  text: string;
  context: string | null;
  date: string;
  type: string;
  mentioned_at: string | null;
  occurred_start: string | null;
  occurred_end: string | null;
  entities: string[];
  document_id: string | null;
  chunk_id: string | null;
  tags: string[];
  metadata: Record<string, unknown> | null;
  state?: "valid" | "invalidated";
  invalidation_reason?: string | null;
  invalidated_at?: string | null;
  edited_at?: string | null;
}

/**
 * One memory in full, with the two things a reader can do to it.
 *
 * The memory tab shows what was extracted and lets it be corrected; the history
 * tab shows what it changed from. Curation is a soft retire rather than a
 * delete, so the control says so and asks for an optional reason.
 *
 * Curation and edits both re-read the memory afterwards rather than patching
 * local state, because the upstream may normalise what was sent — tags folded,
 * entities de-duplicated — and showing the un-normalised copy would drift from
 * what was stored.
 */
export function MemoryDetailModal({
  memoryId,
  bankId,
  onClose,
  initialTab,
  onChanged,
}: {
  memoryId: string | null;
  bankId: string;
  onClose: () => void;
  initialTab?: string;
  onChanged?: () => void;
}) {
  const t = useTranslations("memoryPanel.detail");
  const [tab, setTab] = useState(initialTab ?? "memory");
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState(false);
  const [askingReason, setAskingReason] = useState(false);
  const [nonce, setNonce] = useState(0);

  const memoryState = useAsyncData(
    () => (memoryId ? memoriesApi.get(bankId, memoryId) : Promise.resolve(null)),
    [memoryId, bankId, nonce],
  );
  const memory = memoryState.data as MemoryDetail | null;

  const history = useAsyncData(
    () =>
      memoryId
        ? chunksApi.history(bankId, memoryId)
        : Promise.resolve(null),
    [memoryId, bankId, tab],
  );

  const document = useAsyncData(
    () =>
      memory?.document_id
        ? documentsApi.get(memory.document_id, bankId)
        : Promise.resolve(null),
    [memory?.document_id, bankId],
  );
  const chunk = useAsyncData(
    () => (memory?.chunk_id ? chunksApi.get(memory.chunk_id, bankId) : Promise.resolve(null)),
    [memory?.chunk_id, bankId],
  );

  const refresh = useCallback(() => {
    setNonce((value) => value + 1);
    onChanged?.();
  }, [onChanged]);

  const curate = async (reason?: string) => {
    if (!memory) return;
    setBusy(true);
    try {
      const invalidating = memory.state !== "invalidated";
      await memoriesApi.update(bankId, memory.id, {
        state: invalidating ? "invalidated" : "valid",
        reason: reason ?? null,
      });
      setAskingReason(false);
      refresh();
    } finally {
      setBusy(false);
    }
  };

  const saveEdit = async (fields: EditMemoryFields) => {
    if (!memory) return;
    setBusy(true);
    try {
      await memoriesApi.update(bankId, memory.id, {
        text: fields.text,
        context: fields.context || null,
        fact_type: fields.factType,
        occurred_start: fields.occurredStart || null,
        occurred_end: fields.occurredEnd || null,
        entities: fields.entities,
        resolve_entities: false,
      });
      setEditing(false);
      refresh();
    } finally {
      setBusy(false);
    }
  };

  const open = memoryId !== null;

  return (
    <>
      <Dialog
        open={open && !editing}
        onOpenChange={(next) => {
          if (!next) onClose();
        }}
      >
        <DialogContent className="max-w-3xl">
          <DialogHeader>
            <DialogTitle>{t("title")}</DialogTitle>
          </DialogHeader>

          {memoryState.loading && (
            <div className="flex justify-center py-16">
              <Spinner size="lg" />
            </div>
          )}

          {memoryState.error && (
            <div className="flex flex-col items-center gap-3 py-16">
              <p className="text-[13px] text-muted-foreground">{t("loadFailed")}</p>
              <Button variant="outline" size="sm" onClick={memoryState.reload}>
                {t("retry")}
              </Button>
            </div>
          )}

          {memory && (
            <Tabs value={tab} onValueChange={setTab}>
              <div className="flex items-center justify-between gap-3">
                <TabsList>
                  <TabsTrigger value="memory">{t("tabMemory")}</TabsTrigger>
                  <TabsTrigger value="history">{t("tabHistory")}</TabsTrigger>
                </TabsList>
                <div className="flex items-center gap-2">
                  <Button variant="outline" size="sm" onClick={() => setEditing(true)}>
                    <Pencil className="mr-1.5 h-3.5 w-3.5" />
                    {t("edit")}
                  </Button>
                  <Button
                    variant={memory.state === "invalidated" ? "outline" : "destructive"}
                    size="sm"
                    disabled={busy}
                    onClick={() => {
                      // Restoring is immediate; retiring asks for a reason first.
                      if (memory.state === "invalidated") void curate();
                      else setAskingReason(true);
                    }}
                  >
                    {memory.state === "invalidated" ? t("restore") : t("invalidate")}
                  </Button>
                </div>
              </div>

              <TabsContent value="memory" className="space-y-4">
                {memory.state === "invalidated" && (
                  <div className="rounded-lg border border-destructive/50 bg-destructive/5 px-3 py-2 text-[12px]">
                    <p className="font-medium text-destructive">{t("invalidatedNotice")}</p>
                    {memory.invalidation_reason && (
                      <p className="mt-0.5 text-muted-foreground">
                        {t("reasonLabel")}: {memory.invalidation_reason}
                      </p>
                    )}
                  </div>
                )}

                <p className="whitespace-pre-wrap text-[14px] leading-relaxed text-foreground">
                  {memory.text}
                </p>

                <dl className="grid grid-cols-2 gap-x-4 gap-y-2 border-t border-border pt-3">
                  <Field label={t("fieldType")}>{memory.type}</Field>
                  <Field label={t("fieldMentioned")}>
                    {memory.mentioned_at ? formatRelativeTime(memory.mentioned_at) : "—"}
                  </Field>
                  <Field label={t("fieldOccurredStart")}>
                    {memory.occurred_start ? formatAbsoluteDateTime(memory.occurred_start) : "—"}
                  </Field>
                  <Field label={t("fieldOccurredEnd")}>
                    {memory.occurred_end ? formatAbsoluteDateTime(memory.occurred_end) : "—"}
                  </Field>
                  {memory.edited_at && (
                    <Field label={t("fieldEdited")}>{formatRelativeTime(memory.edited_at)}</Field>
                  )}
                </dl>

                {memory.context && (
                  <div className="border-t border-border pt-3">
                    <Field label={t("fieldContext")}>{memory.context}</Field>
                  </div>
                )}

                {memory.entities.length > 0 && (
                  <div className="border-t border-border pt-3">
                    <p className="mb-1.5 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
                      {t("fieldEntities")}
                    </p>
                    <div className="flex flex-wrap gap-1.5">
                      {memory.entities.map((entity) => (
                        <EntityChip key={entity} entity={entity} />
                      ))}
                    </div>
                  </div>
                )}

                {memory.tags.length > 0 && (
                  <div className="border-t border-border pt-3">
                    <TagList tags={memory.tags} showLabel />
                  </div>
                )}

                <CodingAgent metadata={memory.metadata} />

                {memory.metadata && Object.keys(memory.metadata).length > 0 && (
                  <div className="border-t border-border pt-3">
                    <p className="mb-1.5 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
                      {t("fieldMetadata")}
                    </p>
                    <div className="flex flex-wrap gap-1.5">
                      {Object.entries(memory.metadata).map(([key, value]) => (
                        <MetadataChip key={key} entryKey={key} value={String(value)} truncate />
                      ))}
                    </div>
                  </div>
                )}

                {(memory.document_id || memory.chunk_id) && (
                  <div className="space-y-2 border-t border-border pt-3">
                    {memory.document_id && (
                      <div>
                        <p className="mb-1 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
                          {t("fieldDocument")}
                        </p>
                        <p className="truncate font-mono text-[11px] text-muted-foreground">
                          {readString(document.data, "filename", memory.document_id)}
                        </p>
                      </div>
                    )}
                    {memory.chunk_id && (
                      <div>
                        <p className="mb-1 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
                          {t("fieldChunk")}
                        </p>
                        <p className="truncate font-mono text-[11px] text-muted-foreground">
                          {readString(chunk.data, "text", memory.chunk_id)}
                        </p>
                      </div>
                    )}
                  </div>
                )}
              </TabsContent>

              <TabsContent value="history">
                {history.loading ? (
                  <div className="flex justify-center py-12">
                    <Spinner />
                  </div>
                ) : (
                  <ul className="divide-y divide-border">
                    {readItems<{ text?: string; changed_at?: string; reason?: string }>(
                      history.data,
                      "history",
                    ).map((entry, index) => (
                      <li key={index} className="py-2.5">
                        <p className="text-[13px] text-foreground">{entry.text ?? "—"}</p>
                        <p className="text-[11px] text-muted-foreground">
                          {entry.changed_at ? formatRelativeTime(entry.changed_at) : ""}
                          {entry.reason ? ` · ${entry.reason}` : ""}
                        </p>
                      </li>
                    ))}
                  </ul>
                )}
              </TabsContent>
            </Tabs>
          )}
        </DialogContent>
      </Dialog>

      <Dialog open={editing} onOpenChange={setEditing}>
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle>{t("editTitle")}</DialogTitle>
          </DialogHeader>
          {memory && (
            <EditMemoryForm
              memory={memory}
              busy={busy}
              onCancel={() => setEditing(false)}
              onSave={(fields) => void saveEdit(fields)}
            />
          )}
        </DialogContent>
      </Dialog>

      <InvalidateMemoryDialog
        open={askingReason}
        onOpenChange={setAskingReason}
        onConfirm={(reason) => void curate(reason)}
        busy={busy}
      />
    </>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-0.5">
      <dt className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
        {label}
      </dt>
      <dd className="text-[13px] text-foreground">{children}</dd>
    </div>
  );
}

/**
 * The coding agent behind a fact, when the document recorded one.
 *
 * It is read from the copied document metadata rather than by loading the
 * document, so the mark is available without a second request.
 */
function CodingAgent({ metadata }: { metadata: Record<string, unknown> | null }) {
  const t = useTranslations("memoryPanel.detail");
  const harness = documentHarness(metadata, null);
  if (!harness) return null;
  return (
    <div className="flex items-center gap-2 border-t border-border pt-3">
      <span className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
        {t("fieldAgent")}
      </span>
      <HarnessLogo harness={harness} titlePrefix={t("fieldAgent")} />
      <span className="text-[12px] text-muted-foreground">{harness}</span>
    </div>
  );
}