// Experience «grows by itself» (brief B4): full years since `career_start`,
// recalculated on every build. The site is rebuilt every 6 hours, so the number changes on the anniversary.

export interface PlainDate {
  year: number;
  month: number; // 1–12
  day: number;
}

export function parsePlainDate(iso: string): PlainDate {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(iso);
  if (!match) throw new Error(`Expected a date in YYYY-MM-DD format, got "${iso}"`);
  const [year, month, day] = [Number(match[1]), Number(match[2]), Number(match[3])];
  const probe = new Date(Date.UTC(year, month - 1, day));
  if (
    probe.getUTCFullYear() !== year ||
    probe.getUTCMonth() !== month - 1 ||
    probe.getUTCDate() !== day
  ) {
    throw new Error(`"${iso}" is not a real calendar date`);
  }
  return { year, month, day };
}

/** Calendar date of the moment `now` in the given IANA time zone (the owner's, not the server's). */
export function dateInTimeZone(now: Date, timeZone: string): PlainDate {
  const parts = new Intl.DateTimeFormat('en-CA', {
    timeZone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).formatToParts(now);
  const get = (type: string) => Number(parts.find((part) => part.type === type)?.value);
  return { year: get('year'), month: get('month'), day: get('day') };
}

/** Number of full years between two calendar dates; 0 if `today` is before `start`. */
export function fullYears(start: PlainDate, today: PlainDate): number {
  const beforeAnniversary =
    today.month < start.month || (today.month === start.month && today.day < start.day);
  return Math.max(0, today.year - start.year - (beforeAnniversary ? 1 : 0));
}

export function experienceYears(careerStart: string, now: Date, timeZone: string): number {
  return fullYears(parsePlainDate(careerStart), dateInTimeZone(now, timeZone));
}
