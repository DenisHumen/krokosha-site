// <glyph-field>: a field of monospace glyphs that drifts like smoke (design v3). It only decorates:
// without scripts the element is empty, and nothing on the page depends on it.
//
// Attributes: ink (colour), accent (colour of the densest cells), cell (px), mask (none | right |
// left | center | bottom), speed (1 = default), intensity (0…1.5), chars (a ramp from empty to
// dense), fps (frames per second, 30 by default), still (present: one frame, no motion).
//
// It costs a core when it runs at full speed over a whole screen, so it draws at most `fps` frames
// a second, stops while it is off screen or the tab is hidden, and draws a single frame when the
// system asks for reduced motion. Styles are set through the CSSOM, which the admin area's
// Content-Security-Policy allows (it forbids only style="…" in the markup).
(function () {
  'use strict';
  if (!window.customElements || customElements.get('glyph-field')) return;

  var P = new Uint16Array(512);
  var G = new Float32Array(256);
  var seed = 20260924;
  function rnd() {
    seed = (seed * 16807) % 2147483647;
    return seed / 2147483647;
  }
  var perm = [];
  for (var i = 0; i < 256; i++) perm.push(i);
  for (i = 255; i > 0; i--) {
    var j = Math.floor(rnd() * (i + 1));
    var swap = perm[i];
    perm[i] = perm[j];
    perm[j] = swap;
  }
  for (i = 0; i < 512; i++) P[i] = perm[i & 255];
  for (i = 0; i < 256; i++) G[i] = rnd();

  function hash(x, y) {
    return G[P[P[x & 255] + (y & 255)]];
  }
  function noise(x, y) {
    var xi = Math.floor(x);
    var yi = Math.floor(y);
    var xf = x - xi;
    var yf = y - yi;
    var u = xf * xf * (3 - 2 * xf);
    var v = yf * yf * (3 - 2 * yf);
    var a = hash(xi, yi);
    var b = hash(xi + 1, yi);
    var c = hash(xi, yi + 1);
    var d = hash(xi + 1, yi + 1);
    return a + (b - a) * u + (c - a) * v + (a - b - c + d) * u * v;
  }
  function fbm(x, y) {
    var sum = 0;
    var amplitude = 0.5;
    for (var octave = 0; octave < 4; octave++) {
      sum += amplitude * noise(x, y);
      var nx = x * 1.6 - y * 1.2;
      var ny = x * 1.2 + y * 1.6;
      x = nx + 3.1;
      y = ny + 1.7;
      amplitude *= 0.5;
    }
    return sum / 0.9375;
  }
  function smooth(a, b, x) {
    var t = Math.min(1, Math.max(0, (x - a) / (b - a)));
    return t * t * (3 - 2 * t);
  }

  var reducedMotion = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)');

  class GlyphField extends HTMLElement {
    static get observedAttributes() {
      return ['ink', 'accent', 'cell', 'mask', 'speed', 'intensity', 'chars', 'still', 'fps'];
    }
  }

  GlyphField.prototype.connectedCallback = function () {
    var self = this;
    this.style.display = 'block';
    // The page places the field (a fixed backdrop, a panel filled edge to edge); a field placed
    // nowhere still needs a box of its own for the canvas.
    if (getComputedStyle(this).position === 'static') this.style.position = 'relative';
    this.canvas = document.createElement('canvas');
    this.canvas.setAttribute('aria-hidden', 'true');
    Object.assign(this.canvas.style, { position: 'absolute', inset: '0', width: '100%', height: '100%', display: 'block' });
    this.appendChild(this.canvas);
    this.context = this.canvas.getContext('2d');
    this.pointerX = -9999;
    this.pointerY = -9999;
    this.pull = 0;
    this.onMove = function (event) {
      var box = self.getBoundingClientRect();
      self.pointerX = event.clientX - box.left;
      self.pointerY = event.clientY - box.top;
      self.pull = 1;
    };
    window.addEventListener('pointermove', this.onMove, { passive: true });
    this.resizer = new ResizeObserver(function () {
      self.resize();
      if (self.isStill()) self.draw(self.started + 8000);
    });
    this.resizer.observe(this);
    this.onScreen = true;
    this.watcher = new IntersectionObserver(function (entries) {
      self.onScreen = entries[0].isIntersecting;
    });
    this.watcher.observe(this);
    this.started = performance.now();
    this.last = 0;
    this.loop = function (now) {
      self.frame = requestAnimationFrame(self.loop);
      if (!self.onScreen || document.hidden || now - self.last < self.interval()) return;
      self.last = now;
      self.draw(now);
    };
    this.resize();
    this.start();
  };

  GlyphField.prototype.disconnectedCallback = function () {
    cancelAnimationFrame(this.frame);
    this.frame = 0;
    if (this.resizer) this.resizer.disconnect();
    if (this.watcher) this.watcher.disconnect();
    window.removeEventListener('pointermove', this.onMove);
  };

  GlyphField.prototype.attributeChangedCallback = function () {
    if (!this.context) return;
    this.resize();
    this.start();
  };

  GlyphField.prototype.isStill = function () {
    return this.hasAttribute('still') || (reducedMotion && reducedMotion.matches);
  };

  GlyphField.prototype.interval = function () {
    var fps = parseFloat(this.getAttribute('fps') || '30');
    return 1000 / Math.min(60, Math.max(1, fps || 30));
  };

  GlyphField.prototype.start = function () {
    cancelAnimationFrame(this.frame);
    this.frame = 0;
    if (this.isStill()) {
      this.draw(this.started + 8000);
      return;
    }
    this.frame = requestAnimationFrame(this.loop);
  };

  GlyphField.prototype.resize = function () {
    var box = this.getBoundingClientRect();
    var ratio = Math.min(2, window.devicePixelRatio || 1);
    this.width = Math.max(1, box.width);
    this.height = Math.max(1, box.height);
    this.canvas.width = Math.round(this.width * ratio);
    this.canvas.height = Math.round(this.height * ratio);
    this.context.setTransform(ratio, 0, 0, ratio, 0, 0);
  };

  GlyphField.prototype.draw = function (now) {
    var context = this.context;
    var width = this.width;
    var height = this.height;
    var cell = parseFloat(this.getAttribute('cell')) || 11;
    var cellWidth = cell * 0.78;
    var cols = Math.ceil(width / cellWidth);
    var rows = Math.ceil(height / cell);
    var ink = this.getAttribute('ink') || '#ededea';
    var accent = this.getAttribute('accent');
    var mask = this.getAttribute('mask') || 'none';
    var speed = parseFloat(this.getAttribute('speed') || '1');
    var intensity = parseFloat(this.getAttribute('intensity') || '1');
    var ramp = this.getAttribute('chars') || ' .·:-=+*≡#%@';
    var steps = ramp.length - 1;
    var t = ((now - this.started) / 1000) * 0.09 * speed;
    var scale = (5.2 / Math.max(cols, rows)) * 1.6;
    var hot = [];
    this.pull *= 0.96;
    context.clearRect(0, 0, width, height);
    context.font = cell + 'px "Fira Code", ui-monospace, monospace';
    context.textBaseline = 'top';
    context.fillStyle = ink;
    for (var y = 0; y < rows; y++) {
      for (var x = 0; x < cols; x++) {
        var X = x * cellWidth;
        var Y = y * cell;
        var nx = x * scale;
        var ny = y * scale * 1.25;
        if (this.pull > 0.02) {
          var dx = X - this.pointerX;
          var dy = Y - this.pointerY;
          var distance = dx * dx + dy * dy;
          if (distance < 22000) {
            var force = (1 - distance / 22000) * this.pull * 0.9;
            nx += -dy * force * 0.004;
            ny += dx * force * 0.004;
          }
        }
        var qx = fbm(nx + t * 0.7, ny - t * 0.35);
        var qy = fbm(nx + 5.2 - t * 0.4, ny + 1.3 + t * 0.5);
        var field = fbm(nx + 3.2 * qx + t * 0.3, ny + 3.2 * qy - t * 0.2);
        var weight = 1;
        var u = x / cols;
        var v = y / rows;
        var edge = (qx - 0.5) * 0.55;
        if (mask === 'right') weight = smooth(0.3, 0.72, u + edge);
        else if (mask === 'left') weight = smooth(0.3, 0.72, 1 - u + edge);
        else if (mask === 'bottom') weight = smooth(0.25, 0.85, v + edge);
        else if (mask === 'center') {
          var cx = u - 0.5;
          var cy = v - 0.5;
          weight = 1 - smooth(0.18, 0.55, Math.sqrt(cx * cx + cy * cy) + edge * 0.6);
        }
        var density = Math.min(1, Math.max(0, ((field - 0.32) / 0.42) * intensity)) * weight;
        var index = Math.round(density * steps);
        if (index <= 0) continue;
        if (accent && density > 0.86) {
          hot.push(X, Y, index);
          continue;
        }
        context.fillText(ramp[index], X, Y);
      }
    }
    if (accent && hot.length) {
      context.fillStyle = accent;
      for (var k = 0; k < hot.length; k += 3) context.fillText(ramp[hot[k + 2]], hot[k], hot[k + 1]);
    }
  };

  customElements.define('glyph-field', GlyphField);
})();
