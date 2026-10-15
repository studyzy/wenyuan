"use client";

import { createContext, useCallback, useContext, useState, type ReactNode } from "react";

import { THEME_STORAGE_KEY, resolveTheme, type Theme } from "../app/providers/theme-bootstrap";

interface ThemeContextValue {
  theme: Theme;
  toggleTheme: () => void;
}

const ThemeContext = createContext<ThemeContextValue | undefined>(undefined);

/**
 * Read the theme the pre-paint script already applied.
 *
 * The script has run by the time this is first rendered, so the class on the
 * document is the authoritative answer and no effect is needed to discover it.
 * Reading it lazily keeps the first render agreeing with what is on screen,
 * which is the whole point of bootstrapping before paint.
 */
function initialTheme(): Theme {
  if (typeof document === "undefined") return "light";
  const stored = typeof localStorage !== "undefined" ? localStorage.getItem(THEME_STORAGE_KEY) : null;
  if (stored) return resolveTheme(stored);
  return document.documentElement.classList.contains("dark") ? "dark" : "light";
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setTheme] = useState<Theme>(initialTheme);

  const toggleTheme = useCallback(() => {
    setTheme((previous) => {
      const next: Theme = previous === "light" ? "dark" : "light";
      localStorage.setItem(THEME_STORAGE_KEY, next);
      document.documentElement.classList.toggle("dark", next === "dark");
      return next;
    });
  }, []);

  return (
    <ThemeContext.Provider value={{ theme, toggleTheme }}>{children}</ThemeContext.Provider>
  );
}

export function useTheme(): ThemeContextValue {
  const context = useContext(ThemeContext);
  if (!context) {
    throw new Error("useTheme must be used within a ThemeProvider");
  }
  return context;
}