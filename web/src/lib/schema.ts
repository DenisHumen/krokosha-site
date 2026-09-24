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

/** An achievement of the easter eggs: the banner's name and its line of description. */
const achievement = z.strictObject({ name: localized, text: localized });

const percent = z.number().int().min(0).max(100);

/**
 * Discounts and levels of regular clients (api/internal/loyalty). The API reads the same rules and
 * refuses the same mistakes (api/internal/config/loyalty.go): a level that asks less than the one
 * before it, or gives less.
 */
const loyaltySchema = z
  .strictObject({
    enabled: z.boolean(),
    currency: z.string().regex(/^[A-Z]{3}$/, 'expected an ISO 4217 code, e.g. USD'),
    welcome: percent,
    eggs: percent,
    // The sum from which an order earns the «big project» achievement; 0 — never.
    big_order: z.number().min(0),
    tiers: z.array(
      z
        .strictObject({
          id: z.string().regex(/^[a-z][a-z0-9-]{0,15}$/, 'expected a short lowercase id'),
          name: localized,
          orders: z.number().int().min(0),
          spent: z.number().min(0),
          discount: percent,
        })
        .refine((tier) => tier.orders > 0 || tier.spent > 0, {
          message: 'a level needs orders or spent above zero',
        }),
    ),
  })
  .superRefine((rules, ctx) => {
    const ids = new Set<string>();
    rules.tiers.forEach((tier, i) => {
      if (ids.has(tier.id))
        ctx.addIssue({ code: 'custom', message: `duplicate level "${tier.id}"` });
      ids.add(tier.id);
      const prev = rules.tiers[i - 1];
      if (
        prev &&
        (tier.discount < prev.discount ||
          (tier.orders > 0 && prev.orders > 0 && tier.orders <= prev.orders) ||
          (tier.spent > 0 && prev.spent > 0 && tier.spent <= prev.spent))
      ) {
        ctx.addIssue({
          code: 'custom',
          message: `level "${tier.id}" must ask more than "${prev.id}" and give no less`,
        });
      }
    });
  });

/** Texts of the personal account (/account/, api/internal/clients). */
const accountSchema = z.strictObject({
  title: localized,
  description: localized,
  link: localized,
  no_script: localized,
  loading: localized,
  login: z.strictObject({
    lead: localized,
    method: localized,
    email: localized,
    telegram: localized,
    email_label: localized,
    send: localized,
    telegram_text: localized,
    telegram_open: localized,
    sent_email: localized,
    sent_telegram: localized,
    code: localized,
    submit: localized,
    back: localized,
    link: localized,
    note: localized,
  }),
  // One per error code of the API (api/internal/clients/http.go), plus the page's own.
  errors: z.strictObject({
    bad_email: localized,
    bad_code: localized,
    throttled: localized,
    no_bot: localized,
    taken: localized,
    disabled: localized,
    signed_out: localized,
    bad_contact: localized,
    too_many: localized,
    empty: localized,
    too_long: localized,
    last_way: localized,
    no_way_back: localized,
    server: localized,
    network: localized,
  }),
  home: z.strictObject({
    hello: localized,
    hello_plain: localized,
    since: localized,
    new_request: localized,
    sign_out: localized,
  }),
  loyalty: z.strictObject({
    title: localized,
    next: localized,
    none: localized,
    level: localized,
    newcomer: localized,
    orders: localized,
    spent: localized,
    to_next: localized,
    more_orders: localized,
    more_spent: localized,
    top: localized,
    personal: localized,
    personal_once: localized,
    personal_until: localized,
    levels: localized,
    level_rule: localized,
    rules: localized,
    reasons: z.strictObject({
      welcome: localized,
      eggs: localized,
      tier: localized,
      personal: localized,
      manual: localized,
    }),
  }),
  requests: z.strictObject({
    title: localized,
    empty: localized,
    request: localized,
    inquiry: localized,
    unread: localized,
    about: localized,
    back: localized,
    direction: localized,
    budget: localized,
    timeline: localized,
    contact: localized,
    discount: localized,
    amount: localized,
    conversation: localized,
    you: localized,
    answer: localized,
    status: localized,
    files: localized,
    write: localized,
    send: localized,
    sent: localized,
    closed: localized,
    ask: localized,
    missing: localized,
  }),
  // The statuses of a request as the client sees them (api/internal/leads/store.go), spam aside.
  statuses: z.strictObject({
    new: localized,
    in_progress: localized,
    waiting_client: localized,
    done: localized,
    rejected: localized,
  }),
  channels: z.strictObject({
    form: localized,
    site: localized,
    email: localized,
    telegram: localized,
    phone: localized,
  }),
  inquiry: z.strictObject({
    title: localized,
    lead: localized,
    subject: localized,
    text: localized,
    parent: localized,
    none: localized,
    send: localized,
    sent: localized,
  }),
  contacts: z.strictObject({
    title: localized,
    lead: localized,
    kind: localized,
    value: localized,
    add: localized,
    remove: localized,
    empty: localized,
    // The kinds the API accepts (api/internal/clients/contacts.go → NormalizeContact).
    kinds: z.strictObject({
      phone: localized,
      whatsapp: localized,
      viber: localized,
      signal: localized,
      telegram: localized,
      linkedin: localized,
      facebook: localized,
      instagram: localized,
      x: localized,
      discord: localized,
      skype: localized,
      github: localized,
      website: localized,
    }),
  }),
  profile: z.strictObject({
    title: localized,
    name: localized,
    company: localized,
    lang: localized,
    preferred: localized,
    preferred_email: localized,
    preferred_telegram: localized,
    save: localized,
    saved: localized,
  }),
  access: z.strictObject({
    title: localized,
    email: localized,
    telegram: localized,
    missing: localized,
    change_email: localized,
    add_email: localized,
    link_telegram: localized,
    unlink: localized,
    linked: localized,
    cancel: localized,
  }),
  sessions: z.strictObject({
    title: localized,
    current: localized,
    seen: localized,
    end: localized,
    end_all: localized,
  }),
  eggs: z.strictObject({ title: localized, open: localized, heading: localized }),
  // The achievements of orders (api/internal/clients/orders.go).
  orders: z.strictObject({
    title: localized,
    found: localized,
    rarity: localized,
    locked: localized,
    earned: localized,
    first_order: achievement,
    second_order: achievement,
    big_order: achievement,
    all_orders: achievement,
  }),
  delete: z.strictObject({
    title: localized,
    text: localized,
    confirm: localized,
    button: localized,
  }),
});

/** Texts of the /map page (docs/netmap.md). */
const mapSchema = z.strictObject({
  title: localized,
  status: text,
  lead: localized,
  description: localized,
  stats: z.strictObject({
    networks: localized,
    links: localized,
    exchanges: localized,
    updated: localized,
  }),
  views: z.strictObject({
    label: localized,
    map: localized,
    core: localized,
    globe: localized,
    core_hint: localized,
  }),
  controls: z.strictObject({
    zoom_in: localized,
    zoom_out: localized,
    reset: localized,
    hint: localized,
  }),
  legend: z.strictObject({
    network: localized,
    exchange: localized,
    bundle: localized,
    route: localized,
  }),
  form: z.strictObject({
    heading: localized,
    from: localized,
    to: localized,
    from_placeholder: localized,
    to_placeholder: localized,
    submit: localized,
    swap: localized,
    mine: localized,
    examples_label: localized,
    examples: z
      .array(z.strictObject({ ip: z.union([z.ipv4(), z.ipv6()]), label: localized }))
      .min(1),
  }),
  panel: z.strictObject({
    estimated: localized,
    observed: localized,
    networks: pluralForms,
    rtt: localized,
    ms: localized,
    columns: z.strictObject({
      hop: text,
      network: localized,
      relation: localized,
      meeting: localized,
      ports: localized,
      delay: localized,
      loss: localized,
    }),
    rel: z.strictObject({
      customer: localized,
      provider: localized,
      peer: localized,
      unknown: localized,
    }),
    meet: z.strictObject({ ix: localized, facility: localized, guess: localized }),
    start: localized,
    not_measured: localized,
    anycast: localized,
    note: localized,
  }),
  trace: z.strictObject({
    measure: localized,
    measuring: localized,
    heading: localized,
    model: localized,
    show: localized,
    shown: localized,
    hops: pluralForms,
    columns: z.strictObject({
      hop: text,
      address: localized,
      network: localized,
      answered: localized,
      time: localized,
    }),
    silent: localized,
    arrived: localized,
    refused: localized,
    exchange: localized,
    tcp: localized,
    no_tcp: localized,
    not_reached: localized,
    note: localized,
  }),
  network: z.strictObject({
    heading: localized,
    cone: localized,
    rank: localized,
    customers: localized,
    providers: localized,
    peers: localized,
    exchanges: localized,
    close: localized,
  }),
  errors: z.strictObject({
    bad_address: localized,
    private_address: localized,
    not_routed: localized,
    no_path: localized,
    busy: localized,
    not_ready: localized,
    network: localized,
    trace_failed: localized,
  }),
  loading: localized,
  no_webgl: localized,
  no_data: localized,
  about: z.strictObject({ heading: localized, paragraphs: z.array(localized).min(1) }),
  credits: z.strictObject({
    heading: localized,
    items: z.array(z.strictObject({ name: text, url: httpsUrl, text: localized })).min(1),
    citation: text,
  }),
});

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
  plurals: z.strictObject({
    years: pluralForms,
    public_projects: pluralForms,
    skills: pluralForms,
    orders: pluralForms,
  }),
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
  // A section of the home page (anchor) or a page of its own (page), never both.
  nav: z.array(
    z
      .strictObject({
        number: text,
        anchor: slug.optional(),
        page: slug.optional(),
        label: localized,
      })
      .refine((item) => (item.anchor === undefined) !== (item.page === undefined), {
        message: 'a nav item has either an anchor or a page',
      }),
  ),
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
    skills_headline: localized,
    menu: localized,
    socials: localized,
    contents: localized,
    draft: localized,
  }),
  not_found: z.strictObject({
    title: localized,
    text: localized,
    game_hint: localized,
    back: localized,
    game: z.strictObject({
      prompt: localized,
      warm: localized,
      cold: localized,
      found: localized,
      port: localized,
    }),
  }),
  footer: z.strictObject({
    copyright: localized,
    game_entry: localized,
    play_stub: localized,
    play_status: localized,
  }),
  eggs: z.strictObject({
    found: localized,
    // One achievement per egg (design/components/eggs/eggs.js → ALL), and «all» for finding every one.
    achievements: z.strictObject({
      konami: achievement,
      sudo: achievement,
      croc: achievement,
      croc5: achievement,
      cat: achievement,
      reboot: achievement,
      console: achievement,
      lost_packet: achievement,
      all: achievement,
    }),
    rarity: localized,
    // The panel of achievements, opened by the «eggs 3/8» counter (components/Achievements.astro).
    panel: z.strictObject({
      title: localized,
      open: localized,
      progress: localized,
      locked: localized,
      found_on: localized,
      rare: localized,
      counting: localized,
      discount: localized,
      discount_ready: localized,
      discount_used: localized,
      account: localized,
      close: localized,
    }),
    night: localized,
    croc: localized,
    console: localized,
    terminal: z.strictObject({
      whoami: localized,
      uptime: localized,
      ping: localized,
      rm: localized,
    }),
  }),
  seo: z.strictObject({
    title: localized,
    description: localized,
    og_image_alt: localized,
    service_name: localized,
    area_served: z.array(localized),
    country_code: z.string().regex(/^[A-Z]{2}$/, 'expected an ISO 3166-1 alpha-2 code'),
    // Proofs of ownership for the webmaster tools, when the DNS record is not an option (deploy/README.md).
    verification: z
      .strictObject({
        google: z
          .string()
          .regex(/^[A-Za-z0-9_-]{8,128}$/)
          .optional(),
        bing: z
          .string()
          .regex(/^[0-9A-Fa-f]{16,64}$/)
          .optional(),
      })
      .optional(),
  }),
  loyalty: loyaltySchema,
  account: accountSchema,
  map: mapSchema,
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
