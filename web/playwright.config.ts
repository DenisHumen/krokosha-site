import { defineConfig, devices } from '@playwright/test';

// End-to-end tests run against the production build (`astro preview`), because that is the only place
// where the Content-Security-Policy is active.
//
// Locally the installed Chrome is used, so no browser download is needed.
// CI installs Playwright's own Chromium.
const isCI = Boolean(process.env.CI);
// Astro runs one preview server per project, so tests share the default port with `npm run preview`.
const PORT = 4321;

export default defineConfig({
  testDir: 'tests/e2e',
  fullyParallel: true,
  forbidOnly: isCI,
  retries: isCI ? 1 : 0,
  reporter: isCI ? [['github'], ['html', { open: 'never' }]] : 'list',
  use: {
    baseURL: `http://localhost:${PORT}`,
    trace: 'retain-on-failure',
    ...(isCI ? {} : { channel: 'chrome' }),
  },
  projects: [
    { name: 'desktop', use: { ...devices['Desktop Chrome'] } },
    {
      name: 'phone',
      // 360 px — the narrowest width the design must support (brief A11).
      use: { ...devices['Pixel 7'], viewport: { width: 360, height: 780 } },
    },
  ],
  webServer: {
    command: `npm run preview -- --port ${PORT}`,
    url: `http://localhost:${PORT}/`,
    reuseExistingServer: !isCI,
    timeout: 60_000,
    // Astro sends `preview` to the background when it thinks an AI agent runs it, and Playwright
    // would take the exit for a crash: the variable keeps the server in the foreground.
    env: { ASTRO_PREVIEW_BACKGROUND: '0' },
  },
});
