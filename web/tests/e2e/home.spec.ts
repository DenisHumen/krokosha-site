import { expect, LOCALES, test } from './fixtures.ts';

for (const locale of LOCALES) {
  test.describe(`home, ${locale.code}`, () => {
    test('renders in its language with one headline', async ({ page }) => {
      await page.goto(locale.home);
      await expect(page.locator('html')).toHaveAttribute('lang', locale.code);
      await expect(page.locator('h1')).toHaveCount(1);
      await expect(page.locator('h1')).toBeVisible();
      for (const section of ['hero', 'services', 'skills', 'projects', 'stats', 'contacts']) {
        await expect(page.locator(`[data-section="${section}"]`)).toBeVisible();
      }
    });

    test('does not scroll sideways', async ({ page }) => {
      await page.goto(locale.home);
      const overflow = await page.evaluate(
        () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
      );
      expect(overflow).toBeLessThanOrEqual(0);
    });

    test('main calls to action lead to Telegram, email and the contacts section', async ({
      page,
    }) => {
      await page.goto(locale.home);
      const hero = page.locator('[data-section="hero"]');
      await expect(hero.locator('[data-track="cta-telegram"]')).toHaveAttribute(
        'href',
        /^https:\/\/t\.me\//,
      );
      await expect(hero.locator('[data-track="cta-email"]')).toHaveAttribute('href', /^mailto:/);

      await hero.locator('[data-track="cta-discuss"]').click();
      await expect(page).toHaveURL(/#contacts$/);
      await expect(page.locator('#contacts')).toBeInViewport();
    });
  });
}

test('navigation anchors scroll to their sections', async ({ page }) => {
  await page.goto('/');
  const links = page.locator('.site-header [data-slot="site.nav"] a');
  const count = await links.count();
  expect(count).toBeGreaterThan(0);
  for (let i = 0; i < count; i++) {
    const href = await links.nth(i).getAttribute('href');
    expect(href).toMatch(/^#[a-z-]+$/);
    await expect(page.locator(href!)).toHaveCount(1);
  }
});

test('advertising landing anchors exist (brief B7)', async ({ page }) => {
  await page.goto('/#devops');
  for (const id of ['networks', 'servers', 'devops']) {
    await expect(page.locator(`#${id}`)).toHaveCount(1);
  }
  await expect(page.locator('#devops')).toBeInViewport();
});

test('every repository is listed; the compact ones open on demand', async ({ page }) => {
  await page.goto('/');
  const cards = page.locator('[data-component="project-card"]');
  expect(await cards.count()).toBeGreaterThan(6);

  const compact = page.locator('[data-component="project-card"][data-tier="compact"]').first();
  await expect(compact).toBeHidden();
  await page.locator('[data-track="projects-show-all"]').click();
  await expect(compact).toBeVisible();
});

test('nothing unconfirmed is published', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('body')).not.toContainText('TODO');
  await expect(page.locator('body')).not.toContainText(/\{[a-z_]+\}/);
});
