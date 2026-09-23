// The map of the internet (/map) in a real browser. What the API answers is tested in Go
// (api/internal/netmap); here the page gets the Go test world as its overview — the file
// TestOverviewGolden writes — and answers made up to match that world.
import { readFileSync } from 'node:fs';
import AxeBuilder from '@axe-core/playwright';
import type { Page } from '@playwright/test';
import { expect, LOCALES, test } from './fixtures.ts';

const OVERVIEW = readFileSync(new URL('../fixtures/netmap-overview.bin', import.meta.url));

const VISITOR = {
  ip: '203.0.113.9',
  asn: 6000,
  name: 'SIX',
  country: 'UA',
  lat: 50.45,
  lon: 30.52,
};

const ROUTE = {
  from: VISITOR,
  to: {
    ip: '1.1.1.1',
    asn: 7000,
    name: 'Seven',
    country: 'US',
    lat: 40.71,
    lon: -74,
    anycast: true,
  },
  rtt: 91.2,
  kind: 'inferred',
  built: '2026-09-23T03:00:00Z',
  hops: [
    {
      asn: 6000,
      name: 'SIX',
      country: 'UA',
      known: true,
      meet: { kind: 'guess', lat: 50.45, lon: 30.52, speeds: [0, 0], ports: [0, 0], rtt: 0.4 },
    },
    {
      asn: 3000,
      name: 'Three',
      country: 'DE',
      rel: 'customer',
      known: true,
      meet: {
        kind: 'ix',
        name: 'TEST-IX',
        city: 'Frankfurt',
        country: 'DE',
        lat: 50.11,
        lon: 8.68,
        speeds: [200000, 10000],
        ports: [2, 1],
        rtt: 20.3,
      },
    },
    {
      asn: 1000,
      name: 'One',
      country: 'US',
      rel: 'customer',
      known: true,
      meet: {
        kind: 'facility',
        name: 'Test Facility',
        city: 'New York',
        country: 'US',
        lat: 40.71,
        lon: -74,
        speeds: [0, 0],
        ports: [0, 0],
        rtt: 90.1,
      },
    },
    { asn: 7000, name: 'Seven', country: 'US', rel: 'provider', known: true },
  ],
};

/** The data and the API of the map; returns the queries of the routes asked. */
async function mockMap(
  page: Page,
  answer: { status: number; json: unknown } = { status: 200, json: ROUTE },
): Promise<string[]> {
  const asked: string[] = [];
  await page.route('**/netmap/data/overview.json', (route) =>
    route.fulfill({
      json: {
        file: 'overview-test.bin',
        version: 1,
        built: '2026-09-23T03:00:00Z',
        networks: 13,
        links: 15,
        exchanges: 1,
        bytes: OVERVIEW.length,
      },
    }),
  );
  await page.route('**/netmap/data/overview-test.bin', (route) =>
    route.fulfill({ body: OVERVIEW, contentType: 'application/octet-stream' }),
  );
  await page.route('**/api/net/me', (route) => route.fulfill({ json: VISITOR }));
  await page.route(
    (url) => url.pathname === '/api/net/route',
    (route) => {
      asked.push(new URL(route.request().url()).search);
      return route.fulfill(answer);
    },
  );
  return asked;
}

/** Tests that make the API fail on purpose take those failures off the list of surprises. */
function expectProblems(problems: string[], pattern: RegExp) {
  const expected = problems.filter((problem) => pattern.test(problem));
  expect(expected.length, `expected a problem like ${pattern}`).toBeGreaterThan(0);
  for (const problem of expected) problems.splice(problems.indexOf(problem), 1);
}

// «Rebuilt September 23» must not depend on where the tests run.
test.use({ timezoneId: 'UTC' });

test.describe('the map of the internet', () => {
  test('counts the networks and offers the visitor their own address', async ({ page }) => {
    const asked = await mockMap(page);
    await page.goto('/map/');
    await expect(page.locator('[data-netmap]')).toHaveClass(/is-ready/);
    // Drawn, not the words for a browser without WebGL.
    await expect(page.locator('[data-map-stage]')).not.toHaveClass(/is-flat/);
    await expect(page.locator('[data-map-status]')).toBeEmpty();
    await expect(page.locator('[data-stat="networks"]')).toHaveText('13');
    await expect(page.locator('[data-stat="links"]')).toHaveText('15');
    await expect(page.locator('[data-stat="exchanges"]')).toHaveText('1');
    await expect(page.locator('[data-stat="updated"]')).toHaveText('September 23');
    await expect(page.getByLabel('From', { exact: true })).toHaveValue(VISITOR.ip);
    await expect(page.locator('[data-route-mine]')).toHaveText(
      'your address · 203.0.113.9 · AS6000 · SIX',
    );
    expect(asked).toEqual([]);
  });

  test('traces a route: the table, the anycast note and an address to share', async ({ page }) => {
    const asked = await mockMap(page);
    await page.goto('/map/');
    await expect(page.getByLabel('From', { exact: true })).toHaveValue(VISITOR.ip);
    await page.getByRole('button', { name: /Cloudflare DNS/ }).click();

    const result = page.locator('[data-route-result]');
    await expect(result).toBeVisible();
    expect(asked).toEqual(['?from=203.0.113.9&to=1.1.1.1']);
    await expect(result.locator('.route-summary')).toHaveText(
      '≈ 91 ms there and back · 4 networks',
    );
    const rows = result.locator('tbody tr');
    await expect(rows).toHaveCount(4);
    await expect(rows.nth(0).locator('.hop-relation')).toHaveText('start');
    await expect(rows.nth(0).locator('.hop-delay')).toHaveText('≈ 0.4 ms');
    await expect(rows.nth(1).locator('.hop-network')).toContainText('AS3000');
    await expect(rows.nth(1).locator('.hop-relation')).toHaveText('buys transit');
    await expect(rows.nth(1).locator('.hop-meeting')).toContainText('TEST-IX · Frankfurt, DE');
    await expect(rows.nth(1).locator('.hop-ports')).toHaveText('2×100G ↔ 10G');
    await expect(rows.nth(1).locator('.hop-delay')).toHaveText('≈ 20 ms');
    await expect(rows.nth(3).locator('.hop-meeting')).toHaveText('—');
    await expect(rows.nth(3).locator('.hop-loss')).toHaveText('not measured');
    await expect(result).toContainText('1.1.1.1 is anycast');
    // The address of the page keeps the route, with «me» in place of the visitor's address.
    await expect(page).toHaveURL(/\/map\/\?from=me&to=1\.1\.1\.1$/);
  });

  test('a shared address traces its route on arrival', async ({ page }) => {
    const asked = await mockMap(page);
    await page.goto('/map/?from=me&to=8.8.8.8');
    await expect(page.locator('[data-route-result] tbody tr')).toHaveCount(4);
    expect(asked).toEqual(['?from=203.0.113.9&to=8.8.8.8']);
    await expect(page.getByLabel('To', { exact: true })).toHaveValue('8.8.8.8');
  });

  test('says in words what went wrong', async ({ page, problems }) => {
    await mockMap(page, { status: 400, json: { error: 'private_address' } });
    await page.goto('/map/');
    await page.getByLabel('To', { exact: true }).fill('10.0.0.1');
    await page.getByRole('button', { name: 'Trace', exact: true }).click();
    await expect(page.locator('[data-map-status]')).toHaveText(/not on the internet/);
    await expect(page.locator('[data-route-result]')).toBeHidden();
    expectProblems(problems, /400/);
  });

  test('switches between the map, the core and the globe', async ({ page }) => {
    await mockMap(page);
    await page.goto('/map/');
    await expect(page.locator('[data-netmap]')).toHaveClass(/is-ready/);
    const views = page.getByRole('group', { name: 'View' });
    const button = (name: string) => views.getByRole('button', { name, exact: true });
    await expect(button('Map')).toHaveAttribute('aria-pressed', 'true');
    await button('Core').click();
    await expect(button('Core')).toHaveAttribute('aria-pressed', 'true');
    await expect(button('Map')).toHaveAttribute('aria-pressed', 'false');
    await expect(page.locator('[data-view-hint]')).toBeVisible();
    await button('Globe').click();
    await expect(button('Globe')).toHaveAttribute('aria-pressed', 'true');
    await expect(page.locator('[data-view-hint]')).toBeHidden();
  });

  test('without WebGL: the reason instead of the map, and routes still work', async ({ page }) => {
    await page.addInitScript(() => {
      const getContext = HTMLCanvasElement.prototype.getContext;
      HTMLCanvasElement.prototype.getContext = function (
        this: HTMLCanvasElement,
        ...args: unknown[]
      ) {
        if (String(args[0]).startsWith('webgl')) return null;
        return (getContext as (...a: unknown[]) => RenderingContext | null).apply(this, args);
      } as typeof getContext;
    });
    await mockMap(page);
    await page.goto('/map/');
    await expect(page.locator('[data-netmap]')).toHaveClass(/is-ready/);
    await expect(page.locator('[data-map-stage]')).toHaveClass(/is-flat/);
    await expect(page.locator('[data-map-status]')).toHaveText(/WebGL is off/);
    await expect(page.locator('[data-stat="networks"]')).toHaveText('13');
    await page.getByLabel('To', { exact: true }).fill('1.1.1.1');
    await page.getByRole('button', { name: 'Trace', exact: true }).click();
    await expect(page.locator('[data-route-result] tbody tr')).toHaveCount(4);
  });

  test('before the first map is built: says so', async ({ page, problems }) => {
    await mockMap(page);
    await page.route('**/netmap/data/overview.json', (route) => route.fulfill({ status: 404 }));
    await page.goto('/map/');
    await expect(page.locator('[data-map-status]')).toHaveText('The map data is not ready yet.');
    expectProblems(problems, /404/);
  });

  test('speaks the language of its address', async ({ page }) => {
    await mockMap(page);
    await page.goto('/uk/map/?from=me&to=1.1.1.1');
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Карта інтернету');
    await expect(page.locator('[data-route-mine]')).toContainText('ваша адреса');
    await expect(page.locator('.route-summary')).toContainText('4 мережі');
  });

  for (const { home } of LOCALES) {
    test(`the menu of ${home} leads to the map`, async ({ page }) => {
      await page.goto(home);
      await expect(page.locator(`a[href="${home}map/"]`).first()).toBeAttached();
    });
  }

  for (const colorScheme of ['light', 'dark'] as const) {
    test(`no accessibility violations with a route (${colorScheme})`, async ({ page }) => {
      await mockMap(page);
      await page.emulateMedia({ colorScheme });
      await page.goto('/map/?from=me&to=1.1.1.1');
      await expect(page.locator('[data-route-result] tbody tr')).toHaveCount(4);
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
      const summary = results.violations.map(
        (violation) =>
          `${violation.id} (${violation.impact}): ${violation.nodes.map((node) => node.target.join(' ')).join(', ')}`,
      );
      expect(summary).toEqual([]);
    });
  }
});
