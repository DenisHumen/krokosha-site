// The achievements of the easter eggs, on the server's side (api/internal/achievements, contract §8).
// Every find is reported and comes back as a signed receipt, kept in this browser; the receipts of all
// eight buy the receipt of «all», which the contact form sends to claim the eggs' discount once. The
// shares of players who found each achievement make its rarity, as in Steam. A player is a browser
// that found at least one egg — it counts itself once, with its first find; a visitor who finds none
// sends nothing and keeps nothing. Nothing personal is sent: an id, and the receipts. Like the
// statistics (public/assets/analytics.js), an automated browser sends nothing at all; with «Do Not
// Track» or Global Privacy Control this browser is counted nowhere (the server skips its finds too)
// and still gets its receipts.

export const EGGS = [
  'konami',
  'sudo',
  'croc',
  'cat',
  'reboot',
  'console',
  'croc5',
  'lost_packet',
] as const;
export type Egg = (typeof EGGS)[number];
export type Achievement = Egg | 'all';

const RECEIPTS = 'krokosha:receipts'; // { konami: "konami.<time>.<mac>", …, all: "all.<time>.<span>.<mac>" }
const PLAYER = 'krokosha:player'; // this browser has been counted as a player
const SPENT = 'krokosha:eggs-spent'; // the receipt of «all» went to a request: its number
const ACCOUNT = 'krokosha:account'; // this browser was signed in to the personal account lately (a hint)

/** Automated browsers are not players: they neither count nor get receipts. */
const automated = () => navigator.webdriver === true;
const notCounted = () =>
  navigator.doNotTrack === '1' ||
  (navigator as Navigator & { globalPrivacyControl?: boolean }).globalPrivacyControl === true;

function read<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(key);
    return raw ? (JSON.parse(raw) as T) : fallback;
  } catch {
    return fallback;
  }
}

function write(key: string, value: unknown): boolean {
  try {
    if (value === null) localStorage.removeItem(key);
    else localStorage.setItem(key, JSON.stringify(value));
    return true;
  } catch {
    return false; // storage is blocked: the finds still show, they are just not kept
  }
}

export function receipts(): Partial<Record<Achievement, string>> {
  const stored = read<Record<string, unknown>>(RECEIPTS, {});
  const out: Partial<Record<Achievement, string>> = {};
  for (const id of [...EGGS, 'all'] as const) {
    const value = stored[id];
    if (typeof value === 'string' && value.startsWith(`${id}.`) && value.length <= 80)
      out[id] = value;
  }
  return out;
}

/** The receipt of every egg, or '' — what the contact form sends as «eggs». */
export function everyEgg(): string {
  return receipts().all ?? '';
}

/** When an achievement was found, as its receipt says (the second part is Unix seconds, base 36). */
export function foundAt(receipt: string | undefined): Date | null {
  const seconds = Number.parseInt(receipt?.split('.')[1] ?? '', 36);
  return Number.isFinite(seconds) && seconds > 0 ? new Date(seconds * 1000) : null;
}

export function spentOn(): string {
  return read<string>(SPENT, '');
}

/** The contact form tells which request took the eggs' discount: the panel then says so. */
export function markSpent(number: string): void {
  write(SPENT, number);
}

export function accountHint(signedIn: boolean): void {
  write(ACCOUNT, signedIn ? 1 : null);
}

async function post(path: string, body?: unknown): Promise<Response> {
  return fetch(path, {
    method: 'POST',
    headers: {
      Accept: 'application/json',
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
    },
    body: body === undefined ? null : JSON.stringify(body),
    credentials: 'same-origin',
  });
}

// One request at a time: two finds a moment apart must not overwrite each other's receipt.
let queue: Promise<unknown> = Promise.resolve();
function serial<T>(work: () => Promise<T>): Promise<T> {
  const next = queue.then(work, work);
  queue = next.catch(() => undefined);
  return next;
}

/** Counts this browser as a player, once: the shares are of players. Runs inside serial(). */
async function hello(): Promise<void> {
  if (notCounted() || read(PLAYER, 0)) return;
  // Remembered first: a browser that cannot remember would count itself on every find.
  if (!write(PLAYER, Date.now())) return;
  try {
    const response = await post('/api/eggs/hello');
    if (!response.ok) write(PLAYER, null);
  } catch {
    write(PLAYER, null);
  }
}

async function claim(id: Achievement, body?: unknown): Promise<string> {
  try {
    const response = await post(`/api/eggs/${id}`, body);
    if (!response.ok) return '';
    const answer = (await response.json()) as { receipt?: unknown };
    return typeof answer.receipt === 'string' ? answer.receipt : '';
  } catch {
    return '';
  }
}

/** Reports a find and keeps its receipt; with the eighth egg comes the receipt of «all». */
export function report(id: Egg): Promise<void> {
  return serial(async () => {
    if (automated()) return;
    await hello();
    const list = receipts();
    if (!list[id]) {
      const receipt = await claim(id);
      if (!receipt) return;
      list[id] = receipt;
      write(RECEIPTS, list);
    }
    if (!list.all && EGGS.every((egg) => list[egg])) {
      const all = await claim('all', { receipts: EGGS.map((egg) => list[egg]) });
      if (!all) return;
      list.all = all;
      write(RECEIPTS, list);
      document.dispatchEvent(new CustomEvent('kro:eggs-complete'));
    }
    if (read(ACCOUNT, 0)) await syncAccount(list);
  });
}

/** Eggs found before the site counted them (or while it could not be reached): receipts, once. */
export async function backfill(found: readonly string[]): Promise<void> {
  const known = receipts();
  for (const id of EGGS) {
    if (found.includes(id) && !known[id]) await report(id);
  }
}

/** Brings this browser's finds into the personal account: it keeps them across devices. */
async function syncAccount(list: Partial<Record<Achievement, string>>): Promise<void> {
  const eggs = EGGS.flatMap((egg) => (list[egg] ? [list[egg]] : []));
  if (eggs.length === 0) return;
  try {
    const me = await fetch('/api/account/me', {
      headers: { Accept: 'application/json' },
      credentials: 'same-origin',
    });
    const { ok, csrf } = (await me.json()) as { ok?: boolean; csrf?: string };
    if (!ok || !csrf) {
      accountHint(false); // signed out since: the hint is gone until the next sign-in
      return;
    }
    await fetch('/api/account/eggs', {
      method: 'POST',
      headers: {
        Accept: 'application/json',
        'Content-Type': 'application/json',
        'X-CSRF-Token': csrf,
      },
      body: JSON.stringify({ receipts: eggs }),
      credentials: 'same-origin',
    });
  } catch {
    // The account catches up the next time its page is opened.
  }
}

/** The share of players who found each achievement, in percent; empty while too few are counted. */
let shares: Promise<Partial<Record<Achievement, number>>> | null = null;
export function rarity(): Promise<Partial<Record<Achievement, number>>> {
  if (automated()) return Promise.resolve({});
  shares ??= fetch('/api/eggs', { headers: { Accept: 'application/json' } })
    .then((response) => (response.ok ? response.json() : {}))
    .then((answer: { eggs?: Record<string, unknown> }) => {
      const out: Partial<Record<Achievement, number>> = {};
      for (const [id, value] of Object.entries(answer.eggs ?? {})) {
        if (typeof value === 'number' && value >= 0 && value <= 100) out[id as Achievement] = value;
      }
      return out;
    })
    .catch(() => ({}));
  return shares;
}

/** «3.4 %», «<0.1 %», in the page's language. */
export function formatShare(share: number, lang: string): string {
  const format = new Intl.NumberFormat(lang, {
    style: 'percent',
    maximumFractionDigits: share < 10 ? 1 : 0,
  });
  return share > 0 && share < 0.1 ? `<${format.format(0.001)}` : format.format(share / 100);
}

/** Steam: an achievement fewer than 10 % of players have is rare. */
export const RARE_BELOW = 10;
