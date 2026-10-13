"use client";

import { useState } from "react";
import { Check } from "lucide-react";

import { useTranslations } from "../../i18n";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Spinner } from "../ui/spinner";
import { EntityChip } from "../ui/facet-chip";

export interface EditMemoryFields {
  text: string;
  context: string;
  factType: "world" | "experience";
  /** A date input value, or an empty string to clear the bound. */
  occurredStart: string;
  occurredEnd: string;
  entities: string[];
}

/** An ISO timestamp reduced to the date-input form, or empty when absent. */
function toDateInput(iso: string | null | undefined): string {
  if (!iso) return "";
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? "" : date.toISOString().slice(0, 10);
}

/**
 * Correct what was extracted from a memory.
 *
 * Everything the extractor inferred is editable: the text, the fact type, the
 * occurred bounds, the context and the entities. Entities are edited as
 * removable chips, and clearing them all detaches every entity from the fact,
 * which is a meaningful edit rather than an empty state.
 *
 * The entity list arrives in two shapes depending on the endpoint — an array, or
 * a comma-joined string on a graph node — so both are accepted and normalised.
 */
export function EditMemoryForm({
  memory,
  busy,
  onCancel,
  onSave,
}: {
  memory: {
    text: string;
    context?: string | null;
    type?: string;
    fact_type?: string;
    occurred_start?: string | null;
    occurred_end?: string | null;
    entities?: string[] | string | null;
  };
  busy?: boolean;
  onCancel: () => void;
  onSave: (fields: EditMemoryFields) => void;
}) {
  const t = useTranslations("memoryPanel.edit");

  const [text, setText] = useState(memory.text ?? "");
  const [context, setContext] = useState(memory.context ?? "");
  const [factType, setFactType] = useState<"world" | "experience">(
    (memory.fact_type ?? memory.type) === "experience" ? "experience" : "world",
  );
  const [occurredStart, setOccurredStart] = useState(toDateInput(memory.occurred_start));
  const [occurredEnd, setOccurredEnd] = useState(toDateInput(memory.occurred_end));
  const [entities, setEntities] = useState<string[]>(
    Array.isArray(memory.entities)
      ? memory.entities
      : memory.entities
        ? String(memory.entities).split(", ").filter(Boolean)
        : [],
  );
  const [entityDraft, setEntityDraft] = useState("");

  const addEntity = (raw: string) => {
    const name = raw.trim();
    if (!name) return;
    // De-duplicate case-insensitively, matching how the server normalises.
    if (!entities.some((entity) => entity.toLowerCase() === name.toLowerCase())) {
      setEntities([...entities, name]);
    }
    setEntityDraft("");
  };

  return (
    <div className="space-y-3">
      <div>
        <label
          htmlFor="edit-memory-text"
          className="text-[11px] font-semibold uppercase tracking-wide text-muted-foreground"
        >
          {t("fullText")}
        </label>
        <textarea
          id="edit-memory-text"
          value={text}
          onChange={(event) => setText(event.target.value)}
          className="mt-1 min-h-[100px] w-full resize-y rounded-lg border border-border bg-muted/50 p-3 text-sm leading-relaxed text-card-foreground"
          autoFocus
        />
      </div>

      <div className="grid grid-cols-2 gap-3">
        <div>
          <span className="text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">
            {t("fieldType")}
          </span>
          <div className="mt-1 flex w-fit items-center gap-1 rounded-lg bg-muted p-1">
            {(["world", "experience"] as const).map((value) => (
              <button
                key={value}
                type="button"
                onClick={() => setFactType(value)}
                aria-pressed={factType === value}
                className={`cursor-pointer rounded-md px-3 py-1 text-sm font-medium capitalize transition-all ${
                  factType === value
                    ? "bg-background text-foreground shadow-sm"
                    : "text-muted-foreground hover:text-foreground"
                }`}
              >
                {value}
              </button>
            ))}
          </div>
        </div>
        <div className="grid grid-cols-2 gap-2">
          <div>
            <label
              htmlFor="edit-memory-start"
              className="text-[11px] font-semibold uppercase tracking-wide text-muted-foreground"
            >
              {t("fieldOccurredStart")}
            </label>
            <Input
              id="edit-memory-start"
              type="date"
              value={occurredStart}
              onChange={(event) => setOccurredStart(event.target.value)}
              className="mt-1 h-8 text-sm"
            />
          </div>
          <div>
            <label
              htmlFor="edit-memory-end"
              className="text-[11px] font-semibold uppercase tracking-wide text-muted-foreground"
            >
              {t("fieldOccurredEnd")}
            </label>
            <Input
              id="edit-memory-end"
              type="date"
              value={occurredEnd}
              onChange={(event) => setOccurredEnd(event.target.value)}
              className="mt-1 h-8 text-sm"
            />
          </div>
        </div>
      </div>

      <div>
        <label
          htmlFor="edit-memory-context"
          className="text-[11px] font-semibold uppercase tracking-wide text-muted-foreground"
        >
          {t("fieldContext")}
        </label>
        <Input
          id="edit-memory-context"
          value={context}
          onChange={(event) => setContext(event.target.value)}
          className="mt-1 h-8 text-sm"
        />
      </div>

      <div>
        <span className="text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">
          {t("fieldEntities")}
        </span>
        <div className="mt-1 flex min-h-[2.5rem] flex-wrap items-center gap-1.5 rounded-lg border border-border bg-muted/50 p-2">
          {entities.map((entity) => (
            <EntityChip
              key={entity}
              entity={entity}
              onRemove={() => setEntities(entities.filter((item) => item !== entity))}
              removeLabel={t("removeEntity", { entity })}
            />
          ))}
          <input
            value={entityDraft}
            onChange={(event) => setEntityDraft(event.target.value)}
            onKeyDown={(event) => {
              // Enter and comma both commit, and a backspace on an empty field
              // takes back the last chip, which is the expected chip-editor feel.
              if (event.key === "Enter" || event.key === ",") {
                event.preventDefault();
                addEntity(entityDraft);
              } else if (event.key === "Backspace" && !entityDraft && entities.length > 0) {
                setEntities(entities.slice(0, -1));
              }
            }}
            onBlur={() => addEntity(entityDraft)}
            placeholder={t("entityPlaceholder")}
            aria-label={t("fieldEntities")}
            className="min-w-[8rem] flex-1 bg-transparent text-sm outline-none"
          />
        </div>
      </div>

      <div className="flex items-center justify-end gap-2">
        <Button variant="ghost" size="sm" disabled={busy} onClick={onCancel}>
          {t("cancel")}
        </Button>
        <Button
          size="sm"
          disabled={busy || !text.trim()}
          onClick={() => {
            // Fold an uncommitted draft into the list before saving, otherwise
            // the entity the reader just typed is silently dropped.
            const draft = entityDraft.trim();
            const finalEntities =
              draft && !entities.some((entity) => entity.toLowerCase() === draft.toLowerCase())
                ? [...entities, draft]
                : entities;
            onSave({
              text: text.trim(),
              context,
              factType,
              occurredStart,
              occurredEnd,
              entities: finalEntities,
            });
          }}
        >
          {busy ? <Spinner size="xs" className="mr-1.5" /> : <Check className="mr-1.5 h-3.5 w-3.5" />}
          {t("save")}
        </Button>
      </div>
    </div>
  );
}