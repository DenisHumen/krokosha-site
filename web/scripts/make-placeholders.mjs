// Temporary brand assets until Claude Design delivers the real ones (design/assets/):
//   src/assets/avatar-fallback.png   neutral avatar for builds without a synced GitHub avatar
//   public/favicon.svg, favicon-32.png, apple-touch-icon.png
//   public/img/og-default.png        1200×630 Open Graph card
//
// Everything is drawn with circles (dot-matrix look from the references), so the result does not
// depend on the fonts installed on the machine. Run: node scripts/make-placeholders.mjs

import { mkdirSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import sharp from 'sharp';

const web = join(dirname(fileURLToPath(import.meta.url)), '..');

const LIGHT = '#ededeb';
const INK = '#111111';
const FAINT = '#d6d6d3';

// 5×7 dot-matrix glyphs.
const GLYPHS = {
  A: ['.###.', '#...#', '#...#', '#####', '#...#', '#...#', '#...#'],
  D: ['####.', '#...#', '#...#', '#...#', '#...#', '#...#', '####.'],
  E: ['#####', '#....', '#....', '####.', '#....', '#....', '#####'],
  H: ['#...#', '#...#', '#...#', '#####', '#...#', '#...#', '#...#'],
  K: ['#...#', '#..#.', '#.#..', '##...', '#.#..', '#..#.', '#...#'],
  N: ['#...#', '##..#', '#.#.#', '#..##', '#...#', '#...#', '#...#'],
  O: ['.###.', '#...#', '#...#', '#...#', '#...#', '#...#', '.###.'],
  P: ['####.', '#...#', '#...#', '####.', '#....', '#....', '#....'],
  R: ['####.', '#...#', '#...#', '####.', '#.#..', '#..#.', '#...#'],
  S: ['.####', '#....', '#....', '.###.', '....#', '....#', '####.'],
  T: ['#####', '..#..', '..#..', '..#..', '..#..', '..#..', '..#..'],
  V: ['#...#', '#...#', '#...#', '#...#', '#...#', '.#.#.', '..#..'],
  W: ['#...#', '#...#', '#...#', '#.#.#', '#.#.#', '##.##', '#...#'],
  ' ': ['.....', '.....', '.....', '.....', '.....', '.....', '.....'],
};

/** Circles for a line of text. `pitch` — distance between dot centres, `x`/`y` — top-left corner. */
function dots(text, { x, y, pitch, radius, on = INK, off = null }) {
  const out = [];
  [...text].forEach((char, index) => {
    const glyph = GLYPHS[char];
    if (!glyph) throw new Error(`No glyph for "${char}"`);
    glyph.forEach((row, r) => {
      [...row].forEach((cell, c) => {
        const fill = cell === '#' ? on : off;
        if (!fill) return;
        const cx = x + (index * 6 + c) * pitch + pitch / 2;
        const cy = y + r * pitch + pitch / 2;
        out.push(`<circle cx="${cx}" cy="${cy}" r="${radius}" fill="${fill}"/>`);
      });
    });
  });
  return out.join('');
}

const textWidth = (text, pitch) => (text.length * 6 - 1) * pitch;

function monogram(size, { background = LIGHT, rounded = false } = {}) {
  const pitch = size / 10;
  const x = (size - 5 * pitch) / 2;
  const y = (size - 7 * pitch) / 2;
  const rx = rounded ? size * 0.22 : 0;
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${size} ${size}" width="${size}" height="${size}">
<rect width="${size}" height="${size}" rx="${rx}" fill="${background}"/>
${dots('K', { x, y, pitch, radius: pitch * 0.36, off: FAINT })}
</svg>`;
}

function ogCard() {
  const [w, h] = [1200, 630];
  const title = 'KROKOSHA';
  const titlePitch = 20;
  const sub = 'NETWORKS  SERVERS  DEVOPS';
  const subPitch = 6;
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${w} ${h}" width="${w}" height="${h}">
<rect width="${w}" height="${h}" fill="${LIGHT}"/>
<line x1="80" y1="80" x2="${w - 80}" y2="80" stroke="${FAINT}" stroke-width="2" stroke-dasharray="2 10" stroke-linecap="round"/>
<line x1="80" y1="${h - 80}" x2="${w - 80}" y2="${h - 80}" stroke="${FAINT}" stroke-width="2" stroke-dasharray="2 10" stroke-linecap="round"/>
${dots(title, { x: (w - textWidth(title, titlePitch)) / 2, y: 205, pitch: titlePitch, radius: 7.4, off: FAINT })}
${dots(sub, { x: (w - textWidth(sub, subPitch)) / 2, y: 400, pitch: subPitch, radius: 2.2 })}
</svg>`;
}

async function png(svg, file, size) {
  const path = join(web, file);
  mkdirSync(dirname(path), { recursive: true });
  let image = sharp(Buffer.from(svg));
  if (size) image = image.resize(size, size);
  await image.png({ compressionLevel: 9, palette: true }).toFile(path);
  console.log('wrote', file);
}

writeFileSync(join(web, 'public/favicon.svg'), `${monogram(64, { rounded: true })}\n`);
console.log('wrote public/favicon.svg');
await png(monogram(512), 'src/assets/avatar-fallback.png');
await png(monogram(512, { rounded: true }), 'public/favicon-32.png', 32);
await png(monogram(512), 'public/apple-touch-icon.png', 180);
await png(ogCard(), 'public/img/og-default.png');
