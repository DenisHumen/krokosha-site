// The data layer must produce exactly what docs/contract.md promises to the design: mock/<lang>/*.json.

import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { LOCALES } from '../../src/i18n/locales.ts';
import { loadContent } from '../../src/lib/content.ts';
import {
  buildPageData,
  buildProfile,
  buildProjects,
  buildSite,
  buildSkills,
  type BuildOptions,
} from '../../src/lib/data.ts';
import { repoPaths } from '../../src/lib/paths.ts';

const content = loadContent({ github: 'snapshot' });
const avatar = {
  src: '/img/avatar-256.avif',
  srcset: '/img/avatar-128.avif 128w, /img/avatar-256.avif 256w, /img/avatar-512.avif 512w',
};
const mockOptions: BuildOptions = { now: new Date(content.github.syncedAt), mode: 'mock', avatar };
const siteOptions: BuildOptions = { ...mockOptions, mode: 'site' };

const mock = (lang: string, file: string): unknown =>
  JSON.parse(readFileSync(join(repoPaths().mockDir, lang, file), 'utf8'));

describe.each(LOCALES)('contract data, %s', (lang) => {
  it('profile.json', () => {
    expect(buildProfile(content, lang, mockOptions)).toEqual(mock(lang, 'profile.json'));
  });
  it('skills.json', () => {
    expect(buildSkills(content, lang, mockOptions)).toEqual(mock(lang, 'skills.json'));
  });
  it('projects.json', () => {
    expect(buildProjects(content, lang, mockOptions)).toEqual(mock(lang, 'projects.json'));
  });
  it('site.json', () => {
    expect(buildSite(content, lang, mockOptions)).toEqual(mock(lang, 'site.json'));
  });
});

describe('site mode shows only confirmed data', () => {
  const data = buildPageData(content, 'en', siteOptions);
  const skills = data.skills.flatMap((category) =>
    category.children.flatMap((group) => group.children),
  );

  it('hides unconfirmed skills and the groups left empty', () => {
    expect(skills.length).toBeGreaterThan(0);
    expect(skills.every((skill) => skill.confirmed)).toBe(true);
    expect(
      data.skills.every((category) => category.children.every((g) => g.children.length > 0)),
    ).toBe(true);
    const all = buildSkills(content, 'en', mockOptions).flatMap((c) =>
      c.children.flatMap((g) => g.children),
    );
    expect(skills.length).toBeLessThan(all.length);
  });

  it('shows unconfirmed skills once Denis turns the flag on', () => {
    const flagged = structuredClone(content);
    flagged.site.flags.skills.show_unconfirmed = true;
    expect(buildSkills(flagged, 'en', siteOptions)).toEqual(
      buildSkills(content, 'en', mockOptions),
    );
  });

  it('drops private projects that are still TODO', () => {
    expect(JSON.stringify(data.projects.private)).not.toContain('TODO');
  });

  it('applies translated description overrides from projects.yaml', () => {
    const edited = structuredClone(content);
    const target = edited.github.public[0]!;
    edited.projectsConfig.overrides = {
      [target.name.toUpperCase()]: { description: { en: 'Manual', uk: 'Вручну', ru: 'Вручную' } },
    };
    expect(buildPageData(edited, 'uk', siteOptions).projects.public[0]?.description).toBe('Вручну');
    expect(buildPageData(edited, 'en', siteOptions).projects.public[0]?.description).toBe('Manual');
    expect(buildPageData(edited, 'en', siteOptions).projects.public[1]?.description).toBe(
      content.github.public[1]?.description,
    );
  });

  it('never leaks TODO markers or unresolved build placeholders into page texts', () => {
    for (const lang of LOCALES) {
      const page = buildPageData(content, lang, siteOptions);
      const text = JSON.stringify([
        page.profile,
        page.skills,
        page.site.hero,
        page.site.stats,
        page.seo,
      ]);
      expect(text).not.toContain('TODO');
      expect(text).not.toMatch(/\{(years|public_projects|experience)\}/);
    }
  });

  it('builds the SEO description with the experience in words', () => {
    expect(buildPageData(content, 'en', siteOptions).seo.description).toContain('9 years');
    expect(buildPageData(content, 'uk', siteOptions).seo.description).toContain('9 років');
    expect(buildPageData(content, 'ru', siteOptions).seo.description).toContain('9 лет');
  });
});
