// Text pages from content/pages/<name>.<lang>.md.

import { getEntry, type CollectionEntry } from 'astro:content';
import type { Locale } from '../i18n/locales.ts';

export async function getTextPage(name: string, lang: Locale): Promise<CollectionEntry<'pages'>> {
  const entry = await getEntry('pages', `${name}.${lang}`);
  if (!entry) throw new Error(`content/pages/${name}.${lang}.md not found`);
  return entry;
}

/** `updated` as YYYY-MM-DD, or `null` while it is still a TODO. */
export function updatedDate(entry: CollectionEntry<'pages'>): string | null {
  const { updated } = entry.data;
  if (updated instanceof Date) return updated.toISOString().slice(0, 10);
  return /^\d{4}-\d{2}-\d{2}$/.test(updated) ? updated : null;
}
