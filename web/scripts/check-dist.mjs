// Checks the built site (dist/) before it may go live: used by CI and by the deploy scripts
// (brief B3: «проверка релиза» before the `current` symlink is switched).
//
//   node scripts/check-dist.mjs [dist-dir]
//
// No dependencies on purpose: it must run on the VPS right after `astro build`.

import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { gzipSync } from 'node:zlib';

const dist = resolve(process.argv[2] ?? 'dist');
const LOCALES = { en: '', uk: 'uk/', ru: 'ru/' };
const SECTIONS = [
  'hero',
  'services',
  'skills',
  'projects',
  'stats',
  'curtain',
  'contacts',
  'footer',
];
const MIN_HOME_BYTES = 10_000;

const errors = [];
const fail = (file, message) => errors.push(`${file}: ${message}`);

function read(file) {
  const path = join(dist, file);
  if (!existsSync(path)) {
    fail(file, 'file is missing');
    return null;
  }
  return readFileSync(path, 'utf8');
}

const count = (html, pattern) => (html.match(pattern) ?? []).length;
const attr = (html, pattern) => pattern.exec(html)?.[1] ?? null;

/** Text a visitor can see: no tags, scripts or styles. */
const visibleText = (html) =>
  html.replace(/<(script|style)\b[^>]*>[\s\S]*?<\/\1>/g, ' ').replace(/<[^>]+>/g, ' ');

function checkPage(file, lang, { indexable }) {
  const html = read(file);
  if (html === null) return null;

  if (attr(html, /<html[^>]*\blang="([^"]+)"/) !== lang)
    fail(file, `<html lang> must be "${lang}"`);
  if (count(html, /<h1[\s>]/g) !== 1)
    fail(file, `expected exactly one <h1>, found ${count(html, /<h1[\s>]/g)}`);
  if (!/<title>[^<]{10,}<\/title>/.test(html)) fail(file, '<title> is missing or too short');
  if (!/<meta name="description" content="[^"]{20,}"/.test(html))
    fail(file, 'meta description is missing or too short');
  if (!/<meta name="viewport"/.test(html)) fail(file, 'viewport meta is missing');
  if (!/http-equiv="content-security-policy"/.test(html))
    fail(file, 'Content-Security-Policy meta is missing');

  const robots = attr(html, /<meta name="robots" content="([^"]+)"/) ?? '';
  if (indexable) {
    if (robots.includes('noindex')) fail(file, 'must be indexable but has noindex');
    if (!/<link rel="canonical" href="https:\/\/[^"]+"/.test(html))
      fail(file, 'canonical link is missing');
    for (const code of [...Object.keys(LOCALES), 'x-default']) {
      if (!new RegExp(`<link rel="alternate" hreflang="${code}" href="https://[^"]+"`).test(html)) {
        fail(file, `hreflang="${code}" alternate is missing`);
      }
    }
    if (!/<meta property="og:image" content="https:\/\/[^"]+"/.test(html))
      fail(file, 'og:image is missing');
  } else if (!robots.includes('noindex')) {
    fail(file, 'service page must be noindex');
  }

  const text = visibleText(html);
  if (/TODO/.test(text)) fail(file, 'a TODO marker is visible on the page');
  const placeholder = /\{[a-z_]+\}/.exec(text);
  if (placeholder) fail(file, `unresolved placeholder ${placeholder[0]} is visible on the page`);

  for (const match of html.matchAll(
    /<script[^>]*type="application\/ld\+json"[^>]*>([\s\S]*?)<\/script>/g,
  )) {
    try {
      JSON.parse(match[1]);
    } catch (error) {
      fail(file, `JSON-LD does not parse: ${error.message}`);
    }
  }

  return html;
}

function checkInternalLinks(file, html) {
  for (const [, href] of html.matchAll(/\b(?:href|src)="(\/[^"#?]*)[^"]*"/g)) {
    if (href.startsWith('//')) continue;
    const target = join(dist, decodeURIComponent(href));
    const ok =
      existsSync(target) && (statSync(target).isFile() || existsSync(join(target, 'index.html')));
    if (!ok) fail(file, `broken internal link: ${href}`);
  }
}

// Home pages
for (const [lang, prefix] of Object.entries(LOCALES)) {
  const file = `${prefix}index.html`;
  const html = checkPage(file, lang, { indexable: true });
  if (html === null) continue;
  if (Buffer.byteLength(html) < MIN_HOME_BYTES)
    fail(file, `suspiciously small (< ${MIN_HOME_BYTES} bytes)`);
  for (const section of SECTIONS) {
    if (!html.includes(`data-section="${section}"`)) fail(file, `section "${section}" is missing`);
  }
  for (const track of [
    'cta-telegram',
    'cta-email',
    'cta-discuss',
    'social-github',
    'game-entry',
    `lang-${lang}`,
  ]) {
    if (!html.includes(`data-track="${track}"`)) fail(file, `data-track="${track}" is missing`);
  }
  const graph = /<script[^>]*ld\+json[^>]*>([\s\S]*?)<\/script>/.exec(html)?.[1];
  const types = graph ? (JSON.parse(graph)['@graph'] ?? []).map((node) => node['@type']) : [];
  for (const type of ['Person', 'WebSite', 'ProfessionalService', 'ItemList']) {
    if (!types.includes(type)) fail(file, `JSON-LD ${type} is missing`);
  }
  checkInternalLinks(file, html);
}

// The contact form (brief B10.1): it must work as a plain HTML form, because for a visitor
// without JavaScript that is all there is.
for (const [lang, prefix] of Object.entries(LOCALES)) {
  const file = `${prefix}index.html`;
  const html = read(file);
  const form = html && /<form\b[^>]*data-form[^>]*>[\s\S]*?<\/form>/.exec(html)?.[0];
  if (!html || !html.includes('data-slot="site.contacts.form"')) continue; // the form is switched off in content/site.yaml
  if (!form) {
    fail(file, 'the contact form is announced but has no <form data-form>');
    continue;
  }
  if (!/<form[^>]*method="post"[^>]*action="\/api\/leads"/.test(form))
    fail(file, 'the contact form must POST to /api/leads');
  if (!new RegExp(`<input[^>]*name="lang"[^>]*value="${lang}"`).test(form))
    fail(file, `the contact form must say that the page is in "${lang}"`);
  for (const field of [
    'name',
    'contact_method',
    'contact_value',
    'direction',
    'description',
    'consent',
    'altcha',
    'website',
  ]) {
    if (!new RegExp(`name="${field}"`).test(form))
      fail(file, `the contact form has no field "${field}"`);
  }
  // Files travel only in a multipart form: a file field in a plain one sends just the file's name.
  const fileField = /<input\b[^>]*type="file"[^>]*>/.exec(form)?.[0];
  if (Boolean(fileField) !== /<form[^>]*enctype="multipart\/form-data"/.test(form))
    fail(file, 'a file field and enctype="multipart/form-data" come together or not at all');
  if (fileField && !/name="files"/.test(fileField))
    fail(file, 'the file field must be called "files": that is what the API reads');
  if (
    !/<input[^>]*name="consent"[^>]*required/.test(form) &&
    !/<input[^>]*required[^>]*name="consent"/.test(form)
  )
    fail(file, 'consent must be required even without JavaScript');
  for (const id of ['form-error-invalid', 'form-error-rate', 'form-error-server', 'form-success']) {
    if (!html.includes(`id="${id}"`))
      fail(file, `the block #${id} is missing: the API redirects to it`);
  }
  if (!/<script[^>]+src="\/assets\/form\.js"/.test(html)) fail(file, 'form.js is not included');
}
if (existsSync(join(dist, 'assets/form.js')) === false) fail('assets/form.js', 'file is missing');

// Service pages
for (const [lang, prefix] of Object.entries(LOCALES)) {
  // The API fills these marks in when it answers a form sent without JavaScript
  // (api/internal/leads/http.go); as built, the page must hide them.
  const thanks = checkPage(`${prefix}thanks/index.html`, lang, { indexable: false });
  if (thanks) {
    checkInternalLinks(`${prefix}thanks/index.html`, thanks);
    for (const mark of [
      '%%LEAD_NUMBER%%',
      '%%TELEGRAM_URL%%',
      '%%GENERIC_CLASS%%',
      '%%NUMBERED_CLASS%%',
      '%%TELEGRAM_CLASS%%',
    ]) {
      if (!thanks.includes(mark)) fail(`${prefix}thanks/index.html`, `the mark ${mark} is missing`);
    }
  }
  for (const page of ['404', 'play']) {
    const html = checkPage(`${prefix}${page}/index.html`, lang, { indexable: false });
    if (html) checkInternalLinks(`${prefix}${page}/index.html`, html);
  }
  const privacy = read(`${prefix}privacy/index.html`);
  if (privacy) {
    // A draft policy is noindex; a published one must be indexable.
    const isDraft = /<meta name="robots" content="noindex/.test(privacy);
    checkPage(`${prefix}privacy/index.html`, lang, { indexable: !isDraft });
    checkInternalLinks(`${prefix}privacy/index.html`, privacy);
  }
}

// The map of the internet (docs/netmap.md): the page and the land it is drawn on. Its data come
// from the server (/netmap/data/, rebuilt every night), never from the build.
for (const [lang, prefix] of Object.entries(LOCALES)) {
  const file = `${prefix}map/index.html`;
  const html = checkPage(file, lang, { indexable: true });
  if (!html) continue;
  checkInternalLinks(file, html);
  for (const hook of ['data-netmap', 'data-map-texts', 'data-map-canvas', 'data-route-form']) {
    if (!html.includes(hook)) fail(file, `${hook} is missing: the script of the map needs it`);
  }
}
{
  const land = read('netmap/land.bin');
  if (land !== null && !land.startsWith('KLD1')) fail('netmap/land.bin', 'not a land file (KLD1)');
  if (existsSync(join(dist, 'netmap/data')))
    fail('netmap/data', 'the data of the map belong to the server, not to the build');
}

// The statistics script: present on every page, readable, and small (brief B5: < 3 KB gzip).
{
  const script = read('assets/analytics.js');
  if (script) {
    const gzipped = gzipSync(Buffer.from(script), { level: 9 }).length;
    if (gzipped > 3072)
      fail('assets/analytics.js', `${gzipped} bytes gzipped, the limit is 3072 (3 KB)`);
    const lines = script.split(/\r?\n/);
    const longest = Math.max(...lines.map((line) => line.length));
    if (lines.length < 50 || longest > 200) {
      fail('assets/analytics.js', 'looks minified: it must stay readable');
    }
    if (!/Never collected/.test(script)) {
      fail('assets/analytics.js', 'the header comment that explains what is collected is missing');
    }
  }
  for (const prefix of Object.values(LOCALES)) {
    const html = read(`${prefix}index.html`);
    if (html && !/<script[^>]+src="\/assets\/analytics\.js"/.test(html)) {
      fail(`${prefix}index.html`, 'the statistics script is not included');
    }
  }
}

// robots.txt and sitemap.xml
const robotsTxt = read('robots.txt');
if (robotsTxt && !/^Sitemap: https:\/\/\S+\/sitemap\.xml$/m.test(robotsTxt)) {
  fail('robots.txt', 'Sitemap line is missing');
}
const sitemap = read('sitemap.xml');
if (sitemap) {
  const locs = [...sitemap.matchAll(/<loc>([^<]+)<\/loc>/g)].map((match) => match[1]);
  for (const prefix of Object.values(LOCALES)) {
    for (const page of ['', 'map/']) {
      if (!locs.some((loc) => new URL(loc).pathname === `/${prefix}${page}`)) {
        fail('sitemap.xml', `page /${prefix}${page} is missing`);
      }
    }
  }
  for (const loc of locs) {
    const path = new URL(loc).pathname;
    if (/\/(404|play|api)\//.test(path)) fail('sitemap.xml', `service page listed: ${path}`);
    if (!existsSync(join(dist, path, 'index.html')))
      fail('sitemap.xml', `listed page does not exist: ${path}`);
  }
}

// Nothing hidden or temporary may be published.
const walk = (dir) =>
  readdirSync(dir, { withFileTypes: true }).flatMap((entry) =>
    entry.isDirectory() ? walk(join(dir, entry.name)) : [join(dir, entry.name)],
  );
if (existsSync(dist)) {
  for (const path of walk(dist)) {
    const name = path.slice(dist.length + 1).replaceAll('\\', '/');
    if (/(^|\/)\.|\.map$|\.(ya?ml|env)$/.test(name)) fail(name, 'must not be published');
  }
}

if (errors.length > 0) {
  console.error(`check-dist: ${errors.length} problem(s) in ${dist}\n  ${errors.join('\n  ')}`);
  process.exit(1);
}
console.log(`check-dist: OK (${dist})`);
