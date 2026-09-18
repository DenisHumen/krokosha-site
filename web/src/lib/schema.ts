// Schemas of content/*.yaml and content/generated/github.json.
// Objects are strict on purpose: a typo in a key fails the build with a clear message
// instead of silently dropping a text from the page.

import { z } from 'zod';
import { LOCALES } from '../i18n/locales.ts';

const text = z.string().trim().min(1);

function dictOf<T extends z.ZodType>(value: T) {
  return z.strictObject({ en: value, uk: value, ru: value });
}

/** A string (same in every language) or a `{ en, uk, ru }` dictionary. */
const localized = z.union([text, dictOf(text)]);
/** Same, but a caption may be absent — entirely or in some languages. */
const localizedOrNull = z.union([z.null(), text, dictOf(text.nullable())]);

const slug = z.string().regex(/^[a-z0-9]+(-[a-z0-9]+)*$/, 'expected a lowercase-kebab-case id');
const isoDate = z.string().regex(/^\d{4}-\d{2}-\d{2}$/, 'expected YYYY-MM-DD');
const httpsUrl = z.url({ protocol: /^https$/ });
const webUrl = z.url({ protocol: /^https?$/ });
/** Links rendered as `href`: only web and mail links, never `javascript:` and the like. */
const linkUrl = z.union([
  httpsUrl,
  z.string().regex(/^mailto:[^\s@]+@[^\s@]+$/, 'expected mailto:user@host'),
]);

/** `TODO(Денис)` — a value Denis has not provided yet. */
export const TODO_PREFIX = 'TODO';
export function isTodo(value: unknown): value is string {
  return typeof value === 'string' && value.startsWith(TODO_PREFIX);
}
const todo = z.string().startsWith(TODO_PREFIX);

const pluralForms = z.strictObject({
  en: z.tuple([text, text]),
  uk: z.tuple([text, text, text]),
  ru: z.tuple([text, text, text]),
});

export const tierSchema = z.enum(['featured', 'standard', 'compact']);

const option = z.strictObject({ id: slug, label: localized });

export const siteSchema = z.strictObject({
  url: httpsUrl,
  profile: z.strictObject({
    name: localized,
    nickname: text,
    role: localized,
    location: localized,
    career_start: isoDate,
    avatar_source: httpsUrl,
    avatar_alt: localized,
  }),
  github: z.strictObject({ user: text }),
  timezone: text,
  i18n: z.strictObject({
    default: z.enum(LOCALES),
    locales: z
      .array(
        z.strictObject({ code: z.enum(LOCALES), label: text, path: z.string().startsWith('/') }),
      )
      .length(LOCALES.length),
  }),
  plurals: z.strictObject({ years: pluralForms, public_projects: pluralForms }),
  socials: z
    .array(
      z.strictObject({
        id: slug,
        label: localized,
        url: linkUrl,
        primary: z.boolean().optional(),
      }),
    )
    .min(1),
  nav: z.array(z.strictObject({ number: text, anchor: slug, label: localized })),
  hero: z.strictObject({
    headline: z.strictObject({ muted: localized, strong: localized }),
    lead: localized,
    experience_label: localized,
    captions: z.strictObject({ left: localized, center: localized, right: localized }),
    link_caption: localized,
    cta: z.strictObject({ telegram: localized, email: localized, discuss: localized }),
  }),
  services: z.array(
    z.strictObject({
      id: slug,
      number: text,
      title: localized,
      subtitle: localized,
      text: localized,
    }),
  ),
  stats: z.strictObject({ experience: localized, public_projects: localized }),
  projects: z.strictObject({
    heading: localized,
    note: localized,
    private_heading: localized,
    private_note: localized,
    show_all_label: localized,
    error_note: localized,
    labels: z.strictObject({
      archived: localized,
      updated: localized,
      website: localized,
      source: localized,
    }),
  }),
  curtain: z.strictObject({ heading: localized, text: localized, cta: localized }),
  contacts: z.strictObject({
    heading: localized,
    text: localized,
    form: z.strictObject({
      enabled: z.boolean(),
      attachments: z.boolean(),
      reply_within_hours: z.union([z.number().int().positive(), todo]),
      labels: z.record(z.string(), localized),
      placeholders: z.record(z.string(), localized),
      messages: z.record(z.string(), localized),
      errors: z.record(z.string(), localized),
      directions: z.array(option),
      contact_methods: z.array(option),
      budgets: z.array(localized),
      timelines: z.array(localized),
    }),
  }),
  ui: z.strictObject({
    headings: z.strictObject({ services: localized, skills: localized, stats: localized }),
    skip_to_content: localized,
    theme_toggle: localized,
    language: localized,
    skills_search: localized,
    skills_no_results: localized,
  }),
  not_found: z.strictObject({
    title: localized,
    text: localized,
    game_hint: localized,
    back: localized,
  }),
  footer: z.strictObject({ copyright: localized, game_entry: localized, play_stub: localized }),
  seo: z.strictObject({
    title: localized,
    description: localized,
    og_image_alt: localized,
    service_name: localized,
    area_served: z.array(localized),
    country_code: z.string().regex(/^[A-Z]{2}$/, 'expected an ISO 3166-1 alpha-2 code'),
  }),
  flags: z.strictObject({
    skills: z.strictObject({ show_unconfirmed: z.boolean() }),
    ads: z.strictObject({ pixels: z.boolean(), consent_banner: z.boolean() }),
    easter_eggs: z.object({ enabled: z.boolean() }).catchall(z.boolean()),
  }),
});

const skillSchema = z.strictObject({
  id: slug,
  title: localized,
  note: localizedOrNull,
  confirmed: z.boolean(),
});
const skillGroupSchema = z.strictObject({
  id: slug,
  title: localized,
  children: z.array(skillSchema),
});
const skillCategorySchema = z.strictObject({
  id: slug,
  title: localized,
  icon: z.enum(['network', 'server', 'storage', 'devops', 'os', 'code']),
  children: z.array(skillGroupSchema),
});

export const skillsSchema = z.array(skillCategorySchema).superRefine((categories, ctx) => {
  // Ids end up in data-track="skill-<id>" and must be unique across the whole tree.
  const seen = new Set<string>();
  const ids = categories.flatMap((category) => [
    category.id,
    ...category.children.flatMap((group) => [group.id, ...group.children.map((skill) => skill.id)]),
  ]);
  for (const id of ids) {
    if (seen.has(id)) ctx.addIssue({ code: 'custom', message: `duplicate skill id "${id}"` });
    seen.add(id);
  }
});

export const projectsConfigSchema = z.strictObject({
  pinned: z.array(text),
  exclude: z.array(text),
  overrides: z.record(
    z.string(),
    z.strictObject({ tier: tierSchema.optional(), description: localized.optional() }),
  ),
  show_archived: z.boolean(),
  homepage_ignore_hosts: z.array(text),
  private_projects: z.array(
    z.strictObject({ title: localized, description: localized, tags: z.array(text), note: text }),
  ),
});

export const publicProjectSchema = z.strictObject({
  name: text,
  description: text.nullable(),
  language: text.nullable(),
  languageColor: z
    .string()
    .regex(/^#[0-9a-fA-F]{6}$/, 'expected #rrggbb')
    .nullable(),
  topics: z.array(text),
  stars: z.number().int().nonnegative(),
  updatedAt: isoDate,
  url: httpsUrl,
  homepage: webUrl.nullable(),
  pinned: z.boolean(),
  tier: tierSchema,
  archived: z.boolean(),
});

/** content/generated/github.json — written by the GitHub sync (api/internal/githubsync). */
export const githubDataSchema = z.object({
  public: z.array(publicProjectSchema),
  syncedAt: z.iso.datetime(),
  stale: z.boolean(),
});

export type SiteContent = z.infer<typeof siteSchema>;
export type SkillsContent = z.infer<typeof skillsSchema>;
export type ProjectsConfig = z.infer<typeof projectsConfigSchema>;
export type PublicProject = z.infer<typeof publicProjectSchema>;
export type GithubData = z.infer<typeof githubDataSchema>;
export type Tier = z.infer<typeof tierSchema>;
