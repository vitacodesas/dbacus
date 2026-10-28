import { createI18n } from "vue-i18n";
import es from "./es";
import en from "./en";

export type Locale = "es" | "en";

export const i18n = createI18n({
  legacy: false,
  locale: "es",
  fallbackLocale: "es",
  messages: { es, en },
});

export function setLocale(locale: Locale) {
  i18n.global.locale.value = locale;
  document.documentElement.lang = locale;
}
