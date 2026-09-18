import { expect, test } from './fixtures.ts';

const background = () => getComputedStyle(document.body).backgroundColor;

test.describe('colour theme', () => {
  test('follows the system until the visitor chooses', async ({ page }) => {
    await page.emulateMedia({ colorScheme: 'light' });
    await page.goto('/');
    const light = await page.evaluate(background);

    await page.emulateMedia({ colorScheme: 'dark' });
    const dark = await page.evaluate(background);
    expect(dark).not.toBe(light);
    await expect(page.locator('html')).not.toHaveAttribute('data-theme', /.+/);
  });

  test('the toggle switches the theme and the choice survives a reload', async ({ page }) => {
    await page.emulateMedia({ colorScheme: 'light' });
    await page.goto('/');
    const light = await page.evaluate(background);

    await page.locator('[data-theme-toggle]').click();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
    expect(await page.evaluate(background)).not.toBe(light);

    await page.reload();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');

    await page.locator('[data-theme-toggle]').click();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
    expect(await page.evaluate(background)).toBe(light);
  });

  test('the saved theme is applied before the first paint', async ({ page }) => {
    await page.addInitScript(() => localStorage.setItem('theme', 'dark'));
    await page.emulateMedia({ colorScheme: 'light' });
    // `commit` resolves before the document is parsed; by DOMContentLoaded the attribute must be set
    // by the blocking /assets/theme.js, not by a deferred module.
    await page.goto('/', { waitUntil: 'commit' });
    const themeAtDomReady = await page.evaluate(
      () =>
        new Promise<string | null>((resolve) => {
          const read = () => resolve(document.documentElement.getAttribute('data-theme'));
          if (document.readyState === 'loading') {
            document.addEventListener('DOMContentLoaded', read, { once: true });
          } else {
            read();
          }
        }),
    );
    expect(themeAtDomReady).toBe('dark');
  });
});
