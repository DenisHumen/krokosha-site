// The personal account in a real browser (pages/[...lang]/account.astro, scripts/account.ts; design
// v3). What the API does is tested in Go (api/internal/clients); here the page gets the API's answers
// and is checked for what a client sees, what exactly the browser sends, and how the screens switch.
import AxeBuilder from '@axe-core/playwright';
import type { Page, Request } from '@playwright/test';
import { expect, test } from './fixtures.ts';

const CSRF = 'the-token-of-the-session';

// A photo of an answer, as the API hands it out: a PNG of one pixel.
const PHOTO = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==',
  'base64',
);

const ME = {
  ok: true,
  csrf: CSRF,
  client: {
    id: 7,
    name: 'Ольга Коваль',
    company: 'ACME',
    lang: 'ru',
    email: 'olga@company.com',
    telegram: '',
    preferred: 'email',
    since: '2026-09-01T10:00:00Z',
  },
  contacts: [{ id: 1, kind: 'linkedin', value: 'in/olga-k' }],
  loyalty: {
    enabled: true,
    currency: 'USD',
    orders: 2,
    spent: 3200,
    offer: { percent: 5, reason: 'tier', detail: 'silver' },
    eggs_used: false,
    welcome_used: true,
    tier: 'silver',
    next: { tier: 'gold', orders: 1, spent: 1800, progress: 0.66 },
  },
  eggs: [{ id: 'konami', at: '2026-09-10T12:00:00Z' }],
  orders: {
    earned: [
      { id: 'first_order', at: '2026-09-20T10:00:00Z', new: false },
      { id: 'big_order', at: '2026-09-23T15:00:00Z', new: true },
    ],
    shares: { first_order: 40, second_order: 22, big_order: 6.5, all_orders: 3 },
    big_order: 3000,
  },
  sessions: [
    {
      id: 'aaaaaaaa',
      created: '2026-09-20T08:00:00Z',
      seen: '2026-09-24T08:00:00Z',
      device: 'Chrome · Linux',
      current: true,
    },
    {
      id: 'bbbbbbbb',
      created: '2026-09-02T08:00:00Z',
      seen: '2026-09-12T19:30:00Z',
      device: 'Safari · iOS',
      current: false,
    },
  ],
  bot: true,
};

const SUMMARY = {
  number: 'K-0042',
  kind: 'request',
  status: 'waiting_client',
  created: '2026-09-18T09:00:00Z',
  updated: '2026-09-23T15:00:00Z',
  direction: 'networks',
  excerpt: 'Office network for forty seats: MikroTik and two VLANs.',
  unread: true,
  answered: '2026-09-23T15:00:00Z',
  discount: { percent: 10, reason: 'welcome' },
};

const VIEW = {
  ...SUMMARY,
  description: 'Office network for forty seats: MikroTik and two VLANs.',
  contact: 'olga@company.com',
  method: 'email',
  can_write: true,
  feed: [
    { at: '2026-09-18T09:00:00Z', kind: 'status', status: 'new' },
    {
      at: '2026-09-18T09:00:00Z',
      kind: 'message',
      direction: 'in',
      channel: 'form',
      body: 'Office network for forty seats: MikroTik and two VLANs.',
    },
    { at: '2026-09-23T15:00:00Z', kind: 'status', status: 'waiting_client' },
    {
      at: '2026-09-23T15:00:00Z',
      kind: 'message',
      direction: 'out',
      channel: 'email',
      body: 'Hello! Could you send the plan of the office?',
      files: [
        { id: 8, name: 'offer.pdf', kind: 'pdf', size: 120000 },
        { id: 7, name: 'rack.png', kind: 'png', size: 16700 },
      ],
    },
    // A second answer by the same channel right after the first: one group with it.
    {
      at: '2026-09-23T15:01:00Z',
      kind: 'message',
      direction: 'out',
      channel: 'email',
      body: 'And the number of seats on each floor.',
    },
  ],
};

// A request that is over: the field to write gives way to an inquiry about it.
const CLOSED = {
  number: 'K-0031',
  kind: 'request',
  status: 'done',
  created: '2026-09-02T09:00:00Z',
  updated: '2026-09-10T12:00:00Z',
  direction: 'servers',
  excerpt: 'A backup server for the office.',
  unread: false,
  answered: '2026-09-10T12:00:00Z',
  discount: { percent: 0 },
};

const CLOSED_VIEW = {
  ...CLOSED,
  description: CLOSED.excerpt,
  contact: 'olga@company.com',
  method: 'email',
  can_write: false,
  feed: [
    { at: CLOSED.created, kind: 'status', status: 'new' },
    { at: CLOSED.created, kind: 'message', direction: 'in', channel: 'form', body: CLOSED.excerpt },
    {
      at: '2026-09-10T12:00:00Z',
      kind: 'message',
      direction: 'out',
      channel: 'email',
      body: 'The server is in the rack.',
    },
    { at: '2026-09-10T12:00:00Z', kind: 'status', status: 'done' },
  ],
};

// A long conversation, longer than any screen: it scrolls inside, the page does not.
const LONG_VIEW = {
  ...SUMMARY,
  number: 'K-0060',
  unread: false,
  description: 'A long one.',
  contact: 'olga@company.com',
  method: 'email',
  can_write: true,
  feed: Array.from({ length: 40 }, (_, i) => ({
    at: `2026-09-20T10:${String(i).padStart(2, '0')}:00Z`,
    kind: 'message',
    direction: i % 3 === 0 ? 'out' : 'in',
    channel: 'email',
    body: `Message number ${i + 1} of a long conversation.`,
  })),
};

// What an inquiry sent from the account looks like once it is there.
const INQUIRY = {
  number: 'K-0050',
  kind: 'inquiry',
  status: 'new',
  created: '2026-09-24T10:00:00Z',
  updated: '2026-09-24T10:00:00Z',
  direction: '',
  subject: 'The invoice',
  excerpt: 'Could you send the invoice once more?',
  parent: 'K-0042',
  unread: false,
  discount: { percent: 0 },
};

interface Api {
  /** Requests the page sent, as «METHOD path». */
  calls: string[];
  /** Bodies of the POSTs, by path. */
  bodies: Map<string, unknown>;
}

/** The API of the account, answering like the real one; nobody is signed in unless `signedIn`. */
async function mockApi(page: Page, signedIn = false): Promise<Api> {
  let session = signedIn;
  const leads: Record<string, unknown>[] = [SUMMARY, CLOSED];
  const calls: string[] = [];
  const bodies = new Map<string, unknown>();
  const record = (request: Request) => {
    const path = new URL(request.url()).pathname;
    calls.push(`${request.method()} ${path}`);
    if (request.method() === 'POST') {
      if (path.startsWith('/api/account/upload/')) {
        // A message with files: a form, with the token of the session like any change.
        expect(request.headers()['content-type']).toMatch(/^multipart\/form-data; boundary=/);
        bodies.set(path, request.postData() ?? '');
      } else {
        bodies.set(path, request.postDataJSON());
        expect(request.headers()['content-type']).toBe('application/json');
      }
      if (session && !path.startsWith('/api/account/login'))
        expect(request.headers()['x-csrf-token']).toBe(CSRF);
    }
  };
  await page.route('**/api/account/**', async (route) => {
    const request = route.request();
    record(request);
    const path = new URL(request.url()).pathname;
    switch (path) {
      case '/api/account/me':
        return route.fulfill({ json: session ? ME : { ok: false, error: 'signed_out' } });
      case '/api/account/login':
        return route.fulfill({ json: { ok: true, sent_to: 'o***a@company.com' } });
      case '/api/account/login/link':
        // The page asks whose account a link opens before it spends it.
        if ((request.postDataJSON() as { peek?: boolean }).peek)
          return route.fulfill({ json: { ok: true, account: 'o***a@company.com' } });
        session = true;
        return route.fulfill({ json: { ok: true, lang: 'ru', created: false, linked: 0 } });
      case '/api/account/login/code':
        session = true;
        return route.fulfill({ json: { ok: true, lang: 'ru', created: false, linked: 0 } });
      case '/api/account/logout':
        session = false;
        return route.fulfill({ json: { ok: true } });
      case '/api/account/leads':
        return route.fulfill({ json: { ok: true, leads } });
      case '/api/account/leads/K-0042':
        return route.fulfill({ json: { ok: true, lead: VIEW } });
      case '/api/account/leads/K-0042/files/7':
        return route.fulfill({ body: PHOTO, contentType: 'image/png' });
      case '/api/account/leads/K-0031':
        return route.fulfill({ json: { ok: true, lead: CLOSED_VIEW } });
      case '/api/account/leads/K-0060':
        return route.fulfill({ json: { ok: true, lead: LONG_VIEW } });
      case '/api/account/inquiries':
        leads.unshift(INQUIRY);
        return route.fulfill({ json: { ok: true, number: INQUIRY.number } });
      case '/api/account/leads/K-0050':
        return route.fulfill({
          json: {
            ok: true,
            lead: {
              ...INQUIRY,
              description: INQUIRY.excerpt,
              contact: 'olga@company.com',
              method: 'email',
              can_write: true,
              feed: [{ at: INQUIRY.created, kind: 'status', status: 'new' }],
            },
          },
        });
      default:
        return route.fulfill({ json: { ok: true } });
    }
  });
  return { calls, bodies };
}

test.describe('personal account', () => {
  test('signs in with a code from a letter and shows the account', async ({ page }) => {
    const api = await mockApi(page);
    await page.goto('/ru/account/');
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Вход');
    await expect(page.locator('meta[name="robots"]')).toHaveAttribute('content', /noindex/);

    await page.locator('#login-email').fill('olga@company.com');
    await page.getByRole('button', { name: 'Получить код' }).click();
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Код из письма');
    await expect(page.locator('[data-login-lead]')).toContainText('o***a@company.com');
    await expect(page.locator('[data-resend]')).toHaveText(/Отправить снова через [01]:\d\d/);
    await expect(page.locator('[data-resend]')).toBeDisabled();
    expect(api.bodies.get('/api/account/login')).toEqual({
      method: 'email',
      email: 'olga@company.com',
      lang: 'ru',
    });

    // Six digits sign in by themselves.
    await page.locator('#login-code').fill('123456');
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Здравствуйте, Ольга');
    expect(api.bodies.get('/api/account/login/code')).toEqual({ code: '123456' });
    await expect(page.locator('[data-since]')).toHaveText('С нами с 1 сентября 2026');

    // The header: the discount, the level, what is open, the initials.
    await expect(page.locator('[data-tick="discount"]')).toHaveText('−5%');
    await expect(page.locator('[data-tick="level"]')).toHaveText('Серебряный');
    await expect(page.locator('[data-tick="open"]')).toHaveText('1 заявка');
    await expect(page.locator('[data-initials]')).toHaveText('ОК');
    await expect(page.locator('[data-unread-dot]')).toBeVisible();

    // The home screen: the discount, the level and what the next one asks, the requests.
    const home = page.locator('[data-screen="home"]');
    await expect(home.locator('[data-offer]')).toHaveText('−5%');
    await expect(home.locator('[data-offer-reason]')).toHaveText('уровень «Серебряный»');
    await expect(home.locator('[data-level-name]')).toHaveText('Серебряный');
    await expect(home.locator('[data-level-next]')).toHaveText(
      /^До уровня «Золотой»: 1 заказ или 1\s800\s\$$/,
    );
    await expect(home.locator('[data-answer-label]')).toHaveText('Новый ответ');
    await expect(home.locator('[data-answer-at]')).toContainText('#K-0042');
    const row = home.locator('[data-latest] a').first();
    await expect(row).toHaveAttribute('href', '#K-0042');
    await expect(row).toContainText('Ждёт вашего ответа');
    await expect(row.locator('[data-unread]')).toBeVisible();
    await expect(home.locator('[data-eggs-count]')).toHaveText('1/8');

    // The achievements of orders: the new one comes as a banner, rare (6.5 % of clients), and is
    // then marked as shown.
    const banner = page.locator('#kro-achievement');
    await expect(banner).toContainText('Крупный проект');
    await expect(banner).toContainText('Заказ от');
    await expect(banner).toContainText('Есть у 6,5 % клиентов');
    await expect
      .poll(() => api.bodies.get('/api/account/achievements/seen'))
      .toEqual({ ids: ['big_order'] });

    await page.goto('/ru/account/#achievements');
    const orders = page.locator('[data-screen="achievements"]');
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Ачивки');
    await expect(orders.locator('[data-order="first_order"]')).toHaveClass(/is-found/);
    await expect(orders.locator('[data-order="first_order"] [data-rarity]')).toHaveText(
      '40 % клиентов',
    );
    await expect(orders.locator('[data-order="big_order"] [data-rarity]')).toHaveText(
      'Редкая · 6,5 %',
    );
    await expect(orders.locator('[data-order="second_order"] [data-state]')).toHaveText(
      'Ещё не открыто',
    );
    await expect(orders.locator('[data-egg="konami"]')).toHaveClass(/is-found/);
    await expect(orders.locator('[data-egg="sudo"] [data-name]')).toHaveText('???');

    // The discount and the level.
    await page.locator('[data-nav="loyalty"]').click();
    const loyalty = page.locator('[data-screen="loyalty"]');
    await expect(loyalty.locator('[data-next-percent]')).toHaveText('−5%');
    await expect(loyalty.locator('[data-orders-target]')).toHaveText('/ 3 · Золотой');
    await expect(loyalty.locator('[data-row="welcome"]')).toHaveClass(/is-used/);
    await expect(loyalty.locator('[data-tier-id="silver"]')).toContainText('ваш уровень');

    // Contacts, the ways in, the devices.
    await page.locator('[data-nav="profile"]').click();
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Профиль и вход');
    await expect(page.locator('[data-contact-list]')).toContainText('in/olga-k');
    await expect(page.locator('[data-access-email]')).toHaveText('olga@company.com');
    await expect(page.locator('[data-session-list] li')).toHaveCount(2);
    await expect(page.locator('#profile-name')).toHaveValue('Ольга Коваль');
  });

  test('a link from a letter says whose account it opens, then signs in', async ({ page }) => {
    const api = await mockApi(page);
    await page.goto('/account/#login=1a2b.c2VjcmV0LWxpbmstdG9rZW4');
    const ask = page.locator('[data-login-link]');
    await expect(ask).toContainText('o***a@company.com');
    // The token leaves the address at once, and nothing is spent before the visitor agrees.
    expect(new URL(page.url()).hash).toBe('');
    expect(api.bodies.get('/api/account/login/link')).toEqual({
      token: '1a2b.c2VjcmV0LWxpbmstdG9rZW4',
      peek: true,
    });
    await ask.getByRole('button', { name: 'Sign in' }).click();
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Hello, Ольга');
    expect(api.bodies.get('/api/account/login/link')).toEqual({
      token: '1a2b.c2VjcmV0LWxpbmstdG9rZW4',
    });
  });

  test('a link somebody else sent is turned down with one click', async ({ page }) => {
    const api = await mockApi(page);
    await page.goto('/account/#login=1a2b.c2VjcmV0LWxpbmstdG9rZW4');
    await page.getByRole('button', { name: 'It is not mine' }).click();
    await expect(page.locator('#login-email')).toBeVisible();
    expect(api.calls.filter((call) => call === 'POST /api/account/login/link')).toHaveLength(1);
  });

  test('lists the requests a line each and finds them', async ({ page }, info) => {
    await mockApi(page, true);
    // A wide screen opens a request next to the list: the closed one, so the other stays unread.
    await page.goto(info.project.name === 'phone' ? '/account/#requests' : '/account/#K-0031');
    const list = page.locator('[data-requests-list]');
    await expect(list).toBeVisible();
    await expect(page.locator('#requests-title')).toHaveText('Requests and inquiries · 2');
    const rows = list.getByRole('link');
    await expect(rows).toHaveCount(2);
    // The number, the subject, the status in short — the client's move is «your turn» — and the
    // time, with a mark while an answer waits to be read.
    const request = rows.nth(0);
    await expect(request.locator('[data-number]')).toHaveText('K-0042');
    await expect(request.locator('[data-title]')).toHaveText('Networks & hardware');
    await expect(request.locator('[data-status]')).toHaveText('your turn');
    await expect(request).toHaveAttribute('data-new', '');
    await expect(request.locator('[data-unread]')).toBeVisible();
    await expect(request.locator('[data-time]')).toHaveAttribute('datetime', SUMMARY.updated);
    expect((await request.boundingBox())?.height).toBe(38);
    await expect(rows.nth(1).locator('[data-status]')).toHaveText('done');
    await expect(rows.nth(1).locator('[data-unread]')).toBeHidden();
    await expect(page.locator('[data-requests-foot]')).toHaveText('2 requests');

    // The search: by the number, the words of the request, the status.
    const search = page.getByRole('searchbox', { name: 'Find a request' });
    const shown = rows.filter({ visible: true });
    await search.fill('backup');
    await expect(shown).toHaveCount(1);
    await expect(shown).toContainText('K-0031');
    await expect(page.locator('[data-requests-foot]')).toHaveText('1 of 2');
    await search.fill('#k-0042');
    await expect(shown).toHaveCount(1);
    await expect(shown).toContainText('K-0042');
    await search.fill('Your turn');
    await expect(shown).toContainText('K-0042');
    await search.fill('nothing like this');
    await expect(shown).toHaveCount(0);
    await expect(page.locator('[data-requests-nothing]')).toBeVisible();
    await search.fill('');
    await expect(shown).toHaveCount(2);
    await expect(page.locator('[data-requests-nothing]')).toBeHidden();
  });

  test('opens a request from its address as a conversation', async ({ page }) => {
    await mockApi(page, true);
    await page.goto('/account/#K-0042');
    const thread = page.locator('[data-thread]');
    await expect(thread).toBeVisible();
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Requests');
    // The line over the conversation: the number, the subject, the status; the rest in «Details».
    await expect(thread.locator('[data-thread-number]')).toHaveText('#K-0042');
    await expect(thread.getByRole('heading', { level: 2 })).toHaveText('Networks & hardware');
    await expect(thread.locator('[data-thread-status] .status-full')).toHaveText(
      'Waiting for your answer',
    );
    await expect(thread.locator('[data-thread-status] .status-short')).toHaveText('your turn');
    const facts = thread.locator('[data-thread-facts]');
    await expect(facts).toBeHidden();
    await thread.locator('[data-thread-details] summary').click();
    await expect(facts).toBeVisible();
    await expect(facts).toContainText('Discount');
    await expect(facts).toContainText('−10% · first request');
    await expect(facts).toContainText('olga@company.com');

    // The request itself is the client's first message; then the answers, days and statuses.
    const feed = thread.locator('[data-thread-feed]');
    const messages = feed.locator('.msg');
    await expect(messages).toHaveCount(3);
    await expect(feed.locator('.msg-day')).toHaveCount(2);
    await expect(feed.locator('.msg-event')).toHaveText([
      /The request is received/,
      /Waiting for your answer/,
    ]);
    await expect(messages.nth(0)).toHaveAttribute('data-side', 'mine');
    await expect(messages.nth(0)).toContainText('You · form on the site');
    await expect(messages.nth(0)).toContainText('Office network for forty seats');
    await expect(messages.nth(1)).toHaveAttribute('data-side', 'theirs');
    await expect(messages.nth(1)).toContainText('Krokosha · email');
    await expect(messages.nth(1)).toContainText('Could you send the plan of the office?');

    // Two answers in a row by email make a group: the caption over the first only, and closer; the
    // second keeps its caption for screen readers.
    await expect(messages.nth(1)).toHaveAttribute('data-first', '');
    await expect(messages.nth(2)).not.toHaveAttribute('data-first');
    expect(await messages.nth(1).evaluate((node) => getComputedStyle(node).marginTop)).toBe('10px');
    expect(await messages.nth(2).evaluate((node) => getComputedStyle(node).marginTop)).toBe('2px');
    await expect(messages.nth(2).locator('.msg-head')).toHaveText('Krokosha · email');

    // The photo of the answer is in its bubble, first, though the API lists it second; the document
    // is a download.
    const files = messages.nth(1).getByRole('list', { name: 'Files' });
    const photo = files.getByRole('img', { name: 'rack.png' });
    await expect(photo).toBeVisible();
    await expect(files.locator('li').first().getByRole('img')).toHaveCount(1);
    expect(await photo.boundingBox()).toMatchObject({ width: 240, height: 140 });
    await expect
      .poll(() => photo.evaluate((image: HTMLImageElement) => image.naturalWidth))
      .toBe(1);
    await expect(files.getByRole('link', { name: 'rack.png' })).toHaveAttribute(
      'href',
      '/api/account/leads/K-0042/files/7',
    );
    const document = files.getByRole('link', { name: /offer\.pdf/ });
    await expect(document).toHaveAttribute('download', 'offer.pdf');
    await expect(document).toHaveAttribute('href', '/api/account/leads/K-0042/files/8');
    await expect(document).toContainText('117 kB');
    // Read now: the list and the rail lose their «new».
    await expect(page.locator('[data-unread-dot]')).toBeHidden();
    await expect(page.locator('[data-requests-list] a').first()).not.toHaveAttribute('data-new');
  });

  test('sends with Enter, starts a line with Shift+Enter, asks about the request', async ({
    page,
  }) => {
    const api = await mockApi(page, true);
    await page.goto('/account/#K-0042');
    const thread = page.locator('[data-thread]');
    const field = thread.getByLabel('Your answer');
    await field.fill('Here is the plan.');
    await field.press('Shift+Enter');
    await field.pressSequentially('Twelve seats upstairs.');
    await expect(field).toHaveValue('Here is the plan.\nTwelve seats upstairs.');
    expect(api.bodies.has('/api/account/leads/K-0042/messages')).toBe(false);
    await field.press('Enter');
    await expect(thread.locator('[data-done]')).toHaveText('Sent');
    expect(api.bodies.get('/api/account/leads/K-0042/messages')).toEqual({
      text: 'Here is the plan.\nTwelve seats upstairs.',
    });
    await expect(field).toHaveValue('');
    await expect(field).toBeFocused();
    // An Enter in an empty field sends nothing; the button says what is missing.
    const before = api.calls.length;
    await field.press('Enter');
    await thread.getByRole('button', { name: /^Send/ }).click();
    await expect(thread.locator('[data-error]')).toHaveText('Write a few words');
    expect(api.calls.slice(before).filter((call) => call.startsWith('POST'))).toEqual([]);
    await field.pressSequentially('Or');
    await expect(thread.locator('[data-error]')).toBeEmpty();
    // What could not be sent comes back into the field, with the reason.
    await page.route('**/api/account/leads/K-0042/messages', (route) =>
      route.fulfill({ json: { ok: false, error: 'throttled' } }),
    );
    await field.pressSequentially(' a call tomorrow?');
    await field.press('Enter');
    await expect(thread.locator('[data-error]')).toHaveText(
      'Too many attempts. Please try again in an hour.',
    );
    await expect(field).toHaveValue('Or a call tomorrow?');
    await page.unroute('**/api/account/leads/K-0042/messages');

    // An inquiry about it, from the details: the form opens with the request chosen, and the new
    // inquiry opens itself, its text the first message.
    await thread.locator('[data-thread-details] summary').click();
    await thread
      .locator('[data-thread-details]')
      .getByRole('button', { name: 'A question about this request' })
      .click();
    await expect(page).toHaveURL(/#new$/);
    await expect(page.locator('#inquiry-parent')).toHaveValue('K-0042');
    await expect(page.locator('#inquiry-text')).toBeFocused();
    await page.locator('#inquiry-subject').fill('The invoice');
    await page.locator('#inquiry-text').fill('Could you send the invoice once more?');
    await page.getByRole('button', { name: /^Send/ }).click();
    // The address changes once the answer has come: only then is the request surely recorded.
    await expect(page).toHaveURL(/#K-0050$/);
    expect(api.bodies.get('/api/account/inquiries')).toEqual({
      subject: 'The invoice',
      text: 'Could you send the invoice once more?',
      parent: 'K-0042',
    });
    await expect(thread.locator('[data-thread-title]')).toHaveText('The invoice');
    await expect(thread.locator('[data-done]')).toHaveText('Inquiry #K-0050 sent');
    const first = thread.locator('.msg').first();
    await expect(first).toContainText('You · personal account');
    await expect(first).toContainText('Could you send the invoice once more?');
    await expect(
      thread.locator('[data-thread-facts]').getByRole('link', { name: '#K-0042' }),
    ).toHaveAttribute('href', '#K-0042');
  });

  test('sends files with a message and says which cannot go', async ({ page }) => {
    const api = await mockApi(page, true);
    await page.goto('/account/#K-0042');
    const thread = page.locator('[data-thread]');
    const field = thread.getByLabel('Your answer');
    const input = thread.locator('[data-reply-files]');
    const chips = thread.locator('[data-reply-chosen] li');
    const path = '/api/account/upload/leads/K-0042/messages';
    const pdf = Buffer.from('%PDF-1.7\n%%EOF\n');

    // What cannot go is said at once, and nothing is sent.
    await input.setInputFiles({
      name: 'setup.exe',
      mimeType: 'application/octet-stream',
      buffer: Buffer.from('MZ'),
    });
    await expect(thread.locator('[data-error]')).toHaveText(
      'setup.exe cannot be sent: photos, MP4 videos, PDF, DOCX and TXT are fine.',
    );
    await expect(thread.locator('[data-reply-chosen]')).toBeHidden();
    // Chosen files show as chips; one is taken back.
    await input.setInputFiles([
      { name: 'rack.png', mimeType: 'image/png', buffer: PHOTO },
      { name: 'plan.pdf', mimeType: 'application/pdf', buffer: pdf },
    ]);
    await expect(chips).toHaveCount(2);
    await expect(thread.locator('[data-error]')).toBeEmpty();
    await thread.getByRole('button', { name: 'Remove rack.png' }).click();
    await expect(chips).toHaveCount(1);
    // Files alone are a message too: Enter sends them, as a form to their own path.
    await field.press('Enter');
    await expect(thread.locator('[data-done]')).toHaveText('Sent');
    const body = String(api.bodies.get(path));
    expect(body).toContain('filename="plan.pdf"');
    expect(body).not.toContain('rack.png');
    await expect(chips).toHaveCount(0);

    // What the API turns down comes back — the text and the files — with the name of the file.
    await page.route(`**${path}`, (route) =>
      route.fulfill({ json: { ok: false, error: 'file_type', file: 'plan.pdf' } }),
    );
    await input.setInputFiles({ name: 'plan.pdf', mimeType: 'application/pdf', buffer: pdf });
    await field.fill('The plan');
    await field.press('Enter');
    await expect(thread.locator('[data-error]')).toHaveText(
      'plan.pdf cannot be sent: photos, MP4 videos, PDF, DOCX and TXT are fine.',
    );
    await expect(field).toHaveValue('The plan');
    await expect(chips).toHaveCount(1);
    await page.unroute(`**${path}`);
    // Six files are one too many.
    await input.setInputFiles(
      ['1', '2', '3', '4', '5'].map((name) => ({
        name: `${name}.png`,
        mimeType: 'image/png',
        buffer: PHOTO,
      })),
    );
    await expect(thread.locator('[data-error]')).toHaveText('No more than 5 files in a message.');
    await expect(chips).toHaveCount(1);
  });

  test('a closed request offers an inquiry instead of the field to write', async ({ page }) => {
    await mockApi(page, true);
    await page.goto('/account/#K-0031');
    const thread = page.locator('[data-thread]');
    await expect(thread.locator('[data-thread-title]')).toHaveText('Servers, storage, clusters');
    await expect(thread.locator('[data-thread-reply]')).toBeHidden();
    const closed = thread.locator('[data-thread-closed]');
    await expect(closed).toBeVisible();
    await expect(closed).toContainText('The request is closed');
    await closed.getByRole('button', { name: 'A question about this request' }).click();
    await expect(page).toHaveURL(/#new$/);
    await expect(page.locator('#inquiry-parent')).toHaveValue('K-0031');
    await expect(page.locator('#inquiry-text')).toBeFocused();
  });

  test('the screen fits the window: the conversation scrolls, the page does not', async ({
    page,
  }, info) => {
    await mockApi(page, true);
    await page.goto('/account/#K-0060');
    const thread = page.locator('[data-thread]');
    // Forty messages and the request itself, which opens the conversation.
    await expect(thread.locator('.msg')).toHaveCount(41);
    await expect(thread.locator('.msg').first()).toContainText('A long one.');
    const scroller = thread.locator('[data-thread-scroll]');
    const left = () =>
      scroller.evaluate((node) => node.scrollHeight - node.scrollTop - node.clientHeight);
    // It opens at its end, and it is much taller than its box.
    await expect.poll(left).toBeLessThan(2);
    expect(await scroller.evaluate((node) => node.scrollHeight > 2 * node.clientHeight)).toBe(true);
    expect(
      await page.evaluate(() => document.documentElement.scrollHeight - window.innerHeight),
    ).toBeLessThanOrEqual(0);
    // The field to write is on the screen, over the bar of the rail on a phone.
    const field = thread.getByLabel('Your answer');
    const box = await field.boundingBox();
    const rail = await page.locator('.rail').boundingBox();
    const bottom = info.project.name === 'phone' ? (rail?.y ?? 0) : page.viewportSize()!.height;
    expect(box && box.y + box.height).toBeLessThanOrEqual(bottom);

    // One line that grows with the text up to 160 px, then scrolls.
    const height = () => field.evaluate((node: HTMLTextAreaElement) => node.offsetHeight);
    await field.fill(Array.from({ length: 14 }, (_, i) => `Line ${i + 1}`).join('\n'));
    await expect.poll(height).toBe(160);
    await field.fill('One more.');
    await expect.poll(height).toBe(34);

    // Scrolled up to read, it stays there; a message sent brings it back to the end.
    await scroller.evaluate((node) => node.scrollTo({ top: 0 }));
    await expect.poll(left).toBeGreaterThan(100);
    await field.press('Enter');
    await expect(thread.locator('[data-done]')).toHaveText('Sent');
    await expect.poll(left).toBeLessThan(2);
  });

  test('a phone shows the list, then one request with the way back', async ({ page }, info) => {
    test.skip(info.project.name !== 'phone', 'the layout of narrow screens');
    await mockApi(page, true);
    await page.goto('/account/#requests');
    const list = page.locator('[data-requests-list]');
    const thread = page.locator('[data-thread]');
    await expect(list).toBeVisible();
    await expect(thread).toBeHidden();
    await list.locator('a').first().click();
    await expect(thread).toBeVisible();
    await expect(list).toBeHidden();
    // The keyboard follows to the conversation; its line keeps the short status and the arrow.
    await expect(thread.locator('[data-thread-title]')).toBeFocused();
    await expect(thread.locator('[data-thread-status] .status-short')).toBeVisible();
    await expect(thread.locator('[data-thread-status] .status-full')).toBeHidden();
    await expect(thread.locator('[data-thread-details] summary')).toBeVisible();
    await page.getByRole('link', { name: 'All requests' }).click();
    await expect(list).toBeVisible();
    await expect(thread).toBeHidden();
    // Back on the list, the keyboard is on the request it came from.
    await expect(list.locator('a').first()).toBeFocused();
    // The rail is a bar at the bottom of the screen.
    const rail = await page.locator('.rail').boundingBox();
    expect(rail && rail.y + rail.height).toBeGreaterThan(700);
  });

  test('ends the other sessions and signs out', async ({ page }) => {
    const api = await mockApi(page, true);
    await page.goto('/uk/account/#profile');
    await page.getByRole('button', { name: 'Вийти на інших' }).click();
    await expect(page.locator('[data-session-list] li')).toHaveCount(1);
    expect(api.bodies.get('/api/account/sessions/end')).toEqual({ id: '' });
    await page.locator('[data-menu] summary').click();
    await page.locator('.menu-panel [data-logout]').click();
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Вхід');
    expect(api.calls).toContain('POST /api/account/logout');
    expect(new URL(page.url()).hash).toBe('');
  });

  const screens = ['', '#K-0042', '#K-0031', '#new', '#loyalty', '#achievements', '#profile'];
  for (const screen of ['login', ...screens]) {
    test(`no accessibility violations: ${screen || 'home'}`, async ({ page }) => {
      await mockApi(page, screen !== 'login');
      await page.goto(`/ru/account/${screen === 'login' ? '' : screen}`);
      await expect(
        page.locator(screen === 'login' ? '[data-view="login"]' : '[data-view="app"]'),
      ).toBeVisible();
      if (screen.startsWith('#K-')) {
        // The conversation with its details open: the facts are checked too.
        await expect(page.locator('[data-thread]')).toBeVisible();
        await page.locator('[data-thread-details] summary').click();
        await expect(page.locator('[data-thread-facts]')).toBeVisible();
      }
      await page.waitForFunction(() =>
        document
          .getAnimations()
          .every(
            (animation) =>
              animation.playState !== 'running' ||
              animation.effect?.getComputedTiming().iterations === Infinity,
          ),
      );
      const results = await new AxeBuilder({ page })
        .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'best-practice'])
        // The banner of a new achievement leaves by itself: it is checked on the site's pages.
        .exclude('#kro-achievement')
        .analyze();
      expect(
        results.violations.map(
          (violation) =>
            `${violation.id} (${violation.impact}): ${violation.nodes.map((node) => node.target.join(' ')).join(', ')}`,
        ),
      ).toEqual([]);
    });
  }
});
