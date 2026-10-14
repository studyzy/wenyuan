"use client";

import { useState } from "react";
import { Plus, RefreshCw, Trash2 } from "lucide-react";

import { mentalModelsApi } from "../../../../lib/client/endpoints";
import { readItems, readString, useAsyncData } from "../../../../lib/use-async-data";
import { formatRelativeTime } from "../../../../lib/relative-time";
import { useTranslations } from "../../../../i18n";
import { Button } from "../../../../components/ui/button";
import { Input } from "../../../../components/ui/input";
import { Label } from "../../../../components/ui/label";
import { Spinner } from "../../../../components/ui/spinner";
import { TagList } from "../../../../components/ui/facet-chip";
import { Textarea } from "../../../../components/ui/textarea";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "../../../../components/ui/alert-dialog";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "../../../../components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "../../../../components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "../../../../components/ui/table";
import { DetailRow, PanelState } from "./panel-state";

/**
 * The mental-model subtypes the route accepts. A pinned note and a structural
 * note share one table and differ only by this value, so the reader picks one
 * rather than the console inferring it.
 */
const SUBTYPES = ["structural", "emergent", "pinned", "learned"] as const;
type Subtype = (typeof SUBTYPES)[number];

type MentalModelRow = {
  id?: string;
  name?: string;
  description?: string;
  subtype?: string;
  tags?: string[];
  last_refreshed_at?: string;
};

const DEFAULT_MAX_TOKENS = 2000;

/**
 * Mental models.
 *
 * The list is a table with each model's actions beside it, because a model is
 * something an operator maintains rather than reads once: refreshing pulls new
 * evidence, clearing drops it, and deleting removes the model. Editing opens in
 * a dialog so the list stays in view.
 */
export function MentalModelsView({ bankId }: { bankId: string }) {
  const t = useTranslations("panels.knowledge");
  const state = useAsyncData(() => mentalModelsApi.list(bankId), [bankId]);
  const [openId, setOpenId] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);

  const rows = readItems<MentalModelRow>(state.data, "mental_models");

  async function runAction(id: string, action: () => Promise<unknown>) {
    setBusyId(id);
    setActionError(null);
    try {
      await action();
      state.reload();
    } catch {
      setActionError(t("actionFailed"));
    } finally {
      setBusyId(null);
    }
  }

  return (
    <div>
      <div className="mb-4 flex items-start justify-between gap-4">
        <div className="flex flex-col gap-0.5">
          <h2 className="text-[16px] font-semibold leading-[22px] tracking-tight text-foreground">
            {t("modelsTitle")}
          </h2>
          <p className="text-[13px] text-muted-foreground">{t("modelsDescription")}</p>
        </div>
        <Button type="button" size="sm" onClick={() => setCreating(true)}>
          <Plus className="h-3.5 w-3.5" />
          {t("createModel")}
        </Button>
      </div>

      {actionError && (
        <p role="alert" className="mb-3 text-[12px] text-destructive">
          {actionError}
        </p>
      )}

      <PanelState
        loading={state.loading}
        error={state.error}
        empty={rows.length === 0}
        emptyLabel={t("noModels")}
        onRetry={state.reload}
      >
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t("columns.name")}</TableHead>
              <TableHead>{t("columns.subtype")}</TableHead>
              <TableHead>{t("columns.tags")}</TableHead>
              <TableHead>{t("columns.refreshed")}</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((row, index) => {
              const id = row.id ?? String(index);
              const busy = busyId === id;
              return (
                <TableRow key={id}>
                  <TableCell className="max-w-[320px]">
                    <button
                      type="button"
                      onClick={() => setOpenId(id)}
                      className="truncate text-left text-[13px] font-medium hover:underline"
                    >
                      {row.name || t("untitled")}
                    </button>
                    {row.description && (
                      <span className="block truncate text-[11px] text-muted-foreground">
                        {row.description}
                      </span>
                    )}
                  </TableCell>
                  <TableCell className="text-[12px] text-muted-foreground">
                    {row.subtype ?? "—"}
                  </TableCell>
                  <TableCell>
                    <TagList tags={(row.tags ?? []).slice(0, 3)} size="xs" />
                  </TableCell>
                  <TableCell className="whitespace-nowrap text-[12px] text-muted-foreground">
                    {row.last_refreshed_at ? formatRelativeTime(row.last_refreshed_at) : "—"}
                  </TableCell>
                  <TableCell>
                    <div className="flex justify-end gap-1">
                      <Button
                        type="button"
                        size="sm"
                        variant="outline"
                        disabled={busy}
                        onClick={() => runAction(id, () => mentalModelsApi.refresh(bankId, id))}
                        title={t("refreshModel")}
                      >
                        {busy ? <Spinner /> : <RefreshCw className="h-3.5 w-3.5" />}
                        {t("refreshModel")}
                      </Button>
                      <DeleteModelButton
                        onConfirm={() =>
                          runAction(id, () => mentalModelsApi.remove(bankId, id))
                        }
                        disabled={busy}
                      />
                    </div>
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </PanelState>

      <ModelEditorDialog
        bankId={bankId}
        modelId={openId}
        onClose={() => setOpenId(null)}
        onSaved={() => {
          setOpenId(null);
          state.reload();
        }}
      />
      <ModelCreateDialog
        bankId={bankId}
        open={creating}
        onClose={() => setCreating(false)}
        onCreated={() => {
          setCreating(false);
          state.reload();
        }}
      />
    </div>
  );
}

/** Deleting a model is confirmed, because the model and its history go with it. */
function DeleteModelButton({
  onConfirm,
  disabled,
}: {
  onConfirm: () => void;
  disabled: boolean;
}) {
  const t = useTranslations("panels.knowledge");
  return (
    <AlertDialog>
      <AlertDialogTrigger asChild>
        <Button type="button" size="sm" variant="outline" disabled={disabled} title={t("deleteModel")}>
          <Trash2 className="h-3.5 w-3.5" />
        </Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("deleteModelTitle")}</AlertDialogTitle>
          <AlertDialogDescription>{t("deleteModelDescription")}</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>{t("cancel")}</AlertDialogCancel>
          <AlertDialogAction onClick={onConfirm}>{t("deleteModelAction")}</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

/** A dialog showing one model's fields for editing. */
function ModelEditorDialog({
  bankId,
  modelId,
  onClose,
  onSaved,
}: {
  bankId: string;
  modelId: string | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const t = useTranslations("panels.knowledge");
  const detail = useAsyncData(
    () => (modelId ? mentalModelsApi.get(bankId, modelId) : Promise.resolve(null)),
    [bankId, modelId],
  );
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState(false);
  const [draft, setDraft] = useState<Record<string, string> | null>(null);

  // The draft is seeded from the loaded model the first time it arrives, so
  // typing does not fight the fetch.
  if (detail.data && draft === null) {
    setDraft({
      name: readString(detail.data, "name"),
      description: readString(detail.data, "description"),
      source_query: readString(detail.data, "source_query"),
      content: readString(detail.data, "content"),
    });
  }

  async function save() {
    if (!modelId || !draft) return;
    setSaving(true);
    setError(false);
    try {
      await mentalModelsApi.update(bankId, modelId, draft);
      onSaved();
    } catch {
      setError(true);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Dialog
      open={modelId !== null}
      onOpenChange={(open) => {
        if (!open) {
          setDraft(null);
          onClose();
        }
      }}
    >
      <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{t("editModelTitle")}</DialogTitle>
        </DialogHeader>
        <PanelState loading={detail.loading} error={detail.error} onRetry={detail.reload}>
          {draft && (
            <div className="flex flex-col gap-4">
              <Field
                id="model-name"
                label={t("fieldName")}
                value={draft.name}
                onChange={(value) => setDraft({ ...draft, name: value })}
              />
              <Field
                id="model-description"
                label={t("fieldDescription")}
                value={draft.description}
                onChange={(value) => setDraft({ ...draft, description: value })}
              />
              <Field
                id="model-source-query"
                label={t("fieldSourceQuery")}
                value={draft.source_query}
                onChange={(value) => setDraft({ ...draft, source_query: value })}
                hint={t("fieldSourceQueryHint")}
              />
              <div className="grid gap-1.5">
                <Label htmlFor="model-content">{t("fieldContent")}</Label>
                <Textarea
                  id="model-content"
                  rows={8}
                  value={draft.content}
                  onChange={(event) => setDraft({ ...draft, content: event.target.value })}
                />
              </div>
              <DetailRow label={t("fieldRefreshed")}>
                {readString(detail.data, "last_refreshed_at")
                  ? formatRelativeTime(readString(detail.data, "last_refreshed_at"))
                  : t("neverRefreshed")}
              </DetailRow>
              {error && (
                <p role="alert" className="text-[12px] text-destructive">
                  {t("saveFailed")}
                </p>
              )}
              <div className="flex gap-2">
                <Button type="button" onClick={save} disabled={saving}>
                  {saving ? t("saving") : t("save")}
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => {
                    setDraft(null);
                    onClose();
                  }}
                >
                  {t("cancel")}
                </Button>
              </div>
            </div>
          )}
        </PanelState>
      </DialogContent>
    </Dialog>
  );
}

/** A dialog for creating a model, where the subtype and name are required. */
function ModelCreateDialog({
  bankId,
  open,
  onClose,
  onCreated,
}: {
  bankId: string;
  open: boolean;
  onClose: () => void;
  onCreated: () => void;
}) {
  const t = useTranslations("panels.knowledge");
  const [subtype, setSubtype] = useState<Subtype>("pinned");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [sourceQuery, setSourceQuery] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState(false);

  async function create() {
    setSaving(true);
    setError(false);
    try {
      await mentalModelsApi.create(bankId, {
        subtype,
        name,
        description,
        source_query: sourceQuery,
        max_tokens: DEFAULT_MAX_TOKENS,
      });
      setName("");
      setDescription("");
      setSourceQuery("");
      onCreated();
    } catch {
      setError(true);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
    >
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("createModelTitle")}</DialogTitle>
        </DialogHeader>
        <div className="flex flex-col gap-4">
          <div className="grid gap-1.5">
            <Label htmlFor="new-model-subtype">{t("fieldSubtype")}</Label>
            <Select value={subtype} onValueChange={(value) => setSubtype(value as Subtype)}>
              <SelectTrigger id="new-model-subtype">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {SUBTYPES.map((value) => (
                  <SelectItem key={value} value={value}>
                    {value}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-[11px] text-muted-foreground">{t("fieldSubtypeHint")}</p>
          </div>
          <Field
            id="new-model-name"
            label={t("fieldName")}
            value={name}
            onChange={setName}
          />
          <Field
            id="new-model-description"
            label={t("fieldDescription")}
            value={description}
            onChange={setDescription}
          />
          <Field
            id="new-model-source-query"
            label={t("fieldSourceQuery")}
            value={sourceQuery}
            onChange={setSourceQuery}
            hint={t("fieldSourceQueryHint")}
          />
          {error && (
            <p role="alert" className="text-[12px] text-destructive">
              {t("createFailed")}
            </p>
          )}
          <div className="flex gap-2">
            <Button
              type="button"
              onClick={create}
              disabled={saving || name.trim() === "" || sourceQuery.trim() === ""}
            >
              {saving ? t("saving") : t("createModelAction")}
            </Button>
            <Button type="button" variant="outline" onClick={onClose}>
              {t("cancel")}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

/** A labelled text input. */
function Field({
  id,
  label,
  value,
  onChange,
  hint,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (value: string) => void;
  hint?: string;
}) {
  return (
    <div className="grid gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        value={value}
        onChange={(event) => onChange(event.target.value)}
      />
      {hint && <p className="text-[11px] text-muted-foreground">{hint}</p>}
    </div>
  );
}