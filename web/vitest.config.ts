import { defineConfig } from 'vitest/config';

// Unit tests cover the framework-free library code (src/lib, src/i18n); pages are checked
// after the build by scripts/check-dist.mjs.
export default defineConfig({
  test: {
    include: ['tests/unit/**/*.test.ts'],
    environment: 'node',
  },
});
