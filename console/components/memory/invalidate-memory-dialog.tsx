"use client";

import { useState } from "react";

import { useTranslations } from "../../i18n";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../ui/dialog";

/**
 * Confirmation for retiring a memory.
 *
 * Invalidation is a soft retire rather than a delete: the memory stops being
 * recalled but the record survives, so the wording says what actually happens.
 * The reason is optional and exists so a later reader can tell why it was
 * retired.
 */
export function InvalidateMemoryDialog({
  open,
  onOpenChange,
  onConfirm,
  busy,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onConfirm: (reason?: string) => void;
  busy?: boolean;
}) {
  const t = useTranslations("memoryPanel.curation");
  const [reason, setReason] = useState("");

  // Clear the reason when the dialog closes, so reopening never shows a stale
  // one from a previous memory.
  const handleOpenChange = (next: boolean) => {
    if (!next) setReason("");
    onOpenChange(next);
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t("invalidateTitle")}</DialogTitle>
          <DialogDescription>{t("invalidateExplain")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-1.5">
          <label
            htmlFor="invalidate-reason"
            className="text-xs font-medium text-muted-foreground"
          >
            {t("reasonLabel")}
          </label>
          <Input
            id="invalidate-reason"
            value={reason}
            onChange={(event) => setReason(event.target.value)}
            placeholder={t("reasonPlaceholder")}
            autoFocus
            onKeyDown={(event) => {
              if (event.key === "Enter") onConfirm(reason.trim() || undefined);
            }}
          />
        </div>
        <DialogFooter>
          <Button variant="ghost" disabled={busy} onClick={() => handleOpenChange(false)}>
            {t("cancel")}
          </Button>
          <Button
            variant="destructive"
            disabled={busy}
            onClick={() => onConfirm(reason.trim() || undefined)}
          >
            {t("invalidate")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}