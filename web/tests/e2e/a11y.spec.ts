import AxeBuilder from '@axe-core/playwright';
import { expect, test } from './fixtures.ts';

// WCAG 2.1 A + AA (brief A11: contrast AA, focus styles, alt texts), in both themes.
const PAGES = ['/', '/uk/', '/ru/', '/privacy/', '/play/', '/404/'];

for (const colorScheme of ['light', 'dark'] as const) {
  for (const path of PAGES) {
    test(`no accessibility violations: ${path} (${colorScheme})`, async ({ page }) => {
      await page.emulateMedia({ colorScheme });
      await page.goto(path);
      // Closed <details> hide their content from axe; open them to check everything.
      await page.evaluate(() => {
        for (const details of document.querySelectorAll('details')) details.open = true;
      });
      const results = await new AxeBuilder({ page })
        .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'best-practice'])
        .analyze();
      const summary = results.violations.map(
        (violation) =>
          `${violation.id} (${violation.impact}): ${violation.nodes.map((node) => node.target.join(' ')).join(', ')}`,
      );
      expect(summary).toEqual([]);
    });
  }
}

test('keyboard users can skip to the content', async ({ page }) => {
  await page.goto('/');
  await page.keyboard.press('Tab');
  const skip = page.locator('.skip-link');
  await expect(skip).toBeFocused();
  await expect(skip).toBeInViewport();
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/#main$/);
});
