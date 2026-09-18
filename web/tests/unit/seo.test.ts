import { describe, expect, it } from 'vitest';
import { loadContent } from '../../src/lib/content.ts';
import { buildPageData } from '../../src/lib/data.ts';
import {
  absoluteUrl,
  alternateLinks,
  breadcrumbsJsonLd,
  homeJsonLd,
  serializeJsonLd,
} from '../../src/lib/seo.ts';

const SITE = 'https://krokosha.xyz';

describe('alternateLinks', () => {
  it('lists every language plus x-default → default locale', () => {
    expect(alternateLinks(SITE, '')).toEqual([
      { hreflang: 'en', href: 'https://krokosha.xyz/' },
      { hreflang: 'uk', href: 'https://krokosha.xyz/uk/' },
      { hreflang: 'ru', href: 'https://krokosha.xyz/ru/' },
      { hreflang: 'x-default', href: 'https://krokosha.xyz/' },
    ]);
    expect(alternateLinks(SITE, 'privacy')[1]).toEqual({
      hreflang: 'uk',
      href: 'https://krokosha.xyz/uk/privacy/',
    });
  });

  it('follows the configured domain', () => {
    expect(absoluteUrl('https://example.org', '/uk/')).toBe('https://example.org/uk/');
  });
});

describe('serializeJsonLd', () => {
  it('cannot be broken out of a <script> block by data', () => {
    const json = serializeJsonLd({ description: '</script><script>alert(1)</script> & more' });
    expect(json).not.toContain('<');
    expect(json).not.toContain('>');
    expect(JSON.parse(json)).toEqual({ description: '</script><script>alert(1)</script> & more' });
  });
});

describe('homeJsonLd', () => {
  const content = loadContent({ github: 'snapshot' });
  const data = buildPageData(content, 'uk', {
    now: new Date(content.github.syncedAt),
    mode: 'site',
    avatar: { src: '/a.avif', srcset: '/a.avif 256w' },
  });
  const graph = homeJsonLd({
    site: SITE,
    data,
    avatarUrl: `${SITE}/a.webp`,
    ogImageUrl: `${SITE}/img/og-default.png`,
  })['@graph'] as Record<string, any>[];
  const node = (type: string) => graph.find((item) => item['@type'] === type);

  it('describes the person, the site, the service and the projects', () => {
    expect(graph.map((item) => item['@type'])).toEqual([
      'Person',
      'WebSite',
      'ProfessionalService',
      'ItemList',
    ]);
  });

  it('links the person to social profiles, not to mailto:', () => {
    const person = node('Person')!;
    expect(person.sameAs).toEqual(['https://t.me/DenisHumen', 'https://github.com/DenisHumen']);
    expect(person.email).toBe('denis@krokosha.xyz');
    expect(person.url).toBe('https://krokosha.xyz/uk/');
  });

  it('claims only confirmed skills', () => {
    const person = node('Person')!;
    expect(person.knowsAbout).toContain('MikroTik');
    expect(person.knowsAbout).not.toContain('Juniper');
  });

  it('lists every public repository', () => {
    expect(node('ItemList')!.itemListElement).toHaveLength(content.github.public.length);
  });

  it('points service offers at the landing anchors', () => {
    const offers = node('ProfessionalService')!.hasOfferCatalog.itemListElement;
    expect(offers[0].itemOffered.url).toBe('https://krokosha.xyz/uk/#networks');
  });
});

describe('breadcrumbsJsonLd', () => {
  it('builds localized URLs', () => {
    const crumbs = breadcrumbsJsonLd(SITE, 'ru', [
      { name: 'Krokosha', page: '' },
      { name: 'Политика', page: 'privacy' },
    ]);
    expect(crumbs.itemListElement).toEqual([
      { '@type': 'ListItem', position: 1, name: 'Krokosha', item: 'https://krokosha.xyz/ru/' },
      {
        '@type': 'ListItem',
        position: 2,
        name: 'Политика',
        item: 'https://krokosha.xyz/ru/privacy/',
      },
    ]);
  });
});
