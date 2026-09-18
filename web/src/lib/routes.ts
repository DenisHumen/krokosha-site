// Every page exists in all languages: en in the root (/), uk in /uk/, ru in /ru/.
// Pages live in src/pages/[...lang]/ and share this getStaticPaths().

import { LOCALES, paramFromLocale } from '../i18n/locales.ts';

export function localeStaticPaths() {
  return LOCALES.map((lang) => ({ params: { lang: paramFromLocale(lang) } }));
}
