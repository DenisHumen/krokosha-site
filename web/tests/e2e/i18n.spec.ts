import { expect, LOCALES, test } from './fixtures.ts';

test('language switcher keeps the visitor on the same page', async ({ page }) => {
  await page.goto('/privacy/');
  await page.locator('[data-track="lang-uk"]').click();
  await expect(page).toHaveURL(/\/uk\/privacy\/$/);
  await expect(page.locator('html')).toHaveAttribute('lang', 'uk');

  await page.locator('[data-track="lang-ru"]').click();
  await expect(page).toHaveURL(/\/ru\/privacy\/$/);

  await page.locator('[data-track="lang-en"]').click();
  await expect(page).toHaveURL(/\/privacy\/$/);
  await expect(page.locator('html')).toHaveAttribute('lang', 'en');
});

test('the current language is marked for assistive technology', async ({ page }) => {
  for (const locale of LOCALES) {
    await page.goto(locale.home);
    const current = page.locator('[data-slot="site.i18n"] a[aria-current="page"]');
    await expect(current).toHaveCount(1);
    await expect(current).toHaveText(locale.label);
  }
});

test('hreflang alternates point to pages that exist', async ({ page, request }) => {
  await page.goto('/uk/');
  const alternates = await page.locator('link[rel="alternate"][hreflang]').evaluateAll((links) =>
    links.map((link) => ({
      hreflang: link.getAttribute('hreflang'),
      path: new URL((link as HTMLLinkElement).href).pathname,
    })),
  );
  expect(alternates.map((alternate) => alternate.hreflang).sort()).toEqual(
    ['en', 'ru', 'uk', 'x-default'].sort(),
  );
  for (const { path } of alternates) {
    expect((await request.get(path)).status(), path).toBe(200);
  }
});

test('footer links work in every language', async ({ page }) => {
  for (const locale of LOCALES) {
    await page.goto(locale.home);
    await page.locator('[data-track="game-entry"]').click();
    await expect(page).toHaveURL(new RegExp(`${locale.home}play/$`));
    await expect(page.locator('h1')).toBeVisible();
    await expect(page.locator('meta[name="robots"]')).toHaveAttribute('content', /noindex/);
  }
});

test('robots.txt and sitemap.xml are served', async ({ request }) => {
  const robots = await request.get('/robots.txt');
  expect(robots.status()).toBe(200);
  expect(await robots.text()).toMatch(/^Sitemap: https:\/\/.+\/sitemap\.xml$/m);

  const sitemap = await request.get('/sitemap.xml');
  expect(sitemap.status()).toBe(200);
  const xml = await sitemap.text();
  expect(xml).toContain('<urlset');
  expect(xml).not.toContain('/play/');
  expect(xml).not.toContain('/404/');
});
