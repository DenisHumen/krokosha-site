// <head> helpers: canonical / hreflang URLs and schema.org JSON-LD (brief B7).

import { DEFAULT_LOCALE, LOCALES, localePath, type Locale } from '../i18n/locales.ts';
import type { PageData } from './data.ts';

export function absoluteUrl(site: string | URL, path: string): string {
  return new URL(path, site).href;
}

export interface AlternateLink {
  hreflang: string;
  href: string;
}

/** hreflang set of a page that exists in every language. `x-default` points to the default locale. */
export function alternateLinks(site: string | URL, page: string): AlternateLink[] {
  return [
    ...LOCALES.map((code) => ({ hreflang: code, href: absoluteUrl(site, localePath(code, page)) })),
    { hreflang: 'x-default', href: absoluteUrl(site, localePath(DEFAULT_LOCALE, page)) },
  ];
}

/** JSON for a <script type="application/ld+json"> block; `<` is escaped so that data can never close the tag. */
export function serializeJsonLd(data: unknown): string {
  return JSON.stringify(data)
    .replaceAll('<', '\\u003c')
    .replaceAll('>', '\\u003e')
    .replaceAll('&', '\\u0026')
    .replaceAll(' ', '\\u2028')
    .replaceAll(' ', '\\u2029');
}

type JsonLd = Record<string, unknown>;

interface GraphInput {
  site: string | URL;
  data: PageData;
  /** Absolute URLs of images. */
  avatarUrl: string;
  ogImageUrl: string;
}

const id = (site: string | URL, fragment: string) => absoluteUrl(site, `/#${fragment}`);

export function personJsonLd({ site, data, avatarUrl }: GraphInput): JsonLd {
  const { profile, skills, seo, lang } = data;
  const email = profile.socials.find((social) => social.url.startsWith('mailto:'));
  return {
    '@type': 'Person',
    '@id': id(site, 'person'),
    name: profile.name,
    alternateName: profile.nickname,
    jobTitle: profile.role,
    description: seo.description,
    url: absoluteUrl(site, localePath(lang)),
    image: avatarUrl,
    ...(email ? { email: email.url.slice('mailto:'.length) } : {}),
    sameAs: profile.socials.filter((social) => social.url.startsWith('https://')).map((s) => s.url),
    address: { '@type': 'PostalAddress', addressCountry: seo.countryCode },
    knowsAbout: skills.flatMap((category) =>
      category.children.flatMap((group) => group.children.map((skill) => skill.title)),
    ),
  };
}

export function websiteJsonLd({ site, data }: GraphInput): JsonLd {
  return {
    '@type': 'WebSite',
    '@id': id(site, 'website'),
    url: absoluteUrl(site, '/'),
    name: data.profile.nickname,
    description: data.seo.description,
    inLanguage: data.lang,
    publisher: { '@id': id(site, 'person') },
  };
}

export function serviceJsonLd({ site, data, ogImageUrl }: GraphInput): JsonLd {
  const { profile, seo, lang } = data;
  const home = absoluteUrl(site, localePath(lang));
  return {
    '@type': 'ProfessionalService',
    '@id': id(site, 'service'),
    name: seo.serviceName,
    description: seo.description,
    url: home,
    image: ogImageUrl,
    founder: { '@id': id(site, 'person') },
    address: { '@type': 'PostalAddress', addressCountry: seo.countryCode },
    areaServed: seo.areaServed,
    sameAs: profile.socials.filter((social) => social.url.startsWith('https://')).map((s) => s.url),
    hasOfferCatalog: {
      '@type': 'OfferCatalog',
      name: data.site.ui.headings.services,
      itemListElement: data.site.services.map((service) => ({
        '@type': 'Offer',
        itemOffered: {
          '@type': 'Service',
          name: service.title,
          description: service.text,
          url: `${home}#${service.id}`,
        },
      })),
    },
  };
}

export function projectsJsonLd({ site, data }: GraphInput): JsonLd {
  return {
    '@type': 'ItemList',
    '@id': id(site, 'projects'),
    name: data.site.projects.heading,
    itemListElement: data.projects.public.map((project, index) => ({
      '@type': 'ListItem',
      position: index + 1,
      item: {
        '@type': 'SoftwareSourceCode',
        name: project.name,
        ...(project.description ? { description: project.description } : {}),
        ...(project.language ? { programmingLanguage: project.language } : {}),
        codeRepository: project.url,
        url: project.homepage ?? project.url,
        dateModified: project.updatedAt,
        author: { '@id': id(site, 'person') },
      },
    })),
  };
}

export function homeJsonLd(input: GraphInput): JsonLd {
  return {
    '@context': 'https://schema.org',
    '@graph': [
      personJsonLd(input),
      websiteJsonLd(input),
      serviceJsonLd(input),
      projectsJsonLd(input),
    ],
  };
}

export function breadcrumbsJsonLd(
  site: string | URL,
  lang: Locale,
  trail: { name: string; page: string }[],
): JsonLd {
  return {
    '@context': 'https://schema.org',
    '@type': 'BreadcrumbList',
    itemListElement: trail.map((crumb, index) => ({
      '@type': 'ListItem',
      position: index + 1,
      name: crumb.name,
      item: absoluteUrl(site, localePath(lang, crumb.page)),
    })),
  };
}
