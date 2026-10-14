"use client";

import { useState } from "react";
import { Plus, Trash2 } from "lucide-react";

import { directivesApi } from "../../../../lib/client/endpoints";
import { readItems, useAsyncData } from "../../../../lib/use-async-data";
import { useTranslations } from "../../../../i18n";
import { Button } from "../../../../components/ui/button";
import { Input } from "../../../../components/ui/input";
import { Label } from "../../../../components/ui/label";
import { Spinner } from "../../../../components/ui/spinner";
import { Switch } from "../../../../components/ui/switch";
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

type DirectiveRow = {
  id?: string;
  name?: string;
  content?: string;
  priority?: number;
  is_active?: boolean;
  tags?: string[];
};

/**
 * Directives, with their lifecycle.
 *
 * A directive is a standing instruction the bank applies when it reflects, so
 * an operator adds, edits, enables and removes them rather than only reading
 * them. Priority orders them, because two directives can conflict and the
 * higher one wins.
 */
export function DirectivesView({ bankId }: { bankId: string }) {
  const t = useTranslations("panels.profile");
  const state = useAsyncData(() => directivesApi.list(bankId), [bankId]);
  const [editing, setEditing] = useState<DirectiveRow | null>(null);
  const [creating, setCreating] = useState(false);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);

  const rows = readItems<DirectiveRow>(state.data, "directives");

  async function toggleActive(row: DirectiveRow) {
    if (!row.id) return;
    setBusyId(row.id);
    setActionError(null);
    try {
      await directivesApi.update(bankId, row.id, { is_active: !row.is_active });
      state.reload();
    } catch {
      setActionError(t("actionFailed"));
    } finally {
      setBusyId(null);
    }
  }

  async function remove(id: string) {
    setBusyId(id);
    setActionError(null);
    try {
      await directivesApi.remove(bankId, id);
      state.reload();
    } catch {
      setActionError(t("actionFailed"));
    } finally {
      setBusyId(null);
    }
  }

  if (state.loading) {
    return (
      <div className="flex justify-center py-16">
        <Spinner size="lg" />
      </div>
    );
  }

  if (state.error) {
    return (
      <div className="flex flex-col items-center gap-3 py-16">
        <p className="text-[13px] text-muted-foreground">{t("loadFailed")}</p>
        <Button variant="outline" size="sm" onClick={state.reload}>
          {t("retry")}
        </Button>
      </div>
    );
  }

  return (
    <div>
      <div className="mb-3 flex items-center justify-between gap-3">
        <span className="text-[12px] text-muted-foreground">
          {t("directiveCount", { total: rows.length })}
        </span>
        <Button type="button" size="sm" onClick={() => setCreating(true)}>
          <Plus className="h-3.5 w-3.5" />
          {t("newDirective")}
        </Button>
      </div>

      {actionError && (
        <p role="alert" className="mb-3 text-[12px] text-destructive">
          {actionError}
        </p>
      )}

      {rows.length === 0 ? (
        <p className="text-[13px] text-muted-foreground">{t("noDirectives")}</p>
      ) : (
        <ul className="flex flex-col gap-2">
          {rows.map((row, index) => {
            const id = row.id ?? String(index);
            const busy = busyId === id;
            return (
              <li
                key={id}
                className="flex items-start justify-between gap-3 rounded-[12px] border border-border bg-card px-3 py-2.5"
              >
                <div className="min-w-0">
                  <div className="flex items-baseline gap-2">
                    <button
                      type="button"
                      onClick={() => setEditing(row)}
                      className="truncate text-left text-[13px] font-medium hover:underline"
                    >
                      {row.name || t("untitled")}
                    </button>
                    <span className="shrink-0 font-mono text-[11px] text-muted-foreground">
                      {t("priority", { value: row.priority ?? 0 })}
                    </span>
                  </div>
                  {row.content && (
                    <p className="mt-0.5 line-clamp-2 break-words text-[12px] text-muted-foreground">
                      {row.content}
                    </p>
                  )}
                  {(row.tags ?? []).length > 0 && (
                    <div className="mt-1">
                      <TagList tags={(row.tags ?? []).slice(0, 4)} size="xs" />
                    </div>
                  )}
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  {busy && <Spinner />}
                  <Switch
                    checked={row.is_active === true}
                    onCheckedChange={() => void toggleActive(row)}
                    disabled={busy}
                    aria-label={t("toggleDirective")}
                  />
                  <AlertDialog>
                    <AlertDialogTrigger asChild>
                      <Button
                        type="button"
                        size="sm"
                        variant="outline"
                        disabled={busy}
                        title={t("deleteDirective")}
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                      </Button>
                    </AlertDialogTrigger>
                    <AlertDialogContent>
                      <AlertDialogHeader>
                        <AlertDialogTitle>{t("deleteDirectiveTitle")}</AlertDialogTitle>
                        <AlertDialogDescription>
                          {t("deleteDirectiveDescription")}
                        </AlertDialogDescription>
                      </AlertDialogHeader>
                      <AlertDialogFooter>
                        <AlertDialogCancel>{t("cancel")}</AlertDialogCancel>
                        <AlertDialogAction onClick={() => void remove(id)}>
                          {t("deleteDirectiveAction")}
                        </AlertDialogAction>
                      </AlertDialogFooter>
                    </AlertDialogContent>
                  </AlertDialog>
                </div>
              </li>
            );
          })}
        </ul>
      )}

      <DirectiveDialog
        bankId={bankId}
        editing={editing}
        creating={creating}
        onClose={() => {
          setEditing(null);
          setCreating(false);
        }}
        onSaved={() => {
          setEditing(null);
          setCreating(false);
          state.reload();
        }}
      />
    </div>
  );
}

/** The create and edit form. One dialog serves both, because the fields match. */
function DirectiveDialog({
  bankId,
  editing,
  creating,
  onClose,
  onSaved,
}: {
  bankId: string;
  editing: DirectiveRow | null;
  creating: boolean;
  onClose: () => void;
  onSaved: () => void;
}) {
  const t = useTranslations("panels.profile");
  const open = creating || editing !== null;
  const [fields, setFields] = useState({ name: "", content: "", priority: "0", tags: "" });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState(false);

  // Seed the form when the dialog opens for a specific record.
  const [seededFor, setSeededFor] = useState<string | null>(null);
  const seedKey = creating ? "new" : (editing?.id ?? null);
  if (open && seedKey !== seededFor) {
    setSeededFor(seedKey);
    setFields({
      name: editing?.name ?? "",
      content: editing?.content ?? "",
      priority: String(editing?.priority ?? 0),
      tags: (editing?.tags ?? []).join(", "),
    });
  }

  async function submit() {
    setSaving(true);
    setError(false);
    const payload = {
      name: fields.name,
      content: fields.content,
      priority: Number(fields.priority) || 0,
      tags: fields.tags
        .split(",")
        .map((tag) => tag.trim())
        .filter(Boolean),
    };
    try {
      if (editing?.id) {
        await directivesApi.update(bankId, editing.id, payload);
      } else {
        await directivesApi.create(bankId, { ...payload, is_active: true });
      }
      onSaved();
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
        if (!next) {
          setSeededFor(null);
          onClose();
        }
      }}
    >
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{editing ? t("editDirectiveTitle") : t("newDirectiveTitle")}</DialogTitle>
        </DialogHeader>
        <div className="flex flex-col gap-4">
          <div className="grid gap-1.5">
            <Label htmlFor="directive-name">{t("fieldDirectiveName")}</Label>
            <Input
              id="directive-name"
              value={fields.name}
              onChange={(event) => setFields({ ...fields, name: event.target.value })}
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="directive-content">{t("fieldDirectiveContent")}</Label>
            <Textarea
              id="directive-content"
              rows={4}
              value={fields.content}
              onChange={(event) => setFields({ ...fields, content: event.target.value })}
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="directive-priority">{t("fieldDirectivePriority")}</Label>
            <Input
              id="directive-priority"
              type="number"
              value={fields.priority}
              onChange={(event) => setFields({ ...fields, priority: event.target.value })}
            />
            <p className="text-[11px] text-muted-foreground">{t("fieldDirectivePriorityHint")}</p>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="directive-tags">{t("fieldDirectiveTags")}</Label>
            <Input
              id="directive-tags"
              value={fields.tags}
              onChange={(event) => setFields({ ...fields, tags: event.target.value })}
              placeholder={t("fieldDirectiveTagsPlaceholder")}
            />
          </div>
          {error && (
            <p role="alert" className="text-[12px] text-destructive">
              {t("saveFailed")}
            </p>
          )}
          <div className="flex gap-2">
            <Button
              type="button"
              onClick={submit}
              disabled={saving || fields.name.trim() === "" || fields.content.trim() === ""}
            >
              {saving ? t("saving") : t("save")}
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