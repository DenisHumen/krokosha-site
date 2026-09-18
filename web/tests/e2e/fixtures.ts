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
      page.on('requestfailed', (request) =>
        problems.push(`request failed: ${request.url()} (${request.failure()?.errorText})`),
      );
      page.on('response', (response) => {
        if (response.status() >= 400) problems.push(`HTTP ${response.status()}: ${response.url()}`);
      });
      await page.addInitScript(() => {
        document.addEventListener('securitypolicyviolation', (event) => {
          console.error(`CSP violation: ${event.violatedDirective} blocked ${event.blockedURI}`);
        });
      });
      await use(problems);
      expect(problems, 'browser-side problems').toEqual([]);
    },
    { auto: true },
  ],
});

export { expect };
