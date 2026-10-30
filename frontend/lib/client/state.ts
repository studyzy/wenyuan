import { apiGet, apiPost } from "./api";

/**
 * Browser-side client state for bank selection and feature flags.
 *
 * The dashboard clears the current bank selection so header actions are never
 * offered for a bank that is not on screen.
 */
export type BankSelection = {
  bankId: string | null;
};

export function createBankSelection(initial: string | null = null): BankSelection {
  return { bankId: initial };
}

export function selectBank(selection: BankSelection, bankId: string): BankSelection {
  return { ...selection, bankId };
}

export function clearBankSelection(_selection: BankSelection): BankSelection {
  return { bankId: null };
}

export type FeatureFlags = {
  accessKeyAuth: boolean;
  [key: string]: boolean;
};

export type VersionPayload = {
  features?: Record<string, boolean>;
  [key: string]: unknown;
};

export async function fetchVersion(): Promise<VersionPayload> {
  return apiGet<VersionPayload>("/api/version");
}

export function readFeatureFlags(payload: VersionPayload): FeatureFlags {
  const features = payload.features ?? {};
  return { accessKeyAuth: Boolean(features.access_key_auth), ...features };
}

export async function signIn(key: string, returnTo?: string): Promise<unknown> {
  return apiPost<unknown>("/api/auth/login", { key, returnTo });
}

export async function signOut(): Promise<unknown> {
  return apiPost<unknown>("/api/auth/logout", {});
}