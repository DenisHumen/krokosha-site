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

// The way from the site's server to 1.1.1.1, as /api/net/trace measures it.
const TRACE = {
  from: { ip: '46.175.147.165', asn: 6000, name: 'SIX', country: 'UA', lat: 50.45, lon: 30.52 },
  to: { ip: '1.1.1.1', asn: 7000, name: 'Seven', country: 'US', lat: 40.71, lon: -74 },
  reached: true,
  hops: [
    {
      ttl: 1,
      ip: '46.175.147.1',
      host: 'gw.six.example',
      asn: 6000,
      name: 'SIX',
      country: 'UA',
      lat: 50.45,
      lon: 30.52,
      sent: 3,
      rtts: [0.8, 0.7, 0.9],
    },
    { ttl: 2, lat: null, lon: null, sent: 3, rtts: [] },
    {
      ttl: 3,
      ip: '80.81.192.7',
      others: ['80.81.192.8'],
      ix: 'TEST-IX',
      lat: 50.11,
      lon: 8.68,
      sent: 3,
      rtts: [21.3],
    },
    {
      ttl: 4,
      ip: '1.1.1.1',
      asn: 7000,
      name: 'Seven',
      country: 'US',
      lat: 40.71,
      lon: -74,
      sent: 3,
      rtts: [90.1, 91, 90.4],
      reached: true,
    },
  ],
  connect: { port: 443, sent: 5, rtts: [90.2, 90.4, 91, 90.3, 90.8] },
  at: '2026-09-23T17:00:00Z',
};

/** The data and the API of the map; returns the queries of the routes (and traces) asked. */
async function mockMap(
  page: Page,
  answer: { status: number; json: unknown } = { status: 200, json: ROUTE },
  traceAnswer: { status: number; json: unknown } = { status: 200, json: TRACE },
): Promise<string[]> {
  const asked: string[] = [];
  await page.route(
    (url) => url.pathname === '/api/net/trace',
    (route) => {
      asked.push(`trace${new URL(route.request().url()).search}`);
      return route.fulfill(traceAnswer);
    },
  );
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

  test("measures the way from the site's server, and shows either way on the map", async ({
    page,
  }) => {
    const asked = await mockMap(page);
    await page.goto('/map/?from=me&to=1.1.1.1');
    await expect(page.locator('[data-route-result] tbody tr')).toHaveCount(4);
    // Nothing is measured until asked: every measurement sends packets from the server.
    expect(asked.filter((query) => query.startsWith('trace'))).toEqual([]);
    await expect(page.locator('[data-show="model"]')).toBeHidden();

    await page.getByRole('button', { name: "Measure from this site's server" }).click();
    const measured = page.locator('[data-trace-result]');
    await expect(measured).toBeVisible();
    expect(asked.filter((query) => query.startsWith('trace'))).toEqual(['trace?to=1.1.1.1']);
    await expect(measured.locator('.route-summary')).toHaveText('90 ms there and back · 4 hops');
    await expect(measured.locator('.route-ends')).toHaveText('46.175.147.165 AS6000 SIX → 1.1.1.1');
    const rows = measured.locator('tbody tr');
    await expect(rows).toHaveCount(4);
    await expect(rows.nth(0)).toContainText('gw.six.example');
    await expect(rows.nth(1)).toContainText('* no answer');
    await expect(rows.nth(1).locator('.hop-answered')).toHaveText('0/3');
    await expect(rows.nth(2)).toContainText('exchange point · TEST-IX');
    await expect(rows.nth(2)).toContainText('+ 80.81.192.8');
    await expect(rows.nth(2).locator('.hop-delay')).toHaveText('21 ms');
    await expect(rows.nth(3).locator('.hop-answered')).toHaveText('3/3the address itself');
    await expect(measured).toContainText('TCP handshakes with port 443: 5 of 5, 90 ms');
    await expect(
      page.getByRole('button', { name: "Measure from this site's server" }),
    ).toBeHidden();

    // Either way can be put on the map.
    const model = page.locator('[data-show="model"]');
    const trace = page.locator('[data-show="trace"]');
    await expect(trace).toHaveAttribute('aria-pressed', 'true');
    await expect(model).toHaveAttribute('aria-pressed', 'false');
    await model.click();
    await expect(model).toHaveAttribute('aria-pressed', 'true');
    await expect(trace).toHaveAttribute('aria-pressed', 'false');

    const results = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'best-practice'])
      .analyze();
    expect(results.violations.map((violation) => violation.id)).toEqual([]);
  });

  test('a measurement that fails is said in words, and can be asked again', async ({
    page,
    problems,
  }) => {
    await mockMap(page, undefined, { status: 502, json: { error: 'trace_failed' } });
    await page.goto('/map/?from=me&to=1.1.1.1');
    const button = page.getByRole('button', { name: "Measure from this site's server" });
    await button.click();
    await expect(page.locator('[data-map-status]')).toHaveText(
      'The measurement failed — try again later.',
    );
    await expect(button).toBeEnabled();
    await expect(page.locator('[data-trace-result]')).toBeHidden();
    expectProblems(problems, /502/);
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

  test('fits the screen: nothing makes the page scroll sideways', async ({ page }) => {
    await mockMap(page);
    await page.goto('/ru/map/?from=me&to=1.1.1.1'); // the longest texts, a route and its table
    await expect(page.locator('[data-route-result] tbody tr')).toHaveCount(4);
    const [scroll, client] = await page.evaluate(() => [
      document.documentElement.scrollWidth,
      document.documentElement.clientWidth,
    ]);
    expect(scroll).toBeLessThanOrEqual(client);
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
