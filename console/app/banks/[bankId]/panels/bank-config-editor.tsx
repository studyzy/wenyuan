"use client";

import { useState } from "react";

import {
  DISPOSITION_MAX,
  DISPOSITION_MIN,
  DISPOSITION_NEUTRAL,
  REFLECT_MISSION_KEY,
  banksApi,
  type BankConfig,
} from "../../../../lib/client/endpoints";
import { useTranslations } from "../../../../i18n";
import { Button } from "../../../../components/ui/button";
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
import { Label } from "../../../../components/ui/label";
import { Textarea } from "../../../../components/ui/textarea";

/** The three disposition traits, in the order the editor presents them. */
const TRAITS = [
  { key: "disposition_skepticism", labelKey: "traitSkepticism", hintKey: "traitSkepticismHint" },
  { key: "disposition_literalism", labelKey: "traitLiteralism", hintKey: "traitLiteralismHint" },
  { key: "disposition_empathy", labelKey: "traitEmpathy", hintKey: "traitEmpathyHint" },
] as const;

/**
 * Where a value came from: an override the bank set, or the inherited default.
 */
function isOverridden(overrides: Record<string, unknown>, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(overrides, key);
}

/**
 * Bank configuration editor.
 *
 * Every trait is graded on a fixed scale, so the control is a bounded set of
 * choices rather than a free number: a value outside the scale is rejected by
 * the route, and offering it would only let the reader build a request that
 * fails. Each field marks whether its value is an override or the inherited
 * default, because the two look identical once resolved and only one of them
 * survives a reset.
 *
 * Saving sends only the fields the reader changed, so an untouched trait keeps
 * whatever inheritance it had instead of being frozen to its resolved value.
 */
export function BankConfigEditor({
  bankId,
  initial,
  onSaved,
}: {
  bankId: string;
  initial: BankConfig;
  onSaved: (next: BankConfig) => void;
}) {
  const t = useTranslations("panels.profile");
  const [mission, setMission] = useState(initial.config.reflect_mission ?? "");
  const [traits, setTraits] = useState<Record<string, number>>(() => ({
    disposition_skepticism: initial.config.disposition_skepticism,
    disposition_literalism: initial.config.disposition_literalism,
    disposition_empathy: initial.config.disposition_empathy,
  }));
  const [saving, setSaving] = useState(false);
  const [resetting, setResetting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const dirty =
    mission !== (initial.config.reflect_mission ?? "") ||
    TRAITS.some(({ key }) => traits[key] !== initial.config[key as keyof typeof initial.config]);

  async function save() {
    setSaving(true);
    setError(null);
    try {
      const updates: Record<string, number | string> = {};
      for (const { key } of TRAITS) {
        if (traits[key] !== initial.config[key as keyof typeof initial.config]) {
          updates[key] = traits[key];
        }
      }
      if (mission !== (initial.config.reflect_mission ?? "")) {
        updates[REFLECT_MISSION_KEY] = mission;
      }
      const next = await banksApi.updateConfig(bankId, updates);
      onSaved(next);
    } catch {
      setError(t("saveFailed"));
    } finally {
      setSaving(false);
    }
  }

  async function reset() {
    setResetting(true);
    setError(null);
    try {
      const next = await banksApi.resetConfig(bankId);
      onSaved(next);
      setMission("");
      setTraits({
        disposition_skepticism: DISPOSITION_NEUTRAL,
        disposition_literalism: DISPOSITION_NEUTRAL,
        disposition_empathy: DISPOSITION_NEUTRAL,
      });
    } catch {
      setError(t("resetFailed"));
    } finally {
      setResetting(false);
    }
  }

  return (
    <div className="space-y-6">
      <fieldset className="rounded-[16px] border border-border bg-card px-4 py-4">
        <legend className="px-1 text-[13px] font-semibold">{t("dispositionsTitle")}</legend>
        <p className="mb-4 text-[12px] text-muted-foreground">{t("dispositionsDescription")}</p>
        <div className="space-y-4">
          {TRAITS.map(({ key, labelKey, hintKey }) => (
            <div key={key} className="grid gap-1.5">
              <div className="flex items-baseline justify-between gap-3">
                <Label htmlFor={key}>{t(labelKey)}</Label>
                <OriginBadge overridden={isOverridden(initial.overrides, key)} />
              </div>
              <TraitScale
                id={key}
                value={traits[key]}
                onChange={(value) => setTraits((current) => ({ ...current, [key]: value }))}
                ariaLabel={t(labelKey)}
              />
              <p className="text-[11px] text-muted-foreground">{t(hintKey)}</p>
            </div>
          ))}
        </div>
      </fieldset>

      <div className="rounded-[16px] border border-border bg-card px-4 py-4">
        <div className="mb-1 flex items-baseline justify-between gap-3">
          <Label htmlFor="reflect_mission">{t("missionTitle")}</Label>
          <OriginBadge overridden={isOverridden(initial.overrides, REFLECT_MISSION_KEY)} />
        </div>
        <p className="mb-2 text-[12px] text-muted-foreground">{t("missionDescription")}</p>
        <Textarea
          id="reflect_mission"
          value={mission}
          onChange={(event) => setMission(event.target.value)}
          rows={4}
          placeholder={t("missionPlaceholder")}
        />
      </div>

      {error && (
        <p role="alert" className="text-[12px] text-destructive">
          {error}
        </p>
      )}

      <div className="flex items-center gap-2">
        <Button type="button" onClick={save} disabled={saving || resetting || !dirty}>
          {saving ? t("saving") : t("save")}
        </Button>
        <AlertDialog>
          <AlertDialogTrigger asChild>
            <Button type="button" variant="outline" disabled={saving || resetting}>
              {resetting ? t("resetting") : t("reset")}
            </Button>
          </AlertDialogTrigger>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>{t("resetConfirmTitle")}</AlertDialogTitle>
              <AlertDialogDescription>{t("resetConfirmDescription")}</AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>{t("cancel")}</AlertDialogCancel>
              <AlertDialogAction onClick={reset}>{t("resetConfirmAction")}</AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
        {dirty && !saving && (
          <span className="text-[12px] text-muted-foreground">{t("unsaved")}</span>
        )}
      </div>
    </div>
  );
}

/**
 * A trait's graded scale.
 *
 * A row of radio buttons rather than a select, because five ordered values read
 * better as a scale and each option stays one click away.
 */
function TraitScale({
  id,
  value,
  onChange,
  ariaLabel,
}: {
  id: string;
  value: number;
  onChange: (value: number) => void;
  ariaLabel: string;
}) {
  const options: number[] = [];
  for (let n = DISPOSITION_MIN; n <= DISPOSITION_MAX; n++) options.push(n);
  return (
    <div role="radiogroup" aria-label={ariaLabel} className="flex gap-1">
      {options.map((option) => {
        const active = option === value;
        const neutral = option === DISPOSITION_NEUTRAL;
        return (
          <button
            key={option}
            id={`${id}-${option}`}
            type="button"
            role="radio"
            aria-checked={active}
            onClick={() => onChange(option)}
            className={[
              "h-8 w-9 cursor-pointer rounded-[8px] border text-[13px] font-medium transition-colors",
              active
                ? "border-primary bg-primary/10 text-foreground"
                : "border-border text-muted-foreground hover:text-foreground",
              neutral && !active ? "border-dashed" : "",
            ].join(" ")}
          >
            {option}
          </button>
        );
      })}
    </div>
  );
}

/** Marks whether a field is an override or the inherited default. */
function OriginBadge({ overridden }: { overridden: boolean }) {
  const t = useTranslations("panels.profile");
  return (
    <span
      className={[
        "rounded-full px-2 py-0.5 text-[11px]",
        overridden
          ? "bg-chip-tag text-foreground"
          : "bg-muted text-muted-foreground",
      ].join(" ")}
    >
      {overridden ? t("originOverride") : t("originInherited")}
    </span>
  );
}