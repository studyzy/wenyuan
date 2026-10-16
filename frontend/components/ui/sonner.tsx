"use client";

import { CircleCheck, Info, OctagonX, TriangleAlert } from "lucide-react";
import { useTheme } from "next-themes";
import { Toaster as Sonner } from "sonner";
import { Spinner } from "./spinner";

type ToasterProps = React.ComponentProps<typeof Sonner>;

const Toaster = ({ ...props }: ToasterProps) => {
  const { theme = "system" } = useTheme();

  return (
    <Sonner
      theme={theme as ToasterProps["theme"]}
      className="toaster group"
      icons={{
        success: <CircleCheck className="h-4 w-4" />,
        info: <Info className="h-4 w-4" />,
        warning: <TriangleAlert className="h-4 w-4" />,
        error: <OctagonX className="h-4 w-4" />,
        loading: <Spinner size="sm" />,
      }}
      toastOptions={{
        classNames: {
          toast:
            "group toast group-[.toaster]:border-border group-[.toaster]:border-solid group-[.toaster]:rounded-[12px] group-[.toaster]:shadow-[var(--shadow-lg)]",
          description: "group-[.toast]:opacity-90",
          actionButton: "group-[.toast]:bg-primary group-[.toast]:text-primary-foreground",
          cancelButton: "group-[.toast]:bg-muted group-[.toast]:text-muted-foreground",
          error: "!bg-destructive !text-destructive-foreground !border-destructive",
          // Semantic tokens, so a toast keeps the same green/amber the badges and
          // charts use rather than a fourth palette invented here.
          success: "!bg-[var(--hs-success)] !text-white !border-[var(--hs-success)]",
          warning: "!bg-[var(--hs-warning)] !text-white !border-[var(--hs-warning)]",
          info: "!bg-primary !text-primary-foreground !border-primary",
        },
      }}
      {...props}
    />
  );
};

export { Toaster };
