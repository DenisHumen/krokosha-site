import { describe, expect, it } from 'vitest';
import {
  dateInTimeZone,
  experienceYears,
  fullYears,
  parsePlainDate,
} from '../../src/lib/experience.ts';

const date = (iso: string) => parsePlainDate(iso);

describe('fullYears', () => {
  it('changes exactly on the anniversary (brief B4 boundary dates)', () => {
    const start = date('2017-09-18');
    expect(fullYears(start, date('2026-09-17'))).toBe(8);
    expect(fullYears(start, date('2026-09-18'))).toBe(9);
    expect(fullYears(start, date('2026-09-19'))).toBe(9);
    expect(fullYears(start, date('2027-09-17'))).toBe(9);
    expect(fullYears(start, date('2027-09-18'))).toBe(10);
  });

  it('counts months before and after the anniversary month', () => {
    const start = date('2017-09-18');
    expect(fullYears(start, date('2026-08-31'))).toBe(8);
    expect(fullYears(start, date('2026-10-01'))).toBe(9);
    expect(fullYears(start, date('2026-01-01'))).toBe(8);
    expect(fullYears(start, date('2026-12-31'))).toBe(9);
  });

  it('is 0 in the first year and never negative', () => {
    const start = date('2017-09-18');
    expect(fullYears(start, date('2017-09-18'))).toBe(0);
    expect(fullYears(start, date('2018-09-17'))).toBe(0);
    expect(fullYears(start, date('2018-09-18'))).toBe(1);
    expect(fullYears(start, date('2016-01-01'))).toBe(0);
  });

  it('handles a career that started on 29 February', () => {
    const start = date('2016-02-29');
    expect(fullYears(start, date('2025-02-28'))).toBe(8);
    expect(fullYears(start, date('2025-03-01'))).toBe(9);
    expect(fullYears(start, date('2028-02-28'))).toBe(11);
    expect(fullYears(start, date('2028-02-29'))).toBe(12);
  });
});

describe('parsePlainDate', () => {
  it('rejects malformed and impossible dates', () => {
    expect(() => parsePlainDate('18.09.2017')).toThrow();
    expect(() => parsePlainDate('2017-9-18')).toThrow();
    expect(() => parsePlainDate('2017-02-30')).toThrow();
    expect(() => parsePlainDate('2017-13-01')).toThrow();
  });
});

describe('experienceYears', () => {
  it('uses the calendar date in the owner time zone, not the server one', () => {
    // 21:30 UTC on 17 September is already 00:30 on 18 September in Kyiv (UTC+3 in summer).
    const lateEvening = new Date('2026-09-17T21:30:00Z');
    expect(dateInTimeZone(lateEvening, 'Europe/Kyiv')).toEqual({ year: 2026, month: 9, day: 18 });
    expect(experienceYears('2017-09-18', lateEvening, 'Europe/Kyiv')).toBe(9);
    expect(experienceYears('2017-09-18', lateEvening, 'UTC')).toBe(8);
  });
});
