// sitemap.xml with hreflang alternates. Only indexable pages: no 404, no /play stub,
// no admin paths (brief B6), no drafts.

import type { APIRoute } from 'astro';
import { getCollection } from 'astro:content';
import { LOCALES, localePath } from '../i18n/locales.ts';
import { getContent } from '../lib/content.ts';
import { updatedDate } from '../lib/pages.ts';
import { absoluteUrl, alternateLinks } from '../lib/seo.ts';

const escapeXml = (value: string) =>
  value
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;');

export const GET: APIRoute = async ({ site }) => {
  if (!site) throw new Error('astro.config: "site" is required for sitemap.xml');

  // The home page changes when repositories do: its date is the latest push.
  const lastPush = getContent()
    .github.public.map((project) => project.updatedAt)
    .sort()
    .at(-1);

  const pages: { page: string; lastmod: string | null }[] = [
    { page: '', lastmod: lastPush ?? null },
    // The map of the internet: its data are rebuilt every night on the server, not in the build.
    { page: 'map', lastmod: null },
  ];
  const texts = await getCollection('pages', ({ data }) => data.status === 'published');
  for (const name of new Set(texts.map((entry) => entry.id.split('.')[0] ?? ''))) {
    const dates = texts
      .filter((entry) => entry.id.startsWith(`${name}.`))
      .map(updatedDate)
      .filter((date) => date !== null);
    pages.push({ page: name, lastmod: dates.sort().at(-1) ?? null });
  }

  const urls = pages.flatMap(({ page, lastmod }) =>
    LOCALES.map((lang) =>
      [
        '  <url>',
        `    <loc>${escapeXml(absoluteUrl(site, localePath(lang, page)))}</loc>`,
        ...(lastmod ? [`    <lastmod>${lastmod}</lastmod>`] : []),
        ...alternateLinks(site, page).map(
          ({ hreflang, href }) =>
            `    <xhtml:link rel="alternate" hreflang="${hreflang}" href="${escapeXml(href)}"/>`,
        ),
        '  </url>',
      ].join('\n'),
    ),
  );

  const xml = [
    '<?xml version="1.0" encoding="UTF-8"?>',
    '<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xhtml="http://www.w3.org/1999/xhtml">',
    ...urls,
    '</urlset>',
    '',
  ].join('\n');

  return new Response(xml, { headers: { 'Content-Type': 'application/xml; charset=utf-8' } });
};
