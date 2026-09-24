import { test as base, expect } from '@playwright/test';

export const LOCALES = [
  { code: 'en', label: 'EN', home: '/' },
  { code: 'uk', label: 'UA', home: '/uk/' },
  { code: 'ru', label: 'RU', home: '/ru/' },
] as const;

/**
 * Every test fails on a browser-side problem it did not expect: a console error, an uncaught
 * exception, a failed request or — most importantly — a Content-Security-Policy violation,
 * which shows up nowhere else.
 */
export const test = base.extend<{ problems: string[] }>({
  problems: [
    async ({ page }, use) => {
      const problems: string[] = [];
      page.on('console', (message) => {
        if (message.type() === 'error') problems.push(`console: ${message.text()}`);
      });
      page.on('pageerror', (error) => problems.push(`exception: ${error.message}`));
      page.on('requestfailed', (request) => {
        const reason = request.failure()?.errorText;
        // A test that moves on to the next page cancels what the last one was still loading (the
        // eggs arrive a moment after the load): the browser's doing, not a failure of the site. A
        // blocked or broken request fails otherwise — ERR_BLOCKED_BY_CSP, a network error, a 404.
        if (reason === 'net::ERR_ABORTED') return;
        problems.push(`request failed: ${request.url()} (${reason})`);
      });
      page.on('response', (response) => {
        if (response.status() >= 400) problems.push(`HTTP ${response.status()}: ${response.url()}`);
      });
      await page.addInitScript(() => {
        document.addEventListener('securitypolicyviolation', (event) => {
          console.error(`CSP violation: ${event.violatedDirective} blocked ${event.blockedURI}`);
        });
      });
      // The form asks what discount a request would get as soon as it is touched; there is no API
      // behind `astro preview`, so here the answer is a first request's. Tests may answer otherwise.
      // The eggs ask how rare each achievement is (scripts/achievements.ts): too few players yet.
      await page.route(
        (url) => url.pathname === '/api/eggs',
        (route) => route.fulfill({ json: { updated: '2026-09-24T08:00:00Z', eggs: {} } }),
      );
      await page.route('**/api/leads/offer', (route) =>
        route.fulfill({
          json: { ok: true, enabled: true, signed_in: false, percent: 10, reason: 'welcome' },
        }),
      );
      await use(problems);
      expect(problems, 'browser-side problems').toEqual([]);
    },
    { auto: true },
  ],
});

export { expect };
