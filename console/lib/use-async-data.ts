"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

/**
 * Load data for a panel, with an explicit loading/error/empty state.
 *
 * Every panel needs the same four things and the same two hazards: a response
 * that lands after the panel unmounted, and a slower earlier response landing
 * after a newer one. Both are handled here so no panel has to re-derive them,
 * and `reload` is exposed for the retry affordance each panel offers.
 */
export type AsyncState<T> = {
  data: T | null;
  loading: boolean;
  error: boolean;
  reload: () => void;
  /**
   * Write a value the caller already has — typically the response to a write
   * it just performed — so the panel reflects it without a second round trip.
   * The version counter is advanced, so a reload response still in flight
   * cannot overwrite this value with an older read.
   */
  replace: (next: T) => void;
};

export function useAsyncData<T>(
  load: () => Promise<T>,
  deps: readonly unknown[],
): AsyncState<T> {
  const [data, setData] = useState<T | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);
  const [nonce, setNonce] = useState(0);

  // Guards against a stale or unmounted response overwriting newer state.
  const seq = useRef(0);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  // The loader is captured in a memo keyed on the caller's dependencies rather
  // than read through a ref written during render, which would mutate a ref
  // while rendering.
  const requestKey = useMemo(() => [...deps, nonce], [deps, nonce]);
  const loadRef = useRef(load);
  useEffect(() => {
    loadRef.current = load;
  });

  useEffect(() => {
    const current = ++seq.current;
    let cancelled = false;
    // Marking the request in flight is the point of this effect, not a cascade
    // to be avoided: the panel must not render stale content as current.
    // Marking the request in flight is the point of this effect, not a cascade
    // to be avoided: a panel must not render stale content as if it were current.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setLoading(true);
    setError(false);
    loadRef
      .current()
      .then((result) => {
        if (cancelled || !mounted.current || current !== seq.current) return;
        setData(result);
        setLoading(false);
      })
      .catch(() => {
        if (cancelled || !mounted.current || current !== seq.current) return;
        setError(true);
        setLoading(false);
      });
    return () => {
      cancelled = true;
    };
    // requestKey carries the caller's dependency list plus the reload counter.
  }, [requestKey]);

  const reload = useCallback(() => setNonce((value) => value + 1), []);

  const replace = useCallback((next: T) => {
    // Advancing the sequence makes any in-flight read stale, so it cannot land
    // on top of the value the caller just obtained.
    seq.current++;
    setData(next);
    setError(false);
  }, []);

  return { data, loading, error, reload, replace };
}

/** Narrow an unknown response to a list under a named key, tolerating a bare array. */
export function readItems<T>(payload: unknown, key = "items"): T[] {
  if (Array.isArray(payload)) return payload as T[];
  if (payload && typeof payload === "object") {
    const candidate = (payload as Record<string, unknown>)[key];
    if (Array.isArray(candidate)) return candidate as T[];
  }
  return [];
}

/** Read a numeric field, falling back when it is absent or not a number. */
export function readNumber(payload: unknown, key: string, fallback = 0): number {
  if (payload && typeof payload === "object") {
    const value = (payload as Record<string, unknown>)[key];
    if (typeof value === "number") return value;
  }
  return fallback;
}

/** Read a string field, falling back when it is absent. */
export function readString(payload: unknown, key: string, fallback = ""): string {
  if (payload && typeof payload === "object") {
    const value = (payload as Record<string, unknown>)[key];
    if (typeof value === "string") return value;
  }
  return fallback;
}