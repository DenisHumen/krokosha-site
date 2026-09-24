// <glyph-field>: a field of monospace glyphs that drifts like smoke (design v3; the admin area has the
// same element in api/internal/admin/static/glyph-field.js). It only decorates: without scripts the
// element is empty, and nothing on the page depends on it.
//
// Attributes: ink (colour), accent (colour of the densest cells), cell (px), mask (none | right |
// left | center | bottom), speed (1 = default), intensity (0…1.5), chars (a ramp from empty to
// dense), fps (frames per second, 30 by default), still (present: one frame, no motion).
//
// It costs a core when it runs at full speed over a whole screen, so it draws at most `fps` frames a
// second, stops while it is off screen, hidden or the tab is in the background, and draws a single
// frame when the system asks for reduced motion. Styles go through the CSSOM: the site's
// Content-Security-Policy forbids only style="…" in the markup.

const P = new Uint16Array(512);
const G = new Float32Array(256);
{
  let seed = 20260924;
  const random = () => {
    seed = (seed * 16807) % 2147483647;
    return seed / 2147483647;
  };
  const order = Array.from({ length: 256 }, (_, i) => i);
  for (let i = 255; i > 0; i--) {
    const j = Math.floor(random() * (i + 1));
    [order[i], order[j]] = [order[j] ?? 0, order[i] ?? 0];
  }
  for (let i = 0; i < 512; i++) P[i] = order[i & 255] ?? 0;
  for (let i = 0; i < 256; i++) G[i] = random();
}

const hash = (x: number, y: number) => G[P[(P[x & 255] ?? 0) + (y & 255)] ?? 0] ?? 0;

function noise(x: number, y: number): number {
  const xi = Math.floor(x);
  const yi = Math.floor(y);
  const xf = x - xi;
  const yf = y - yi;
  const u = xf * xf * (3 - 2 * xf);
  const v = yf * yf * (3 - 2 * yf);
  const a = hash(xi, yi);
  const b = hash(xi + 1, yi);
  const c = hash(xi, yi + 1);
  const d = hash(xi + 1, yi + 1);
  return a + (b - a) * u + (c - a) * v + (a - b - c + d) * u * v;
}

function fbm(x: number, y: number): number {
  let sum = 0;
  let amplitude = 0.5;
  for (let octave = 0; octave < 4; octave++) {
    sum += amplitude * noise(x, y);
    const nx = x * 1.6 - y * 1.2;
    const ny = x * 1.2 + y * 1.6;
    x = nx + 3.1;
    y = ny + 1.7;
    amplitude *= 0.5;
  }
  return sum / 0.9375;
}

function smooth(a: number, b: number, x: number): number {
  const t = Math.min(1, Math.max(0, (x - a) / (b - a)));
  return t * t * (3 - 2 * t);
}

const reducedMotion = matchMedia('(prefers-reduced-motion: reduce)');

class GlyphField extends HTMLElement {
  static observedAttributes = [
    'ink',
    'accent',
    'cell',
    'mask',
    'speed',
    'intensity',
    'chars',
    'still',
    'fps',
  ];

  private canvas: HTMLCanvasElement | null = null;
  private context: CanvasRenderingContext2D | null = null;
  private resizer: ResizeObserver | null = null;
  private watcher: IntersectionObserver | null = null;
  private frame = 0;
  private started = 0;
  private last = 0;
  private width = 1;
  private height = 1;
  private onScreen = true;
  private pointerX = -9999;
  private pointerY = -9999;
  private pull = 0;
  private font = '';

  private readonly onMove = (event: PointerEvent) => {
    const box = this.getBoundingClientRect();
    this.pointerX = event.clientX - box.left;
    this.pointerY = event.clientY - box.top;
    this.pull = 1;
  };

  private readonly loop = (now: number) => {
    this.frame = requestAnimationFrame(this.loop);
    if (!this.onScreen || document.hidden || now - this.last < this.interval()) return;
    this.last = now;
    this.draw(now);
  };

  private readonly onMotion = () => this.start();

  connectedCallback(): void {
    this.style.display = 'block';
    // The page places the field (a fixed backdrop, a panel filled edge to edge); a field placed
    // nowhere still needs a box of its own for the canvas.
    if (getComputedStyle(this).position === 'static') this.style.position = 'relative';
    const canvas = document.createElement('canvas');
    canvas.setAttribute('aria-hidden', 'true');
    Object.assign(canvas.style, {
      position: 'absolute',
      inset: '0',
      width: '100%',
      height: '100%',
      display: 'block',
    });
    this.append(canvas);
    this.canvas = canvas;
    this.context = canvas.getContext('2d');
    // The canvas draws with the page's own monospace face (Astro's font pipeline names it).
    this.font = getComputedStyle(document.documentElement).getPropertyValue('--font-mono').trim();
    window.addEventListener('pointermove', this.onMove, { passive: true });
    reducedMotion.addEventListener('change', this.onMotion);
    this.resizer = new ResizeObserver(() => {
      this.resize();
      if (this.isStill()) this.draw(this.started + 8000);
    });
    this.resizer.observe(this);
    this.watcher = new IntersectionObserver((entries) => {
      this.onScreen = entries.some((entry) => entry.isIntersecting);
    });
    this.watcher.observe(this);
    this.started = performance.now();
    this.resize();
    this.start();
  }

  disconnectedCallback(): void {
    cancelAnimationFrame(this.frame);
    this.frame = 0;
    this.resizer?.disconnect();
    this.watcher?.disconnect();
    window.removeEventListener('pointermove', this.onMove);
    reducedMotion.removeEventListener('change', this.onMotion);
    this.canvas?.remove();
    this.canvas = null;
    this.context = null;
  }

  attributeChangedCallback(): void {
    if (!this.context) return;
    this.resize();
    this.start();
  }

  private isStill(): boolean {
    return this.hasAttribute('still') || reducedMotion.matches;
  }

  private interval(): number {
    const fps = Number.parseFloat(this.getAttribute('fps') ?? '30') || 30;
    return 1000 / Math.min(60, Math.max(1, fps));
  }

  private start(): void {
    cancelAnimationFrame(this.frame);
    this.frame = 0;
    if (this.isStill()) {
      this.draw(this.started + 8000);
      return;
    }
    this.frame = requestAnimationFrame(this.loop);
  }

  private resize(): void {
    if (!this.canvas || !this.context) return;
    const box = this.getBoundingClientRect();
    const ratio = Math.min(2, window.devicePixelRatio || 1);
    this.width = Math.max(1, box.width);
    this.height = Math.max(1, box.height);
    this.canvas.width = Math.round(this.width * ratio);
    this.canvas.height = Math.round(this.height * ratio);
    this.context.setTransform(ratio, 0, 0, ratio, 0, 0);
  }

  private draw(now: number): void {
    const context = this.context;
    if (!context) return;
    const { width, height } = this;
    const cell = Number.parseFloat(this.getAttribute('cell') ?? '') || 11;
    const cellWidth = cell * 0.78;
    const cols = Math.ceil(width / cellWidth);
    const rows = Math.ceil(height / cell);
    const ink = this.getAttribute('ink') || '#ededea';
    const accent = this.getAttribute('accent');
    const mask = this.getAttribute('mask') || 'none';
    const speed = Number.parseFloat(this.getAttribute('speed') ?? '1') || 1;
    const intensity = Number.parseFloat(this.getAttribute('intensity') ?? '1') || 1;
    const ramp = [...(this.getAttribute('chars') || ' .·:-=+*≡#%@')];
    const steps = ramp.length - 1;
    const t = ((now - this.started) / 1000) * 0.09 * speed;
    const scale = (5.2 / Math.max(cols, rows)) * 1.6;
    const hot: number[] = [];
    this.pull *= 0.96;
    context.clearRect(0, 0, width, height);
    context.font = `${cell}px ${this.font || 'ui-monospace, monospace'}`;
    context.textBaseline = 'top';
    context.fillStyle = ink;
    for (let y = 0; y < rows; y++) {
      for (let x = 0; x < cols; x++) {
        const X = x * cellWidth;
        const Y = y * cell;
        let nx = x * scale;
        let ny = y * scale * 1.25;
        if (this.pull > 0.02) {
          const dx = X - this.pointerX;
          const dy = Y - this.pointerY;
          const distance = dx * dx + dy * dy;
          if (distance < 22000) {
            const force = (1 - distance / 22000) * this.pull * 0.9;
            nx += -dy * force * 0.004;
            ny += dx * force * 0.004;
          }
        }
        const qx = fbm(nx + t * 0.7, ny - t * 0.35);
        const qy = fbm(nx + 5.2 - t * 0.4, ny + 1.3 + t * 0.5);
        const field = fbm(nx + 3.2 * qx + t * 0.3, ny + 3.2 * qy - t * 0.2);
        const u = x / cols;
        const v = y / rows;
        const edge = (qx - 0.5) * 0.55;
        let weight = 1;
        if (mask === 'right') weight = smooth(0.3, 0.72, u + edge);
        else if (mask === 'left') weight = smooth(0.3, 0.72, 1 - u + edge);
        else if (mask === 'bottom') weight = smooth(0.25, 0.85, v + edge);
        else if (mask === 'center') {
          const cx = u - 0.5;
          const cy = v - 0.5;
          weight = 1 - smooth(0.18, 0.55, Math.sqrt(cx * cx + cy * cy) + edge * 0.6);
        }
        const density = Math.min(1, Math.max(0, ((field - 0.32) / 0.42) * intensity)) * weight;
        const index = Math.round(density * steps);
        if (index <= 0) continue;
        if (accent && density > 0.86) {
          hot.push(X, Y, index);
          continue;
        }
        context.fillText(ramp[index] ?? '', X, Y);
      }
    }
    if (accent && hot.length > 0) {
      context.fillStyle = accent;
      for (let k = 0; k < hot.length; k += 3)
        context.fillText(ramp[hot[k + 2] ?? 0] ?? '', hot[k] ?? 0, hot[k + 1] ?? 0);
    }
  }
}

/** Defines <glyph-field> once; pages that show one call it. */
export function defineGlyphField(): void {
  if (!customElements.get('glyph-field')) customElements.define('glyph-field', GlyphField);
}
