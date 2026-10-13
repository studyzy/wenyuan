"use client";

import { createContext, useContext, useMemo, useState, type ReactNode } from "react";

/** Feature flags surfaced to client components, seeded from the version probe. */
type FeatureContextValue = {
  accessKeyAuth: boolean;
  setFeatures: (features: Record<string, boolean>) => void;
};

const FeatureContext = createContext<FeatureContextValue | null>(null);

export function FeatureProvider({
  children,
  initial = {},
}: {
  children: ReactNode;
  initial?: Record<string, boolean>;
}) {
  const [features, setFeatures] = useState<Record<string, boolean>>(initial);

  const value = useMemo<FeatureContextValue>(
    () => ({
      accessKeyAuth: Boolean(features.access_key_auth),
      setFeatures,
    }),
    [features],
  );

  return <FeatureContext.Provider value={value}>{children}</FeatureContext.Provider>;
}

export function useFeatures(): FeatureContextValue {
  const context = useContext(FeatureContext);
  if (!context) {
    throw new Error("useFeatures must be used inside FeatureProvider");
  }
  return context;
}