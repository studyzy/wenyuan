"use client";

import { useEffect, useState } from "react";
import { useTranslations } from "next-intl";
import { Button, type ButtonProps } from "./button";
import { copyText } from "@/lib/client/clipboard";

type CopyButtonProps = Pick<ButtonProps, "size" | "variant" | "className"> & {
  /** Text placed on the clipboard. Usually a one-time credential. */
  value: string;
};

/**
 * Copy control with a short "copied" confirmation.
 *
 * It goes through `copyText`, which falls back to a selection-based copy when
 * the Clipboard API is unavailable (an HTTP console on a non-localhost host).
 */
export function CopyButton({ value, size = "sm", variant = "outline", className }: CopyButtonProps) {
  const t = useTranslations("common");
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!copied) return;
    const timer = window.setTimeout(() => setCopied(false), 1500);
    return () => window.clearTimeout(timer);
  }, [copied]);

  async function copy() {
    if (await copyText(value)) setCopied(true);
  }

  return (
    <Button
      type="button"
      size={size}
      variant={variant}
      className={className}
      onClick={() => void copy()}
    >
      {copied ? t("copied") : t("copy")}
    </Button>
  );
}