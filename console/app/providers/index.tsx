"use client";

import type { ReactNode } from "react";
import { NotificationProvider } from "./notification-provider";
import { BankProvider } from "./bank-provider";
import { FeatureProvider } from "./feature-provider";

/**
 * Compose the client providers around the route tree: notifications, bank
 * selection, and feature flags. Theme is applied by the pre-paint bootstrap.
 */
export function Providers({
  children,
  features = {},
}: {
  children: ReactNode;
  features?: Record<string, boolean>;
}) {
  return (
    <FeatureProvider initial={features}>
      <BankProvider>
        <NotificationProvider>{children}</NotificationProvider>
      </BankProvider>
    </FeatureProvider>
  );
}