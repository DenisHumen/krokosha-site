// The personal account in a real browser (pages/[...lang]/account.astro, scripts/account.ts). What the
// API does is tested in Go (api/internal/clients); here the page gets the API's answers and is checked
// for what a client sees and what exactly the browser sends.
import AxeBuilder from '@axe-core/playwright';
import type { Page, Request } from '@playwright/test';
import { expect, test } from './fixtures.ts';

const CSRF = 'the-token-of-the-session';

const ME = {
  ok: true,
  csrf: CSRF,
  client: {
    id: 7,
    name: 'Ольга',
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
    },
  ],
};

interface Api {
  /** Requests the page sent, as «METHOD path». */
  calls: string[];
  /** Bodies of the POSTs, by path. */
  bodies: Map<string, unknown>;
  signIn(): void;
}

/** The API of the account, answering like the real one; nobody is signed in until signIn(). */
async function mockApi(page: Page, signedIn = false): Promise<Api> {
  let session = signedIn;
  const calls: string[] = [];
  const bodies = new Map<string, unknown>();
  const record = (request: Request) => {
    const path = new URL(request.url()).pathname;
    calls.push(`${request.method()} ${path}`);
    if (request.method() === 'POST') {
      bodies.set(path, request.postDataJSON());
      expect(request.headers()['content-type']).toBe('application/json');
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
      case '/api/account/leads':
        return route.fulfill({ json: { ok: true, leads: [SUMMARY] } });
      case '/api/account/leads/K-0042':
        return route.fulfill({ json: { ok: true, lead: VIEW } });
      default:
        return route.fulfill({ json: { ok: true } });
    }
  });
  return { calls, bodies, signIn: () => (session = true) };
}

test.describe('personal account', () => {
  test('signs in with a code from a letter and shows the requests', async ({ page }) => {
    const api = await mockApi(page);
    await page.goto('/ru/account/');
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Личный кабинет');
    await expect(page.locator('meta[name="robots"]')).toHaveAttribute('content', /noindex/);

    await page.locator('#login-email').fill('olga@company.com');
    await page.getByRole('button', { name: 'Прислать код' }).click();
    await expect(page.locator('[data-sent]').first()).toContainText('o***a@company.com');
    expect(api.bodies.get('/api/account/login')).toEqual({
      method: 'email',
      email: 'olga@company.com',
      lang: 'ru',
    });

    await page.locator('#login-code').fill('123456');
    await page.getByRole('button', { name: 'Войти', exact: true }).click();
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Здравствуйте, Ольга');
    expect(api.bodies.get('/api/account/login/code')).toEqual({ code: '123456' });

    // The discount and the level.
    const loyalty = page.locator('[data-loyalty]');
    await expect(loyalty.locator('[data-next-percent]')).toHaveText('−5%');
    await expect(loyalty.locator('[data-next-reason]')).toHaveText('уровень «Серебряный»');
    await expect(loyalty.locator('[data-tier]')).toHaveText('Серебряный');
    await expect(loyalty.locator('[data-progress-title]')).toHaveText('До уровня «Золотой»');
    await expect(loyalty.locator('[data-progress-left]')).toHaveText(/ещё 1 заказ или ещё/);

    // The request, with its status and an unread answer.
    const request = page.locator('[data-requests-list] a').first();
    await expect(request).toContainText('#K-0042');
    await expect(request).toContainText('Ждёт вашего ответа');
    await expect(request).toContainText('новое');
    await expect(request).toHaveAttribute('href', '#K-0042');

    // The achievements of orders: the new one comes as a banner, gold (6.5 % of clients), and is
    // then marked as shown.
    const banner = page.locator('#kro-achievement');
    await expect(banner).toContainText('Крупный проект');
    await expect(banner).toContainText('Заказ от');
    await expect(banner).toContainText('Есть у 6,5 % клиентов');
    await expect
      .poll(() => api.bodies.get('/api/account/achievements/seen'))
      .toEqual({
        ids: ['big_order'],
      });
    const orders = page.locator('[data-orders]');
    await expect(orders.locator('[data-order="first_order"]')).toHaveClass(/is-found/);
    await expect(orders.locator('[data-order="big_order"]')).toHaveClass(/is-rare/);
    await expect(orders.locator('[data-order="second_order"] [data-state]')).toHaveText('Пока нет');
    await expect(orders.locator('[data-order="first_order"] [data-share]')).toHaveText('40 %');

    // Contacts, the ways in, the devices.
    await expect(page.locator('[data-contact-list]')).toContainText('in/olga-k');
    await expect(page.locator('[data-access-email]')).toHaveText('olga@company.com');
    await expect(page.locator('[data-session-list] li')).toHaveCount(2);
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

  test('opens a request from its address and answers in it', async ({ page }) => {
    const api = await mockApi(page, true);
    await page.goto('/account/#K-0042');
    const thread = page.locator('[data-thread]');
    await expect(thread).toBeVisible();
    await expect(thread.locator('[data-thread-status]')).toHaveText('Waiting for your answer');
    const feed = thread.locator('[data-thread-feed] li');
    await expect(feed).toHaveCount(4);
    await expect(feed.nth(0)).toContainText('Status: Under review');
    await expect(feed.nth(1)).toContainText('You · form on the site');
    await expect(feed.nth(3)).toContainText('Answer · email');
    await expect(feed.nth(3)).toContainText('Could you send the plan of the office?');
    // Read now: the list loses its «new».
    await expect(page.locator('[data-requests-list] [data-unread]')).toBeHidden();

    await thread.getByLabel('Your message').fill('Sure, here it is.');
    await thread.getByRole('button', { name: 'Send' }).click();
    await expect(thread.locator('[data-done]')).toHaveText('Sent');
    expect(api.bodies.get('/api/account/leads/K-0042/messages')).toEqual({
      text: 'Sure, here it is.',
    });

    // An inquiry about it.
    await thread.getByRole('button', { name: /Ask about this request/ }).click();
    await expect(page.locator('#inquiry-parent')).toHaveValue('K-0042');

    await thread.getByRole('button', { name: /All requests/ }).click();
    await expect(thread).toBeHidden();
    await expect(page.locator('[data-requests-list] a')).toHaveCount(1);
  });

  test('ends the other sessions and signs out', async ({ page }) => {
    const api = await mockApi(page, true);
    await page.goto('/uk/account/');
    await page.getByRole('button', { name: 'Вийти на всіх інших пристроях' }).click();
    await expect(page.locator('[data-session-list] li')).toHaveCount(1);
    expect(api.bodies.get('/api/account/sessions/end')).toEqual({ id: '' });
    await page.locator('[data-logout]').click();
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Особистий кабінет');
    expect(api.calls).toContain('POST /api/account/logout');
  });

  for (const colorScheme of ['light', 'dark'] as const) {
    for (const state of ['signed out', 'signed in'] as const) {
      test(`no accessibility violations: ${state} (${colorScheme})`, async ({ page }) => {
        await mockApi(page, state === 'signed in');
        await page.emulateMedia({ colorScheme });
        await page.goto('/ru/account/');
        await expect(
          page.locator(state === 'signed in' ? '[data-view="dashboard"]' : '[data-view="login"]'),
        ).toBeVisible();
        await page.evaluate(() => {
          for (const details of document.querySelectorAll('details')) details.open = true;
        });
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
          .analyze();
        expect(
          results.violations.map(
            (violation) =>
              `${violation.id} (${violation.impact}): ${violation.nodes.map((node) => node.target.join(' ')).join(', ')}`,
          ),
        ).toEqual([]);
      });
    }
  }
});
