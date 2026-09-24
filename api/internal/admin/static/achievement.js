/*
 * The achievement banner of the easter eggs, made after Steam's in-game one: a dark strip 300 px wide against
 * the right edge of the screen — icon, trophy, name, a line of description — that slides in, stays five seconds
 * and fades. Sizes, colours and timings are measured from the Steam client; the icons are the site's own dot
 * matrices, and the sounds are synthesised here after Steam's: the desktop «ding» for a common achievement, the
 * same ding with a glitter rising over it for a rare one (fewer than 10 % of players have it), the Steam Deck
 * achievement sound for the one that crowns a set (every egg, every achievement of orders). Nothing of Valve's
 * is used — no logo, no image, no sound file.
 * CSP: built with createElement and element.style, animated with element.animate(); no markup styles.
 */
const css = (el, s) => { el.style.cssText += ';' + s; return el; };
const el = (tag, style, parent) => { const e = css(document.createElement(tag), style); if (parent) parent.appendChild(e); return e; };
const reduced = () => matchMedia('(prefers-reduced-motion: reduce)').matches;

const WIDTH = 300; // Steam: the toast is 300 × 40 px, up to 60 with two lines of description
const SHOWN = 5000; // ms on screen, as Steam's default toast
const SLIDE = 300; // ms in and out
const FONT = '"Motiva Sans", Arial, Helvetica, sans-serif'; // Steam's stack: Motiva if the system has it
const GOLD = '#ffb84e';

// Dot-matrix icons, 9 × 9: '#' lit, '.' dim, ' ' off (a faint dot).
const ICONS = {
  konami: ['   ###   ', '   ###   ', '   ###   ', '#########', '####.####', '#########', '   ###   ', '   ###   ', '   ###   '],
  sudo: ['#########', '#       #', '# #     #', '#  #    #', '#   #   #', '#  #    #', '# # ### #', '#       #', '#########'],
  croc: ['         ', '         ', '  ##     ', ' #.##    ', '#########', '#########', '# # # #  ', '######## ', '         '],
  croc5: ['# # #    ', '#####    ', ' ##      ', '#.##     ', '#########', '#########', '# # # #  ', '######## ', '         '],
  cat: [' #   #   ', ' #####   ', ' #.#.#   ', ' #####  #', ' ###### #', ' ########', ' ####### ', ' ##   ## ', '         '],
  reboot: ['    #    ', ' #  #  # ', '#   #   #', '#   #   #', '#       #', '#       #', ' #     # ', '  #####  ', '         '],
  console: ['         ', ' ##   ## ', ' #     # ', ' #     # ', '#   #   #', ' #     # ', ' #     # ', ' ##   ## ', '         '],
  lost_packet: ['         ', '#########', '##     ##', '# #   # #', '#  # #  #', '#   #   #', '#       #', '#########', '         '],
  all: ['  #   #  ', '  #   #  ', '#########', '  #   #  ', '  #   #  ', '#########', '  #   #  ', '  #   #  ', '         '],
  // The achievements of orders in the personal account: a badge with 1, a badge with 2, a diamond, a crown.
  first_order: [' ####### ', '#   #   #', '#  ##   #', '# # #   #', '#   #   #', '#   #   #', '#  ###  #', '#       #', ' ####### '],
  second_order: [' ####### ', '#  ###  #', '# #   # #', '#     # #', '#    #  #', '#   #   #', '#  #### #', '#       #', ' ####### '],
  big_order: ['         ', '  #####  ', ' #.#.#.# ', '#########', ' #.....# ', '  #...#  ', '   #.#   ', '    #    ', '         '],
  all_orders: ['         ', '#   #   #', '## ### ##', '#########', '#.#.#.#.#', '#########', '#########', '         ', '         '],
};

/**
 * The icon of an achievement: its dot matrix on a dark square. Rare ones are gold; a locked one (the panel of
 * achievements shows what is still to find) is grey and dim. Also used by the site's panel of achievements.
 */
export function icon(id, rare, { size = 40, locked = false } = {}) {
  const dpr = Math.min(devicePixelRatio || 1, 2), n = 9, cell = size / (n + 1);
  const c = document.createElement('canvas');
  c.width = c.height = size * dpr;
  css(c, `display:block;position:relative;width:${size}px;height:${size}px` + (rare && !locked ? `;box-shadow:0 0 2px 1px rgba(255,184,78,.6),0 0 16px 1px rgba(255,184,78,.4)` : ''));
  const g = c.getContext('2d');
  if (!g) return c;
  g.scale(dpr, dpr);
  const bg = g.createLinearGradient(0, 0, 0, size);
  bg.addColorStop(0, locked ? '#22252b' : rare ? '#3b2f19' : '#2a3040');
  bg.addColorStop(1, locked ? '#111316' : rare ? '#140f08' : '#11151c');
  g.fillStyle = bg;
  g.fillRect(0, 0, size, size);
  const lit = locked ? '#6b717b' : rare ? GOLD : '#9d86f0';
  (ICONS[id] || ICONS.all).forEach((row, y) => [...row].forEach((ch, x) => {
    g.globalAlpha = (ch === '#' ? 1 : ch === '.' ? 0.4 : 0.08) * (locked && ch !== ' ' ? 0.55 : 1);
    g.fillStyle = ch === ' ' ? '#ffffff' : lit;
    g.beginPath(); g.arc(cell + x * cell, cell + y * cell, cell * 0.36, 0, 6.2832); g.fill();
  }));
  return c;
}

// A rare achievement: golden rays turn slowly behind the icon.
function rays(logo) {
  const r = el('div', 'position:absolute;inset:-9px;border-radius:50%;pointer-events:none;mix-blend-mode:screen;'
    + 'background:repeating-conic-gradient(rgba(255,184,78,0) 0deg 14deg,rgba(255,206,110,.9) 22deg,rgba(255,184,78,0) 30deg 45deg);'
    + '-webkit-mask-image:radial-gradient(circle,#000 32%,transparent 70%);mask-image:radial-gradient(circle,#000 32%,transparent 70%)');
  logo.insertBefore(r, logo.firstChild);
  if (!reduced()) r.animate([{ transform: 'rotate(0turn)' }, { transform: 'rotate(1turn)' }], { duration: 6000, iterations: Infinity });
}

// The trophy of the header, 13 × 13.
function trophy() {
  const NS = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(NS, 'svg');
  svg.setAttribute('viewBox', '0 0 13 13');
  svg.setAttribute('width', '13');
  svg.setAttribute('height', '13');
  svg.setAttribute('aria-hidden', 'true');
  css(svg, 'flex-shrink:0;margin-right:8px;fill:#fff');
  const path = document.createElementNS(NS, 'path');
  path.setAttribute('fill-rule', 'evenodd');
  path.setAttribute('d', 'M3 1h7v1h2.5v2.2A2.6 2.6 0 0 1 10 6.8 3.6 3.6 0 0 1 7.5 9v1.3h2V12h-6v-1.7h2V9A3.6 3.6 0 0 1 3 6.8 2.6 2.6 0 0 1 .5 4.2V2H3V1Zm-1.2 2.3v.9c0 .7.5 1.2 1.1 1.3V3.3H1.8Zm8.3 0v2.2c.6-.1 1.1-.6 1.1-1.3v-.9h-1.1Z');
  svg.appendChild(path);
  return svg;
}

// The strip in the corner, one at a time; it is also the live region that reads a new achievement out.
const queue = [];
let current = null;

function host() {
  let box = document.getElementById('kro-achievement');
  if (!box) {
    box = el('div', `position:fixed;right:0;width:${WIDTH}px;min-height:60px;display:flex;align-items:center;z-index:60;pointer-events:none`, document.body);
    box.id = 'kro-achievement';
    box.setAttribute('role', 'status');
  }
  // Where Steam puts it, a little lower: the sudo terminal starts 84 px up. On phones above the floating bar.
  box.style.bottom = matchMedia('(max-width: 900px)').matches ? 'calc(env(safe-area-inset-bottom) + 84px)' : 'calc(env(safe-area-inset-bottom) + 24px)';
  return box;
}

function toast(a) {
  const t = el('div', `display:flex;flex-direction:row;align-items:center;width:${WIDTH}px;min-height:40px;max-height:${a.rarity ? 76 : 60}px;background-color:#0e141b;`
    + `box-shadow:0 0 20px rgba(0,0,0,.5);font-family:${FONT};-webkit-font-smoothing:antialiased;pointer-events:auto;cursor:default;transition:background-color 50ms ease-out`);
  const logo = el('div', 'position:relative;width:40px;height:40px;flex-shrink:0', t);
  logo.appendChild(icon(a.id, a.rare));
  if (a.rare) rays(logo);
  const content = el('div', 'margin:5px 12px;min-width:0;flex-grow:1', t);
  if (a.found) el('span', 'position:absolute;width:1px;height:1px;margin:-1px;overflow:hidden;clip:rect(0 0 0 0);white-space:nowrap', content).textContent = `${a.found}: `;
  const header = el('div', 'display:flex;flex-direction:row;align-items:center;height:14px;line-height:14px', content);
  header.appendChild(trophy());
  el('div', 'font-weight:500;font-size:11px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;color:#fff', header).textContent = a.name;
  if (a.count) el('div', 'margin-left:auto;padding-left:8px;flex-shrink:0;font-size:11px;color:#8b929a', header).textContent = ` ${a.count}/${a.total}`;
  el('div', 'font-size:12px;line-height:16px;color:#b8bcbf;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden;overflow-wrap:anywhere', content).textContent = a.text;
  // How rare it is, the way Steam's pages show it: «3.4% of players have it»; gold under 10 %.
  if (a.rarity) el('div', `font-size:11px;line-height:14px;margin-top:1px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;color:${a.rare ? GOLD : '#8b929a'}`, content).textContent = a.rarity;
  t.addEventListener('pointerenter', () => { t.style.backgroundColor = '#23262e'; });
  t.addEventListener('pointerleave', () => { t.style.backgroundColor = '#0e141b'; });
  t.addEventListener('click', () => hide(t));
  return t;
}

function next() {
  const a = queue.shift();
  if (!a) { current = null; return; }
  const t = toast(a);
  current = t;
  host().appendChild(t);
  if (!a.silent) sound(a.sound || (a.rare ? 'rare' : 'egg'));
  const still = reduced();
  t.animate(still ? [{ opacity: 0 }, { opacity: 1 }] : [{ transform: `translateX(${WIDTH}px)` }, { transform: 'translateX(0)' }],
    { duration: still ? 150 : SLIDE, easing: 'cubic-bezier(0, 0.73, 0.48, 1)', fill: 'both' });
  t.timer = setTimeout(() => hide(t), SLIDE + SHOWN);
}

function hide(t) {
  if (t.hiding) return;
  t.hiding = true;
  clearTimeout(t.timer);
  t.animate([{ opacity: 1 }, { opacity: 0 }], { duration: SLIDE, fill: 'forwards' }).onfinish = () => { t.remove(); next(); };
}

/**
 * Shows an achievement: { id, name, text, count, total, rare, rarity, sound, silent, found } — found is read out
 * before the name; rarity is a line under the description; sound is «egg», «rare» or «epic» (by default a rare
 * one sounds «rare», any other «egg»); silent — no sound at all.
 */
export function unlock(a) {
  queue.push(a);
  if (!current) next();
}

// --- sound -----------------------------------------------------------------------------------------------

let context = null;

function audio() {
  if (context) return context;
  const AC = window.AudioContext || window.webkitAudioContext;
  if (!AC) return null;
  // Safari: mix with whatever else plays and respect the silent switch, instead of pausing the music.
  try { if (navigator.audioSession) navigator.audioSession.type = 'ambient'; } catch { /* not there */ }
  try { context = new AC(); } catch { return null; }
  return context;
}

// A small room: noise that dies away, for the tail after the bells.
function room(c, seconds, fall) {
  const len = Math.round(c.sampleRate * seconds), buf = c.createBuffer(2, len, c.sampleRate);
  let seed = 7;
  const rand = () => { seed = (seed * 16807) % 2147483647; return seed / 1073741823.5 - 1; };
  for (let ch = 0; ch < 2; ch++) {
    const data = buf.getChannelData(ch);
    for (let i = 0; i < len; i++) data[i] = rand() * Math.exp(-i / (c.sampleRate * fall));
  }
  const conv = c.createConvolver();
  conv.buffer = buf;
  return conv;
}

// One voice: an oscillator with an attack and an exponential fall; «wobble» is a 62 Hz vibrato (Hz deep)
// that gives Steam's bells their sidebands; «glide» is a list of [Hz, seconds after the start] the pitch
// slides through.
function voice(c, out, { t, f, glide = [], type = 'sine', peak, attack = 0.004, fall, wobble = 0, hold = 0 }) {
  const osc = c.createOscillator(), env = c.createGain();
  osc.type = type;
  osc.frequency.setValueAtTime(f, t);
  for (const [hz, at] of glide) osc.frequency.exponentialRampToValueAtTime(hz, t + at);
  env.gain.setValueAtTime(0, t);
  env.gain.linearRampToValueAtTime(peak, t + attack);
  env.gain.setTargetAtTime(0, t + attack + hold, fall);
  osc.connect(env);
  env.connect(out);
  const end = t + attack + hold + fall * 9;
  osc.start(t);
  osc.stop(end);
  if (wobble) {
    const lfo = c.createOscillator(), depth = c.createGain();
    lfo.frequency.value = 62;
    depth.gain.value = wobble;
    lfo.connect(depth);
    depth.connect(osc.frequency);
    lfo.start(t);
    lfo.stop(end);
  }
}

// A bell of Steam's toasts: the note, its octave, and a short «tine» three octaves up.
function bell(c, out, t, f, peak, fall, tine = 0, attack = 0.004) {
  voice(c, out, { t, f, peak, attack, fall, wobble: 30 });
  voice(c, out, { t, f: f * 2, peak: peak * 0.18, attack, fall: fall * 0.6 });
  if (tine) voice(c, out, { t, f: f * 8, peak: peak * tine, fall: 0.02 });
}

const NOTE = {
  F2: 87.31, F3: 174.61, C4: 261.63, F4: 349.23, Bb4: 466.16, F5: 698.46, A5: 880, Bb5: 932.33, C6: 1046.5,
  F6: 1396.91, G6: 1567.98, A6: 1760, C7: 2093, F7: 2793.83,
};

const SOUNDS = {
  // Steam's desktop toast: B♭4, then F5 with A5, then C6 — a quick rising chime.
  egg(c, out, t) {
    bell(c, out, t, NOTE.Bb4, 1, 0.05, 0, 0.012);
    voice(c, out, { t, f: NOTE.F5, peak: 0.22, attack: 0.012, fall: 0.05, wobble: 30 });
    bell(c, out, t + 0.09, NOTE.F5, 1, 0.04);
    bell(c, out, t + 0.09, NOTE.A5, 1.1, 0.019, 0.3);
    bell(c, out, t + 0.2, NOTE.C6, 0.6, 0.025, 0.1);
  },
  // A rare one: the chime of an egg, then a glitter rising over it — bells up the F major chord from where
  // the chime ends, and a soft fifth that swells under them. About a second: more than a common find, less
  // than the fanfare of a whole set.
  rare(c, out, t) {
    SOUNDS.egg(c, out, t);
    [NOTE.F6, NOTE.A6, NOTE.C7, NOTE.F7].forEach((f, i) =>
      bell(c, out, t + 0.27 + i * 0.06, f, 0.5 - i * 0.07, 0.035, i < 2 ? 0.12 : 0));
    voice(c, out, { t: t + 0.3, f: NOTE.F5, peak: 0.16, attack: 0.09, hold: 0.2, fall: 0.13, wobble: 10 });
    voice(c, out, { t: t + 0.3, f: NOTE.C6, peak: 0.1, attack: 0.11, hold: 0.18, fall: 0.13, wobble: 10 });
  },
  // Steam Deck's achievement: bells F5 B♭5 C6 G6 over a tone rising a fourth, then a low F that drops in
  // and hums for a second.
  epic(c, out, t) {
    bell(c, out, t, NOTE.F5, 0.45, 0.022, 0.09);
    bell(c, out, t + 0.05, NOTE.Bb5, 1, 0.02, 0.25);
    voice(c, out, { t: t + 0.1, f: 377, glide: [[392, 0.08], [466, 0.2]], peak: 0.55, fall: 0.09, wobble: 30 });
    bell(c, out, t + 0.13, NOTE.C6, 1.1, 0.02, 0.2);
    bell(c, out, t + 0.26, NOTE.G6, 0.19, 0.026);
    bell(c, out, t + 0.3, NOTE.C6, 0.27, 0.02);
    voice(c, out, { t: t + 0.3, f: 474, glide: [[506, 0.07]], peak: 0.45, fall: 0.03, wobble: 30 });
    const lp = c.createBiquadFilter();
    lp.type = 'lowpass';
    lp.frequency.value = 800;
    lp.Q.value = 0.5;
    lp.connect(out);
    // The low F goes first, its overtones linger: the hum thins out as it fades.
    const pad = { t: t + 0.32, attack: 0.13, hold: 0.2, fall: 0.15 };
    const drop = (hz) => ({ f: hz * 1.3, glide: [[hz, 0.045]] });
    voice(c, lp, { ...pad, ...drop(NOTE.F2), type: 'sawtooth', peak: 0.24 });
    voice(c, lp, { ...pad, ...drop(NOTE.F2 * 1.005), type: 'sawtooth', peak: 0.18 });
    voice(c, lp, { ...pad, ...drop(NOTE.F2 / 2), type: 'square', peak: 0.08 });
    voice(c, lp, { ...pad, ...drop(NOTE.F3), type: 'sawtooth', peak: 0.3, hold: 0.3, fall: 0.28 });
    voice(c, lp, { ...pad, ...drop(NOTE.C4), peak: 0.12, hold: 0.35, fall: 0.22 });
    voice(c, lp, { ...pad, ...drop(NOTE.F4), peak: 0.07, hold: 0.4, fall: 0.25 });
  },
};

/** Plays «egg», «rare» or «epic» — into the page's audio, or at «when» into the given context (an OfflineAudioContext in tests). */
export function sound(kind, c = audio(), when) {
  if (!c) return;
  if (when === undefined && c.state === 'suspended') {
    // The browser lets a page sound only after the visitor has done something on it; until then the sound
    // is skipped rather than left waiting to play with the next one.
    c.resume().then(() => play(kind, c, c.currentTime + 0.02), () => {});
    return;
  }
  play(kind, c, when ?? c.currentTime + 0.02);
}

function play(kind, c, t) {
  try {
    // About as loud and as long as Steam's own: 0.6 s for an egg, a second for a rare one, 1.6 s for a set.
    const [level, end, wetness, tailSeconds, tailFall] = {
      egg: [0.09, 0.62, 0.4, 0.7, 0.18],
      rare: [0.11, 1.05, 0.3, 1, 0.24],
      epic: [0.19, 1.6, 0.16, 1.4, 0.3],
    }[kind] || [];
    if (!SOUNDS[kind]) return;
    const master = c.createGain();
    master.gain.setValueAtTime(level, t);
    master.gain.setValueAtTime(level, t + end - 0.25);
    master.gain.linearRampToValueAtTime(0, t + end);
    master.connect(c.destination);
    const wet = c.createGain();
    wet.gain.value = wetness;
    const tail = room(c, tailSeconds, tailFall);
    const out = c.createGain();
    out.connect(master);
    out.connect(tail);
    tail.connect(wet);
    wet.connect(master);
    SOUNDS[kind](c, out, t);
  } catch { /* a sound is a nicety: the banner shows without it */ }
}
