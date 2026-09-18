// Languages of the site (docs/contract.md §6). The list itself lives in content/site.yaml → i18n;
// this module only knows what a locale code means technically.

export const LOCALES = ['en', 'uk', 'ru'] as const;
export type Locale = (typeof LOCALES)[number];
export const DEFAULT_LOCALE: Locale = 'en';

/** `og:locale` values. */
export const OG_LOCALE: Record<Locale, string> = {
  en: 'en_US',
  uk: 'uk_UA',
  ru: 'ru_RU',
};

export function isLocale(value: unknown): value is Locale {
  return typeof value === 'string' && (LOCALES as readonly string[]).includes(value);
}

/** Route param of `src/pages/[...lang]/` → locale. The default locale has no prefix. */
export function localeFromParam(param: string | undefined): Locale {
  if (param === undefined || param === '') return DEFAULT_LOCALE;
  if (isLocale(param) && param !== DEFAULT_LOCALE) return param;
  throw new Error(`Unknown locale in route: "${param}"`);
}

/** Locale → route param: `undefined` for the default locale (served from `/`). */
export function paramFromLocale(locale: Locale): string | undefined {
  return locale === DEFAULT_LOCALE ? undefined : locale;
}

/**
 * Site-relative URL of a page in the given locale, always with a trailing slash.
 * `localePath('uk', 'privacy')` → `/uk/privacy/`, `localePath('en', '')` → `/`.
 */
export function localePath(locale: Locale, page = ''): string {
  const clean = page.replace(/^\/+|\/+$/g, '');
  const prefix = locale === DEFAULT_LOCALE ? '' : `/${locale}`;
  return clean === '' ? `${prefix}/` : `${prefix}/${clean}/`;
}
