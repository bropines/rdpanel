/** Theme preference persisted in localStorage; "system" follows the OS setting. */
export type Theme = "light" | "dark" | "system";

export const THEME_STORAGE_KEY = "rdpanel-theme";

export function readStoredTheme(storage: Pick<Storage, "getItem"> = localStorage): Theme {
  const v = storage.getItem(THEME_STORAGE_KEY);
  return v === "light" || v === "dark" || v === "system" ? v : "system";
}

/** Resolves "system" to a concrete theme. */
export function resolveTheme(theme: Theme, prefersDark: boolean): "light" | "dark" {
  if (theme === "system") return prefersDark ? "dark" : "light";
  return theme;
}

/** Applies the resolved theme to <html> (Tailwind `darkMode: "class"`). */
export function applyTheme(theme: Theme, root: HTMLElement = document.documentElement): "light" | "dark" {
  const prefersDark = window.matchMedia?.("(prefers-color-scheme: dark)").matches ?? false;
  const resolved = resolveTheme(theme, prefersDark);
  root.classList.toggle("dark", resolved === "dark");
  root.style.colorScheme = resolved;
  return resolved;
}

/** Cycles light → dark → system. */
export function nextTheme(theme: Theme): Theme {
  return theme === "light" ? "dark" : theme === "dark" ? "system" : "light";
}
