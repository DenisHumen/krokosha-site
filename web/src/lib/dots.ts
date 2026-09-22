// Dot-matrix pictures of the design, computed at build time so that the page carries them as plain
// markup: no canvas, no script, and nothing for the Content-Security-Policy to object to.

/** Digits 0–9 as 5×7 bitmaps, row by row (design/pages/index.dc.html → DIG). */
const DIGITS: Record<string, string> = {
  '0': '01110100011001110101110011000101110',
  '1': '00100011000010000100001000010001110',
  '2': '01110100010000100010001000100011111',
  '3': '11111000100010000010000011000101110',
  '4': '00010001100101010010111110001000010',
  '5': '11111100001111000001000011000101110',
  '6': '00110010001000011110100011000101110',
  '7': '11111000010001000100010000100001000',
  '8': '01110100011000101110100011000101110',
  '9': '01110100011000101111000010001001100',
};

/** A number as a list of digits, each a list of 35 dots (true — lit). */
export function digitDots(value: number): boolean[][] {
  return String(value)
    .split('')
    .map((digit) => (DIGITS[digit] ?? DIGITS['0'] ?? '').split('').map((bit) => bit === '1'));
}

/** The ring of the curtain: 96 dots on three radii, the outer one full, the inner ones sparser. */
export function ringDots(radius: number): { x: number; y: number; opacity: number }[] {
  return Array.from({ length: 96 }, (_, i) => {
    const angle = (i * 3.75 * Math.PI) / 180;
    const r = radius - (i % 3 === 0 ? 0 : i % 3 === 1 ? 26 : 52);
    return {
      x: Math.round(r * Math.cos(angle) * 100) / 100,
      y: Math.round(r * Math.sin(angle) * 100) / 100,
      opacity: 0.3 + ((i * 7) % 5) / 8,
    };
  });
}

/** The hatch of /play: a door outline (#), empty inside (.) and a blinking lock (o). */
export const HATCH = [
  '#########',
  '#.......#',
  '#.......#',
  '#.......#',
  '#....o..#',
  '#.......#',
  '#########',
]
  .join('')
  .split('') as ('#' | '.' | 'o')[];
