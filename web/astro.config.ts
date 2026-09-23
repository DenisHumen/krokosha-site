import { readFileSync } from 'node:fs';
import { defineConfig, fontProviders } from 'astro/config';
import { parse as parseYaml } from 'yaml';
import { DEFAULT_LOCALE, LOCALES } from './src/i18n/locales.ts';
import { repoPaths } from './src/lib/paths.ts';

// The domain is not hard-coded: it comes from content/site.yaml → url,
// and install.sh can override it with SITE_URL (brief B8: «домен не зашит в код»).
function siteUrl(): string {
  const fromEnv = process.env.SITE_URL?.trim();
  if (fromEnv) return fromEnv;
  const site = parseYaml(readFileSync(repoPaths().siteYaml, 'utf8')) as { url?: unknown };
  if (typeof site.url !== 'string') throw new Error('content/site.yaml: "url" is required');
  return site.url;
}

// Unicode ranges of the Fontsource subsets (node_modules/@fontsource-variable/*/wght.css).
const SUBSETS = {
  latin:
    'U+0000-00FF,U+0131,U+0152-0153,U+02BB-02BC,U+02C6,U+02DA,U+02DC,U+0304,U+0308,U+0329,U+2000-206F,U+20AC,U+2122,U+2191,U+2193,U+2212,U+2215,U+FEFF,U+FFFD',
  'latin-ext':
    'U+0100-02BA,U+02BD-02C5,U+02C7-02CC,U+02CE-02D7,U+02DD-02FF,U+0304,U+0308,U+0329,U+1D00-1DBF,U+1E00-1E9F,U+1EF2-1EFF,U+2020,U+20A0-20AB,U+20AD-20C0,U+2113,U+2C60-2C7F,U+A720-A7FF',
  cyrillic: 'U+0301,U+0400-045F,U+0490-0491,U+04B0-04B1,U+2116',
  'cyrillic-ext': 'U+0460-052F,U+1C80-1C8A,U+20B4,U+2DE0-2DFF,U+A640-A69F,U+FE2E-FE2F',
};

/** One variable font family from a @fontsource-variable package, under the name the design uses. */
function fontFamily(
  name: string,
  cssVariable: `--${string}`,
  slug: string,
  weight: string,
  generic: 'sans-serif' | 'monospace',
) {
  const variants = Object.entries(SUBSETS).map(([subset, range]) => ({
    src: [`@fontsource-variable/${slug}/files/${slug}-${subset}-wght-normal.woff2`] as [string],
    weight,
    style: 'normal' as const,
    unicodeRange: range.split(',') as [string, ...string[]],
  }));
  return {
    provider: fontProviders.local(),
    name,
    cssVariable,
    fallbacks: [generic],
    options: { variants: variants as [(typeof variants)[number], ...typeof variants] },
  };
}

export default defineConfig({
  site: siteUrl(),
  output: 'static',
  // Pages end with a slash. While the map page is developed against `krokosha-cli netmap serve`,
  // the dev server must let /api/net/… (no slash) through to its proxy instead of answering 404.
  trailingSlash: process.env.NETMAP_DEV_API ? 'ignore' : 'always',
  build: { format: 'directory' },
  // HTML-aware whitespace handling: markup that comes from design/ is plain HTML,
  // where a line break between inline elements is a space. The 'jsx' default would glue them together.
  compressHTML: true,
  i18n: {
    locales: [...LOCALES],
    defaultLocale: DEFAULT_LOCALE,
    routing: { prefixDefaultLocale: false },
  },
  security: {
    // Hashes of bundled scripts and styles go into <meta http-equiv="content-security-policy">.
    // Nginx adds the directives a <meta> cannot carry (frame-ancestors) — see deploy/nginx.
    csp: {
      directives: [
        "default-src 'self'",
        "img-src 'self' data:",
        "font-src 'self'",
        "connect-src 'self'",
        "object-src 'none'",
        "base-uri 'self'",
        "form-action 'self'",
      ],
    },
  },
  // The typefaces of the design (design/tokens.css): Geologica and Fira Code, OFL, from the Fontsource
  // packages in node_modules — nothing is fetched from Google or a CDN, neither by the build nor by a
  // visitor's browser. Four subsets each: a page downloads only the ones its text needs.
  fonts: [
    fontFamily('Geologica', '--font-geologica', 'geologica', '100 900', 'sans-serif'),
    fontFamily('Fira Code', '--font-fira-code', 'fira-code', '300 700', 'monospace'),
  ],
  devToolbar: { enabled: false },
  vite: {
    server: {
      // content/ and mock/ live next to web/, one level above the Vite root.
      fs: { allow: ['..'] },
      // The map page asks the API; while developing, `krokosha-cli netmap serve` stands in for it.
      ...(process.env.NETMAP_DEV_API
        ? {
            proxy: {
              '/api/net': process.env.NETMAP_DEV_API,
              '/netmap/data': process.env.NETMAP_DEV_API,
            },
          }
        : {}),
    },
  },
});
