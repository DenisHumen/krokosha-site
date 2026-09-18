// Resolving `{ en, uk, ru }` dictionaries from content/*.yaml into one language,
// CLDR plural forms and `{placeholder}` substitution.

import { LOCALES, type Locale } from './locales.ts';

export type LocalizedDict<T> = Record<Locale, T>;

/** Type of a value after `loc()`: every `{ en, uk, ru }` dictionary is replaced by its value type. */
export type Resolved<T> =
  T extends LocalizedDict<infer U>
    ? U
    : T extends readonly (infer E)[]
      ? Resolved<E>[]
      : T extends object
        ? { [K in keyof T]: Resolved<T[K]> }
        : T;

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

/** A dictionary is an object whose keys are exactly the locale codes. */
export function isLocalizedDict(value: unknown): value is LocalizedDict<unknown> {
  if (!isPlainObject(value)) return false;
  const keys = Object.keys(value);
  return keys.length === LOCALES.length && LOCALES.every((code) => keys.includes(code));
}

/** Recursively replaces every `{ en, uk, ru }` dictionary with the value for `lang`. */
export function loc<T>(value: T, lang: Locale): Resolved<T> {
  if (isLocalizedDict(value)) return value[lang] as Resolved<T>;
  if (Array.isArray(value)) return value.map((item) => loc(item, lang)) as Resolved<T>;
  if (isPlainObject(value)) {
    return Object.fromEntries(
      Object.entries(value).map(([key, item]) => [key, loc(item, lang)]),
    ) as Resolved<T>;
  }
  return value as Resolved<T>;
}

/**
 * A dictionary with only some of the languages is a mistake: the page would silently lose text.
 * Returns the paths of such dictionaries (empty array = everything is translated).
 */
export function findIncompleteTranslations(value: unknown, path = ''): string[] {
  if (Array.isArray(value)) {
    return value.flatMap((item, i) => findIncompleteTranslations(item, `${path}[${i}]`));
  }
  if (!isPlainObject(value)) return [];
  const keys = Object.keys(value);
  const localeKeys = keys.filter((key) => (LOCALES as readonly string[]).includes(key));
  if (localeKeys.length > 0 && !isLocalizedDict(value)) {
    return [`${path || '<root>'} (has: ${keys.join(', ')})`];
  }
  return Object.entries(value).flatMap(([key, item]) =>
    findIncompleteTranslations(item, path ? `${path}.${key}` : key),
  );
}

/**
 * Picks a plural form by CLDR rules. `forms` follows content/site.yaml → plurals:
 * two forms `[one, other]` (en) or three `[one, few, many]` (uk, ru).
 */
export function plural(forms: readonly string[], n: number, lang: Locale): string {
  const category = new Intl.PluralRules(lang).select(n);
  const index =
    forms.length === 2
      ? category === 'one'
        ? 0
        : 1
      : category === 'one'
        ? 0
        : category === 'few'
          ? 1
          : 2;
  const form = forms[index];
  if (form === undefined) {
    throw new Error(
      `plural(): ${forms.length} form(s) given for "${lang}", need 2 or 3: ${forms.join(', ')}`,
    );
  }
  return form;
}

/** Replaces `{name}` placeholders. Unknown placeholders are left as is: some are filled in the browser. */
export function fill(template: string, values: Record<string, string | number>): string {
  return template.replace(/\{([a-z_]+)\}/g, (match, name: string) =>
    name in values ? String(values[name]) : match,
  );
}
