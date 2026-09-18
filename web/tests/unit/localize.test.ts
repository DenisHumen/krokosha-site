import { describe, expect, it } from 'vitest';
import {
  fill,
  findIncompleteTranslations,
  isLocalizedDict,
  loc,
  plural,
} from '../../src/i18n/localize.ts';
import { localeFromParam, localePath, paramFromLocale } from '../../src/i18n/locales.ts';

describe('loc', () => {
  it('resolves dictionaries at any depth and keeps plain values', () => {
    const source = {
      name: { en: 'Denis', uk: 'Денис', ru: 'Денис' },
      nickname: 'Krokosha',
      nested: [{ label: { en: 'Email', uk: 'Пошта', ru: 'Почта' }, primary: true }],
      note: { en: null, uk: 'примітка', ru: 'примечание' },
    };
    expect(loc(source, 'uk')).toEqual({
      name: 'Денис',
      nickname: 'Krokosha',
      nested: [{ label: 'Пошта', primary: true }],
      note: 'примітка',
    });
    expect(loc(source, 'en').note).toBeNull();
  });

  it('treats only an exact { en, uk, ru } key set as a dictionary', () => {
    expect(isLocalizedDict({ en: 'a', uk: 'b', ru: 'c' })).toBe(true);
    expect(isLocalizedDict({ en: 'a', uk: 'b' })).toBe(false);
    expect(isLocalizedDict({ en: 'a', uk: 'b', ru: 'c', de: 'd' })).toBe(false);
    expect(isLocalizedDict(['en', 'uk', 'ru'])).toBe(false);
  });
});

describe('findIncompleteTranslations', () => {
  it('reports dictionaries with a missing language', () => {
    const issues = findIncompleteTranslations({
      hero: { lead: { en: 'Hi', uk: 'Привіт' } },
      nav: [{ label: { en: 'A', uk: 'Б', ru: 'В' } }, { label: { ru: 'только ru' } }],
    });
    expect(issues).toHaveLength(2);
    expect(issues[0]).toContain('hero.lead');
    expect(issues[1]).toContain('nav[1].label');
  });

  it('accepts complete content', () => {
    expect(findIncompleteTranslations({ a: { en: '1', uk: '2', ru: '3' }, b: 'same' })).toEqual([]);
  });
});

describe('plural', () => {
  const years = {
    en: ['year', 'years'],
    uk: ['рік', 'роки', 'років'],
    ru: ['год', 'года', 'лет'],
  } as const;

  it.each([
    [1, 'year', 'рік', 'год'],
    [2, 'years', 'роки', 'года'],
    [4, 'years', 'роки', 'года'],
    [5, 'years', 'років', 'лет'],
    [9, 'years', 'років', 'лет'],
    [11, 'years', 'років', 'лет'],
    [12, 'years', 'років', 'лет'],
    [21, 'years', 'рік', 'год'],
    [22, 'years', 'роки', 'года'],
    [25, 'years', 'років', 'лет'],
    [111, 'years', 'років', 'лет'],
    [0, 'years', 'років', 'лет'],
  ])('%i → %s / %s / %s', (n, en, uk, ru) => {
    expect(plural(years.en, n, 'en')).toBe(en);
    expect(plural(years.uk, n, 'uk')).toBe(uk);
    expect(plural(years.ru, n, 'ru')).toBe(ru);
  });

  it('fails loudly when forms are missing', () => {
    expect(() => plural(['год'], 5, 'ru')).toThrow();
  });
});

describe('fill', () => {
  it('replaces known placeholders and leaves the rest for the browser', () => {
    expect(fill('© {year} Krokosha', { year: 2026 })).toBe('© 2026 Krokosha');
    expect(fill('Request {id} received', { year: 2026 })).toBe('Request {id} received');
  });
});

describe('locale paths', () => {
  it('serves the default language from the root and the others from a prefix', () => {
    expect(localePath('en')).toBe('/');
    expect(localePath('uk')).toBe('/uk/');
    expect(localePath('en', 'privacy')).toBe('/privacy/');
    expect(localePath('ru', '/privacy/')).toBe('/ru/privacy/');
  });

  it('maps route params both ways', () => {
    expect(localeFromParam(undefined)).toBe('en');
    expect(localeFromParam('uk')).toBe('uk');
    expect(paramFromLocale('en')).toBeUndefined();
    expect(paramFromLocale('ru')).toBe('ru');
    expect(() => localeFromParam('de')).toThrow();
    // `/en/` must not exist next to `/`: duplicate content.
    expect(() => localeFromParam('en')).toThrow();
  });
});
