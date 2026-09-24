// The personal account (pages/[...lang]/account.astro; design v3): signing in with a one-time code,
// then an application of six screens — home, requests, a new inquiry, the discount and the level,
// achievements, the profile — switched by the address: #requests, #K-0042 (a request of the list,
// the links of letters and of the bot lead here), #new, #loyalty, #achievements, #profile.
// Everything comes from /api/account/* (api/internal/clients/http.go): JSON both ways, the session in
// an HttpOnly cookie, and every change carries the session's CSRF token in a header.

import type { Site } from '../lib/data.ts';
import { accountHint, EGGS, receipts } from './achievements.ts';
import { defineGlyphField } from './glyph-field.ts';

type Texts = Site['account'];
type Screen = 'home' | 'requests' | 'new' | 'loyalty' | 'achievements' | 'profile';
const SCREENS: readonly Screen[] = [
  'home',
  'requests',
  'new',
  'loyalty',
  'achievements',
  'profile',
];

interface Tier {
  id: string;
  name: string;
  discount: number;
  orders: number;
  spent: number;
}

interface PageTexts {
  lang: string;
  account: Texts;
  plurals: { orders: string[]; requests: string[]; open: string[] };
  examples: Record<string, string>;
  directions: Record<string, string>;
  eggs: Record<string, string>;
  loyalty: { enabled: boolean; currency: string; eggs: number; tiers: Tier[] };
}

interface Offer {
  percent: number;
  reason?: string;
  detail?: string;
}

interface Me {
  ok: boolean;
  csrf: string;
  client: {
    id: number;
    name: string;
    company: string;
    lang: string;
    email: string;
    telegram: string;
    preferred: string;
    since: string;
  };
  contacts: { id: number; kind: string; value: string }[];
  loyalty: {
    enabled: boolean;
    currency: string;
    orders: number;
    spent: number;
    offer: Offer;
    eggs_used: boolean;
    welcome_used: boolean;
    tier?: string;
    next?: { tier: string; orders: number; spent: number; progress: number };
    personal?: { percent: number; note: string; once: boolean; until?: string };
  };
  eggs: { id: string; at: string }[];
  // The achievements of orders: what the account has (new — not shown yet), and how many have each.
  orders: {
    earned: { id: string; at: string; new: boolean }[];
    shares: Record<string, number>;
    big_order: number;
  };
  sessions: { id: string; created: string; seen: string; device: string; current: boolean }[];
  bot: boolean;
}

interface Summary {
  number: string;
  kind: 'request' | 'inquiry';
  status: string;
  created: string;
  updated: string;
  direction: string;
  subject?: string;
  excerpt: string;
  amount?: number;
  parent?: string;
  unread: boolean;
  answered?: string;
  discount: Offer;
}

interface View extends Summary {
  description: string;
  budget?: string;
  timeline?: string;
  contact: string;
  method: string;
  can_write: boolean;
  feed: {
    at: string;
    kind: 'message' | 'status' | 'call';
    direction?: 'in' | 'out';
    channel?: string;
    body?: string;
    status?: string;
    files?: FileOf[];
  }[];
}

/** A file of the conversation: the account hands it out (/api/account/leads/<K-0042>/files/<id>). */
interface FileOf {
  id: number;
  name: string;
  kind: string;
  size: number;
}

interface Answer {
  ok?: boolean;
  error?: string;
  [key: string]: unknown;
}

type OrderId = 'first_order' | 'second_order' | 'big_order' | 'all_orders';

const fill = (template: string, values: Record<string, string | number>) =>
  template.replace(/\{([a-z_]+)\}/g, (match, name: string) =>
    name in values ? String(values[name]) : match,
  );

function $<T extends Element = HTMLElement>(root: ParentNode, selector: string): T {
  const node = root.querySelector<T>(selector);
  if (!node) throw new Error(`account: ${selector} is missing`);
  return node;
}

const all = <T extends Element = HTMLElement>(root: ParentNode, selector: string): T[] => [
  ...root.querySelectorAll<T>(selector),
];

const OPEN_STATUSES = new Set(['new', 'in_progress', 'waiting_client']);

/** The eggs this browser found (design/components/eggs/eggs.js keeps them). */
function browserFinds(): string[] {
  try {
    const list: unknown = JSON.parse(localStorage.getItem('krokosha:eggs') ?? '[]');
    return Array.isArray(list) ? list.filter((id): id is string => typeof id === 'string') : [];
  } catch {
    return [];
  }
}

export function initAccount(): void {
  const root = document.querySelector<HTMLElement>('[data-account]');
  if (!root) return;
  const T = JSON.parse(root.dataset['texts'] ?? '{}') as PageTexts;
  const A = T.account;
  const lang = T.lang;
  defineGlyphField();

  // --- formatting --------------------------------------------------------------------------------

  const clock = new Intl.DateTimeFormat(lang, {
    hour: '2-digit',
    minute: '2-digit',
    hourCycle: 'h23',
  });
  const time = (value: Date) => clock.format(value);
  /** «22 Sep», «22 сент 2026»: the day, the short month without its dot, the year if asked. */
  function shortDate(value: string | Date, year = false): string {
    // «YYYY-MM-DD» is a day, not a moment: read as UTC midnight, it is shown in UTC too, or a
    // visitor west of Greenwich would see the day before (a personal discount's last day).
    const dayOnly = typeof value === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(value);
    const parts = new Intl.DateTimeFormat(lang, {
      day: 'numeric',
      month: 'short',
      ...(year ? { year: 'numeric' as const } : {}),
      ...(dayOnly ? { timeZone: 'UTC' } : {}),
    }).formatToParts(new Date(value));
    const part = (type: string) => parts.find((item) => item.type === type)?.value ?? '';
    return [part('day'), part('month').replace(/\.$/, ''), part('year')].filter(Boolean).join(' ');
  }
  /** «3 March 2026» for «with us since». */
  const longDate = (value: string) =>
    new Intl.DateTimeFormat(lang, { day: 'numeric', month: 'long', year: 'numeric' })
      .format(new Date(value))
      .replace(/\s?[гр]\.$/u, '');
  const moment = (value: string) => `${shortDate(value)} ${time(new Date(value))}`;
  const dayNumber = (value: Date) =>
    Math.floor((value.getTime() - value.getTimezoneOffset() * 60_000) / 86_400_000);
  /** «today 09:12», «yesterday 21:40», «22 Sep». */
  function relative(value: string): string {
    const at = new Date(value);
    const now = new Date();
    const days = dayNumber(now) - dayNumber(at);
    if (days <= 0) return fill(A.ticker.today, { time: time(at) });
    if (days === 1) return fill(A.ticker.yesterday, { time: time(at) });
    return shortDate(at, at.getFullYear() !== now.getFullYear());
  }
  /** «09:12» today, «yesterday 21:40», «22 Sep»: the shortest that is still clear. */
  function stamp(value: string): string {
    const at = new Date(value);
    return dayNumber(new Date()) === dayNumber(at) ? time(at) : relative(value);
  }

  let currency = T.loyalty.currency || 'USD';
  const money = (value: number) =>
    new Intl.NumberFormat(lang, {
      style: 'currency',
      currency,
      currencyDisplay: 'narrowSymbol',
      maximumFractionDigits: 0,
    }).format(value);
  const rules = new Intl.PluralRules(lang);
  function form(forms: string[], n: number): string {
    const category = rules.select(n);
    return (
      (forms.length === 2
        ? forms[category === 'one' ? 0 : 1]
        : forms[category === 'one' ? 0 : category === 'few' ? 1 : 2]) ?? ''
    );
  }
  const count = (forms: string[], n: number) => `${n} ${form(forms, n)}`.trim();
  /** «16 kB», «4,2 МБ»: the units of the language, from the browser. */
  const size = (bytes: number) => {
    const [value, unit] =
      bytes >= 1 << 20
        ? [bytes / (1 << 20), 'megabyte']
        : bytes >= 1024
          ? [bytes / 1024, 'kilobyte']
          : [bytes, 'byte'];
    return new Intl.NumberFormat(lang, {
      style: 'unit',
      unit,
      unitDisplay: 'short',
      maximumFractionDigits: value < 10 ? 1 : 0,
    }).format(value);
  };
  const percent = (value: number) => (value > 0 ? `−${value}%` : '0%');
  const shareOf = (share: number) => {
    const format = new Intl.NumberFormat(lang, {
      style: 'percent',
      maximumFractionDigits: share < 10 ? 1 : 0,
    });
    return share > 0 && share < 0.1 ? `<${format.format(0.001)}` : format.format(share / 100);
  };
  const tierOf = (id?: string) => T.loyalty.tiers.find((tier) => tier.id === id);
  const statusName = (status: string) => (A.statuses as Record<string, string>)[status] ?? status;
  const error = (code?: string) =>
    (A.errors as Record<string, string>)[code === 'server_error' ? 'server' : (code ?? '')] ??
    A.errors.server;
  function reasonText(offer: Offer): string {
    const reasons = A.loyalty.reasons as Record<string, string>;
    if (offer.reason === 'tier')
      return fill(reasons['tier'] ?? '', { tier: tierOf(offer.detail)?.name ?? '' });
    if (offer.reason === 'personal' || offer.reason === 'manual')
      return [reasons[offer.reason], offer.detail].filter(Boolean).join(' · ');
    return reasons[offer.reason ?? ''] ?? '';
  }
  const titleOf = (item: Summary) =>
    item.subject || T.directions[item.direction] || item.direction || '';
  const kindOf = (item: Summary) =>
    item.kind === 'inquiry' ? A.requests.inquiry : A.requests.request;
  const upper = (text: string) => text.toLocaleUpperCase(lang);

  // --- the API ----------------------------------------------------------------------------------

  let csrf = '';

  async function api<R extends Answer>(
    method: 'GET' | 'POST',
    path: string,
    body?: unknown,
  ): Promise<{ status: number; data: R }> {
    let response: Response;
    try {
      response = await fetch(path, {
        method,
        headers: {
          Accept: 'application/json',
          ...(method === 'POST'
            ? { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf }
            : {}),
        },
        body: method === 'POST' ? JSON.stringify(body ?? {}) : null,
        credentials: 'same-origin',
      });
    } catch {
      return { status: 0, data: { ok: false, error: 'network' } as R };
    }
    const data = (await response.json().catch(() => ({ ok: false, error: 'server' }))) as R;
    // A session that ended while the page was open (signed out on another device, expired).
    if (response.status === 401 && view === 'app') signedOut(A.errors.signed_out);
    return { status: response.status, data };
  }

  /** Disables a form's buttons while it is being sent. */
  async function busy<R>(form: HTMLElement, work: () => Promise<R>): Promise<R> {
    const buttons = all<HTMLButtonElement>(form, 'button');
    for (const button of buttons) button.disabled = true;
    try {
      return await work();
    } finally {
      for (const button of buttons) button.disabled = false;
    }
  }

  // --- views ------------------------------------------------------------------------------------

  type ViewName = 'loading' | 'login' | 'app';
  let view: ViewName = 'loading';
  function show(name: ViewName) {
    view = name;
    for (const node of all(root!, '[data-view]')) node.hidden = node.dataset['view'] !== name;
  }

  // --- signing in --------------------------------------------------------------------------------

  const login = $(root, '[data-view="login"]');
  const loginTitle = $(login, '[data-login-title]');
  const loginLead = $(login, '[data-login-lead]');
  const startStep = $(login, '[data-login-start]');
  const emailForm = $<HTMLFormElement>(login, '[data-login-email]');
  const emailInput = $<HTMLInputElement>(emailForm, 'input');
  const telegramStep = $(login, '[data-login-telegram]');
  const botLink = $<HTMLAnchorElement>(login, '[data-bot-link]');
  const codeForm = $<HTMLFormElement>(login, '[data-login-code]');
  const linkStep = $(login, '[data-login-link]');
  const codeInput = $<HTMLInputElement>(codeForm, 'input[name="code"]');
  const cells = all(codeForm, '[data-cell]');
  const resend = $<HTMLButtonElement>(codeForm, '[data-resend]');
  const codeBot = $<HTMLAnchorElement>(codeForm, '[data-code-bot]');
  const loginError = $(login, '[data-login-error]');
  let botReady = false;
  let via: 'email' | 'telegram' = 'email';
  let sentText = '';
  let lastEmail = '';
  let countdown = 0;
  let sending = false;

  function loginStep(step: 'start' | 'telegram' | 'code' | 'link') {
    startStep.hidden = step !== 'start';
    telegramStep.hidden = step !== 'telegram';
    codeForm.hidden = step !== 'code';
    linkStep.hidden = step !== 'link';
    loginTitle.textContent =
      step === 'start' || step === 'link'
        ? A.login.title
        : step === 'telegram'
          ? A.login.telegram_title
          : via === 'email'
            ? A.login.code_email
            : A.login.code_telegram;
    loginLead.textContent =
      step === 'start' || step === 'link'
        ? A.login.lead
        : step === 'telegram'
          ? A.login.telegram_text
          : sentText;
    resend.hidden = step !== 'code' || via !== 'email';
    codeBot.hidden = step !== 'code' || via !== 'telegram' || !botReady;
    if (step !== 'code') stopCountdown();
    if (step === 'code') {
      codeInput.value = '';
      codeInput.removeAttribute('aria-invalid');
      paintCells();
      codeInput.focus();
    }
  }

  function paintCells() {
    const value = codeInput.value;
    const focused = document.activeElement === codeInput;
    cells.forEach((cell, index) => {
      cell.textContent = value[index] ?? '';
      cell.classList.toggle('is-current', focused && index === Math.min(value.length, 5));
    });
  }

  function stopCountdown() {
    window.clearInterval(countdown);
    countdown = 0;
  }

  /** «Send again in 0:59»: the address gets five codes an hour, so the button waits a minute. */
  function startCountdown(seconds = 60) {
    stopCountdown();
    let left = seconds;
    const tick = () => {
      resend.disabled = left > 0;
      resend.textContent =
        left > 0
          ? fill(A.login.resend_in, {
              time: `${Math.floor(left / 60)}:${String(left % 60).padStart(2, '0')}`,
            })
          : A.login.resend;
      if (left <= 0) stopCountdown();
      left -= 1;
    };
    tick();
    countdown = window.setInterval(tick, 1000);
  }

  function signedOut(message = '') {
    csrf = '';
    me = null;
    open = '';
    accountHint(false);
    show('login');
    loginStep('start');
    loginError.textContent = message;
  }

  /** Asks whether to sign in to the account a link opens; true — the visitor agreed. */
  function askLink(account: string): Promise<boolean> {
    show('login');
    loginStep('link');
    loginError.textContent = '';
    $(linkStep, '[data-link-account]').textContent = fill(A.login.link_ask, { account });
    const yes = $<HTMLButtonElement>(linkStep, '[data-link-yes]');
    const no = $<HTMLButtonElement>(linkStep, '[data-link-no]');
    yes.focus();
    return new Promise((resolve) => {
      const answer = (agreed: boolean) => {
        yes.removeEventListener('click', onYes);
        no.removeEventListener('click', onNo);
        resolve(agreed);
      };
      const onYes = () => answer(true);
      const onNo = () => answer(false);
      yes.addEventListener('click', onYes);
      no.addEventListener('click', onNo);
    });
  }

  async function startTelegram() {
    botReady = false;
    botLink.classList.add('is-waiting');
    botLink.setAttribute('aria-disabled', 'true');
    for (const label of all(login, '[data-bot-label]')) label.textContent = A.login.telegram_open;
    const { data } = await api<Answer & { bot_url?: string }>('POST', '/api/account/login', {
      method: 'telegram',
      lang,
    });
    if (!data.ok || typeof data.bot_url !== 'string' || !data.bot_url.startsWith('https://t.me/')) {
      loginError.textContent = error(data.error);
      return;
    }
    botLink.href = codeBot.href = data.bot_url;
    const bot = new URL(data.bot_url).pathname.split('/')[1] ?? '';
    if (/^\w{3,64}$/.test(bot)) {
      for (const label of all(login, '[data-bot-label]'))
        label.textContent = fill(A.login.telegram_open_named, { bot });
    }
    botLink.classList.remove('is-waiting');
    botLink.removeAttribute('aria-disabled');
    botReady = true;
  }

  $(login, '[data-login-telegram-start]').addEventListener('click', () => {
    loginError.textContent = '';
    loginStep('telegram');
    void startTelegram();
  });

  botLink.addEventListener('click', (event) => {
    if (!botReady) {
      event.preventDefault();
      return;
    }
    // The bot opens in its own tab or app; the code comes back to this page.
    via = 'telegram';
    sentText = A.login.sent_telegram;
    window.setTimeout(() => loginStep('code'), 300);
  });

  async function sendEmail(email: string): Promise<boolean> {
    const { data } = await api<Answer & { sent_to?: string }>('POST', '/api/account/login', {
      method: 'email',
      email,
      lang,
    });
    if (!data.ok) {
      loginError.textContent = error(data.error);
      return false;
    }
    loginError.textContent = '';
    via = 'email';
    lastEmail = email;
    sentText = fill(A.login.sent_email, { address: data.sent_to ?? email });
    return true;
  }

  emailForm.addEventListener('submit', (event) => {
    event.preventDefault();
    const email = emailInput.value.trim();
    if (!/^[^\s@<>()[\],;:"]+@[^\s@]+\.[^\s@.]+$/.test(email)) {
      emailInput.setAttribute('aria-invalid', 'true');
      loginError.textContent = A.errors.bad_email;
      emailInput.focus();
      return;
    }
    emailInput.removeAttribute('aria-invalid');
    void busy(emailForm, async () => {
      if (!(await sendEmail(email))) return;
      loginStep('code');
      startCountdown();
    });
  });

  resend.addEventListener('click', () => {
    if (!lastEmail) return;
    resend.disabled = true;
    void sendEmail(lastEmail).then((sent) => {
      if (sent) {
        loginLead.textContent = sentText;
        startCountdown();
      } else resend.disabled = false;
    });
  });

  codeInput.addEventListener('input', () => {
    const digits = codeInput.value.replace(/\D/g, '').slice(0, 6);
    if (digits !== codeInput.value) codeInput.value = digits;
    codeInput.removeAttribute('aria-invalid');
    paintCells();
    // Six digits typed or pasted: no need to reach for the button.
    if (digits.length === 6) codeForm.requestSubmit();
  });
  codeInput.addEventListener('focus', paintCells);
  codeInput.addEventListener('blur', paintCells);

  codeForm.addEventListener('submit', (event) => {
    event.preventDefault();
    if (sending) return;
    const code = codeInput.value.replace(/\D/g, '');
    if (code.length !== 6) {
      codeInput.setAttribute('aria-invalid', 'true');
      loginError.textContent = A.errors.bad_code;
      codeInput.focus();
      return;
    }
    sending = true;
    void busy(codeForm, async () => {
      const { data } = await api('POST', '/api/account/login/code', { code });
      if (!data.ok) {
        codeInput.setAttribute('aria-invalid', 'true');
        loginError.textContent = error(data.error);
        codeInput.focus();
        return;
      }
      codeInput.value = '';
      loginError.textContent = '';
      stopCountdown();
      await load();
    }).finally(() => (sending = false));
  });

  for (const back of all(login, '[data-login-back]')) {
    back.addEventListener('click', () => {
      loginError.textContent = '';
      loginStep('start');
      emailInput.focus();
    });
  }

  // --- the account --------------------------------------------------------------------------------

  const app = $(root, '[data-view="app"]');
  const heading = $(app, '[data-title]');
  const menu = $<HTMLDetailsElement>(app, '[data-menu]');
  let me: Me | null = null;
  let list: Summary[] = [];
  let eggsFound = 0;

  async function load() {
    const { data } = await api<Me & Answer>('GET', '/api/account/me');
    if (!data.ok) {
      // Nobody signed in is the usual answer here, not a failure: the form, without a word.
      signedOut(data.error === 'signed_out' ? '' : error(data.error));
      return;
    }
    me = data;
    csrf = data.csrf;
    accountHint(true);
    if (data.loyalty.currency) currency = data.loyalty.currency;
    show('app');
    renderLoyalty();
    renderProfile();
    renderAccess();
    renderContacts();
    renderSessions();
    await Promise.all([loadRequests(), syncEggs(), renderOrders()]);
    renderFrame();
    route(false);
  }

  const firstName = () => me?.client.name.trim().split(/\s+/)[0] ?? '';

  /** The header, the ticker and the title row: who, what discount, what is open, when answered. */
  function renderFrame() {
    if (!me) return;
    const { client, loyalty } = me;
    const name = firstName() || client.email.split('@')[0] || client.telegram;
    $(app, '[data-who-name]').textContent = name;
    $(app, '[data-who-id]').textContent = `#${client.id}`;
    const words = client.name.trim().split(/\s+/).filter(Boolean);
    $(app, '[data-initials]').textContent = upper(
      words.length > 0
        ? words
            .slice(0, 2)
            .map((word) => Array.from(word)[0] ?? '')
            .join('')
        : (Array.from(name)[0] ?? 'K'),
    );
    $(app, '[data-since]').textContent = fill(A.home.since, { date: longDate(client.since) });
    $(app, '[data-tick="discount"]').textContent = percent(loyalty.offer.percent);
    $(app, '[data-tick="level"]').textContent = tierOf(loyalty.tier)?.name ?? A.loyalty.base;
    const opened = list.filter((item) => OPEN_STATUSES.has(item.status)).length;
    $(app, '[data-tick="open"]').textContent = count(T.plurals.requests, opened);
    const answered = latestAnswer(list);
    $(app, '[data-tick="answer"]').textContent = answered?.answered
      ? relative(answered.answered)
      : '—';
    $(app, '[data-menu-discount]').textContent = percent(loyalty.offer.percent);
    $(app, '[data-unread-dot]').hidden = !list.some((item) => item.unread);
    heading.textContent = screenTitle(screen);
  }

  const latestAnswer = (items: Summary[]) =>
    items
      .filter((item) => item.answered)
      .sort((a, b) => Date.parse(b.answered ?? '') - Date.parse(a.answered ?? ''))[0];

  function screenTitle(name: Screen): string {
    if (name === 'home') {
      const first = firstName();
      return first ? fill(A.home.hello, { name: first }) : A.home.hello_plain;
    }
    if (name === 'profile') return A.profile.heading;
    return A.nav[name];
  }

  // --- screens and the address --------------------------------------------------------------------

  let screen: Screen = 'home';
  let focusAfter: HTMLElement | null = null;
  const wide = matchMedia('(min-width: 901px)');

  function showScreen(next: Screen, focus: boolean) {
    const changed = next !== screen;
    screen = next;
    for (const section of all(app, '[data-screen]'))
      section.hidden = section.dataset['screen'] !== next;
    for (const link of all(app, '[data-nav]')) {
      if (link.dataset['nav'] === next) link.setAttribute('aria-current', 'page');
      else link.removeAttribute('aria-current');
    }
    heading.textContent = screenTitle(next);
    menu.open = false;
    if (!focus) return;
    if (focusAfter) {
      focusAfter.focus();
      focusAfter = null;
    } else if (changed) {
      heading.focus({ preventScroll: true });
      window.scrollTo({ top: 0 });
    }
  }

  /** The address says the screen; «#K-0042» opens that request. */
  function route(focus = true) {
    const hash = decodeURIComponent(location.hash.slice(1));
    const number = /^K-\d{1,10}$/i.test(hash) ? hash.toUpperCase() : '';
    const next: Screen = number
      ? 'requests'
      : SCREENS.includes(hash as Screen)
        ? (hash as Screen)
        : 'home';
    if (next === 'loyalty' && me && !me.loyalty.enabled) {
      showScreen('home', focus);
      return;
    }
    showScreen(next, focus);
    if (next !== 'requests') return;
    if (number) {
      if (number !== open) void openThread(number);
    } else if (wide.matches && list[0]) {
      // A wide screen has room for both: the newest request opens next to the list.
      history.replaceState(null, '', `#${list[0].number}`);
      if (list[0].number !== open) void openThread(list[0].number);
    } else closeThread();
  }

  window.addEventListener('hashchange', () => {
    if (view === 'app') route();
  });

  // The menu of the header: closed by a click elsewhere and by Escape.
  document.addEventListener('click', (event) => {
    if (menu.open && !menu.contains(event.target as Node)) menu.open = false;
  });
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && menu.open) {
      menu.open = false;
      $(menu, 'summary').focus();
    }
  });
  // Another language keeps the screen: the address goes along (a sign-in token never does).
  for (const link of all<HTMLAnchorElement>(app, '[data-lang-link]')) {
    link.addEventListener('click', () => {
      const hash = location.hash.startsWith('#login=') ? '' : location.hash;
      link.href = `${link.pathname}${hash}`;
    });
  }

  for (const button of all(root, '[data-logout]')) {
    button.addEventListener('click', () => {
      void api('POST', '/api/account/logout').then(() => {
        history.replaceState(null, '', location.pathname + location.search);
        signedOut();
      });
    });
  }

  // --- home ---------------------------------------------------------------------------------------

  const home = $(app, '[data-screen="home"]');
  const rowTemplate = $<HTMLTemplateElement>(document, '#tpl-row');

  function renderHome() {
    if (!me) return;
    const { loyalty, client } = me;
    const offer = loyalty.offer;
    $(home, '[data-offer]').textContent = percent(offer.percent);
    $(home, '[data-offer-reason]').textContent =
      offer.percent > 0 ? reasonText(offer) : A.loyalty.none;

    // Requests: how many, how many open, the answer that waits to be read.
    $(home, '[data-count]').textContent = String(list.length);
    const opened = list.filter((item) => OPEN_STATUSES.has(item.status)).length;
    $(home, '[data-open]').textContent = `/ ${opened} ${form(T.plurals.open, opened)}`;
    const unread = latestAnswer(list.filter((item) => item.unread));
    const last = unread ?? latestAnswer(list);
    const tile = $<HTMLAnchorElement>(home, '[data-home-requests]');
    tile.href = last ? `#${last.number}` : '#requests';
    $(home, '[data-answer-label]').textContent = unread
      ? A.home.new_answer
      : last
        ? A.home.last_answer
        : A.home.no_answers;
    $(home, '[data-answer-at]').textContent = last?.answered
      ? `#${last.number} · ${stamp(last.answered)}`
      : '';

    $(home, '[data-spent]').textContent = money(loyalty.spent);
    $(home, '[data-orders]').textContent = `/ ${count(T.plurals.orders, loyalty.orders)}`;
    renderLevel();

    $(home, '[data-latest]').replaceChildren(
      ...list.slice(0, 4).map((item) => {
        const row = rowTemplate.content.cloneNode(true) as DocumentFragment;
        $<HTMLAnchorElement>(row, 'a').href = `#${item.number}`;
        $(row, '[data-number]').textContent = `#${item.number}`;
        $(row, '[data-title]').textContent = titleOf(item);
        $(row, '[data-sub]').textContent =
          `${upper(kindOf(item))} · ${shortDate(item.created, true)}`;
        $(row, '[data-unread]').hidden = !item.unread;
        statusPill($(row, '[data-status]'), item.status);
        return row;
      }),
    );
    $(home, '[data-latest-empty]').hidden = list.length > 0;

    const telegram = client.preferred === 'telegram' && Boolean(client.telegram);
    $(home, '[data-ask-text]').textContent = fill(A.home.ask_text, {
      channel: telegram ? A.home.by_telegram : A.home.by_email,
    });
    $(home, '[data-project-note]').textContent =
      loyalty.enabled && offer.percent > 0
        ? fill(A.home.project_discount, { percent: offer.percent })
        : A.home.project_plain;
    renderEggsRing();
  }

  /** The level: tabs, the name, what the next one asks, a ladder of bars. */
  function renderLevel() {
    if (!me) return;
    const { loyalty } = me;
    const current = loyalty.tier ?? '';
    const levels = ['', ...T.loyalty.tiers.map((tier) => tier.id)];
    const index = Math.max(0, levels.indexOf(current));
    for (const tab of all(home, '[data-level]'))
      tab.classList.toggle('is-current', tab.dataset['level'] === current);
    $(home, '[data-level-name]').textContent = tierOf(current)?.name ?? A.loyalty.base;
    const next = loyalty.next;
    const left = next
      ? [
          next.orders > 0 ? count(T.plurals.orders, next.orders) : '',
          next.spent > 0 ? money(next.spent) : '',
        ].filter(Boolean)
      : [];
    $(home, '[data-level-next]').textContent = next
      ? `${fill(A.loyalty.to_next, { tier: tierOf(next.tier)?.name ?? '' })}: ${
          left.length === 2
            ? fill(A.loyalty.level_rule, { orders: left[0] ?? '', spent: left[1] ?? '' })
            : (left[0] ?? '')
        }`
      : A.loyalty.top;
    const rungs = all(home, '[data-rung]');
    const tallest = 150;
    rungs.forEach((rung, i) => {
      rung.classList.toggle('is-past', i < index);
      rung.classList.toggle('is-current', i === index);
      const tier = tierOf(rung.dataset['rung']);
      $(rung, '[data-rung-label]').textContent =
        i === index ? A.loyalty.here : `${tier?.discount ?? 0}%`;
      $(rung, '[data-rung-bar]').style.height =
        `${Math.round(((i + 1) / rungs.length) * tallest)}px`;
    });
  }

  function renderEggsRing() {
    const ring = home.querySelector<SVGCircleElement>('[data-ring]');
    if (!ring || !me) return;
    const total = EGGS.length;
    const circumference = 2 * Math.PI * 64;
    ring.setAttribute(
      'stroke-dasharray',
      `${((eggsFound / total) * circumference).toFixed(1)} ${circumference.toFixed(1)}`,
    );
    $(home, '[data-eggs-count]').textContent = `${eggsFound}/${total}`;
    const eggs = { percent: T.loyalty.eggs };
    $(home, '[data-eggs-hint]').textContent = !me.loyalty.enabled
      ? ''
      : me.loyalty.eggs_used
        ? A.home.eggs_used
        : eggsFound === total
          ? fill(A.home.eggs_ready, eggs)
          : fill(A.home.eggs_hint, eggs);
  }

  // --- the discount and the level ----------------------------------------------------------------

  const loyaltyScreen = $(app, '[data-screen="loyalty"]');

  function renderLoyalty() {
    if (!me) return;
    const { loyalty } = me;
    for (const node of all(app, '[data-loyalty-only]')) node.hidden = !loyalty.enabled;
    $(app, '[data-nav-item="loyalty"]').hidden = !loyalty.enabled;
    const offer = loyalty.offer;
    $(loyaltyScreen, '[data-next-percent]').textContent = percent(offer.percent);
    $(loyaltyScreen, '[data-next-reason]').textContent = [
      offer.percent > 0 ? reasonText(offer) : A.loyalty.none,
      A.loyalty.no_stack,
    ].join(' · ');

    const next = loyalty.next ? tierOf(loyalty.next.tier) : undefined;
    const bar = (selector: string, share: number) =>
      ($(loyaltyScreen, selector).style.width =
        `${Math.round(Math.max(0, Math.min(1, share)) * 100)}%`);
    $(loyaltyScreen, '[data-orders-count]').textContent = String(loyalty.orders);
    $(loyaltyScreen, '[data-spent-total]').textContent = money(loyalty.spent);
    if (!next) {
      // The top, or no levels at all.
      $(loyaltyScreen, '[data-orders-target]').textContent = loyalty.tier ? A.loyalty.top : '';
      $(loyaltyScreen, '[data-spent-target]').textContent = '';
      bar('[data-orders-bar]', loyalty.tier ? 1 : 0);
      bar('[data-spent-bar]', loyalty.tier ? 1 : 0);
    } else {
      $(loyaltyScreen, '[data-orders-target]').textContent =
        next.orders > 0 ? `/ ${next.orders} · ${next.name}` : '';
      $(loyaltyScreen, '[data-spent-target]').textContent =
        next.spent > 0 ? `/ ${money(next.spent)}` : '';
      bar('[data-orders-bar]', next.orders > 0 ? loyalty.orders / next.orders : 0);
      bar('[data-spent-bar]', next.spent > 0 ? loyalty.spent / next.spent : 0);
    }

    const personal = $(loyaltyScreen, '[data-personal]');
    const own = loyalty.personal;
    personal.hidden = !own;
    if (own) {
      personal.textContent = [
        fill(A.loyalty.personal, { percent: own.percent }),
        own.once ? A.loyalty.personal_once : '',
        own.until ? fill(A.loyalty.personal_until, { date: shortDate(own.until, true) }) : '',
        own.note,
      ]
        .filter(Boolean)
        .join(' · ');
    }

    const state = (row: HTMLElement, text: string) =>
      ($(row, '[data-state]').textContent = text ? ` · ${text}` : '');
    for (const row of all(loyaltyScreen, '[data-row]')) {
      const kind = row.dataset['row'];
      if (kind === 'welcome') {
        row.classList.toggle('is-used', loyalty.welcome_used);
        state(row, loyalty.welcome_used ? A.loyalty.used : '');
      } else if (kind === 'eggs') {
        row.classList.toggle('is-used', loyalty.eggs_used);
        state(row, loyalty.eggs_used ? A.loyalty.used : '');
      } else {
        const mine = row.dataset['tierId'] === loyalty.tier;
        row.classList.toggle('is-current', mine);
        state(row, mine ? A.loyalty.yours : '');
      }
    }
  }

  // --- requests and inquiries ------------------------------------------------------------------

  const requests = $(app, '[data-screen="requests"]');
  const thread = $(requests, '[data-thread]');
  const parentSelect = $<HTMLSelectElement>(app, '[data-parent]');
  const cardTemplate = $<HTMLTemplateElement>(document, '#tpl-card');
  const messageTemplate = $<HTMLTemplateElement>(document, '#tpl-msg');
  let open = '';

  function statusPill(node: HTMLElement, status: string) {
    node.textContent = statusName(status);
    node.dataset['value'] = status;
  }

  async function loadRequests() {
    const { data } = await api<Answer & { leads?: Summary[] }>('GET', '/api/account/leads');
    if (!data.ok) return;
    list = data.leads ?? [];
    renderRequests();
  }

  function renderRequests() {
    $(requests, '[data-total]').textContent = String(list.length);
    $(requests, '[data-requests-list]').replaceChildren(
      ...list.map((item) => {
        const row = cardTemplate.content.cloneNode(true) as DocumentFragment;
        const link = $<HTMLAnchorElement>(row, 'a');
        link.href = `#${item.number}`;
        if (item.number === open) link.setAttribute('aria-current', 'true');
        $(row, '[data-number]').textContent = [
          `#${item.number}`,
          kindOf(item),
          item.parent ? fill(A.requests.about, { id: `#${item.parent}` }) : '',
        ]
          .filter(Boolean)
          .join(' · ');
        statusPill($(row, '[data-status]'), item.status);
        $(row, '[data-title]').textContent = titleOf(item);
        $(row, '[data-excerpt]').textContent = item.excerpt;
        $(row, '[data-date]').textContent = shortDate(item.created, true);
        $(row, '[data-unread]').hidden = !item.unread;
        $(row, '[data-discount]').textContent =
          item.discount.percent > 0 ? `−${item.discount.percent}%` : '';
        return row;
      }),
    );
    $(requests, '[data-requests-empty]').hidden = list.length > 0;

    const chosen = parentSelect.value;
    const none = parentSelect.options[0];
    parentSelect.replaceChildren(
      ...(none ? [none] : []),
      ...list.map((item) => {
        const option = document.createElement('option');
        option.value = item.number;
        option.textContent = `#${item.number} · ${titleOf(item)}`;
        return option;
      }),
    );
    parentSelect.value = list.some((item) => item.number === chosen) ? chosen : '';
    renderHome();
    renderFrame();
  }

  async function openThread(number: string) {
    open = number;
    const { status, data } = await api<Answer & { lead?: View }>(
      'GET',
      `/api/account/leads/${encodeURIComponent(number)}`,
    );
    if (open !== number) return; // another one was chosen meanwhile
    if (status === 404 || !data.lead) {
      closeThread();
      const none = $(requests, '[data-thread-none]');
      none.textContent = status === 404 ? A.requests.missing : error(data.error);
      none.hidden = false;
      return;
    }
    $(requests, '[data-thread-none]').hidden = true;
    const lead = data.lead;
    thread.hidden = false;
    requests.classList.add('is-open');
    for (const card of all(requests, '.card')) {
      if (card.getAttribute('href') === `#${lead.number}`)
        card.setAttribute('aria-current', 'true');
      else card.removeAttribute('aria-current');
    }
    $(thread, '[data-thread-meta]').textContent = [
      `#${lead.number}`,
      kindOf(lead),
      lead.parent ? fill(A.requests.about, { id: `#${lead.parent}` }) : '',
      longDate(lead.created),
    ]
      .filter(Boolean)
      .join(' · ');
    $(thread, '[data-thread-title]').textContent = titleOf(lead);
    statusPill($(thread, '[data-thread-status]'), lead.status);

    const facts: [string, string][] = [
      [A.requests.direction, T.directions[lead.direction] ?? lead.direction],
      [A.requests.budget, lead.budget ?? ''],
      [A.requests.timeline, lead.timeline ?? ''],
      [
        A.requests.discount,
        lead.discount.percent > 0
          ? `−${lead.discount.percent}% · ${reasonText(lead.discount)}`
          : '',
      ],
      [A.requests.amount, lead.amount !== undefined ? money(lead.amount) : ''],
      [A.requests.contact, lead.contact],
    ];
    $(thread, '[data-thread-facts]').replaceChildren(
      ...facts
        .filter(([, value]) => value)
        .map(([name, value]) => {
          const line = document.createElement('div');
          const term = document.createElement('dt');
          const detail = document.createElement('dd');
          line.className = 'fact';
          term.textContent = name;
          detail.textContent = value;
          line.append(term, detail);
          return line;
        }),
    );
    // The description opens the conversation too (the form's message): shown once, as the request.
    $(thread, '[data-thread-description]').textContent = lead.description;

    const channels = A.channels as Record<string, string>;
    const events = A.events as Record<string, string>;
    $(thread, '[data-thread-feed]').replaceChildren(
      ...lead.feed
        .filter(
          (entry, index) =>
            !(
              index <= 1 &&
              entry.kind === 'message' &&
              entry.direction === 'in' &&
              entry.channel === 'form' &&
              entry.body?.trim() === lead.description.trim()
            ),
        )
        .map((entry) => {
          const row = messageTemplate.content.cloneNode(true) as DocumentFragment;
          const item = $(row, 'li');
          const meta = $(row, '[data-meta]');
          const body = $(row, '[data-body]');
          if (entry.kind === 'status' || entry.kind === 'call') {
            item.classList.add('msg-event');
            meta.textContent = `${entry.kind === 'call' ? channels['phone'] : A.requests.status} · ${moment(entry.at)}`;
            body.textContent =
              entry.kind === 'call'
                ? A.requests.call
                : (events[entry.status ?? ''] ?? statusName(entry.status ?? ''));
          } else {
            const mine = entry.direction === 'in';
            item.classList.add(mine ? 'msg-in' : 'msg-out');
            const channel = entry.channel ? channels[entry.channel] : '';
            meta.textContent = [
              mine ? A.requests.you : A.requests.answer,
              channel,
              moment(entry.at),
            ]
              .filter(Boolean)
              .join(' · ');
            body.textContent = entry.body ?? '';
            body.hidden = !entry.body; // an answer of files alone
            if (entry.files?.length) {
              const files = $(row, '[data-files]');
              files.replaceChildren(
                ...entry.files.map((file) => fileItem(lead.number, file, !mine)),
              );
              files.hidden = false;
            }
          }
          return row;
        }),
    );
    const reply = $<HTMLFormElement>(thread, '[data-thread-reply]');
    reply.hidden = !lead.can_write;
    $(thread, '[data-thread-closed]').hidden = lead.can_write;
    $(reply, '[data-done]').textContent = '';
    $(reply, '[data-error]').textContent = '';

    // Opened, so read: the list, the home screen and the rail lose their «new».
    const summary = list.find((item) => item.number === lead.number);
    if (summary?.unread) {
      summary.unread = false;
      renderRequests();
    }
    if (!wide.matches) window.scrollTo({ top: 0 });
  }

  /**
   * A file in the conversation. The photos and videos of an answer are shown as they are; the
   * rest — and whatever the client sent — are downloads, as the API hands them out.
   */
  function fileItem(number: string, file: FileOf, answer: boolean): HTMLLIElement {
    const item = document.createElement('li');
    const address = `/api/account/leads/${encodeURIComponent(number)}/files/${file.id}`;
    if (answer && (file.kind === 'jpg' || file.kind === 'png')) {
      const link = document.createElement('a');
      link.href = address;
      link.target = '_blank';
      link.rel = 'noopener';
      const photo = document.createElement('img');
      photo.className = 'msg-photo';
      photo.src = address;
      photo.alt = file.name;
      photo.loading = 'lazy';
      photo.decoding = 'async';
      link.append(photo);
      item.append(link);
      return item;
    }
    if (answer && file.kind === 'mp4') {
      const video = document.createElement('video');
      video.className = 'msg-video';
      video.src = address;
      video.controls = true;
      video.preload = 'metadata';
      video.setAttribute('aria-label', file.name);
      item.append(video);
      return item;
    }
    const link = document.createElement('a');
    link.className = 'msg-file';
    link.href = address;
    link.download = file.name;
    const name = document.createElement('span');
    name.className = 'msg-file-name';
    name.textContent = file.name;
    const weight = document.createElement('span');
    weight.className = 'msg-file-size';
    weight.textContent = size(file.size);
    link.append(name, weight);
    item.append(link);
    return item;
  }

  function closeThread() {
    open = '';
    thread.hidden = true;
    $(requests, '[data-thread-none]').hidden = true;
    requests.classList.remove('is-open');
    for (const card of all(requests, '.card')) card.removeAttribute('aria-current');
  }

  for (const ask of all(thread, '[data-thread-ask]')) {
    ask.addEventListener('click', () => {
      parentSelect.value = open;
      focusAfter = $(app, '#inquiry-text');
      location.hash = '#new';
    });
  }

  const replyForm = $<HTMLFormElement>(thread, '[data-thread-reply]');
  replyForm.addEventListener('submit', (event) => {
    event.preventDefault();
    const field = $<HTMLTextAreaElement>(replyForm, 'textarea');
    const text = field.value.trim();
    const problem = $(replyForm, '[data-error]');
    if (!text) {
      problem.textContent = A.errors.empty;
      field.focus();
      return;
    }
    const number = open;
    void busy(replyForm, async () => {
      const { data } = await api(
        'POST',
        `/api/account/leads/${encodeURIComponent(number)}/messages`,
        { text },
      );
      if (!data.ok) {
        problem.textContent = error(data.error);
        return;
      }
      problem.textContent = '';
      field.value = '';
      // An answer can move the status (waiting for the client → in progress): the list follows.
      await Promise.all([openThread(number), loadRequests()]);
      $(replyForm, '[data-done]').textContent = A.requests.sent;
    });
  });

  // --- a new inquiry --------------------------------------------------------------------------------

  const inquiry = $<HTMLFormElement>(app, '[data-inquiry]');
  inquiry.addEventListener('submit', (event) => {
    event.preventDefault();
    const text = $<HTMLTextAreaElement>(inquiry, 'textarea');
    const subject = $<HTMLInputElement>(inquiry, 'input[name="subject"]');
    const problem = $(inquiry, '[data-error]');
    if (Array.from(text.value.trim()).length < 2) {
      problem.textContent = A.errors.empty;
      text.setAttribute('aria-invalid', 'true');
      text.focus();
      return;
    }
    text.removeAttribute('aria-invalid');
    void busy(inquiry, async () => {
      const { data } = await api<Answer & { number?: string }>('POST', '/api/account/inquiries', {
        subject: subject.value.trim(),
        text: text.value.trim(),
        parent: parentSelect.value,
      });
      if (!data.ok || !data.number) {
        problem.textContent = error(data.error);
        return;
      }
      problem.textContent = '';
      text.value = '';
      subject.value = '';
      parentSelect.value = '';
      await loadRequests();
      // The inquiry opens with its number: the conversation goes on there.
      history.pushState(null, '', `#${data.number}`);
      showScreen('requests', true);
      await openThread(data.number);
      $(replyForm, '[data-done]').textContent = fill(A.inquiry.sent, { id: `#${data.number}` });
    });
  });

  // --- achievements -------------------------------------------------------------------------------

  const achievements = $(app, '[data-screen="achievements"]');

  /** The achievements of orders: the tiles, and a banner for each the client has not seen yet. */
  async function renderOrders() {
    if (!me) return;
    const { earned, shares } = me.orders;
    const have = new Map(earned.map((item) => [item.id, item]));
    const rareOf = (id: string) =>
      id === 'all_orders' || (shares[id] !== undefined && (shares[id] ?? 100) < 10);
    for (const tile of all(achievements, '[data-order]')) {
      const id = tile.dataset['order'] ?? '';
      const item = have.get(id);
      const share = shares[id];
      tile.classList.toggle('is-found', Boolean(item));
      tile.classList.toggle('is-beige', id === 'second_order' || id === 'big_order');
      $(tile, '[data-rarity]').textContent =
        share === undefined
          ? ''
          : fill(
              id === 'all_orders' ? A.orders.epic : share < 10 ? A.orders.rare : A.orders.share,
              { percent: shareOf(share) },
            );
      $(tile, '[data-state]').textContent = item
        ? fill(A.orders.earned, { date: shortDate(item.at, true) })
        : A.orders.locked;
    }

    // What happened since the last visit comes the way Steam announces it: one banner after another.
    const fresh = earned.filter((item) => item.new);
    if (fresh.length === 0) return;
    const { unlock } = await import('../../../design/components/eggs/achievement.js');
    const base = earned.filter((item) => item.id !== 'all_orders');
    for (const item of fresh) {
      const texts = A.orders[item.id as OrderId];
      if (!texts) continue;
      const share = shares[item.id];
      const rare = rareOf(item.id);
      unlock({
        id: item.id,
        name: texts.name,
        text: fill(texts.text, { amount: money(me.orders.big_order) }),
        found: A.orders.found,
        ...(item.id === 'all_orders'
          ? {}
          : { count: base.findIndex((other) => other.id === item.id) + 1, total: 3 }),
        rare,
        rarity: share === undefined ? '' : fill(A.orders.rarity, { percent: shareOf(share) }),
        sound: item.id === 'all_orders' ? 'epic' : rare ? 'rare' : 'egg',
      });
    }
    await api('POST', '/api/account/achievements/seen', { ids: fresh.map((item) => item.id) });
  }

  /** The eggs: this browser's receipts go to the account, the finds of both are shown. */
  async function syncEggs() {
    if (!me) return;
    const own = receipts();
    const mine = EGGS.flatMap((id) => (own[id] ? [own[id]] : []));
    if (mine.length > 0) {
      const { data } = await api<Answer & { eggs?: Me['eggs'] }>('POST', '/api/account/eggs', {
        receipts: mine,
      });
      if (data.ok && data.eggs && me) me.eggs = data.eggs;
    }
    if (!me) return;
    const found = new Set([...browserFinds(), ...me.eggs.map((egg) => egg.id)]);
    eggsFound = EGGS.filter((id) => found.has(id)).length;
    const grid = achievements.querySelector<HTMLElement>('.t-eggs-grid');
    if (grid) {
      $(grid, '[data-eggs-found]').textContent = `${eggsFound}/${EGGS.length}`;
      $(grid, '[data-eggs-reward]').textContent = me.loyalty.eggs_used
        ? A.eggs.reward_used
        : fill(A.eggs.reward, { total: EGGS.length, percent: T.loyalty.eggs });
      for (const tile of all(grid, '[data-egg]')) {
        const id = tile.dataset['egg'] ?? '';
        const have = found.has(id);
        tile.classList.toggle('is-found', have);
        $(tile, '.egg-mark').textContent = have ? '◆' : '◇';
        $(tile, '[data-name]').textContent = have ? (T.eggs[id] ?? id) : A.eggs.hidden;
      }
    }
    const eggsRow = app.querySelector<HTMLElement>('[data-eggs-rule]');
    if (eggsRow)
      eggsRow.textContent = fill(A.loyalty.eggs_found, { found: eggsFound, total: EGGS.length });
    renderEggsRing();
  }

  // --- profile ----------------------------------------------------------------------------------

  const profile = $<HTMLFormElement>(app, '[data-profile]');

  function renderProfile() {
    if (!me) return;
    const { client } = me;
    $<HTMLInputElement>(profile, '[name="name"]').value = client.name;
    $<HTMLInputElement>(profile, '[name="company"]').value = client.company;
    for (const radio of all<HTMLInputElement>(profile, '[name="lang"]'))
      radio.checked = radio.value === (client.lang || lang);
    const preferred = client.preferred || (client.email ? 'email' : 'telegram');
    for (const radio of all<HTMLInputElement>(profile, '[name="preferred"]')) {
      radio.checked = radio.value === preferred;
      radio.disabled = radio.value === 'telegram' ? !client.telegram : !client.email;
    }
  }

  profile.addEventListener('submit', (event) => {
    event.preventDefault();
    const done = $(profile, '[data-done]');
    done.textContent = '';
    void busy(profile, async () => {
      const values = {
        name: $<HTMLInputElement>(profile, '[name="name"]').value.trim(),
        company: $<HTMLInputElement>(profile, '[name="company"]').value.trim(),
        lang: profile.querySelector<HTMLInputElement>('[name="lang"]:checked')?.value ?? lang,
        preferred:
          profile.querySelector<HTMLInputElement>('[name="preferred"]:checked')?.value ?? '',
      };
      const { data } = await api('POST', '/api/account/profile', values);
      done.textContent = data.ok ? A.profile.saved : error(data.error);
      done.classList.toggle('error', !data.ok);
      if (data.ok && me) {
        me.client = { ...me.client, ...values };
        renderFrame();
        renderHome();
      }
    });
  });

  // --- the ways in: email and Telegram -----------------------------------------------------------

  const access = $(app, '[data-access]');
  const accessEmailForm = $<HTMLFormElement>(access, '[data-access-email-form]');
  const accessCodeForm = $<HTMLFormElement>(access, '[data-access-code-form]');
  const accessBot = $<HTMLAnchorElement>(access, '[data-access-bot]');
  const accessError = $(access, '[data-error]');

  function renderAccess(step: 'idle' | 'email' | 'code' = 'idle') {
    if (!me) return;
    const { client } = me;
    $(access, '[data-access-email]').textContent = client.email || A.access.missing;
    $(access, '[data-access-telegram]').textContent = client.telegram || A.access.missing;
    const change = $<HTMLButtonElement>(access, '[data-email-change]');
    change.textContent = client.email ? A.access.change_email : A.access.add_email;
    $(access, '[data-telegram-link]').hidden = Boolean(client.telegram) || !me.bot;
    $(access, '[data-telegram-unlink]').hidden = !client.telegram;
    // One person, one account: a way in that opens another account makes the two one.
    $(access, '[data-access-hint]').hidden =
      Boolean(client.email) && (Boolean(client.telegram) || !me.bot);
    accessEmailForm.hidden = step !== 'email';
    accessCodeForm.hidden = step !== 'code';
    if (step === 'idle') accessError.textContent = '';
    else $(access, '[data-access-status]').hidden = true;
  }

  $(access, '[data-email-change]').addEventListener('click', () => {
    renderAccess('email');
    $<HTMLInputElement>(accessEmailForm, 'input').focus();
  });
  for (const cancel of all(access, '[data-access-cancel]'))
    cancel.addEventListener('click', () => renderAccess());

  accessEmailForm.addEventListener('submit', (event) => {
    event.preventDefault();
    const input = $<HTMLInputElement>(accessEmailForm, 'input');
    const email = input.value.trim();
    void busy(accessEmailForm, async () => {
      const { data } = await api<Answer & { sent_to?: string }>('POST', '/api/account/email', {
        email,
      });
      if (!data.ok) {
        accessError.textContent = error(data.error);
        return;
      }
      accessError.textContent = '';
      input.value = '';
      accessBot.hidden = true;
      $(accessCodeForm, '[data-sent]').textContent = fill(A.login.sent_email, {
        address: data.sent_to ?? email,
      });
      renderAccess('code');
      $<HTMLInputElement>(accessCodeForm, 'input').focus();
    });
  });

  $(access, '[data-telegram-link]').addEventListener('click', (event) => {
    const button = event.currentTarget as HTMLButtonElement;
    button.disabled = true;
    void api<Answer & { bot_url?: string }>('POST', '/api/account/telegram', {}).then(
      ({ data }) => {
        button.disabled = false;
        if (
          !data.ok ||
          typeof data.bot_url !== 'string' ||
          !data.bot_url.startsWith('https://t.me/')
        ) {
          accessError.textContent = error(data.error);
          return;
        }
        accessError.textContent = '';
        accessBot.href = data.bot_url;
        accessBot.hidden = false;
        $(accessCodeForm, '[data-sent]').textContent = A.login.sent_telegram;
        renderAccess('code');
      },
    );
  });

  $(access, '[data-telegram-unlink]').addEventListener('click', (event) => {
    const button = event.currentTarget as HTMLButtonElement;
    button.disabled = true;
    void api('POST', '/api/account/telegram/unlink').then(async ({ data }) => {
      button.disabled = false;
      if (!data.ok) {
        accessError.textContent = error(data.error);
        return;
      }
      await load();
    });
  });

  accessCodeForm.addEventListener('submit', (event) => {
    event.preventDefault();
    const input = $<HTMLInputElement>(accessCodeForm, 'input');
    const code = input.value.replace(/\D/g, '');
    if (code.length !== 6) {
      accessError.textContent = A.errors.bad_code;
      return;
    }
    void busy(accessCodeForm, async () => {
      const { data } = await api<Answer & { merged?: boolean }>('POST', '/api/account/login/code', {
        code,
      });
      if (!data.ok) {
        accessError.textContent = error(data.error);
        return;
      }
      input.value = '';
      await load();
      $(access, '[data-access-status]').hidden = data.merged !== true;
    });
  });

  // --- contacts ---------------------------------------------------------------------------------

  const contactTemplate = $<HTMLTemplateElement>(document, '#tpl-contact');
  const contactForm = $<HTMLFormElement>(app, '[data-contact-form]');
  const kindSelect = $<HTMLSelectElement>(contactForm, 'select');
  const valueInput = $<HTMLInputElement>(contactForm, 'input[name="value"]');
  const contactError = $(app, '[data-contact-error]');
  kindSelect.addEventListener('change', () => {
    valueInput.placeholder = T.examples[kindSelect.value] ?? '';
  });

  function renderContacts() {
    if (!me) return;
    const kinds = A.contacts.kinds as Record<string, string>;
    $(app, '[data-contact-list]').replaceChildren(
      ...me.contacts.map((contact) => {
        const row = contactTemplate.content.cloneNode(true) as DocumentFragment;
        $(row, '[data-kind]').textContent = kinds[contact.kind] ?? contact.kind;
        $(row, '[data-value]').textContent = contact.value;
        const remove = $<HTMLButtonElement>(row, '[data-remove]');
        remove.setAttribute('aria-label', fill(A.contacts.remove, { value: contact.value }));
        remove.addEventListener('click', () => {
          remove.disabled = true;
          void api('POST', '/api/account/contacts/remove', { id: contact.id }).then(({ data }) => {
            remove.disabled = false;
            if (!data.ok || !me) {
              contactError.textContent = error(data.error);
              return;
            }
            me.contacts = me.contacts.filter((item) => item.id !== contact.id);
            renderContacts();
          });
        });
        return row;
      }),
    );
    $(app, '[data-contacts-empty]').hidden = me.contacts.length > 0;
  }

  contactForm.addEventListener('submit', (event) => {
    event.preventDefault();
    if (!valueInput.value.trim()) {
      contactError.textContent = A.errors.bad_contact;
      valueInput.focus();
      return;
    }
    void busy(contactForm, async () => {
      const { data } = await api<Answer & { contact?: Me['contacts'][number] }>(
        'POST',
        '/api/account/contacts',
        { kind: kindSelect.value, value: valueInput.value.trim() },
      );
      if (!data.ok || !data.contact || !me) {
        contactError.textContent = error(data.error);
        valueInput.setAttribute('aria-invalid', 'true');
        return;
      }
      contactError.textContent = '';
      valueInput.removeAttribute('aria-invalid');
      valueInput.value = '';
      const added = data.contact;
      me.contacts = [...me.contacts.filter((item) => item.id !== added.id), added];
      renderContacts();
    });
  });

  // --- devices ----------------------------------------------------------------------------------

  const sessionTemplate = $<HTMLTemplateElement>(document, '#tpl-session');
  const endAll = $<HTMLButtonElement>(app, '[data-sessions-end-all]');

  function renderSessions() {
    if (!me) return;
    $(app, '[data-session-list]').replaceChildren(
      ...me.sessions.map((session) => {
        const row = sessionTemplate.content.cloneNode(true) as DocumentFragment;
        $(row, '[data-device]').textContent = session.device;
        $(row, '[data-seen]').textContent = session.current
          ? A.sessions.now
          : relative(session.seen);
        $(row, '[data-current]').hidden = !session.current;
        const end = $<HTMLButtonElement>(row, '[data-end]');
        end.hidden = session.current;
        end.setAttribute('aria-label', `${A.sessions.end}: ${session.device}`);
        end.addEventListener('click', () => {
          end.disabled = true;
          void api('POST', '/api/account/sessions/end', { id: session.id }).then(({ data }) => {
            if (!data.ok || !me) {
              end.disabled = false;
              return;
            }
            me.sessions = me.sessions.filter((item) => item.id !== session.id);
            renderSessions();
          });
        });
        return row;
      }),
    );
    endAll.hidden = me.sessions.every((session) => session.current);
  }

  endAll.addEventListener('click', () => {
    void api('POST', '/api/account/sessions/end', { id: '' }).then(({ data }) => {
      if (!data.ok || !me) return;
      me.sessions = me.sessions.filter((session) => session.current);
      renderSessions();
    });
  });

  // --- deleting the account ------------------------------------------------------------------------

  const remove = $<HTMLFormElement>(app, '[data-delete]');
  const confirm = $<HTMLInputElement>(remove, '[data-delete-confirm]');
  const removeButton = $<HTMLButtonElement>(remove, '[data-delete-button]');
  confirm.addEventListener('change', () => (removeButton.disabled = !confirm.checked));
  remove.addEventListener('submit', (event) => {
    event.preventDefault();
    if (!confirm.checked) return;
    void busy(remove, async () => {
      const { data } = await api('POST', '/api/account/delete', { confirm: true });
      if (data.ok) {
        confirm.checked = false;
        history.replaceState(null, '', location.pathname + location.search);
        signedOut();
      }
    }).finally(() => (removeButton.disabled = !confirm.checked));
  });

  // --- start ------------------------------------------------------------------------------------

  async function start() {
    // A link from a letter or from the bot: «#login=<token>». The token leaves the address at once.
    // It signs in once the visitor has seen whose account it opens and agreed: a link somebody
    // else sent would quietly put this browser into their account, and whatever is sent from it
    // later would be theirs to read.
    const token = /^#login=([A-Za-z0-9._-]{8,200})$/.exec(location.hash)?.[1];
    if (token) {
      history.replaceState(null, '', location.pathname + location.search);
      const peek = await api<Answer & { account?: string }>('POST', '/api/account/login/link', {
        token,
        peek: true,
      });
      if (!peek.data.ok || typeof peek.data.account !== 'string') {
        signedOut(error(peek.data.error));
        return;
      }
      if (await askLink(peek.data.account)) {
        $(root!, '[data-loading]').textContent = A.login.link;
        show('loading');
        const { data } = await api('POST', '/api/account/login/link', { token });
        if (!data.ok) {
          signedOut(error(data.error));
          return;
        }
      }
    }
    await load();
  }

  void start();
}
