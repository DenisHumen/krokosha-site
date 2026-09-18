// What /assets/analytics.js really sends (brief B5). The server side is tested in Go
// (api/internal/analytics); the rules both sides must agree on are repeated here on purpose.
import type { Page, Request } from '@playwright/test';
import { expect, test } from './fixtures.ts';

interface Batch {
  v: number;
  id: string;
  p: string;
  l: string;
  r: string;
  u: Record<string, string>;
  ad: string;
  e: { t: string; o: number; x?: string; v?: number }[];
}

const EVENT_TYPES = [
  'pageview',
  'scroll',
  'time',
  'section',
  'section_time',
  'click',
  'outbound',
  'egg',
];

/** Opens a page the way a person would: Playwright itself counts as an automated browser, which the script ignores. */
async function open(page: Page, url: string, init?: () => void) {
  const batches: Batch[] = [];
  await page.addInitScript(() => {
    Object.defineProperty(Navigator.prototype, 'webdriver', { get: () => false });
  });
  if (init) await page.addInitScript(init);
  await page.route('**/api/e', (route) => route.fulfill({ status: 204 }));
  page.on('request', (request: Request) => {
    if (new URL(request.url()).pathname === '/api/e') batches.push(request.postDataJSON() as Batch);
  });
  await page.goto(url);
  return {
    batches,
    events: () => batches.flatMap((batch) => batch.e),
    hide: () =>
      page.evaluate(() => {
        Object.defineProperty(document, 'visibilityState', { value: 'hidden', configurable: true });
        document.dispatchEvent(new Event('visibilitychange'));
      }),
  };
}

test('a page view is reported with the page, its language and the campaign tags', async ({
  page,
}) => {
  const { batches } = await open(
    page,
    '/uk/?utm_source=google&utm_medium=cpc&utm_campaign=mikrotik&gclid=SECRET-CLICK-ID',
  );
  await expect.poll(() => batches.length, { timeout: 5000 }).toBeGreaterThan(0);

  const first = batches[0]!;
  expect(first.v).toBe(1);
  expect(first.id).toMatch(/^[0-9a-f]{16}$/);
  expect(first.p).toBe('/uk/');
  expect(first.l).toBe('uk');
  expect(first.u).toEqual({ s: 'google', m: 'cpc', c: 'mikrotik', t: '', n: '' });
  expect(first.e[0]).toMatchObject({ t: 'pageview', o: expect.any(Number) });
  // The ad click id marks the visit as paid; its value is nobody's business.
  expect(first.ad).toBe('g');
  expect(JSON.stringify(batches)).not.toContain('SECRET-CLICK-ID');
  // Only what the server accepts: no extra keys, nothing about the device.
  expect(Object.keys(first).sort()).toEqual(['ad', 'e', 'id', 'l', 'p', 'r', 'u', 'v']);
});

test('clicks on contact buttons and outbound links are reported', async ({ page }) => {
  const { events } = await open(page, '/');
  // Stay on the page: the link leads to Telegram.
  await page.evaluate(() =>
    document.addEventListener('click', (event) => event.preventDefault(), true),
  );
  await page.locator('[data-section="hero"] [data-track="cta-telegram"]').click();

  await expect
    .poll(() => events().map((event) => `${event.t}:${event.x ?? ''}`))
    .toEqual(expect.arrayContaining(['click:cta-telegram', 'outbound:t.me']));
});

test('scrolling reports sections and depth', async ({ page }) => {
  const { events, hide } = await open(page, '/');
  await page.evaluate(async () => {
    for (let y = 0; y <= document.documentElement.scrollHeight; y += 400) {
      window.scrollTo(0, y);
      await new Promise((resolve) => setTimeout(resolve, 40));
    }
    window.scrollTo(0, document.documentElement.scrollHeight);
  });
  // A section must stay on screen for a second to get a time record; rendering callbacks of a
  // background browser window lag, so give it a generous pause.
  await page.waitForTimeout(2500);
  await hide();

  await expect
    .poll(() =>
      events()
        .filter((event) => event.t === 'scroll')
        .map((event) => event.v),
    )
    .toEqual([25, 50, 75, 100]);
  const sections = events()
    .filter((event) => event.t === 'section')
    .map((event) => event.x);
  expect(sections).toEqual(expect.arrayContaining(['hero', 'services', 'projects', 'contacts']));
  expect(new Set(sections).size).toBe(sections.length); // each section once
  expect(events().some((event) => event.t === 'time' && (event.v ?? 0) > 0)).toBe(true);
  expect(events().some((event) => event.t === 'section_time')).toBe(true);
});

test('everything sent passes the rules of the server', async ({ page }) => {
  const { batches, events, hide } = await open(page, '/ru/');
  await page.evaluate(() =>
    document.addEventListener('click', (event) => event.preventDefault(), true),
  );
  await page.locator('[data-track="projects-show-all"]').click();
  await page.locator('[data-component="project-card"] a[data-track]').first().click();
  await page.evaluate(() =>
    document.dispatchEvent(new CustomEvent('krokosha:egg', { detail: 'konami code!' })),
  );
  await hide();
  await expect.poll(() => events().length).toBeGreaterThan(4);

  for (const batch of batches) {
    expect(batch.p).toMatch(/^\/[A-Za-z0-9/_.~%-]{0,199}$/);
    expect(batch.e.length).toBeLessThanOrEqual(40);
    for (const event of batch.e) {
      expect(EVENT_TYPES).toContain(event.t);
      expect(event.o).toBeGreaterThanOrEqual(0);
      if (event.x !== undefined) expect(event.x).toMatch(/^[A-Za-z0-9][A-Za-z0-9_.:-]{0,99}$/);
    }
  }
  // A stray character in an id is cleaned up instead of costing the whole batch.
  expect(events().find((event) => event.t === 'egg')?.x).toBe('konami-code-');
});

for (const [name, init] of [
  [
    'Do Not Track',
    () => Object.defineProperty(Navigator.prototype, 'doNotTrack', { get: () => '1' }),
  ],
  [
    'Global Privacy Control',
    () => Object.defineProperty(Navigator.prototype, 'globalPrivacyControl', { get: () => true }),
  ],
] as const) {
  test(`${name}: nothing is sent at all`, async ({ page }) => {
    const { batches, hide } = await open(page, '/', init);
    await page.locator('[data-section="hero"] [data-track="cta-discuss"]').click();
    await page.waitForTimeout(1500);
    await hide();
    await page.waitForTimeout(300);
    expect(batches).toEqual([]);
  });
}

test('nothing is kept in the browser', async ({ page, context }) => {
  const { batches } = await open(page, '/');
  await expect.poll(() => batches.length).toBeGreaterThan(0);
  expect(await context.cookies()).toEqual([]);
  const stored = await page.evaluate(() => ({
    local: Object.keys(localStorage),
    session: Object.keys(sessionStorage),
  }));
  expect(stored).toEqual({ local: [], session: [] });
});
