/**
 * Pre-paint theme bootstrap.
 *
 * The snippet runs before paint to apply the stored theme and avoid a flash.
 * The script text is derived from a single source of truth so the shell and its
 * test agree on the behavior.
 *
 * The theme is expressed as a `.dark` class on the document element, which is
 * what the stylesheet's dark variant keys off. Setting a data attribute instead
 * would leave every `dark:` utility inert.
 */
export const THEME_STORAGE_KEY = "wenyuan_cp_theme";

export function themeBootstrapScript(): string {
  return `(function(){try{var t=localStorage.getItem('${THEME_STORAGE_KEY}');if(t==='dark'||t==='light'){var e=document.documentElement;e.classList.toggle('dark',t==='dark');e.dataset.theme=t;}}catch(e){}})();`;
}

export type Theme = "light" | "dark";

export function resolveTheme(stored: string | null | undefined): Theme {
  return stored === "dark" ? "dark" : "light";
}