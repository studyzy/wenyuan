"use client";

import type { ReactNode } from "react";

import { BankProvider } from "../../lib/bank-context";
import { FeaturesProvider } from "../../lib/features-context";
import { ThemeProvider } from "../../lib/theme-context";
import { NotificationProvider } from "./notification-provider";

/**
 * Compose the client providers around the route tree.
 *
 * Order matters: theme and feature flags sit above the bank selection because
 * the navigation shell reads both when deciding what to offer, and the
 * notification surface is innermost so anything above it can raise a toast.
 */
export function Providers({ children }: { children: ReactNode }) {
  return (
    <ThemeProvider>
      <FeaturesProvider>
        <BankProvider>
          <NotificationProvider>{children}</NotificationProvider>
        </BankProvider>
      </FeaturesProvider>
    </ThemeProvider>
  );
}