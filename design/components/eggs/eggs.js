/*
 * Easter eggs (brief A6). Loaded lazily after `load`. Every egg is gated by site.flags.easter_eggs.*.
 * Style: dot-matrix bitmaps drawn on small canvases (no GIF). Nothing here covers a CTA;
 * nothing auto-starts under prefers-reduced-motion. Tracking: data-track="egg-<id>" via found().
 * A found egg is an achievement, shown the way Steam shows one (achievement.js).
 */
import { unlock } from './achievement.js';

const KEY = 'krokosha:eggs';
const ALL = ['konami', 'sudo', 'croc', 'cat', 'reboot', 'console', 'croc5', 'lost_packet'];
export const TOTAL = ALL.length;
let years = 9; // set by initEggs: the BIOS screen of the avatar counts the real years
let achievements = true; // flag achievements: the banner with its sound
const reduced = matchMedia('(prefers-reduced-motion: reduce)').matches;
const css = (el, s) => { el.style.cssText += ';' + s; return el; };
const mono = "font:12px/1.5 var(--font-mono, 'Fira Code', ui-monospace, monospace)"; // the site's own family name

/** What found() needs where initEggs() does not run (the 404 page): the flags and the years. */
export function configure({ flags = {}, experience } = {}) {
  achievements = flags.enabled !== false && flags.achievements !== false;
  if (experience) years = experience;
}
export function foundList() { try { return JSON.parse(localStorage.getItem(KEY) || '[]'); } catch { return []; } }
export function found(id, texts = {}) {
  const list = foundList(); if (list.includes(id)) return list.length;
  list.push(id); try { localStorage.setItem(KEY, JSON.stringify(list)); } catch {}
  document.dispatchEvent(new CustomEvent('kro:egg', { detail: { id, count: list.length, total: ALL.length } }));
  document.dispatchEvent(new CustomEvent('krokosha:egg', { detail: id })); // statistics (contract §4)
  if (achievements) {
    const names = texts.achievements || {}, label = texts.found || 'Пасхалка найдена';
    unlock({ id, name: id, text: label, ...names[id], found: label, count: list.length, total: ALL.length });
    if (list.length >= ALL.length) unlock({ id: 'all', name: 'root@krokosha', text: '', ...names.all, found: label, rare: true });
  }
  return list.length;
}

// Dot-matrix sprite: array of strings ('#' = dot) → canvas with animation frames.
function dotSprite(frames, dot = 3, gap = 1, color = 'var(--fg)') {
  const c = document.createElement('canvas'); const rows = frames[0].length, cols = frames[0][0].length;
  const dpr = Math.min(devicePixelRatio || 1, 2), s = dot + gap;
  c.width = cols * s * dpr; c.height = rows * s * dpr; c.style.width = cols * s + 'px'; c.style.height = rows * s + 'px';
  const g = c.getContext('2d'); let f = 0;
  const draw = () => { g.setTransform(dpr, 0, 0, dpr, 0, 0); g.clearRect(0, 0, c.width, c.height); g.fillStyle = getComputedStyle(document.documentElement).getPropertyValue('--fg') || '#111';
    frames[f].forEach((row, y) => [...row].forEach((ch, x) => { if (ch !== ' ') { g.globalAlpha = ch === '.' ? 0.35 : 1; g.beginPath(); g.arc(x * s + dot / 2, y * s + dot / 2, dot / 2, 0, 6.28); g.fill(); } })); };
  draw(); c.step = () => { f = (f + 1) % frames.length; draw(); }; c.redraw = draw; return c;
}

const CROC = [[
  '           ##      ##   ',
  '          #.#     #.#   ',
  '  ########################',
  ' #.#.#.#.#.#.#.#.#.#.#.#.#',
  '##########################',
], [
  '           ##      ##   ',
  '          ###     ###   ',
  '  ########################',
  ' #.#.#.#.#.#.#.#.#.#.#.#.#',
  '##########################',
]];
const CAT = [[
  ' #   #      ',
  ' #####      ',
  ' #.#.#      ',
  ' #####      ',
  ' ########## ',
  ' ########## ',
  '  #  #  #  #',
], [
  ' #   #      ',
  ' #####      ',
  ' #.#.#      ',
  ' #####      ',
  ' ########## ',
  ' ##########.',
  ' #  #  #  # ',
]];
const CAT_SIT = [[
  ' #   #   ',
  ' #####   ',
  ' #.#.#   ',
  ' #####   ',
  ' ####### ',
  ' ####### ',
  ' ####### ',
  ' ##   ## ',
]];

export function initEggs({ flags = {}, texts = {}, experience = 9 } = {}) {
  configure({ flags, experience });
  const on = (k) => flags.enabled !== false && flags[k] !== false;
  if (on('console_message')) {
    console.log('%c krokosha.com %c link up · 10Gbps · full-duplex ', 'background:#111;color:#fff;padding:4px 8px;border-radius:4px 0 0 4px;font-family:Fira Code,monospace', 'background:#6b4de6;color:#fff;padding:4px 8px;border-radius:0 4px 4px 0;font-family:Fira Code,monospace');
    window.krokosha = { hello: () => { found('console', texts); return 'ack · link up'; } };
    console.log('%c' + (texts.console || 'кто читает консоль — тот уже почти коллега. krokosha.hello()  ·  или набери sudo на странице  ·  или ↑↑↓↓←→←→BA'), 'color:#6a6a66;font-family:Fira Code,monospace');
  }
  if (on('konami')) konami(texts);
  if (on('sudo_terminal')) sudo(texts, experience);
  if (on('croc_footer')) croc(texts);
  if (on('cat_walker') && !reduced) cat(texts);
}

function konami(texts) {
  const seq = ['ArrowUp', 'ArrowUp', 'ArrowDown', 'ArrowDown', 'ArrowLeft', 'ArrowRight', 'ArrowLeft', 'ArrowRight', 'b', 'a']; let i = 0;
  addEventListener('keydown', (e) => {
    if (e.key === seq[i] || e.key.toLowerCase() === seq[i]) { if (++i === seq.length) { i = 0; nightMode(texts); } } else i = 0;
  });
}
export function nightMode(texts) {
  const root = document.documentElement; const onNow = root.dataset.night === '1';
  if (onNow) { root.dataset.night = ''; root.dataset.theme = root.dataset.prevTheme || ''; document.getElementById('kro-night')?.remove(); return; }
  root.dataset.prevTheme = root.dataset.theme || ''; root.dataset.theme = 'dark'; root.dataset.night = '1';
  const wrap = css(document.createElement('div'), 'position:fixed;inset:0;pointer-events:none;z-index:39'); wrap.id = 'kro-night';
  // rack LEDs along the top edge + a soft cold glow — "the server room at night"
  const leds = css(document.createElement('div'), 'position:absolute;top:0;left:0;right:0;height:3px;display:flex;gap:6px;padding:0 12px;opacity:.9');
  const colors = ['#5fd39a', '#5fd39a', '#5fd39a', '#e6b450', '#4fd6e0', '#5fd39a'];
  for (let k = 0; k < 90; k++) { const d = css(document.createElement('i'), `flex:0 0 6px;height:3px;background:${colors[k % colors.length]};border-radius:1px;animation:kro-led ${(0.6 + (k * 37 % 13) / 10).toFixed(2)}s steps(2) infinite`); d.style.animationDelay = (k * 53 % 17) / 10 + 's'; leds.appendChild(d); }
  wrap.appendChild(leds);
  const glow = css(document.createElement('div'), 'position:absolute;inset:0;background:radial-gradient(60% 40% at 50% 0%,rgba(79,214,224,.08),transparent 70%)'); wrap.appendChild(glow);
  // @keyframes kro-led lives in the site's stylesheet: the CSP allows no <style> elements.
  const cap = css(document.createElement('div'), `position:absolute;left:50%;top:14px;transform:translateX(-50%);color:#9a9a95;${mono}`); cap.textContent = texts.night || 'серверная · 03:14 · всё зелёное'; wrap.appendChild(cap);
  document.body.appendChild(wrap); found('konami', texts);
}

function sudo(texts, experience) {
  let buf = '';
  addEventListener('keydown', (e) => {
    const t = e.target; if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)) return;
    if (e.key.length !== 1) return; buf = (buf + e.key.toLowerCase()).slice(-4); if (buf === 'sudo') { buf = ''; openTerminal(texts, experience); }
  });
}
export function openTerminal(texts, experience) {
  if (document.getElementById('kro-term')) return;
  const box = css(document.createElement('div'), `position:fixed;right:16px;bottom:calc(env(safe-area-inset-bottom) + 84px);width:min(440px,calc(100vw - 32px));z-index:50;background:#111113;color:#ededea;border:1px solid #2c2c30;border-radius:10px;box-shadow:var(--shadow-2);${mono};font-size:12.5px;overflow:hidden`); box.id = 'kro-term';
  const el = (tag, style, parent) => { const e = css(document.createElement(tag), style); parent.appendChild(e); return e; };
  const head = el('div', 'display:flex;align-items:center;gap:8px;padding:8px 12px;border-bottom:1px solid #2c2c30;color:#9a9a95', box);
  el('span', 'width:8px;height:8px;border-radius:50%;background:#5fd39a', head);
  head.appendChild(document.createTextNode('krokosha@edge: ~'));
  const close = el('button', 'margin-left:auto;background:none;border:0;color:#9a9a95;cursor:pointer;font:inherit', head);
  close.setAttribute('aria-label', 'close'); close.textContent = '✕';
  const out = el('pre', 'margin:0;padding:12px;max-height:220px;overflow:auto;white-space:pre-wrap', box);
  const line = el('div', 'display:flex;gap:8px;padding:8px 12px;border-top:1px solid #2c2c30', box);
  el('span', 'color:#9d86f0', line).textContent = '$';
  const inp = el('input', 'flex:1;background:none;border:0;color:inherit;font:inherit;outline:none', line);
  inp.setAttribute('aria-label', 'terminal'); inp.autocomplete = 'off'; inp.spellcheck = false;
  const term = texts.terminal || {};
  const print = (s) => { out.textContent += s + '\n'; out.scrollTop = out.scrollHeight; };
  print('[sudo] password for guest: ********\nOK. Try: whoami · ping denis · uptime · ls · help · exit');
  const cmds = {
    whoami: () => term.whoami || 'guest (but curious). Denis is root here.',
    uptime: () => term.uptime || `up ${experience} years, 0 incidents caused by coffee`,
    'ping denis': () => `PING denis (${texts.telegram || 't.me/DenisHumen'}): 56 bytes\n64 bytes: icmp_seq=1 ttl=64 time=0.9 ms  ← ${term.ping || 'он отвечает быстро'}`,
    ls: () => 'networks/  servers/  devops/  highload-lan/  tooling/  .secrets (permission denied)',
    help: () => 'whoami · ping denis · uptime · ls · clear · exit',
    clear: () => { out.textContent = ''; return ''; },
    exit: () => { box.remove(); return ''; },
    'rm -rf /': () => term.rm || 'nice try. snapshot restored in 0.2 s',
  };
  inp.addEventListener('keydown', (e) => { if (e.key !== 'Enter') return; const c = inp.value.trim(); inp.value = ''; print('$ ' + c); const f = cmds[c]; print(f ? f() : `${c}: command not found`); });
  close.onclick = () => box.remove();
  document.body.appendChild(box); inp.focus(); found('sudo', texts);
}

// Avatar «reboots» like a server: a POST screen over the avatar, then it comes back.
export function rebootAvatar(el, texts = {}) {
  if (!el || el.dataset.rebooting) return; el.dataset.rebooting = '1';
  const r = el.getBoundingClientRect();
  const scr = css(document.createElement('div'), `position:absolute;left:0;top:0;width:100%;height:100%;border-radius:inherit;background:#111113;color:#ededea;${mono};font-size:9px;line-height:1.35;padding:10px;overflow:hidden;z-index:2`);
  el.style.position = 'relative'; el.appendChild(scr);
  const lines = ['KROKOSHA BIOS v9.0', 'CPU: 1 x Engineer @ 4.0GHz', `Memory test: ${years} years OK`, 'Detecting links... eth0 10G', 'Booting denis.img ...', 'link up · full-duplex'];
  let i = 0; const tick = () => { if (i < lines.length) { scr.textContent += lines[i++] + '\n'; setTimeout(tick, 260); } else setTimeout(() => { scr.remove(); delete el.dataset.rebooting; found('reboot', texts); }, 500); }; tick();
}

function croc(texts) {
  const foot = document.querySelector('[data-section="footer"]'); if (!foot) return;
  const wrap = css(document.createElement('button'), 'position:fixed;left:24px;bottom:-40px;z-index:40;background:none;border:0;padding:0;cursor:pointer;transition:bottom .6s var(--ease-spring)'); wrap.setAttribute('aria-label', texts.croc || 'крокодил'); wrap.dataset.track = 'egg-croc';
  const c = dotSprite(CROC, 3, 1); wrap.appendChild(c); document.body.appendChild(wrap);
  let clicks = 0, tmr; const blink = () => { c.step(); tmr = setTimeout(blink, 400 + Math.random() * 1600); };
  const io = new IntersectionObserver(([e]) => { if (e.isIntersecting && !reduced) { wrap.style.bottom = '0px'; clearTimeout(tmr); blink(); } else { wrap.style.bottom = '-40px'; clearTimeout(tmr); } }); io.observe(foot);
  wrap.onclick = () => { clicks++; found('croc', texts); wrap.style.bottom = '-40px'; if (clicks >= 5) found('croc5', texts); else setTimeout(() => { if (wrap.style.bottom === '-40px') wrap.style.bottom = '0px'; }, 2200 + clicks * 400); };
  new MutationObserver(() => c.redraw()).observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
}

function cat(texts) {
  const cards = () => [...document.querySelectorAll('[data-component="project-card"][data-tier="featured"]')];
  const walk = () => {
    const list = cards(); if (!list.length) return setTimeout(walk, 8000);
    const card = list[Math.floor(Math.random() * list.length)];
    const c = dotSprite(CAT, 2, 1); css(c, 'position:absolute;top:-21px;left:-30px;z-index:3;cursor:pointer;transition:left 6s linear');
    card.style.position = 'relative'; card.appendChild(c);
    let sitting = false, step = setInterval(() => !sitting && c.step(), 180);
    requestAnimationFrame(() => requestAnimationFrame(() => { c.style.left = 'calc(100% - 6px)'; }));
    const done = setTimeout(() => { clearInterval(step); c.remove(); setTimeout(walk, 20000 + Math.random() * 30000); }, 6200);
    c.onmouseenter = () => { if (sitting) return; sitting = true; clearTimeout(done); const left = getComputedStyle(c).left; c.style.transition = 'none'; c.style.left = left; const sit = dotSprite(CAT_SIT, 2, 1); css(sit, `position:absolute;top:-24px;left:${left};z-index:3;cursor:pointer`); c.replaceWith(sit); found('cat', texts); sit.onclick = () => { sit.remove(); setTimeout(walk, 30000); }; setTimeout(() => { sit.remove(); setTimeout(walk, 30000); }, 12000); };
  };
  setTimeout(walk, 9000);
}
