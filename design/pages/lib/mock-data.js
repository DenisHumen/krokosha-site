// Loads mock/<lang>/*.json (contract v1.4) for previews. Same files the Astro build consumes.
export async function loadMock(lang = 'ru') {
  const names = ['profile', 'site', 'skills', 'projects'];
  const out = {};
  await Promise.all(names.map(async (n) => {
    const r = await fetch(`../../mock/${lang}/${n}.json`);
    out[n] = await r.json();
  }));
  return out;
}
export function fill(str, vars) {
  return String(str ?? '').replace(/\{(\w+)\}/g, (m, k) => (k in vars ? vars[k] : m));
}
export function fmtDate(iso, lang) {
  try { return new Intl.DateTimeFormat(lang, { dateStyle: 'medium', timeZone: 'UTC' }).format(new Date(iso + 'T00:00:00Z')); } catch { return iso; }
}
