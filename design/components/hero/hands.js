/*
 * <kro-hands> — two hands made of glyphs (hero, brief A8). Own procedural silhouette, Canvas 2D.
 * Attributes: progress="0..1" (scroll scrub: 0 = apart, ≥ touch = fingers meet), layout="h|v",
 * density="auto|low|high". Reads colours from CSS vars (--fg, --muted, --accent, --bg).
 * Events: "kro:link" (fingers touched, detail:{x,y}) and "kro:unlink".
 * Reduced motion: no fly-in, no cursor repulsion, no breathing. No canvas → renders nothing (SVG fallback lives in assets/svg).
 */
(() => {
  const TOUCH = 0.72;
  const DENSE = '$%#@&', MID = '?S%*+', LIGHT = '·:·.';
  const ALL = DENSE + MID + LIGHT;
  const clamp = (v, a, b) => Math.max(a, Math.min(b, v));
  const lerp = (a, b, t) => a + (b - a) * t;
  const easeOut = (t) => 1 - Math.pow(1 - t, 3);

  // Signed distance of a point to a capsule (segment with radius).
  function capsule(px, py, ax, ay, bx, by, r) {
    const pax = px - ax, pay = py - ay, bax = bx - ax, bay = by - ay;
    const h = clamp((pax * bax + pay * bay) / (bax * bax + bay * bay), 0, 1);
    return Math.hypot(pax - bax * h, pay - bay * h) - r;
  }
  function ellipse(px, py, cx, cy, rx, ry) {
    const k = Math.hypot((px - cx) / rx, (py - cy) / ry);
    return (k - 1) * Math.min(rx, ry);
  }
  // Hand in local space: wrist at x≈0, index fingertip at x≈0.98, y down = positive. Own silhouette (Adam-like: index
  // extended, other fingers curling down, thumb resting above). Returns SDF (<0 inside).
  function handSDF(x, y) {
    let d = capsule(x, y, -1.0, 0.03, -0.35, 0.01, 0.125); d = Math.min(d, capsule(x, y, -0.4, 0.01, 0.0, 0.0, 0.105)); // forearm, from behind the edge
    d = Math.min(d, ellipse(x, y, 0.2, 0.03, 0.28, 0.165)); // palm
    d = Math.min(d, capsule(x, y, 0.4, -0.06, 0.74, -0.05, 0.05)); d = Math.min(d, capsule(x, y, 0.74, -0.05, 0.98, 0.0, 0.044)); // index
    d = Math.min(d, capsule(x, y, 0.43, 0.03, 0.67, 0.09, 0.05)); d = Math.min(d, capsule(x, y, 0.67, 0.09, 0.70, 0.2, 0.044)); // middle
    d = Math.min(d, capsule(x, y, 0.41, 0.1, 0.58, 0.17, 0.047)); d = Math.min(d, capsule(x, y, 0.58, 0.17, 0.57, 0.27, 0.04)); // ring
    d = Math.min(d, capsule(x, y, 0.37, 0.15, 0.48, 0.22, 0.04)); d = Math.min(d, capsule(x, y, 0.48, 0.22, 0.45, 0.3, 0.035)); // pinky
    d = Math.min(d, capsule(x, y, 0.15, -0.1, 0.42, -0.17, 0.052)); // thumb
    return d;
  }
  function noise(x, y) { const s = Math.sin(x * 127.1 + y * 311.7) * 43758.5453; return s - Math.floor(s); }

  class KroHands extends HTMLElement {
    static get observedAttributes() { return ['progress', 'layout', 'density']; }
    constructor() {
      super();
      this.p = 0; this.layout = 'h'; this.parts = []; this.t0 = performance.now(); this.mouse = { x: -1e4, y: -1e4 };
      this.linked = false; this.visible = true; this.sprites = new Map(); this.fps = { last: 0, slow: 0 };
      this.reduced = matchMedia('(prefers-reduced-motion: reduce)').matches;
    }
    attributeChangedCallback(n, _o, v) {
      if (n === 'progress') this.p = clamp(parseFloat(v) || 0, 0, 1);
      if (n === 'layout') { this.layout = v === 'v' ? 'v' : 'h'; this.build(); }
      if (n === 'density') this.build();
    }
    connectedCallback() {
      this.style.display = 'block'; if (!this.style.position) this.style.position = 'absolute'; this.style.width = this.style.width || '100%'; this.style.height = this.style.height || '100%'; if (this.style.position === 'absolute' && !this.style.inset) this.style.inset = '0';
      this.canvas = document.createElement('canvas');
      this.canvas.style.cssText = 'position:absolute;inset:0;width:100%;height:100%;display:block';
      this.appendChild(this.canvas);
      this.ctx = this.canvas.getContext('2d');
      if (!this.ctx) return;
      this.ro = new ResizeObserver(() => this.resize()); this.ro.observe(this);
      this.io = new IntersectionObserver(([e]) => { this.visible = e.isIntersecting; if (this.visible) this.loop(); }); this.io.observe(this);
      this.onMove = (e) => { const r = this.getBoundingClientRect(); this.mouse.x = e.clientX - r.left; this.mouse.y = e.clientY - r.top; };
      this.onLeave = () => { this.mouse.x = -1e4; this.mouse.y = -1e4; };
      addEventListener('pointermove', this.onMove, { passive: true }); addEventListener('pointerleave', this.onLeave);
      this.themeObs = new MutationObserver(() => { this.sprites.clear(); this.readColors(); });
      this.themeObs.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
      this.resize(); this.loop();
    }
    disconnectedCallback() {
      this.ro?.disconnect(); this.io?.disconnect(); this.themeObs?.disconnect();
      removeEventListener('pointermove', this.onMove); removeEventListener('pointerleave', this.onLeave);
      cancelAnimationFrame(this.raf);
    }
    readColors() {
      const cs = getComputedStyle(this);
      this.colors = { fg: cs.getPropertyValue('--fg').trim() || '#111', muted: cs.getPropertyValue('--muted').trim() || '#777', accent: cs.getPropertyValue('--accent').trim() || '#6b4de6', bg: cs.getPropertyValue('--bg').trim() || '#eee' };
    }
    resize() {
      const r = this.getBoundingClientRect(); if (!r.width || !r.height) return;
      this.dpr = Math.min(devicePixelRatio || 1, 2); this.W = r.width; this.H = r.height;
      this.canvas.width = Math.round(this.W * this.dpr); this.canvas.height = Math.round(this.H * this.dpr);
      this.readColors(); this.sprites.clear(); this.build();
    }
    // Sample both hands into particles. Cell size drives density (desktop ≈ 6–8k, phone ≈ 2–3k).
    build() {
      if (!this.W) return;
      const v = this.layout === 'v';
      const density = this.getAttribute('density') || 'auto';
      const span = v ? this.H : this.W; // axis along which the hands reach
      let cell = density === 'high' ? 6 : density === 'low' ? 10 : (span < 700 ? 9 : 7.5);
      if (this.fps.slow > 2) cell += 2;
      const handLen = v ? Math.min(this.W * 0.6, this.H * 0.28) : Math.min(this.W * 0.36, this.H * 0.8); // px per local unit 1.0
      const parts = []; const t0 = 0;
      for (const side of [-1, 1]) {
        // local grid over x ∈ [-1.15, 1.05], y ∈ [-0.4, 0.4]; the hand is tilted so the arm rises toward the centre
        const step = cell / handLen, ca = Math.cos(-0.1), sa = Math.sin(-0.1);
        for (let y = -0.4; y <= 0.4; y += step) for (let x = -1.15; x <= 1.05; x += step) {
          const jx0 = x + (noise(x, y) - 0.5) * step * 0.3, jy0 = y + (noise(y, x) - 0.5) * step * 0.3;
          const jx = jx0 * ca - jy0 * sa, jy = jx0 * sa + jy0 * ca;
          const d = handSDF(jx0, jy0); if (d > 0) continue;
          const depth = clamp(-d / 0.06, 0, 1); // 0 at edge → 1 inside (fingers count as solid too)
          const shade = clamp(depth * 0.6 + (0.3 - jy0) * 0.5 + (noise(jx0 * 3, jy0 * 3) - 0.5) * 0.25, 0, 1);
          if (shade < 0.12 && noise(jy0, jx0) > 0.55) continue; // sparse rim
          const set = shade > 0.62 ? DENSE : shade > 0.32 ? MID : LIGHT;
          const glyph = set[Math.floor(noise(jx * 7, jy * 5) * set.length)];
          const size = shade > 0.62 ? 2 : shade > 0.32 ? 1 : 0;
          const alpha = 0.3 + shade * 0.6;
          parts.push({ side, lx: jx, ly: jy, glyph, size, alpha, delay: noise(jx * 11, jy * 13) * 0.7 + (1 - clamp(jx + 1.1, 0, 2.1) / 2.1) * 0.25, phase: noise(jx, jy * 9) * 6.28, ox: 0, oy: 0, vx: 0, vy: 0, cg: glyph, ct: t0 });
        }
      }
      this.parts = parts; this.handLen = handLen; this.t0 = performance.now();
    }
    // Where a hand's local (lx,ly) lands on the canvas at scroll progress p.
    place(side, lx, ly, p) {
      const v = this.layout === 'v', L = this.handLen;
      const pp = clamp(p / TOUCH, 0, 1);
      if (!v) {
        const gapApart = 0.17 * this.W, gapTouch = 0; // distance from centre to fingertip
        const tipOffset = lerp(gapApart, gapTouch, easeOut(pp));
        const cx = this.W / 2 + side * tipOffset; // fingertip (local x = 0.98) sits here
        const cy = this.H * 0.6 + side * this.H * 0.03 * (1 - pp);
        return { x: cx - side * (lx - 0.98) * L, y: cy + ly * L };
      }
      const gapApart = 0.2 * this.H, tipOffset = lerp(gapApart, 0, easeOut(pp));
      const cy = this.H / 2 + side * tipOffset;
      return { x: this.W / 2 - side * ly * L, y: cy - side * (lx - 0.98) * L };
    }
    sprite(glyph, size, tone) {
      const key = glyph + size + tone; let s = this.sprites.get(key); if (s) return s;
      const px = [8, 10, 13][size] * this.dpr, pad = 4 * this.dpr, c = document.createElement('canvas');
      c.width = c.height = px + pad * 2; const g = c.getContext('2d');
      g.font = `${size === 2 ? 600 : 400} ${px}px 'Fira Code', ui-monospace, monospace`; g.textAlign = 'center'; g.textBaseline = 'middle';
      g.fillStyle = tone === 'accent' ? this.colors.accent : this.colors.fg; g.fillText(glyph, c.width / 2, c.height / 2);
      this.sprites.set(key, s = c); return s;
    }
    loop() {
      cancelAnimationFrame(this.raf);
      const frame = (now) => {
        if (!this.visible) return;
        this.draw(now); this.raf = requestAnimationFrame(frame);
      }; this.raf = requestAnimationFrame(frame);
    }
    draw(now) {
      const { ctx, W, H, dpr, parts } = this; if (!ctx || !parts.length) return;
      if (this.fps.last) { const dt = now - this.fps.last; if (dt > 34) this.fps.slow++; else this.fps.slow = Math.max(0, this.fps.slow - 0.05); if (this.fps.slow > 60) { this.fps.slow = 0; this.setAttribute('density', 'low'); } }
      this.fps.last = now;
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0); ctx.clearRect(0, 0, W, H);
      const t = (now - this.t0) / 1000, p = this.p, reduced = this.reduced;
      const intro = reduced ? 1 : clamp(t / 1.6, 0, 1);
      const linked = p >= TOUCH;
      if (linked !== this.linked) { this.linked = linked; const c = this.contact(); this.dispatchEvent(new CustomEvent(linked ? 'kro:link' : 'kro:unlink', { bubbles: true, detail: c })); }
      const contact = this.contact();
      const wave = linked ? clamp((p - TOUCH) / (1 - TOUCH), 0, 1) : 0;
      const R = wave * Math.hypot(W, H) * 0.9;
      const mx = this.mouse.x, my = this.mouse.y, breathe = reduced ? 0 : 1;
      const v = this.layout === 'v';
      for (let i = 0; i < parts.length; i++) {
        const q = parts[i];
        const home = this.place(q.side, q.lx, q.ly, p);
        // fly-in from the edge (own delay + decode)
        let k = 1;
        if (intro < 1) { k = easeOut(clamp((t - q.delay) / 0.85, 0, 1)); if (k <= 0) continue; }
        let x = home.x, y = home.y;
        if (k < 1) {
          const sx = v ? home.x + (noise(q.lx, q.ly) - 0.5) * W * 0.3 : (q.side === -1 ? -W * 0.25 : W * 1.25);
          const sy = v ? (q.side === -1 ? -H * 0.3 : H * 1.3) : home.y + (noise(q.ly, q.lx) - 0.5) * H * 0.5;
          x = lerp(sx, home.x, k); y = lerp(sy, home.y, k);
          if (now - q.ct > 60) { q.cg = ALL[Math.floor(Math.random() * ALL.length)]; q.ct = now; }
        } else q.cg = q.glyph;
        // breathing + cursor repulsion (spring)
        x += Math.sin(t * 1.3 + q.phase) * 1.2 * breathe; y += Math.cos(t * 1.1 + q.phase) * 1.2 * breathe;
        if (!reduced) {
          const dx = x - mx, dy = y - my, dd = dx * dx + dy * dy;
          if (dd < 6400 && dd > 0.01) { const d = Math.sqrt(dd), f = (80 - d) / 80 * 14; q.vx += dx / d * f; q.vy += dy / d * f; }
          q.vx = (q.vx - q.ox * 0.08) * 0.82; q.vy = (q.vy - q.oy * 0.08) * 0.82; q.ox += q.vx * 0.25; q.oy += q.vy * 0.25;
          x += q.ox; y += q.oy;
        }
        // ripple from the contact point
        let tone = 'fg', a = q.alpha;
        if (wave > 0) { const d = Math.abs(Math.hypot(x - contact.x, y - contact.y) - R); if (d < 70) { tone = 'accent'; a = 1; } }
        const s = this.sprite(q.cg, q.size, tone);
        ctx.globalAlpha = a * (k < 1 ? 0.5 + 0.5 * k : 1);
        ctx.drawImage(s, x - s.width / dpr / 2, y - s.height / dpr / 2, s.width / dpr, s.height / dpr);
      }
      ctx.globalAlpha = 1;
      if (linked) this.sparks(ctx, contact, wave, t);
    }
    contact() { return this.place(-1, 0.98, 0.0, TOUCH); }
    // Touch: flash, then sparks that straighten into connector lines running to the next section.
    sparks(ctx, c, w, t) {
      const { W, H } = this, ac = this.colors.accent;
      const flash = clamp(1 - w * 4, 0, 1);
      if (flash > 0) { const g = ctx.createRadialGradient(c.x, c.y, 0, c.x, c.y, 90); g.addColorStop(0, ac); g.addColorStop(1, 'rgba(0,0,0,0)'); ctx.globalAlpha = flash * 0.9; ctx.fillStyle = g; ctx.fillRect(c.x - 90, c.y - 90, 180, 180); ctx.globalAlpha = 1; }
      ctx.strokeStyle = ac; ctx.lineWidth = 1; ctx.lineCap = 'round';
      const n = 7, fan = Math.min(W * 0.32, 420);
      for (let i = 0; i < n; i++) {
        const u = (i / (n - 1) - 0.5), seed = noise(i, 3), grow = clamp((w - seed * 0.25) / 0.6, 0, 1); if (grow <= 0) continue;
        const ex = c.x + u * fan, ey = H + 2, sx = c.x + u * 24 * (1 - grow) + Math.sin(t * 3 + i) * 2 * (1 - grow), sy = c.y + 8;
        ctx.globalAlpha = 0.35 + 0.65 * grow; ctx.beginPath(); ctx.moveTo(sx, sy);
        const my = lerp(sy, ey, 0.55);
        ctx.bezierCurveTo(sx, my, ex, my, ex, lerp(sy, ey, grow)); ctx.stroke();
        // the packet that runs down the line
        const pk = (t * 0.6 + seed) % 1; if (grow > 0.9) { const py = lerp(sy, ey, pk), px = pk < 0.55 ? lerp(sx, ex, easeOut(pk / 0.55)) : ex; ctx.fillStyle = ac; ctx.beginPath(); ctx.arc(px, py, 2.2, 0, 6.28); ctx.fill(); }
      }
      ctx.globalAlpha = 1;
    }
  }
  if (!customElements.get('kro-hands')) customElements.define('kro-hands', KroHands);
})();
