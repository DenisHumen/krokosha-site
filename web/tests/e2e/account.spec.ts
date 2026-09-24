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
        { id: 7, name: 'rack.png', kind: 'png', size: 16700 },
        { id: 8, name: 'offer.pdf', kind: 'pdf', size: 120000 },
      ],
    },
  ],
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
  const leads: Record<string, unknown>[] = [SUMMARY];
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
      case '/api/account/logout':
        session = false;
        return route.fulfill({ json: { ok: true } });
      case '/api/account/leads':
        return route.fulfill({ json: { ok: true, leads } });
      case '/api/account/leads/K-0042':
        return route.fulfill({ json: { ok: true, lead: VIEW } });
      case '/api/account/leads/K-0042/files/7':
        return route.fulfill({ body: PHOTO, contentType: 'image/png' });
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

  test('opens a request from its address and answers in it', async ({ page }) => {
    const api = await mockApi(page, true);
    await page.goto('/account/#K-0042');
    const thread = page.locator('[data-thread]');
    await expect(thread).toBeVisible();
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Requests');
    await expect(thread.locator('[data-thread-status]')).toHaveText('Waiting for your answer');
    // The form's message is the description itself: it is not repeated in the conversation.
    const feed = thread.locator('[data-thread-feed] > li');
    await expect(feed).toHaveCount(3);
    await expect(feed.nth(0)).toContainText('The request is received');
    await expect(feed.nth(1)).toContainText('Waiting for your answer');
    await expect(feed.nth(2)).toContainText('Answer · email');
    await expect(feed.nth(2)).toContainText('Could you send the plan of the office?');
    // The photo of the answer is in the conversation; the document is a download.
    const files = feed.nth(2).getByRole('list', { name: 'Files' });
    const photo = files.getByRole('img', { name: 'rack.png' });
    await expect(photo).toBeVisible();
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
    // Read now: the rail loses its «new».
    await expect(page.locator('[data-unread-dot]')).toBeHidden();

    await thread.getByLabel('Your answer').fill('Sure, here it is.');
    await thread.getByRole('button', { name: /^Send/ }).click();
    await expect(thread.locator('[data-done]')).toHaveText('Sent');
    expect(api.bodies.get('/api/account/leads/K-0042/messages')).toEqual({
      text: 'Sure, here it is.',
    });

    // An inquiry about it: the form opens with the request chosen, and the new one opens itself.
    await thread.getByRole('button', { name: 'A question about this request' }).first().click();
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
    await page.getByRole('link', { name: 'All requests' }).click();
    await expect(list).toBeVisible();
    await expect(thread).toBeHidden();
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

  const screens = ['', '#K-0042', '#new', '#loyalty', '#achievements', '#profile'];
  for (const screen of ['login', ...screens]) {
    test(`no accessibility violations: ${screen || 'home'}`, async ({ page }) => {
      await mockApi(page, screen !== 'login');
      await page.goto(`/ru/account/${screen === 'login' ? '' : screen}`);
      await expect(
        page.locator(screen === 'login' ? '[data-view="login"]' : '[data-view="app"]'),
      ).toBeVisible();
      if (screen === '#K-0042') await expect(page.locator('[data-thread]')).toBeVisible();
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
