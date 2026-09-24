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

test.describe('achievements on the site', () => {
  const SHARES = { konami: 24, sudo: 16, croc: 12, cat: 8, reboot: 6, console: 36, croc5: 3.2 };

  test('a find is reported, comes back as a receipt and shows how rare it is', async ({ page }) => {
    // A browser of a person: automated ones are not players (scripts/achievements.ts).
    await page.addInitScript(() =>
      Object.defineProperty(Navigator.prototype, 'webdriver', { get: () => false }),
    );
    const posted: string[] = [];
    // A person's browser also sends the statistics of the page.
    await page.route('**/api/e', (route) => route.fulfill({ status: 204 }));
    await page.route('**/api/eggs', (route) =>
      route.fulfill({ json: { updated: '2026-09-24T08:00:00Z', eggs: SHARES } }),
    );
    await page.route('**/api/eggs/*', (route) => {
      const id = new URL(route.request().url()).pathname.split('/').pop() ?? '';
      posted.push(id);
      if (id === 'hello') return route.fulfill({ json: {} });
      return route.fulfill({ json: { receipt: `${id}.tlv0aa.c2lnbmVkLWJ5LXNlcnZlcg` } });
    });
    await page.goto('/');
    await page.waitForFunction(() => 'krokosha' in window);
    // Nobody is a player before the first find: nothing is sent, nothing is kept.
    await page.waitForTimeout(300);
    expect(posted).toEqual([]);
    expect(await page.evaluate(() => Object.keys(localStorage))).toEqual([]);
    await page.locator('body').click({ position: { x: 5, y: 200 } });
    await page.evaluate(() =>
      (window as unknown as { krokosha: { hello(): string } }).krokosha.hello(),
    );
    // The banner says how many players have it, as Steam does.
    const banner = page.locator('#kro-achievement');
    await expect(banner).toContainText('Almost a colleague');
    await expect(banner).toContainText('36% of players have it');
    await expect
      .poll(() => page.evaluate(() => localStorage.getItem('krokosha:receipts')))
      .toContain('"console":"console.tlv0aa.');
    // The first find counts the player, once.
    expect(posted).toEqual(['hello', 'console']);

    // The counter opens the panel: what is found, when, and the share of each.
    const caption = page.locator('footer [data-eggs-caption]');
    await caption.scrollIntoViewIfNeeded();
    await expect(caption).toHaveText('eggs 1/8');
    await expect(caption).toHaveAttribute('aria-label', 'Achievements: 1 of 8');
    await caption.click();
    const panel = page.getByRole('dialog', { name: 'Achievements' });
    await expect(panel).toBeVisible();
    await expect(panel.locator('[data-count]')).toHaveText('1 of 8');
    const found = panel.locator('[data-achievement="console"]');
    await expect(found).toHaveClass(/is-found/);
    await expect(found.locator('[data-share]')).toHaveText('36%');
    await expect(found.locator('[data-text]')).toBeVisible();
    // Still to find: the name, not the answer; a rare one is marked when found.
    const locked = panel.locator('[data-achievement="croc5"]');
    await expect(locked).not.toHaveClass(/is-found/);
    await expect(locked.locator('[data-text]')).toBeHidden();
    await expect(locked.locator('[data-state]')).toHaveText('Not found yet');
    await expect(locked.locator('[data-share]')).toHaveText('3.2%');
    await expect(panel.locator('[data-discount]')).toHaveText(
      'Find every egg — and get 20% off one request.',
    );
    await page.keyboard.press('Escape');
    await expect(panel).toBeHidden();
  });

  test('an automated browser sends nothing about the eggs', async ({ page }) => {
    const asked: string[] = [];
    page.on('request', (request) => {
      if (new URL(request.url()).pathname.startsWith('/api/eggs')) asked.push(request.url());
    });
    await page.goto('/');
    await page.waitForFunction(() => 'krokosha' in window);
    await page.evaluate(() =>
      (window as unknown as { krokosha: { hello(): string } }).krokosha.hello(),
    );
    await expect(page.locator('#kro-achievement')).toContainText('Almost a colleague');
    await page.waitForTimeout(500);
    expect(asked).toEqual([]);
  });
});
