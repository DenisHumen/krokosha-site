// The panel of achievements (components/Achievements.astro): what this browser — and, on the personal
// account's page, the account — has found, when, and how many players have each. Loaded on the first
// click on the counter: the home page does not carry it until somebody wants to look.

import { icon } from '../../../design/components/eggs/achievement.js';
import {
  EGGS,
  RARE_BELOW,
  everyEgg,
  formatShare,
  foundAt,
  rarity,
  receipts,
  spentOn,
  type Achievement,
} from './achievements.ts';

interface PanelTexts {
  progress: string;
  locked: string;
  found_on: string;
  rare: string;
  discount: string;
  discount_ready: string;
  discount_used: string;
  rarity: string;
  percent: number;
}

/** A find the account knows of: from another device, perhaps. */
export interface AccountFind {
  id: string;
  at: string;
}

const fill = (template: string, values: Record<string, string | number>) =>
  template.replace(/\{([a-z_]+)\}/g, (match, name: string) =>
    name in values ? String(values[name]) : match,
  );

/** The eggs this browser found (design/components/eggs/eggs.js keeps them). */
export function browserFinds(): string[] {
  try {
    const list: unknown = JSON.parse(localStorage.getItem('krokosha:eggs') ?? '[]');
    return Array.isArray(list) ? list.filter((id): id is string => typeof id === 'string') : [];
  } catch {
    return [];
  }
}

let accountFinds: AccountFind[] = [];
let eggsUsed = false;

/** The account's side, from the personal account's page: its finds, and whether the eggs' discount went. */
export function useAccount(finds: AccountFind[], used: boolean): void {
  accountFinds = finds;
  eggsUsed = used;
}

function render(
  dialog: HTMLDialogElement,
  texts: PanelTexts,
  shares: Partial<Record<Achievement, number>>,
  lang: string,
): void {
  const stored = receipts();
  const dates = new Map<string, Date>();
  for (const find of accountFinds) dates.set(find.id, new Date(find.at));
  for (const id of EGGS) {
    const at = foundAt(stored[id]);
    const known = dates.get(id);
    if (at && (!known || at < known)) dates.set(id, at);
  }
  const found = new Set([...browserFinds(), ...accountFinds.map((find) => find.id)]);
  const eggs = EGGS.filter((id) => found.has(id)).length;
  if (eggs === EGGS.length) {
    found.add('all');
    const all = foundAt(stored.all);
    const last = Math.max(...EGGS.map((id) => dates.get(id)?.getTime() ?? 0));
    // No date known yet (the finds are still being fetched): none shown, rather than 1970.
    if (all || last > 0) dates.set('all', all ?? new Date(last));
  }
  const day = new Intl.DateTimeFormat(lang, { day: 'numeric', month: 'long', year: 'numeric' });

  const progress = fill(texts.progress, { found: eggs, total: EGGS.length });
  dialog.querySelector('[data-progress]')!.textContent = `${eggs}/${EGGS.length}`;
  dialog.querySelector('[data-count]')!.textContent = progress;
  (dialog.querySelector('[data-meter]') as HTMLElement).style.width =
    `${(eggs / EGGS.length) * 100}%`;

  for (const item of dialog.querySelectorAll<HTMLElement>('[data-achievement]')) {
    const id = item.dataset['achievement'] as Achievement;
    const has = found.has(id);
    const share = shares[id];
    const rare = id === 'all' || (share !== undefined && share < RARE_BELOW);
    item.classList.toggle('is-found', has);
    item.classList.toggle('is-rare', has && rare);

    const slot = item.querySelector<HTMLElement>('[data-icon]')!;
    const drawn = `${has ? 'on' : 'off'}${has && rare ? '-rare' : ''}`;
    if (slot.dataset['drawn'] !== drawn) {
      slot.dataset['drawn'] = drawn;
      const size = slot.getBoundingClientRect().width || 48;
      slot.replaceChildren(icon(id, has && rare, { size, locked: !has }));
    }

    item.querySelector<HTMLElement>('[data-text]')!.hidden = !has;
    const at = dates.get(id);
    const state = has
      ? [at ? fill(texts.found_on, { date: day.format(at) }) : '', rare ? texts.rare : '']
          .filter(Boolean)
          .join(' · ')
      : texts.locked;
    item.querySelector('[data-state]')!.textContent = state;

    const cell = item.querySelector<HTMLElement>('[data-share]')!;
    const bar = item.querySelector<HTMLElement>('[data-bar]')!;
    if (share === undefined) {
      cell.textContent = '';
      cell.removeAttribute('title');
      bar.style.width = '0';
    } else {
      const line = fill(texts.rarity, { percent: formatShare(share, lang) });
      cell.textContent = formatShare(share, lang);
      cell.title = line;
      cell.setAttribute('aria-label', line);
      bar.style.width = `${Math.max(share, 0.5)}%`;
    }
  }
  dialog.querySelector<HTMLElement>('[data-counting]')!.hidden = Object.keys(shares).length > 0;

  const discount = dialog.querySelector<HTMLElement>('[data-discount]');
  if (discount) {
    const complete = eggs === EGGS.length;
    const used = eggsUsed || spentOn() !== '';
    discount.classList.toggle('is-ready', complete && !used);
    discount.textContent = fill(
      complete ? (used ? texts.discount_used : texts.discount_ready) : texts.discount,
      { percent: texts.percent },
    );
  }
}

let checked = false;

/**
 * Whether the receipt of «all» is still unused: a request that was sent with it — from another tab, a
 * reload that lost the answer — spent it. The price of a request without an account tells: with an
 * unused receipt it is the eggs' discount.
 */
async function checkSpent(): Promise<void> {
  const receipt = everyEgg();
  if (checked || !receipt || spentOn() !== '') return;
  checked = true;
  try {
    const response = await fetch('/api/leads/offer', {
      method: 'POST',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: JSON.stringify({ eggs: receipt }),
      credentials: 'same-origin',
    });
    const offer = (await response.json()) as {
      enabled?: boolean;
      signed_in?: boolean;
      reason?: string;
    };
    if (response.ok && offer.enabled && offer.signed_in === false && offer.reason !== 'eggs')
      eggsUsed = true;
  } catch {
    // Unknown: the panel says what this browser knows.
  }
}

/** Opens the panel; the rarity arrives a moment later and fills the shares in. */
export function openAchievements(): void {
  const dialog = document.querySelector<HTMLDialogElement>('[data-achievements]');
  if (!dialog) return;
  const texts = JSON.parse(dialog.dataset['texts'] ?? '{}') as PanelTexts;
  const lang = document.documentElement.lang || 'en';
  if (!dialog.dataset['ready']) {
    dialog.dataset['ready'] = '1';
    dialog.querySelector('[data-close]')?.addEventListener('click', () => dialog.close());
    // A click on the backdrop (outside the panel's box) closes it too.
    dialog.addEventListener('click', (event) => {
      const box = dialog.getBoundingClientRect();
      const { clientX: x, clientY: y } = event;
      if (
        event.target === dialog &&
        (x < box.left || x > box.right || y < box.top || y > box.bottom)
      )
        dialog.close();
    });
  }
  let shares: Partial<Record<Achievement, number>> = {};
  if (!dialog.open) dialog.showModal();
  render(dialog, texts, shares, lang);
  void Promise.all([rarity(), checkSpent()]).then(([value]) => {
    shares = value;
    render(dialog, texts, shares, lang);
  });
}
