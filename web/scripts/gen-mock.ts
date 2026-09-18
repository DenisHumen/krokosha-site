// Regenerates mock/<lang>/*.json from content/*.yaml with the same code the site is built with.
//
//   npm run mock          rewrite mock/
//   npm run mock:check    exit 1 if mock/ is out of date (CI)
//
// The list of public repositories is a snapshot: it is taken from mock/en/projects.json and kept as is.
// «Today» is the snapshot date (`syncedAt`), so the result does not depend on when the script runs.

import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { LOCALES } from '../src/i18n/locales.ts';
import { loadContent } from '../src/lib/content.ts';
import {
  buildProfile,
  buildProjects,
  buildSite,
  buildSkills,
  type BuildOptions,
} from '../src/lib/data.ts';
import { repoPaths } from '../src/lib/paths.ts';

const checkOnly = process.argv.includes('--check');

const content = loadContent({ github: 'snapshot' });
const options: BuildOptions = {
  now: new Date(content.github.syncedAt),
  mode: 'mock',
  avatar: {
    src: '/img/avatar-256.avif',
    srcset: '/img/avatar-128.avif 128w, /img/avatar-256.avif 256w, /img/avatar-512.avif 512w',
  },
};

const { mockDir } = repoPaths();
const outdated: string[] = [];

for (const lang of LOCALES) {
  const files = {
    'profile.json': buildProfile(content, lang, options),
    'skills.json': buildSkills(content, lang, options),
    'projects.json': buildProjects(content, lang, options),
    'site.json': buildSite(content, lang, options),
  };
  mkdirSync(join(mockDir, lang), { recursive: true });
  for (const [name, data] of Object.entries(files)) {
    const path = join(mockDir, lang, name);
    const next = `${JSON.stringify(data, null, 2)}\n`;
    const current = existsSync(path) ? readFileSync(path, 'utf8') : null;
    if (current === next) continue;
    outdated.push(`mock/${lang}/${name}`);
    if (!checkOnly) writeFileSync(path, next, 'utf8');
  }
}

if (outdated.length === 0) {
  console.log('mock/ is up to date.');
} else if (checkOnly) {
  console.error(
    `mock/ is out of date — run "npm run mock" in web/ and commit:\n  ${outdated.join('\n  ')}`,
  );
  process.exit(1);
} else {
  console.log(`Updated:\n  ${outdated.join('\n  ')}`);
}
