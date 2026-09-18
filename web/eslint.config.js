import js from '@eslint/js';
import { defineConfig, globalIgnores } from 'eslint/config';
import astro from 'eslint-plugin-astro';
import globals from 'globals';
import tseslint from 'typescript-eslint';

export default defineConfig([
  globalIgnores(['dist/', '.astro/', 'node_modules/']),
  js.configs.recommended,
  tseslint.configs.recommended,
  astro.configs['flat/recommended'],
  astro.configs['flat/jsx-a11y-recommended'],
  {
    languageOptions: { globals: { ...globals.node } },
  },
  {
    // Code that runs in the browser.
    files: ['public/**/*.js', 'src/components/**/*.astro/*.ts', 'src/scripts/**'],
    languageOptions: { globals: { ...globals.browser } },
  },
  {
    // Tests poke at cloned content to break it on purpose.
    files: ['tests/**'],
    rules: {
      '@typescript-eslint/no-explicit-any': 'off',
      '@typescript-eslint/no-non-null-assertion': 'off',
    },
  },
]);
