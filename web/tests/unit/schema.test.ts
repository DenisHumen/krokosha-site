// A typo in content/*.yaml must stop the build, not quietly change the site.

import { describe, expect, it } from 'vitest';
import { loadContent } from '../../src/lib/content.ts';
import {
  githubDataSchema,
  projectsConfigSchema,
  siteSchema,
  skillsSchema,
} from '../../src/lib/schema.ts';

const content = loadContent({ github: 'snapshot' });

describe('content/*.yaml in the repository', () => {
  it('is valid', () => {
    expect(siteSchema.safeParse(content.site).success).toBe(true);
    expect(skillsSchema.safeParse(content.skills).success).toBe(true);
    expect(projectsConfigSchema.safeParse(content.projectsConfig).success).toBe(true);
  });
});

describe('siteSchema', () => {
  const broken = (patch: (site: Record<string, any>) => void) => {
    const site = structuredClone(content.site) as Record<string, any>;
    patch(site);
    return siteSchema.safeParse(site).success;
  };

  it('rejects unknown keys (typos)', () => {
    expect(broken((site) => (site.hero.leed = 'typo'))).toBe(false);
  });

  it('rejects a career start that is not a date', () => {
    expect(broken((site) => (site.profile.career_start = 'September 2017'))).toBe(false);
  });

  it('accepts only https and mailto links for socials', () => {
    expect(broken((site) => (site.socials[0].url = 'javascript:alert(1)'))).toBe(false);
    expect(broken((site) => (site.socials[0].url = 'http://t.me/DenisHumen'))).toBe(false);
    expect(broken((site) => (site.socials[0].url = 'https://t.me/DenisHumen'))).toBe(true);
    expect(broken((site) => (site.socials[1].url = 'mailto:denis@krokosha.xyz'))).toBe(true);
  });

  it('requires all plural forms', () => {
    expect(broken((site) => (site.plurals.years.uk = ['рік', 'роки']))).toBe(false);
  });

  it('takes the verification tokens of the webmaster tools as they are issued, nothing else', () => {
    expect(broken((site) => (site.seo.verification = { google: 'AbC-123_xyz789' }))).toBe(true);
    expect(broken((site) => (site.seo.verification = { bing: 'ABCDEF0123456789' }))).toBe(true);
    expect(broken((site) => (site.seo.verification = { google: '<script>' }))).toBe(false);
    expect(broken((site) => (site.seo.verification = { yandex: 'abc' }))).toBe(false);
  });

  it('accepts a number of hours or a TODO marker for reply_within_hours', () => {
    expect(broken((site) => (site.contacts.form.reply_within_hours = 24))).toBe(true);
    expect(broken((site) => (site.contacts.form.reply_within_hours = 'TODO(Денис)'))).toBe(true);
    expect(broken((site) => (site.contacts.form.reply_within_hours = 'soon'))).toBe(false);
  });
});

describe('skillsSchema', () => {
  it('rejects duplicate ids anywhere in the tree', () => {
    const skills = structuredClone(content.skills);
    const first = skills[0]?.children[0]?.children[0];
    const last = skills.at(-1)?.children.at(-1)?.children.at(-1);
    if (!first || !last) throw new Error('skills.yaml is unexpectedly empty');
    last.id = first.id;
    expect(skillsSchema.safeParse(skills).success).toBe(false);
  });
});

describe('githubDataSchema', () => {
  const repo = content.github.public[0];

  it('accepts the snapshot', () => {
    expect(githubDataSchema.safeParse(content.github).success).toBe(true);
  });

  it('rejects links that are not web links', () => {
    const data = { ...content.github, public: [{ ...repo, homepage: 'javascript:alert(1)' }] };
    expect(githubDataSchema.safeParse(data).success).toBe(false);
  });

  it('rejects an unknown tier', () => {
    const data = { ...content.github, public: [{ ...repo, tier: 'huge' }] };
    expect(githubDataSchema.safeParse(data).success).toBe(false);
  });
});
