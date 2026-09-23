// What the design adds on top of the content (design/pages/*.dc.html): the skills topology, the phone
// menu, the 404 game, the easter eggs. The fixture fails every test on a console error or a CSP
// violation, so these also prove that the design's scripts live with the strict policy.
import { expect, test } from './fixtures.ts';

test.describe('skills topology', () => {
  test('a rack opens with its units and live lines; a search finds a skill', async ({ page }) => {
    await page.goto('/');
    const rack = page.locator('[data-rack]').first();
    const head = rack.locator('button[aria-expanded]');
    await expect(head).toHaveAttribute('aria-expanded', 'false');
    await head.scrollIntoViewIfNeeded();
    // One line from the core to every rack.
    const racks = await page.locator('[data-rack]').count();
    await expect(page.locator('[data-topology-lines] path')).toHaveCount(racks);

    await head.click();
    await expect(head).toHaveAttribute('aria-expanded', 'true');
    const unit = rack.locator('[data-level="group"]').first();
    await expect(unit).toBeVisible();
    // The open rack gets lines to its units, drawn in the accent colour.
    await expect(page.locator('[data-topology-lines] path.live').first()).toBeAttached();

    const search = page.locator('[data-skills-query]');
    const skill =
      (await rack.locator('[data-level="skill"] [data-field="title"]').first().textContent()) ?? '';
    await search.fill(skill.slice(0, 4));
    await expect(rack.locator('[data-level="skill"].is-hit').first()).toBeVisible();
    await search.fill('zzzz-nothing-like-this');
    await expect(page.locator('[data-skills-empty]')).toBeVisible();
    await search.fill('');
    await expect(page.locator('[data-skills-empty]')).toBeHidden();
  });
});

test.describe('phone menu', () => {
  test('opens, leads to a section and closes', async ({ page, isMobile }) => {
    test.skip(!isMobile, 'the menu button is for narrow screens');
    await page.goto('/uk/');
    const menu = page.locator('#site-menu');
    await expect(menu).toBeHidden();
    await page.locator('[popovertarget="site-menu"]').click();
    await expect(menu).toBeVisible();
    await menu.locator('a[href="#skills"]').click();
    await expect(menu).toBeHidden();
    await expect(page).toHaveURL(/#skills$/);
    await expect(page.locator('#skills')).toBeInViewport();
  });

  test('is not there on wide screens, where the navigation is', async ({ page, isMobile }) => {
    test.skip(isMobile, 'wide screens only');
    await page.goto('/');
    await expect(page.locator('[popovertarget="site-menu"]')).toBeHidden();
    await expect(page.locator('.site-header [data-slot="site.nav"]')).toBeVisible();
  });
});

test('the lost packet can be found on the 404 page', async ({ page }) => {
  await page.goto('/ru/404/');
  const ports = page.locator('[data-port]');
  await expect(ports).toHaveCount(24);
  const log = page.locator('[data-game-log]');
  for (let i = 0; i < 24; i++) {
    const port = ports.nth(i);
    if (await port.isDisabled()) continue;
    await port.click();
    if ((await page.locator('[data-game-status]').textContent()) === 'LINK UP') break;
  }
  await expect(page.locator('[data-game-status]')).toHaveText('LINK UP');
  await expect(log).toHaveText(/пакет найден/);
  // The find is an achievement, like the eggs of the home page.
  const banner = page.locator('#kro-achievement');
  await expect(banner).toContainText('Пакет найден');
  await expect(banner).toContainText('1/8');
  expect(await page.evaluate(() => localStorage.getItem('krokosha:eggs'))).toContain('lost_packet');
});

test('the last egg found brings the golden root@krokosha', async ({ page }) => {
  await page.goto('/');
  await page.evaluate(() =>
    localStorage.setItem(
      'krokosha:eggs',
      JSON.stringify(['konami', 'sudo', 'croc', 'cat', 'reboot', 'croc5', 'lost_packet']),
    ),
  );
  await page.reload();
  await page.waitForFunction(() => 'krokosha' in window);
  await page.locator('body').click({ position: { x: 5, y: 200 } });
  await page.evaluate(() =>
    (window as unknown as { krokosha: { hello(): string } }).krokosha.hello(),
  );
  const banner = page.locator('#kro-achievement');
  await expect(banner).toContainText('Almost a colleague');
  await expect(banner).toContainText('8/8');
  // A click puts the banner away, as in Steam; the next one in the queue comes.
  await banner.locator(':scope > div').click();
  await expect(banner).toContainText('root@krokosha');
  await expect(banner).toContainText('Every easter egg found');
  await expect(page.locator('[data-eggs-caption]').first()).toHaveText('eggs 8/8');
});

test('typing sudo opens the terminal, which answers in the language of the page', async ({
  page,
}) => {
  await page.goto('/uk/');
  // The eggs arrive a moment after the page has loaded.
  await page.waitForFunction(() => 'krokosha' in window);
  await page.locator('body').click({ position: { x: 5, y: 200 } });
  await page.keyboard.type('sudo');
  const terminal = page.locator('#kro-term');
  await expect(terminal).toBeVisible();
  // The egg is an achievement: the Steam-style banner in the corner, in the language of the page.
  await expect(page.locator('#kro-achievement')).toContainText('Суперкористувач');
  await terminal.locator('input').fill('whoami');
  await terminal.locator('input').press('Enter');
  await expect(terminal.locator('pre')).toContainText('root тут — Денис');
  await expect(page.locator('[data-eggs-caption]').first()).toHaveText(/^eggs 1\/\d+$/);
});
