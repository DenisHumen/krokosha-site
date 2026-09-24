// The personal account (pages/[...lang]/account.astro): signing in with a one-time code, then the
// client's requests and inquiries, the discount and the level, contacts, profile, the ways in and the
// devices. Everything comes from /api/account/* (api/internal/clients/http.go): JSON both ways, the
// session in an HttpOnly cookie, and every change carries the session's CSRF token in a header.

import type { Site } from '../lib/data.ts';
import { accountHint, EGGS, receipts } from './achievements.ts';

type Texts = Site['account'];

interface PageTexts {
  lang: string;
  account: Texts;
  orders: string[];
  examples: Record<string, string>;
  directions: Record<string, string>;
  loyalty: {
    enabled: boolean;
    currency: string;
    eggs: number;
    tiers: { id: string; name: string; discount: number }[];
  };
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
    kind: 'message' | 'status';
    direction?: 'in' | 'out';
    channel?: string;
    body?: string;
    status?: string;
    files?: string[];
  }[];
}

interface Answer {
  ok?: boolean;
  error?: string;
  [key: string]: unknown;
}

const fill = (template: string, values: Record<string, string | number>) =>
  template.replace(/\{([a-z_]+)\}/g, (match, name: string) =>
    name in values ? String(values[name]) : match,
  );

function $<T extends Element = HTMLElement>(root: ParentNode, selector: string): T {
  const node = root.querySelector<T>(selector);
  if (!node) throw new Error(`account: ${selector} is missing`);
  return node;
}

export function initAccount(): void {
  const root = document.querySelector<HTMLElement>('[data-account]');
  if (!root) return;
  const T = JSON.parse(root.dataset['texts'] ?? '{}') as PageTexts;
  const A = T.account;
  const lang = T.lang;

  // --- formatting --------------------------------------------------------------------------------

  const day = new Intl.DateTimeFormat(lang, { day: 'numeric', month: 'long', year: 'numeric' });
  const moment = new Intl.DateTimeFormat(lang, {
    day: 'numeric',
    month: 'short',
    hour: '2-digit',
    minute: '2-digit',
  });
  const date = (value: string) => day.format(new Date(value));
  const when = (value: string) => moment.format(new Date(value));
  let money = new Intl.NumberFormat(lang, {
    style: 'currency',
    currency: T.loyalty.currency || 'USD',
    maximumFractionDigits: 0,
  });
  const rules = new Intl.PluralRules(lang);
  const orders = (n: number) => {
    const category = rules.select(n);
    const forms = T.orders;
    const form =
      forms.length === 2
        ? forms[category === 'one' ? 0 : 1]
        : forms[category === 'one' ? 0 : category === 'few' ? 1 : 2];
    return `${n} ${form ?? ''}`.trim();
  };
  const tierName = (id?: string) => T.loyalty.tiers.find((tier) => tier.id === id)?.name ?? '';
  const statusName = (status: string) => (A.statuses as Record<string, string>)[status] ?? status;
  const error = (code?: string) =>
    (A.errors as Record<string, string>)[code === 'server_error' ? 'server' : (code ?? '')] ??
    A.errors.server;
  const reasonText = (offer: Offer) => {
    const reasons = A.loyalty.reasons as Record<string, string>;
    if (offer.reason === 'tier')
      return fill(reasons['tier'] ?? '', { tier: tierName(offer.detail) });
    if (offer.reason === 'personal' || offer.reason === 'manual')
      return [reasons[offer.reason], offer.detail].filter(Boolean).join(' · ');
    return reasons[offer.reason ?? ''] ?? '';
  };

  // --- the API ----------------------------------------------------------------------------------

  let csrf = '';

  async function api<T extends Answer>(
    method: 'GET' | 'POST',
    path: string,
    body?: unknown,
  ): Promise<{ status: number; data: T }> {
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
      return { status: 0, data: { ok: false, error: 'network' } as T };
    }
    const data = (await response.json().catch(() => ({ ok: false, error: 'server' }))) as T;
    // A session that ended while the page was open (signed out on another device, expired).
    if (response.status === 401 && view === 'dashboard') signedOut(A.errors.signed_out);
    return { status: response.status, data };
  }

  /** Disables a form's buttons while it is being sent. */
  async function busy<T>(form: HTMLElement, work: () => Promise<T>): Promise<T> {
    const buttons = [...form.querySelectorAll<HTMLButtonElement>('button')];
    for (const button of buttons) button.disabled = true;
    try {
      return await work();
    } finally {
      for (const button of buttons) button.disabled = false;
    }
  }

  // --- views ------------------------------------------------------------------------------------

  type ViewName = 'loading' | 'login' | 'dashboard';
  let view: ViewName = 'loading';
  function show(name: ViewName) {
    view = name;
    for (const node of root!.querySelectorAll<HTMLElement>('[data-view]'))
      node.hidden = node.dataset['view'] !== name;
  }

  // --- signing in --------------------------------------------------------------------------------

  const login = $(root, '[data-view="login"]');
  const emailForm = $<HTMLFormElement>(login, '[data-login-email]');
  const telegramStep = $(login, '[data-login-telegram]');
  const botLink = $<HTMLAnchorElement>(login, '[data-bot-link]');
  const codeForm = $<HTMLFormElement>(login, '[data-login-code]');
  const loginError = $(login, '[data-login-error]');
  const methods = $(login, '[data-login-methods]');
  let botReady = false;

  function loginStep(step: 'email' | 'telegram' | 'code') {
    methods.hidden = step === 'code';
    emailForm.hidden = step !== 'email';
    telegramStep.hidden = step !== 'telegram';
    codeForm.hidden = step !== 'code';
    if (step === 'code') $<HTMLInputElement>(codeForm, 'input').focus();
  }

  function signedOut(message = '') {
    csrf = '';
    accountHint(false);
    show('login');
    loginStep(method());
    loginError.textContent = message;
  }

  const method = () =>
    (login.querySelector<HTMLInputElement>('input[name="login-method"]:checked')?.value ??
      'email') as 'email' | 'telegram';

  async function startTelegram() {
    botReady = false;
    botLink.classList.add('is-waiting');
    botLink.setAttribute('aria-disabled', 'true');
    const { data } = await api<Answer & { bot_url?: string }>('POST', '/api/account/login', {
      method: 'telegram',
      lang,
    });
    if (!data.ok || typeof data.bot_url !== 'string' || !data.bot_url.startsWith('https://t.me/')) {
      loginError.textContent = error(data.error);
      return;
    }
    botLink.href = data.bot_url;
    botLink.classList.remove('is-waiting');
    botLink.removeAttribute('aria-disabled');
    botReady = true;
  }

  for (const radio of login.querySelectorAll<HTMLInputElement>('input[name="login-method"]')) {
    radio.addEventListener('change', () => {
      loginError.textContent = '';
      loginStep(method());
      if (method() === 'telegram') void startTelegram();
    });
  }

  botLink.addEventListener('click', (event) => {
    if (!botReady) {
      event.preventDefault();
      return;
    }
    // The bot opens in its own tab or app; the code comes back to this page.
    $(codeForm, '[data-sent]').textContent = A.login.sent_telegram;
    window.setTimeout(() => loginStep('code'), 300);
  });

  emailForm.addEventListener('submit', (event) => {
    event.preventDefault();
    const input = $<HTMLInputElement>(emailForm, 'input');
    const email = input.value.trim();
    if (!/^[^\s@<>()[\],;:"]+@[^\s@]+\.[^\s@.]+$/.test(email)) {
      input.setAttribute('aria-invalid', 'true');
      loginError.textContent = A.errors.bad_email;
      input.focus();
      return;
    }
    input.removeAttribute('aria-invalid');
    void busy(emailForm, async () => {
      const { data } = await api<Answer & { sent_to?: string }>('POST', '/api/account/login', {
        method: 'email',
        email,
        lang,
      });
      if (!data.ok) {
        loginError.textContent = error(data.error);
        return;
      }
      loginError.textContent = '';
      $(codeForm, '[data-sent]').textContent = fill(A.login.sent_email, {
        address: data.sent_to ?? email,
      });
      loginStep('code');
    });
  });

  codeForm.addEventListener('submit', (event) => {
    event.preventDefault();
    const input = $<HTMLInputElement>(codeForm, 'input');
    const code = input.value.replace(/\D/g, '');
    if (code.length !== 6) {
      input.setAttribute('aria-invalid', 'true');
      loginError.textContent = A.errors.bad_code;
      return;
    }
    input.removeAttribute('aria-invalid');
    void busy(codeForm, async () => {
      const { data } = await api('POST', '/api/account/login/code', { code });
      if (!data.ok) {
        loginError.textContent = error(data.error);
        return;
      }
      input.value = '';
      loginError.textContent = '';
      await load();
    });
  });

  $(codeForm, '[data-login-back]').addEventListener('click', () => {
    loginError.textContent = '';
    loginStep(method());
    if (method() === 'telegram') void startTelegram();
  });

  // --- the account --------------------------------------------------------------------------------

  const board = $(root, '[data-view="dashboard"]');
  let me: Me | null = null;
  let list: Summary[] = [];

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
    if (data.loyalty.currency) {
      money = new Intl.NumberFormat(lang, {
        style: 'currency',
        currency: data.loyalty.currency,
        maximumFractionDigits: 0,
      });
    }
    show('dashboard');
    renderHead();
    renderLoyalty();
    renderContacts();
    renderProfile();
    renderAccess();
    renderSessions();
    await Promise.all([loadRequests(), syncEggs(), renderOrders()]);
    route();
  }

  function renderHead() {
    if (!me) return;
    const { client } = me;
    $(board, '[data-hello]').textContent = client.name
      ? fill(A.home.hello, { name: client.name })
      : A.home.hello_plain;
    $(board, '[data-since]').textContent = fill(A.home.since, { date: date(client.since) });
  }

  $(board, '[data-logout]').addEventListener('click', () => {
    void api('POST', '/api/account/logout').then(() => signedOut());
  });

  // --- the discount and the level ----------------------------------------------------------------

  function renderLoyalty() {
    if (!me) return;
    const card = $(board, '[data-loyalty]');
    const { loyalty } = me;
    card.hidden = !loyalty.enabled;
    const offer = loyalty.offer;
    $(card, '[data-next-percent]').textContent = offer.percent > 0 ? `−${offer.percent}%` : '0%';
    $(card, '[data-next-reason]').textContent =
      offer.percent > 0 ? reasonText(offer) : A.loyalty.none;
    const tier = $(card, '[data-tier]');
    tier.textContent = loyalty.tier ? tierName(loyalty.tier) : A.loyalty.newcomer;
    tier.dataset['value'] = loyalty.tier ?? '';
    $(card, '[data-orders]').textContent = String(loyalty.orders);
    $(card, '[data-spent]').textContent = money.format(loyalty.spent);

    const progress = $(card, '[data-progress]');
    const next = loyalty.next;
    progress.hidden = !next;
    if (next) {
      $(progress, '[data-progress-title]').textContent = fill(A.loyalty.to_next, {
        tier: tierName(next.tier),
      });
      $(progress, '[data-progress-bar]').style.width =
        `${Math.round(Math.max(0, Math.min(1, next.progress)) * 100)}%`;
      const left = [
        next.orders > 0 ? fill(A.loyalty.more_orders, { count: orders(next.orders) }) : '',
        next.spent > 0 ? fill(A.loyalty.more_spent, { amount: money.format(next.spent) }) : '',
      ].filter(Boolean);
      // «or $1,200 more» reads as a sentence only after something; alone it loses its «or».
      if (left.length === 1 && next.orders <= 0) left[0] = money.format(next.spent);
      $(progress, '[data-progress-left]').textContent = left.join(' ');
    }
    $(card, '[data-top]').hidden = Boolean(next) || !loyalty.tier;

    const personal = $(card, '[data-personal]');
    const own = loyalty.personal;
    personal.hidden = !own;
    if (own) {
      personal.textContent = [
        fill(A.loyalty.personal, { percent: own.percent }),
        own.once ? A.loyalty.personal_once : '',
        own.until ? fill(A.loyalty.personal_until, { date: date(own.until) }) : '',
        own.note,
      ]
        .filter(Boolean)
        .join(' · ');
    }
    for (const row of card.querySelectorAll<HTMLElement>('[data-tier-id]')) {
      row.classList.toggle('is-current', row.dataset['tierId'] === loyalty.tier);
    }
  }

  // --- requests and inquiries ------------------------------------------------------------------

  const requests = $(board, '[data-requests]');
  const listView = $(requests, '[data-list-view]');
  const thread = $(requests, '[data-thread]');
  const parentSelect = $<HTMLSelectElement>(board, '[data-parent]');
  const requestTemplate = $<HTMLTemplateElement>(document, '#tpl-request');
  const entryTemplate = $<HTMLTemplateElement>(document, '#tpl-entry');

  const titleOf = (item: Summary) =>
    item.subject || T.directions[item.direction] || item.direction || '';
  const kindOf = (item: Summary) =>
    item.kind === 'inquiry' ? A.requests.inquiry : A.requests.request;

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
    const holder = $(requests, '[data-requests-list]');
    holder.replaceChildren(
      ...list.map((item) => {
        const row = requestTemplate.content.cloneNode(true) as DocumentFragment;
        const link = $<HTMLAnchorElement>(row, 'a');
        link.href = `#${item.number}`;
        $(row, '[data-number]').textContent = `#${item.number}`;
        $(row, '[data-kind]').textContent = [
          kindOf(item),
          item.parent ? fill(A.requests.about, { id: `#${item.parent}` }) : '',
        ]
          .filter(Boolean)
          .join(' · ');
        $(row, '[data-unread]').hidden = !item.unread;
        statusPill($(row, '[data-status]'), item.status);
        $(row, '[data-title]').textContent = titleOf(item);
        $(row, '[data-excerpt]').textContent = item.excerpt;
        $(row, '[data-date]').textContent = date(item.created);
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
  }

  let open = '';

  async function openThread(number: string) {
    const { status, data } = await api<Answer & { lead?: View }>(
      'GET',
      `/api/account/leads/${encodeURIComponent(number)}`,
    );
    if (status === 404 || !data.lead) {
      closeThread();
      const empty = $(requests, '[data-requests-empty]');
      empty.textContent = A.requests.missing;
      empty.hidden = false;
      return;
    }
    const lead = data.lead;
    open = lead.number;
    listView.hidden = true;
    thread.hidden = false;
    $(thread, '[data-thread-meta]').textContent = [
      `#${lead.number}`,
      kindOf(lead),
      lead.parent ? fill(A.requests.about, { id: `#${lead.parent}` }) : '',
      date(lead.created),
    ]
      .filter(Boolean)
      .join(' · ');
    $(thread, '[data-thread-title]').textContent = titleOf(lead);
    statusPill($(thread, '[data-thread-status]'), lead.status);

    const facts: [string, string][] = [
      [A.requests.direction, T.directions[lead.direction] ?? lead.direction],
      [A.requests.budget, lead.budget ?? ''],
      [A.requests.timeline, lead.timeline ?? ''],
      [A.requests.contact, lead.contact],
      [
        A.requests.discount,
        lead.discount.percent > 0
          ? `−${lead.discount.percent}% · ${reasonText(lead.discount)}`
          : '',
      ],
      [A.requests.amount, lead.amount !== undefined ? money.format(lead.amount) : ''],
    ];
    $(thread, '[data-thread-facts]').replaceChildren(
      ...facts
        .filter(([, value]) => value)
        .map(([name, value]) => {
          const line = document.createElement('div');
          const term = document.createElement('dt');
          const detail = document.createElement('dd');
          term.textContent = name;
          detail.textContent = value;
          line.append(term, detail);
          return line;
        }),
    );
    $(thread, '[data-thread-description]').textContent = lead.description;

    const channels = A.channels as Record<string, string>;
    $(thread, '[data-thread-feed]').replaceChildren(
      ...lead.feed.map((entry) => {
        const row = entryTemplate.content.cloneNode(true) as DocumentFragment;
        const item = $(row, 'li');
        if (entry.kind === 'status') {
          item.classList.add('is-status');
          $(row, '[data-who]').textContent = fill(A.requests.status, {
            status: statusName(entry.status ?? ''),
          });
        } else {
          item.classList.add(entry.direction === 'in' ? 'is-in' : 'is-out');
          const who = entry.direction === 'in' ? A.requests.you : A.requests.answer;
          const channel = entry.channel ? channels[entry.channel] : '';
          $(row, '[data-who]').textContent = [who, channel].filter(Boolean).join(' · ');
          const files = entry.files?.length
            ? `\n${fill(A.requests.files, { names: entry.files.join(', ') })}`
            : '';
          $(row, '[data-body]').textContent = `${entry.body ?? ''}${files}`;
        }
        $(row, '[data-when]').textContent = when(entry.at);
        return row;
      }),
    );
    const reply = $<HTMLFormElement>(thread, '[data-thread-reply]');
    reply.hidden = !lead.can_write;
    $(thread, '[data-thread-closed]').hidden = lead.can_write;
    $(reply, '[data-done]').textContent = '';

    // Opened, so read: the list loses its «new».
    const summary = list.find((item) => item.number === lead.number);
    if (summary?.unread) {
      summary.unread = false;
      renderRequests();
    }
    requests.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }

  function closeThread() {
    open = '';
    thread.hidden = true;
    listView.hidden = false;
  }

  /** «#K-0042» in the address opens that request: the links of letters and of the bot lead here. */
  function route() {
    const number = /^#(K-\d+)$/i.exec(location.hash)?.[1]?.toUpperCase();
    if (number && number !== open) void openThread(number);
    else if (!number && open) closeThread();
  }
  window.addEventListener('hashchange', () => {
    if (view === 'dashboard') route();
  });

  $(thread, '[data-thread-back]').addEventListener('click', () => {
    history.pushState(null, '', location.pathname + location.search);
    closeThread();
  });

  $(thread, '[data-thread-ask]').addEventListener('click', () => {
    parentSelect.value = open;
    const card = $(board, '#inquiry');
    card.scrollIntoView({ behavior: 'smooth', block: 'start' });
    $<HTMLTextAreaElement>(card, 'textarea').focus({ preventScroll: true });
  });

  const replyForm = $<HTMLFormElement>(thread, '[data-thread-reply]');
  replyForm.addEventListener('submit', (event) => {
    event.preventDefault();
    const field = $<HTMLTextAreaElement>(replyForm, 'textarea');
    const text = field.value.trim();
    const problem = $(replyForm, '[data-error]');
    if (!text) {
      problem.textContent = A.errors.empty;
      return;
    }
    void busy(replyForm, async () => {
      const { data } = await api(
        'POST',
        `/api/account/leads/${encodeURIComponent(open)}/messages`,
        { text },
      );
      if (!data.ok) {
        problem.textContent = error(data.error);
        return;
      }
      problem.textContent = '';
      field.value = '';
      // An answer can move the status (waiting for the client → in progress): the list follows.
      await Promise.all([openThread(open), loadRequests()]);
      $(replyForm, '[data-done]').textContent = A.requests.sent;
    });
  });

  const inquiry = $<HTMLFormElement>(board, '[data-inquiry]');
  inquiry.addEventListener('submit', (event) => {
    event.preventDefault();
    const text = $<HTMLTextAreaElement>(inquiry, 'textarea');
    const subject = $<HTMLInputElement>(inquiry, 'input[name="subject"]');
    const problem = $(inquiry, '[data-error]');
    const done = $(inquiry, '[data-done]');
    done.textContent = '';
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
      done.textContent = fill(A.inquiry.sent, { id: `#${data.number}` });
      await loadRequests();
    });
  });

  // --- contacts ---------------------------------------------------------------------------------

  const contactTemplate = $<HTMLTemplateElement>(document, '#tpl-contact');
  const contactForm = $<HTMLFormElement>(board, '[data-contact-form]');
  const kindSelect = $<HTMLSelectElement>(contactForm, 'select');
  const valueInput = $<HTMLInputElement>(contactForm, 'input[name="value"]');
  kindSelect.addEventListener('change', () => {
    valueInput.placeholder = T.examples[kindSelect.value] ?? '';
  });

  function renderContacts() {
    if (!me) return;
    const kinds = A.contacts.kinds as Record<string, string>;
    const holder = $(board, '[data-contact-list]');
    holder.replaceChildren(
      ...me.contacts.map((contact) => {
        const row = contactTemplate.content.cloneNode(true) as DocumentFragment;
        $(row, '[data-kind]').textContent = kinds[contact.kind] ?? contact.kind;
        $(row, '[data-value]').textContent = contact.value;
        $(row, '[data-remove]').addEventListener('click', (event) => {
          const button = event.currentTarget as HTMLButtonElement;
          button.disabled = true;
          void api('POST', '/api/account/contacts/remove', { id: contact.id }).then(({ data }) => {
            button.disabled = false;
            if (!data.ok || !me) return;
            me.contacts = me.contacts.filter((item) => item.id !== contact.id);
            renderContacts();
          });
        });
        return row;
      }),
    );
    $(board, '[data-contacts-empty]').hidden = me.contacts.length > 0;
  }

  contactForm.addEventListener('submit', (event) => {
    event.preventDefault();
    const problem = $(contactForm, '[data-error]');
    if (!valueInput.value.trim()) {
      problem.textContent = A.errors.bad_contact;
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
        problem.textContent = error(data.error);
        valueInput.setAttribute('aria-invalid', 'true');
        return;
      }
      problem.textContent = '';
      valueInput.removeAttribute('aria-invalid');
      valueInput.value = '';
      const added = data.contact;
      me.contacts = [...me.contacts.filter((item) => item.id !== added.id), added];
      renderContacts();
    });
  });

  // --- profile ----------------------------------------------------------------------------------

  const profile = $<HTMLFormElement>(board, '[data-profile]');

  function renderProfile() {
    if (!me) return;
    const { client } = me;
    $<HTMLInputElement>(profile, '[name="name"]').value = client.name;
    $<HTMLInputElement>(profile, '[name="company"]').value = client.company;
    $<HTMLSelectElement>(profile, '[name="lang"]').value = client.lang || lang;
    for (const radio of profile.querySelectorAll<HTMLInputElement>('[name="preferred"]')) {
      radio.checked = radio.value === (client.preferred || (client.email ? 'email' : 'telegram'));
      radio.disabled = radio.value === 'telegram' ? !client.telegram : !client.email;
    }
  }

  profile.addEventListener('submit', (event) => {
    event.preventDefault();
    const done = $(profile, '[data-done]');
    void busy(profile, async () => {
      const values = {
        name: $<HTMLInputElement>(profile, '[name="name"]').value.trim(),
        company: $<HTMLInputElement>(profile, '[name="company"]').value.trim(),
        lang: $<HTMLSelectElement>(profile, '[name="lang"]').value,
        preferred:
          profile.querySelector<HTMLInputElement>('[name="preferred"]:checked')?.value ?? '',
      };
      const { data } = await api('POST', '/api/account/profile', values);
      done.textContent = data.ok ? A.profile.saved : error(data.error);
      if (data.ok && me) {
        me.client = { ...me.client, ...values };
        renderHead();
      }
    });
  });

  // --- the ways in: email and Telegram -----------------------------------------------------------

  const access = $(board, '[data-access]');
  const accessEmailForm = $<HTMLFormElement>(access, '[data-access-email-form]');
  const accessCodeForm = $<HTMLFormElement>(access, '[data-access-code-form]');
  const accessBot = $(access, '[data-access-bot]');
  const accessBotLink = $<HTMLAnchorElement>(access, '[data-access-bot-link]');
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
  for (const cancel of access.querySelectorAll('[data-access-cancel]')) {
    cancel.addEventListener('click', () => renderAccess());
  }

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
        accessBotLink.href = data.bot_url;
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
      $(access, '[data-access-email]').closest('dl')?.scrollIntoView({ block: 'nearest' });
    });
  });

  // --- devices ----------------------------------------------------------------------------------

  const sessionTemplate = $<HTMLTemplateElement>(document, '#tpl-session');

  function renderSessions() {
    if (!me) return;
    const holder = $(board, '[data-session-list]');
    holder.replaceChildren(
      ...me.sessions.map((session) => {
        const row = sessionTemplate.content.cloneNode(true) as DocumentFragment;
        $(row, '[data-device]').textContent = session.device;
        $(row, '[data-seen]').textContent = fill(A.sessions.seen, { date: when(session.seen) });
        $(row, '[data-current]').hidden = !session.current;
        const end = $<HTMLButtonElement>(row, '[data-end]');
        end.hidden = session.current;
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
    $(board, '[data-sessions-end-all]').hidden = me.sessions.every((session) => session.current);
  }

  $(board, '[data-sessions-end-all]').addEventListener('click', () => {
    void api('POST', '/api/account/sessions/end', { id: '' }).then(({ data }) => {
      if (!data.ok || !me) return;
      me.sessions = me.sessions.filter((session) => session.current);
      renderSessions();
    });
  });

  // --- achievements -------------------------------------------------------------------------------

  const shareOf = (share: number) => {
    const format = new Intl.NumberFormat(lang, {
      style: 'percent',
      maximumFractionDigits: share < 10 ? 1 : 0,
    });
    return share > 0 && share < 0.1 ? `<${format.format(0.001)}` : format.format(share / 100);
  };

  /** The achievements of orders: the list, and a banner for each the client has not seen yet. */
  async function renderOrders() {
    if (!me) return;
    const { earned, shares } = me.orders;
    const { icon, unlock } = await import('../../../design/components/eggs/achievement.js');
    const have = new Map(earned.map((item) => [item.id, item]));
    const rareOf = (id: string) =>
      id === 'all_orders' || (shares[id] !== undefined && (shares[id] ?? 100) < 10);
    for (const row of board.querySelectorAll<HTMLElement>('[data-order]')) {
      const id = row.dataset['order'] ?? '';
      const item = have.get(id);
      const rare = Boolean(item) && rareOf(id);
      row.classList.toggle('is-found', Boolean(item));
      row.classList.toggle('is-rare', rare);
      $(row, '[data-icon]').replaceChildren(icon(id, rare, { size: 40, locked: !item }));
      $(row, '[data-state]').textContent = item
        ? fill(A.orders.earned, { date: date(item.at) })
        : A.orders.locked;
      const share = shares[id];
      const cell = $(row, '[data-share]');
      cell.textContent = share === undefined ? '' : shareOf(share);
      if (share !== undefined) cell.title = fill(A.orders.rarity, { percent: shareOf(share) });
    }

    // What happened since the last visit comes the way Steam announces it: one banner after another.
    const fresh = earned.filter((item) => item.new);
    if (fresh.length === 0) return;
    const base = earned.filter((item) => item.id !== 'all_orders');
    for (const item of fresh) {
      const texts =
        A.orders[item.id as 'first_order' | 'second_order' | 'big_order' | 'all_orders'];
      if (!texts) continue;
      const share = shares[item.id];
      const rare = rareOf(item.id);
      unlock({
        id: item.id,
        name: texts.name,
        text: fill(texts.text, { amount: money.format(me.orders.big_order) }),
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

  async function syncEggs() {
    const card = board.querySelector<HTMLElement>('[data-eggs-card]');
    if (!me) return;
    const own = receipts();
    const mine = EGGS.flatMap((id) => (own[id] ? [own[id]] : []));
    if (mine.length > 0) {
      const { data } = await api<Answer & { eggs?: Me['eggs'] }>('POST', '/api/account/eggs', {
        receipts: mine,
      });
      if (data.ok && data.eggs) me.eggs = data.eggs;
    }
    const panel = await import('./achievements-panel.ts');
    panel.useAccount(me.eggs, me.loyalty.eggs_used);
    if (!card) return;
    const found = new Set([...panel.browserFinds(), ...me.eggs.map((egg) => egg.id)]);
    const count = EGGS.filter((id) => found.has(id)).length;
    $(card, '[data-eggs-count]').textContent = `${count}/${EGGS.length}`;
    const { icon } = await import('../../../design/components/eggs/achievement.js');
    $(card, '[data-eggs-icons]').replaceChildren(
      ...EGGS.map((id) => icon(id, false, { size: 28, locked: !found.has(id) })),
      icon('all', count === EGGS.length, { size: 28, locked: count < EGGS.length }),
    );
  }

  board.querySelector('[data-eggs-open]')?.addEventListener('click', () => {
    void import('./achievements-panel.ts').then((panel) => panel.openAchievements());
  });

  // --- deleting the account ------------------------------------------------------------------------

  const remove = $<HTMLFormElement>(board, '[data-delete]');
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
        signedOut();
      }
    });
  });

  // --- start ------------------------------------------------------------------------------------

  async function start() {
    // A link from a letter or from the bot: «#login=<token>». The token leaves the address at once.
    const token = /^#login=([A-Za-z0-9._-]{8,200})$/.exec(location.hash)?.[1];
    if (token) {
      history.replaceState(null, '', location.pathname + location.search);
      $(root!, '[data-loading]').textContent = A.login.link;
      const { data } = await api('POST', '/api/account/login/link', { token });
      if (!data.ok) {
        signedOut(error(data.error));
        return;
      }
    }
    await load();
  }

  void start();
}
