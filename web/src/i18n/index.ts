import i18n from "i18next";
import LanguageDetector from "i18next-browser-languagedetector";
import { initReactI18next } from "react-i18next";

import de from "@/i18n/locales/de.json";
import en from "@/i18n/locales/en.json";
import ru from "@/i18n/locales/ru.json";

export const SUPPORTED_LANGUAGES = ["en", "ru", "de"] as const;
export type Language = (typeof SUPPORTED_LANGUAGES)[number];

export const resources = { en: { translation: en }, ru: { translation: ru }, de: { translation: de } } as const;

/** Initialises i18next: stored choice first, then the browser language (ru → ru, de → de, else en). */
export function initI18n() {
  if (i18n.isInitialized) return i18n;
  void i18n
    .use(LanguageDetector)
    .use(initReactI18next)
    .init({
      resources,
      fallbackLng: "en",
      supportedLngs: [...SUPPORTED_LANGUAGES],
      nonExplicitSupportedLngs: true,
      load: "languageOnly",
      interpolation: { escapeValue: false },
      detection: {
        order: ["localStorage", "navigator"],
        lookupLocalStorage: "rdpanel-lang",
        caches: ["localStorage"],
      },
    });
  i18n.on("languageChanged", (lng) => {
    document.documentElement.lang = lng;
  });
  return i18n;
}

export default i18n;
