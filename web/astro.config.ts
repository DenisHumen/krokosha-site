import { readFileSync } from 'node:fs';
import { defineConfig } from 'astro/config';
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

export default defineConfig({
  site: siteUrl(),
  output: 'static',
  trailingSlash: 'always',
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
  devToolbar: { enabled: false },
  vite: {
    // content/ and mock/ live next to web/, one level above the Vite root.
    server: { fs: { allow: ['..'] } },
  },
});
