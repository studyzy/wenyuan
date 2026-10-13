"use client";

import { createContext, useContext, useEffect, useState, type ReactNode } from "react";

import { versionApi } from "./client/endpoints";

/**
 * Feature flags reported by the server.
 *
 * Each flag gates a view or a control that only makes sense when the backend
 * supports it, so an older backend degrades to hidden rather than broken.
 */
export interface Features {
  observations: boolean;
  mcp: boolean;
  worker: boolean;
  bank_config_api: boolean;
  bank_llm_health: boolean;
  access_key_auth: boolean;
  document_export_api: boolean;
  document_import_api: boolean;
  audit_log: boolean;
  llm_trace: boolean;
  store_document_text: boolean;
}

interface FeaturesContextValue {
  features: Features | null;
  loading: boolean;
  error: string | null;
}

/**
 * Text storage defaults to on so the "text not stored" warning only appears when
 * the server explicitly reports it is off, not when the probe itself failed.
 */
const defaultFeatures: Features = {
  observations: false,
  mcp: false,
  worker: false,
  bank_config_api: false,
  bank_llm_health: false,
  access_key_auth: false,
  document_export_api: false,
  document_import_api: false,
  audit_log: false,
  llm_trace: false,
  store_document_text: true,
};

const FeaturesContext = createContext<FeaturesContextValue | undefined>(undefined);

export function FeaturesProvider({ children }: { children: ReactNode }) {
  const [features, setFeatures] = useState<Features | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    versionApi
      .get()
      .then((response) => {
        if (cancelled) return;
        const reported = (response as { features?: Partial<Features> }).features ?? {};
        setFeatures({ ...defaultFeatures, ...reported });
        setError(null);
      })
      .catch(() => {
        if (cancelled) return;
        setError("Failed to load feature flags");
        setFeatures(defaultFeatures);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <FeaturesContext.Provider value={{ features, loading, error }}>
      {children}
    </FeaturesContext.Provider>
  );
}

export function useFeatures(): FeaturesContextValue {
  const context = useContext(FeaturesContext);
  if (context === undefined) {
    throw new Error("useFeatures must be used within a FeaturesProvider");
  }
  return context;
}