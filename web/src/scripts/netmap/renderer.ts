// Draws the map of /map with WebGL: land, networks, exchange points, bundles of links, the links of
// the core and a route — every layer from one vertex shader that places a point in two views at
// once and blends them, so that switching the view makes every network fly to its new place.
//
// No library: points and lines in plain WebGL 1, a few hundred lines. Everything is sized in CSS
// pixels and multiplied by the device pixel ratio here.

import type { View } from './projection.ts';
import { EQUAL_EARTH_WIDTH, MAP_SHIFT } from './projection.ts';

const VIEW_INDEX: Record<View, number> = { map: 0, core: 1, globe: 2 };

/** Colours of the current theme, from the CSS variables of the design. */
export interface Palette {
  dark: boolean;
  land: RGB;
  dot: RGB;
  dotDim: RGB;
  accent: RGB;
  accentLine: RGB;
  fg: RGB;
  ok: RGB;
}
export type RGB = [number, number, number];

/** What a frame shows. */
export interface Frame {
  width: number; // CSS pixels
  height: number;
  dpr: number;
  from: View;
  to: View;
  mix: number; // 0 — `from`, 1 — `to`
  zoom: number;
  panX: number;
  panY: number;
  lon0: number; // the side of the globe facing the viewer
  lat0: number;
  scales: [map: number, core: number, globe: number]; // pixels per world unit
  progress: number; // how much of the route is drawn, 0…1
  time: number; // seconds, for pulsing
  dim: number; // how bright the map behind a route is, 0…1
}

// Layers say in which views they show: geographic ones fade out in «core», the core links fade in.
const GEO = 0;
const CORE = 1;
const BOTH = 2;

const SHARED = `
precision highp float;
attribute vec2 aLonLat;
attribute float aCoreR;
attribute float aA;
attribute float aB;
uniform vec2 uViews;
uniform float uMix;
uniform vec2 uRot;
uniform vec3 uScales;
uniform float uZoom;
uniform vec2 uPan;
uniform vec2 uHalf;
uniform float uKind;
uniform float uLift;
varying float vAlpha;

vec2 equalEarth(vec2 ll) {
  float lam = radians(ll.x);
  float theta = asin(0.8660254 * sin(radians(ll.y)));
  float t2 = theta * theta;
  float t6 = t2 * t2 * t2;
  float x = lam * cos(theta) / (0.8660254 * (1.340264 + 3.0 * -0.081106 * t2 + t6 * (7.0 * 0.000893 + 9.0 * 0.003796 * t2)));
  float y = theta * (1.340264 - 0.081106 * t2 + t6 * (0.000893 + 0.003796 * t2));
  return vec2(x, y) / ${EQUAL_EARTH_WIDTH.toFixed(4)} - vec2(0.0, ${MAP_SHIFT.toFixed(4)});
}

vec3 place(float view, vec2 ll, float coreR, float lift) {
  if (view < 0.5) return vec3(equalEarth(ll), 1.0);
  if (view < 1.5) {
    float a = radians(ll.x);
    float r = coreR < 0.0 ? 1.12 : coreR;
    return vec3(r * sin(a), r * cos(a), 1.0);
  }
  float phi = radians(ll.y);
  float d = radians(ll.x) - uRot.x;
  float x = cos(phi) * sin(d);
  float y = cos(uRot.y) * sin(phi) - sin(uRot.y) * cos(phi) * cos(d);
  float z = sin(uRot.y) * sin(phi) + cos(uRot.y) * cos(phi) * cos(d);
  return vec3(vec2(x, y) * (1.0 + lift), smoothstep(-0.02, 0.06, z));
}

float scaleOf(float view) {
  return view < 0.5 ? uScales.x : (view < 1.5 ? uScales.y : uScales.z);
}

float isCore(float view) { return view > 0.5 && view < 1.5 ? 1.0 : 0.0; }

vec4 project(float lift) {
  vec3 a = place(uViews.x, aLonLat, aCoreR, lift);
  vec3 b = place(uViews.y, aLonLat, aCoreR, lift);
  vec2 px = mix(a.xy, b.xy, uMix) * mix(scaleOf(uViews.x), scaleOf(uViews.y), uMix) * uZoom + uPan;
  float coreness = mix(isCore(uViews.x), isCore(uViews.y), uMix);
  float layer = uKind < 0.5 ? 1.0 - coreness : (uKind < 1.5 ? coreness : 1.0);
  vAlpha = mix(a.z, b.z, uMix) * layer;
  return vec4(px / uHalf, 0.0, 1.0);
}
`;

// Points: aA is the size in CSS pixels, aB the brightness 0…1 (or, for the route, its place 0…1).
const POINT_VERTEX = `${SHARED}
uniform float uDpr;
uniform float uGrow;
varying float vB;
void main() {
  gl_Position = project(0.0);
  gl_PointSize = aA * uDpr * mix(1.0, sqrt(uZoom), uGrow);
  vB = aB;
}`;

const POINT_FRAGMENT = `
precision mediump float;
uniform vec4 uColor;
uniform float uMode;
uniform float uProgress;
uniform float uTime;
varying float vAlpha;
varying float vB;
void main() {
  float r = length(gl_PointCoord - 0.5) * 2.0;
  float shape;
  float bright = vB;
  if (uMode < 0.5) {
    shape = 1.0 - smoothstep(0.6, 1.0, r);
    bright = mix(0.35, 1.0, vB);
  } else if (uMode < 1.5) {
    shape = smoothstep(0.5, 0.68, r) * (1.0 - smoothstep(0.84, 1.0, r));
    bright = mix(0.45, 1.0, vB);
  } else if (uMode < 2.5) {
    // the route: what the packet has passed glows, the rest waits faintly
    shape = exp(-r * r * 3.5);
    bright = vB <= uProgress ? 1.0 : 0.16;
  } else {
    // markers of the route: pulse when the packet reaches them
    float wave = fract(uTime * 0.8 - vB);
    shape = smoothstep(0.5, 0.66, r) * (1.0 - smoothstep(0.82, 1.0, r)) + (1.0 - smoothstep(0.0, 0.34, r));
    bright = vB <= uProgress + 0.001 ? 0.75 + 0.25 * (1.0 - wave) : 0.25;
  }
  float alpha = uColor.a * shape * bright * vAlpha;
  if (alpha < 0.003) discard;
  gl_FragColor = vec4(uColor.rgb * alpha, alpha);
}`;

// Lines: aA is the place along the arc 0…1 (for the lift on the globe), aB the opacity.
const LINE_VERTEX = `${SHARED}
varying float vB;
void main() {
  gl_Position = project(uLift * sin(3.14159265 * aA));
  vB = aB;
}`;

const LINE_FRAGMENT = `
precision mediump float;
uniform vec4 uColor;
varying float vAlpha;
varying float vB;
void main() {
  float alpha = uColor.a * vB * vAlpha;
  if (alpha < 0.002) discard;
  gl_FragColor = vec4(uColor.rgb * alpha, alpha);
}`;

/** A layer: a buffer of vertices, each [lon, lat, coreR, a, b]. */
interface Layer {
  buffer: WebGLBuffer;
  count: number;
}

const STRIDE = 5 * 4;

export class Renderer {
  readonly canvas: HTMLCanvasElement;
  readonly gl: WebGLRenderingContext;
  private points: Program;
  private lines: Program;
  private layers = new Map<string, Layer>();
  private palette: Palette | null = null;

  constructor(canvas: HTMLCanvasElement) {
    this.canvas = canvas;
    const gl = canvas.getContext('webgl', {
      antialias: true,
      alpha: true,
      premultipliedAlpha: true,
      preserveDrawingBuffer: false,
    });
    if (!gl) throw new Error('WebGL is not available');
    this.gl = gl;
    this.points = new Program(gl, POINT_VERTEX, POINT_FRAGMENT);
    this.lines = new Program(gl, LINE_VERTEX, LINE_FRAGMENT);
  }

  /** Puts vertices into a layer, replacing what it held. */
  setLayer(name: string, vertices: Float32Array): void {
    const { gl } = this;
    let layer = this.layers.get(name);
    if (!layer) {
      const buffer = gl.createBuffer();
      if (!buffer) throw new Error('WebGL: no buffer');
      layer = { buffer, count: 0 };
      this.layers.set(name, layer);
    }
    gl.bindBuffer(gl.ARRAY_BUFFER, layer.buffer);
    gl.bufferData(gl.ARRAY_BUFFER, vertices, gl.STATIC_DRAW);
    layer.count = vertices.length / 5;
  }

  clearLayer(name: string): void {
    const layer = this.layers.get(name);
    if (layer) layer.count = 0;
  }

  setPalette(palette: Palette): void {
    this.palette = palette;
  }

  draw(frame: Frame): void {
    const { gl, canvas, palette } = this;
    if (!palette) return;
    const w = Math.max(1, Math.round(frame.width * frame.dpr));
    const h = Math.max(1, Math.round(frame.height * frame.dpr));
    if (canvas.width !== w || canvas.height !== h) {
      canvas.width = w;
      canvas.height = h;
    }
    gl.viewport(0, 0, w, h);
    gl.clearColor(0, 0, 0, 0);
    gl.clear(gl.COLOR_BUFFER_BIT);
    gl.enable(gl.BLEND);
    // Dark theme: light adds up and crossings glow; light theme: ink covers ink.
    if (palette.dark) gl.blendFunc(gl.ONE, gl.ONE);
    else gl.blendFunc(gl.ONE, gl.ONE_MINUS_SRC_ALPHA);

    const common = (program: Program, kind: number) => {
      program.use();
      program.set2('uViews', VIEW_INDEX[frame.from], VIEW_INDEX[frame.to]);
      program.set1('uMix', frame.mix);
      program.set2('uRot', (frame.lon0 * Math.PI) / 180, (frame.lat0 * Math.PI) / 180);
      program.set3('uScales', ...frame.scales);
      program.set1('uZoom', frame.zoom);
      program.set2('uPan', frame.panX, frame.panY);
      program.set2('uHalf', frame.width / 2, frame.height / 2);
      program.set1('uKind', kind);
    };
    // Behind a route everything else steps back.
    const back = frame.dim;

    // Land, the grid of the world.
    this.drawPoints('land', frame, GEO, palette.land, Math.max(back, 0.6), 0, 0, common);
    // Bundles of links between places, arcs lifted over the globe.
    this.drawLines('bundles', GEO, palette.accent, (palette.dark ? 0.8 : 0.7) * back, 0.12, common);
    // Links among the largest networks, for «core».
    this.drawLines('core', CORE, palette.accent, (palette.dark ? 0.16 : 0.22) * back, 0, common);
    // Links of the chosen network.
    this.drawLines('focus', BOTH, palette.accentLine, 0.9, 0.12, common);
    // Exchange points: rings.
    this.drawPoints(
      'exchanges',
      frame,
      GEO,
      palette.accentLine,
      (palette.dark ? 0.55 : 0.5) * back,
      1,
      0.5,
      common,
    );
    // Networks.
    this.drawPoints(
      'nodes',
      frame,
      BOTH,
      palette.dot,
      (palette.dark ? 0.85 : 0.9) * Math.max(back, 0.5),
      0,
      0.35,
      common,
    );
    // The route: a trail of light, the places it passes, the packet — along the arcs on the map
    // and the globe, from network to network in the core.
    this.drawPoints('route', frame, GEO, palette.accent, 1, 2, 0, common);
    this.drawPoints('routeCore', frame, CORE, palette.accent, 1, 2, 0, common);
    this.drawPoints('stops', frame, GEO, palette.fg, 1, 3, 0, common);
    this.drawPoints('stopsCore', frame, CORE, palette.fg, 1, 3, 0, common);
    this.drawPoints('packet', frame, GEO, palette.fg, 1, 2, 0, common);
    this.drawPoints('packetCore', frame, CORE, palette.fg, 1, 2, 0, common);
  }

  private drawPoints(
    name: string,
    frame: Frame,
    kind: number,
    color: RGB,
    alpha: number,
    mode: number,
    grow: number,
    common: (program: Program, kind: number) => void,
  ): void {
    const layer = this.layers.get(name);
    if (!layer || layer.count === 0) return;
    const program = this.points;
    common(program, kind);
    program.set1('uDpr', frame.dpr);
    program.set1('uGrow', grow);
    program.set4('uColor', color[0], color[1], color[2], alpha);
    program.set1('uMode', mode);
    program.set1('uProgress', name.startsWith('packet') ? 2 : frame.progress);
    program.set1('uTime', frame.time);
    program.bind(layer.buffer);
    this.gl.drawArrays(this.gl.POINTS, 0, layer.count);
  }

  private drawLines(
    name: string,
    kind: number,
    color: RGB,
    alpha: number,
    lift: number,
    common: (program: Program, kind: number) => void,
  ): void {
    const layer = this.layers.get(name);
    if (!layer || layer.count === 0) return;
    const program = this.lines;
    common(program, kind);
    program.set1('uLift', lift);
    program.set4('uColor', color[0], color[1], color[2], alpha);
    program.bind(layer.buffer);
    this.gl.drawArrays(this.gl.LINES, 0, layer.count);
  }

  dispose(): void {
    for (const layer of this.layers.values()) this.gl.deleteBuffer(layer.buffer);
    this.layers.clear();
  }
}

/** A compiled program with its attributes and uniforms. */
class Program {
  private gl: WebGLRenderingContext;
  private program: WebGLProgram;
  private attributes: number[];
  private uniforms = new Map<string, WebGLUniformLocation | null>();

  constructor(gl: WebGLRenderingContext, vertex: string, fragment: string) {
    this.gl = gl;
    const program = gl.createProgram();
    if (!program) throw new Error('WebGL: no program');
    gl.attachShader(program, shader(gl, gl.VERTEX_SHADER, vertex));
    gl.attachShader(program, shader(gl, gl.FRAGMENT_SHADER, fragment));
    gl.linkProgram(program);
    if (!gl.getProgramParameter(program, gl.LINK_STATUS)) {
      throw new Error(`WebGL link: ${gl.getProgramInfoLog(program) ?? ''}`);
    }
    this.program = program;
    this.attributes = ['aLonLat', 'aCoreR', 'aA', 'aB'].map((name) =>
      gl.getAttribLocation(program, name),
    );
  }

  use(): void {
    this.gl.useProgram(this.program);
  }

  bind(buffer: WebGLBuffer): void {
    const { gl } = this;
    gl.bindBuffer(gl.ARRAY_BUFFER, buffer);
    const [lonLat, coreR, a, b] = this.attributes as [number, number, number, number];
    const attribute = (location: number, size: number, offset: number) => {
      if (location < 0) return;
      gl.enableVertexAttribArray(location);
      gl.vertexAttribPointer(location, size, gl.FLOAT, false, STRIDE, offset);
    };
    attribute(lonLat, 2, 0);
    attribute(coreR, 1, 8);
    attribute(a, 1, 12);
    attribute(b, 1, 16);
  }

  private location(name: string): WebGLUniformLocation | null {
    if (!this.uniforms.has(name)) {
      this.uniforms.set(name, this.gl.getUniformLocation(this.program, name));
    }
    return this.uniforms.get(name) ?? null;
  }

  set1(name: string, x: number): void {
    this.gl.uniform1f(this.location(name), x);
  }

  set2(name: string, x: number, y: number): void {
    this.gl.uniform2f(this.location(name), x, y);
  }

  set3(name: string, x: number, y: number, z: number): void {
    this.gl.uniform3f(this.location(name), x, y, z);
  }

  set4(name: string, x: number, y: number, z: number, w: number): void {
    this.gl.uniform4f(this.location(name), x, y, z, w);
  }
}

function shader(gl: WebGLRenderingContext, type: number, source: string): WebGLShader {
  const compiled = gl.createShader(type);
  if (!compiled) throw new Error('WebGL: no shader');
  gl.shaderSource(compiled, source);
  gl.compileShader(compiled);
  if (!gl.getShaderParameter(compiled, gl.COMPILE_STATUS)) {
    throw new Error(`WebGL shader: ${gl.getShaderInfoLog(compiled) ?? ''}`);
  }
  return compiled;
}

/** A colour of the design, «#9d86f0» or «rgb(…)», as three numbers 0…1. */
export function parseColor(value: string): RGB {
  const text = value.trim();
  const hex = /^#([0-9a-f]{6})$/i.exec(text)?.[1];
  if (hex) {
    const n = parseInt(hex, 16);
    return [((n >> 16) & 255) / 255, ((n >> 8) & 255) / 255, (n & 255) / 255];
  }
  const rgb = /rgba?\(\s*([\d.]+)[\s,]+([\d.]+)[\s,]+([\d.]+)/i.exec(text);
  if (rgb) return [Number(rgb[1]) / 255, Number(rgb[2]) / 255, Number(rgb[3]) / 255];
  return [0.5, 0.5, 0.5];
}
