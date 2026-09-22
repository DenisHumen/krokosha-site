// Builds the four data objects of the contract (docs/contract.md §2) for one language.
// The same functions produce mock/<lang>/*.json (scripts/gen-mock.ts), so the mocks the design
// works with and the data the real pages get can never drift apart.

import { fill, loc, plural, type Resolved } from '../i18n/localize.ts';
import type { Locale } from '../i18n/locales.ts';
import type { Content } from './content.ts';
import { experienceYears } from './experience.ts';
import { isTodo, type PublicProject, type SiteContent } from './schema.ts';

export interface Avatar {
  src: string;
  srcset: string;
  alt: string;
}

export interface Social {
  id: string;
  label: string;
  url: string;
  primary?: boolean | undefined;
}

export interface Profile {
  name: string;
  nickname: string;
  role: string;
  experience_years: number;
  avatar: Avatar;
  location: string;
  socials: Social[];
}

export interface Skill {
  id: string;
  title: string;
  note: string | null;
  confirmed: boolean;
}
export interface SkillGroup {
  id: string;
  title: string;
  children: Skill[];
}
export interface SkillCategory {
  id: string;
  title: string;
  icon: string;
  children: SkillGroup[];
}

export interface PrivateProject {
  title: string;
  description: string;
  tags: string[];
  note: string;
}

export interface Projects {
  public: PublicProject[];
  private: PrivateProject[];
  syncedAt: string;
  stale: boolean;
}

/** Keys of content/site.yaml that are not page texts and do not go into site.json. */
type NonTextKeys =
  'url' | 'profile' | 'github' | 'timezone' | 'i18n' | 'plurals' | 'socials' | 'seo';

type SiteTexts = Resolved<Omit<SiteContent, NonTextKeys>>;

export interface SiteI18n {
  locale: Locale;
  default: Locale;
  locales: { code: Locale; label: string; href: string }[];
}

type FormTexts = SiteTexts['contacts']['form'];

/** site.json: page texts in one language. */
export type Site = Omit<SiteTexts, 'contacts'> & {
  i18n: SiteI18n;
  contacts: Omit<SiteTexts['contacts'], 'form'> & {
    /** `reply_within_hours` is `null` until Denis sets it: the phrase is then not shown. */
    form: Omit<FormTexts, 'reply_within_hours'> & { reply_within_hours: number | null };
  };
};

export interface BuildOptions {
  /** The moment the build runs at: experience is counted up to this date. */
  now: Date;
  /** URLs of the prepared avatar images. */
  avatar: { src: string; srcset: string };
  /**
   * `mock` keeps everything the design must be checked against: unconfirmed skills and TODO cards.
   * `site` shows only what is confirmed (content/README.md).
   */
  mode: 'site' | 'mock';
}

export function buildProfile(content: Content, lang: Locale, options: BuildOptions): Profile {
  const profile = loc(content.site.profile, lang);
  return {
    name: profile.name,
    nickname: profile.nickname,
    role: profile.role,
    experience_years: experienceYears(profile.career_start, options.now, content.site.timezone),
    avatar: { src: options.avatar.src, srcset: options.avatar.srcset, alt: profile.avatar_alt },
    location: profile.location,
    socials: loc(content.site.socials, lang),
  };
}

export function buildSkills(
  content: Content,
  lang: Locale,
  options: BuildOptions,
): SkillCategory[] {
  const showAll = options.mode === 'mock' || content.site.flags.skills.show_unconfirmed;
  const tree: SkillCategory[] = loc(content.skills, lang);
  if (showAll) return tree;
  return tree
    .map((category) => ({
      ...category,
      children: category.children
        .map((group) => ({ ...group, children: group.children.filter((skill) => skill.confirmed) }))
        .filter((group) => group.children.length > 0),
    }))
    .filter((category) => category.children.length > 0);
}

export function buildProjects(content: Content, lang: Locale, options: BuildOptions): Projects {
  const all: PrivateProject[] = loc(content.projectsConfig.private_projects, lang);
  const privateProjects =
    options.mode === 'mock'
      ? all.map((project) => ({
          ...project,
          title: isTodo(project.title) ? 'TODO' : project.title,
          description: isTodo(project.description) ? 'TODO' : project.description,
        }))
      : all.filter((project) => !isTodo(project.title) && !isTodo(project.description));
  // A manual description from content/projects.yaml → overrides replaces the GitHub one. It is applied
  // here and not by the GitHub sync because it can be translated, while github.json has no languages.
  const descriptions = new Map(
    Object.entries(content.projectsConfig.overrides).flatMap(([name, override]) =>
      override.description === undefined
        ? []
        : [[name.toLowerCase(), loc(override.description, lang)] as const],
    ),
  );
  const publicProjects = content.github.public.map((project) => {
    const description = descriptions.get(project.name.toLowerCase());
    return description === undefined ? project : { ...project, description };
  });

  return {
    public: publicProjects,
    private: privateProjects,
    syncedAt: content.github.syncedAt,
    stale: content.github.stale,
  };
}

export function buildSite(content: Content, lang: Locale, options: BuildOptions): Site {
  const { site } = content;
  const years = experienceYears(site.profile.career_start, options.now, site.timezone);
  const skills = buildSkills(content, lang, options)
    .flatMap((category) => category.children)
    .reduce((sum, group) => sum + group.children.length, 0);
  const words = {
    years: plural(site.plurals.years[lang], years, lang),
    public_projects: plural(site.plurals.public_projects[lang], content.github.public.length, lang),
    skills: plural(site.plurals.skills[lang], skills, lang),
  };

  // eslint-disable-next-line @typescript-eslint/no-unused-vars -- dropping the non-text keys
  const { url, profile, github, timezone, i18n, plurals, socials, seo, ...rest } = site;
  const texts: SiteTexts = loc(rest, lang);
  const replyWithin = texts.contacts.form.reply_within_hours;

  return {
    i18n: {
      locale: lang,
      default: i18n.default,
      locales: i18n.locales.map(({ code, label, path }) => ({ code, label, href: path })),
    },
    ...texts,
    hero: { ...texts.hero, experience_label: fill(texts.hero.experience_label, words) },
    ui: { ...texts.ui, skills_headline: fill(texts.ui.skills_headline, words) },
    eggs: {
      ...texts.eggs,
      terminal: {
        ...texts.eggs.terminal,
        uptime: fill(texts.eggs.terminal.uptime, { years: `${years} ${words.years}` }),
      },
    },
    stats: {
      experience: fill(texts.stats.experience, words),
      public_projects: fill(texts.stats.public_projects, words),
    },
    contacts: {
      ...texts.contacts,
      form: {
        ...texts.contacts.form,
        reply_within_hours: typeof replyWithin === 'number' ? replyWithin : null,
      },
    },
  };
}

export interface SeoTexts {
  title: string;
  description: string;
  ogImageAlt: string;
  serviceName: string;
  areaServed: string[];
  countryCode: string;
  /** <meta> proofs for Google Search Console and Bing Webmaster Tools, if any. */
  verification: { google?: string | undefined; bing?: string | undefined };
}

/** Texts of <head>; not part of site.json — the design does not need them. */
export function buildSeo(content: Content, lang: Locale, options: BuildOptions): SeoTexts {
  const { site } = content;
  const seo = loc(site.seo, lang);
  const years = experienceYears(site.profile.career_start, options.now, site.timezone);
  const experience = `${years} ${plural(site.plurals.years[lang], years, lang)}`;
  return {
    title: seo.title,
    description: fill(seo.description, { experience }),
    ogImageAlt: seo.og_image_alt,
    serviceName: seo.service_name,
    areaServed: seo.area_served,
    countryCode: seo.country_code,
    verification: site.seo.verification ?? {},
  };
}

export interface PageData {
  lang: Locale;
  profile: Profile;
  skills: SkillCategory[];
  projects: Projects;
  site: Site;
  seo: SeoTexts;
}

export function buildPageData(content: Content, lang: Locale, options: BuildOptions): PageData {
  return {
    lang,
    profile: buildProfile(content, lang, options),
    skills: buildSkills(content, lang, options),
    projects: buildProjects(content, lang, options),
    site: buildSite(content, lang, options),
    seo: buildSeo(content, lang, options),
  };
}
