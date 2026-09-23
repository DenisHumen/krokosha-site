// The /map page: loads the map of the internet, draws it in three views, answers the pointer and
// the keyboard, and traces routes. The page itself is static; everything that moves is here.

import { ApiError, getJSON, type NetworkJSON, type RouteJSON } from './api.ts';
import { readLand, readOverview, type Manifest, type Overview } from './data.ts';
import {
  bundleVertices,
  coreVertices,
  exchangeVertices,
  focusVertices,
  landVertices,
  nodeVertices,
  packetAt,
  places,
  routeTrails,
  type Places,
  type Trails,
} from './layers.ts';
import { renderNetwork, renderRoute, type MapTexts } from './panel.ts';
import {
  equalEarth,
  HOME,
  MAP_HEIGHT,
  toScreen,
  viewScale,
  VIEWS,
  type Camera,
  type View,
} from './projection.ts';
import { parseColor, Renderer, type Palette } from './renderer.ts';

const MORPH_MS = 1200;
const CAMERA_MS = 900;
const MIN_ZOOM = 0.8;
const MAX_ZOOM = 40;
const HIT_PX = 9;

export function initMap(): void {
  const root = document.querySelector<HTMLElement>('[data-netmap]');
  if (root) void start(root);
}

interface Elements {
  stage: HTMLElement;
  canvas: HTMLCanvasElement;
  status: HTMLElement;
  tip: HTMLElement;
  labels: HTMLElement;
  halo: HTMLElement;
  form: HTMLFormElement;
  from: HTMLInputElement;
  to: HTMLInputElement;
  mine: HTMLElement;
  result: HTMLElement;
  card: HTMLElement;
  cardBody: HTMLElement;
  views: HTMLButtonElement[];
  hint: HTMLElement | null;
}

function find(root: HTMLElement): Elements | null {
  const q = <T extends Element>(selector: string) => root.querySelector<T>(selector);
  const stage = q<HTMLElement>('[data-map-stage]');
  const canvas = q<HTMLCanvasElement>('[data-map-canvas]');
  const status = q<HTMLElement>('[data-map-status]');
  const tip = q<HTMLElement>('[data-map-tip]');
  const labels = q<HTMLElement>('[data-map-labels]');
  const halo = q<HTMLElement>('[data-map-halo]');
  const form = q<HTMLFormElement>('[data-route-form]');
  const from = q<HTMLInputElement>('[name="from"]');
  const to = q<HTMLInputElement>('[name="to"]');
  const mine = q<HTMLElement>('[data-route-mine]');
  const result = q<HTMLElement>('[data-route-result]');
  const card = q<HTMLElement>('[data-network-card]');
  const cardBody = q<HTMLElement>('[data-network-body]');
  if (!stage || !canvas || !status || !tip || !labels || !halo || !form || !from || !to || !mine)
    return null;
  if (!result || !card || !cardBody) return null;
  return {
    stage,
    canvas,
    status,
    tip,
    labels,
    halo,
    form,
    from,
    to,
    mine,
    result,
    card,
    cardBody,
    views: [...root.querySelectorAll<HTMLButtonElement>('[data-view]')],
    hint: q<HTMLElement>('[data-view-hint]'),
  };
}

async function start(root: HTMLElement): Promise<void> {
  const found = find(root);
  const textsNode = root.querySelector('script[type="application/json"][data-map-texts]');
  if (!found || !textsNode?.textContent) return;
  const el: Elements = found; // not narrowed: the functions below may be called before any check
  const texts = JSON.parse(textsNode.textContent) as MapTexts;
  const locale = document.documentElement.lang || 'en';
  const reduced = matchMedia('(prefers-reduced-motion: reduce)');

  // --- drawing -------------------------------------------------------------------------------
  let renderer: Renderer | null = null;
  try {
    renderer = new Renderer(el.canvas);
  } catch {
    el.stage.classList.add('is-flat');
    el.status.textContent = texts.no_webgl;
  }

  let overview: Overview | null = null;
  let at: Places | null = null;
  let trails: Trails | null = null;
  let routeStartedAt = 0;
  let routeDuration = 0;
  let progress = 1;

  let from: View = 'map';
  let to: View = 'map';
  let mix = 1;
  let morphStarted = 0;
  const camera: Camera = { ...HOME };
  let cameraTween: { from: Camera; to: Camera; started: number } | null = null;
  let dragging = false;
  let lastInteraction = 0;
  let raf = 0;
  let visible = true;
  let started = performance.now();
  // Where every network is on the screen, for the pointer: rebuilt when the camera moved.
  let hitsDirty = true;
  let hitX = new Float32Array(0);
  let hitY = new Float32Array(0);
  const grid = new Map<number, number[]>();
  // The places of the current route and their labels.
  let stops: { lon: number; lat: number; label: string }[] = [];
  const labelNodes: HTMLElement[] = [];

  const size = () => {
    const box = el.canvas.getBoundingClientRect();
    return { width: Math.max(1, box.width), height: Math.max(1, box.height) };
  };

  const readPalette = (): Palette => {
    const style = getComputedStyle(document.documentElement);
    const color = (name: string) => parseColor(style.getPropertyValue(name));
    const bg = color('--bg');
    const dark = bg[0] + bg[1] + bg[2] < 1.5;
    const dot = color('--dot');
    const dim = color('--dot-dim');
    // The land: the unlit dots of the design, a little lighter on the dark theme to be seen at all.
    const share = dark ? 0.16 : 0;
    return {
      dark,
      land: [
        dim[0] + (dot[0] - dim[0]) * share,
        dim[1] + (dot[1] - dim[1]) * share,
        dim[2] + (dot[2] - dim[2]) * share,
      ],
      dot,
      dotDim: color('--dot-dim'),
      accent: color('--accent'),
      accentLine: color('--accent-line'),
      fg: color('--fg'),
      ok: color('--ok'),
    };
  };
  renderer?.setPalette(readPalette());

  const wake = () => {
    if (!raf && visible) raf = requestAnimationFrame(frame);
  };

  function frame(now: number): void {
    raf = 0;
    let busy = false;
    if (mix < 1) {
      mix = reduced.matches ? 1 : Math.min(1, (now - morphStarted) / MORPH_MS);
      if (mix >= 1) from = to;
      busy = true;
    }
    if (cameraTween) {
      const k = reduced.matches ? 1 : Math.min(1, (now - cameraTween.started) / CAMERA_MS);
      const e = ease(k);
      camera.zoom = lerp(cameraTween.from.zoom, cameraTween.to.zoom, e);
      camera.panX = lerp(cameraTween.from.panX, cameraTween.to.panX, e);
      camera.panY = lerp(cameraTween.from.panY, cameraTween.to.panY, e);
      camera.lon0 = lerpAngle(cameraTween.from.lon0, cameraTween.to.lon0, e);
      camera.lat0 = lerp(cameraTween.from.lat0, cameraTween.to.lat0, e);
      if (k >= 1) cameraTween = null;
      busy = true;
      hitsDirty = true;
    }
    if (trails && progress < 1) {
      progress = reduced.matches ? 1 : Math.min(1, (now - routeStartedAt) / routeDuration);
      renderer?.setLayer('packet', packetAt(trails.geo, progress));
      renderer?.setLayer('packetCore', packetAt(trails.core, progress));
      busy = true;
    }
    // The globe turns by itself while nobody touches it.
    if (
      to === 'globe' &&
      !dragging &&
      !reduced.matches &&
      now - lastInteraction > 2500 &&
      !cameraTween
    ) {
      camera.lon0 -= 0.012 * 16;
      hitsDirty = true;
      busy = true;
    }
    // The places of a route pulse for a while after the packet arrives.
    if (trails && now - routeStartedAt < routeDuration + 6000) busy = true;
    draw(now);
    if (busy) wake();
  }

  function draw(now = performance.now()): void {
    const { width, height } = size();
    renderer?.draw({
      width,
      height,
      dpr: Math.min(window.devicePixelRatio || 1, 2),
      from,
      to,
      mix,
      zoom: camera.zoom,
      panX: camera.panX,
      panY: camera.panY,
      lon0: camera.lon0,
      lat0: camera.lat0,
      scales: [
        viewScale('map', width, height),
        viewScale('core', width, height),
        viewScale('globe', width, height),
      ],
      progress,
      time: (now - started) / 1000,
      dim: trails ? 0.4 : 1,
    });
    // The halo of the globe, drawn by CSS behind the canvas.
    const globeness = (from === 'globe' ? 1 - mix : 0) + (to === 'globe' ? mix : 0);
    const radius = viewScale('globe', width, height) * camera.zoom;
    el.halo.style.opacity = String(globeness);
    el.halo.style.width = el.halo.style.height = `${radius * 2}px`;
    el.halo.style.transform = `translate(${width / 2 + camera.panX - radius}px, ${height / 2 - camera.panY - radius}px)`;
    placeLabels();
  }

  // --- data ----------------------------------------------------------------------------------
  // Without WebGL the words why stay: the data still comes, for the totals and the routes.
  el.status.textContent = renderer ? texts.loading : texts.no_webgl;
  // Who is looking: asked at once, used for the form and, on a phone, for where the map opens.
  const whoAmI = getJSON<{
    ip: string;
    asn: number;
    name: string;
    lat: number | null;
    lon: number | null;
  }>('/api/net/me').catch(() => null);
  try {
    const [landBuffer, manifest] = await Promise.all([
      fetch('/netmap/land.bin').then((r) =>
        r.ok ? r.arrayBuffer() : Promise.reject(new Error('land')),
      ),
      getJSON<Manifest>('/netmap/data/overview.json'),
    ]);
    renderer?.setLayer('land', landVertices(readLand(landBuffer)));
    const response = await fetch(`/netmap/data/${encodeURIComponent(manifest.file)}`);
    if (!response.ok) throw new Error('overview');
    overview = readOverview(await response.arrayBuffer());
    at = places(overview);
    renderer?.setLayer('nodes', nodeVertices(overview, at));
    renderer?.setLayer('exchanges', exchangeVertices(overview));
    renderer?.setLayer('bundles', bundleVertices(overview));
    renderer?.setLayer('core', coreVertices(overview, at));
    fillStats(root, overview, locale);
    if (renderer) el.status.textContent = '';
    root.classList.add('is-ready');
  } catch {
    el.status.textContent = texts.no_data;
  }
  const visitor = await whoAmI;
  // A narrow screen shows the world too small to read: the map opens larger, on the visitor's side
  // of it (or Europe).
  {
    const { width, height } = size();
    if (width < 700) {
      const scale = viewScale('map', width, height);
      const zoom = clamp((height * 0.7) / (MAP_HEIGHT * scale), 1, 3.2);
      const [x, y] = equalEarth(visitor?.lon ?? 15, visitor?.lat ?? 50);
      camera.zoom = zoom;
      camera.panX = -x * scale * zoom;
      camera.panY = -y * scale * zoom;
    }
  }
  wake();

  // --- the pointer and the keyboard ----------------------------------------------------------
  const currentView = (): View => to;

  const zoomAt = (factor: number, x: number, y: number) => {
    const { width, height } = size();
    const zoom = clamp(camera.zoom * factor, MIN_ZOOM, MAX_ZOOM);
    const applied = zoom / camera.zoom;
    if (currentView() !== 'globe') {
      // Keep the point under the pointer where it is.
      const cx = x - width / 2;
      const cy = height / 2 - y;
      camera.panX = cx - (cx - camera.panX) * applied;
      camera.panY = cy - (cy - camera.panY) * applied;
    }
    camera.zoom = zoom;
    cameraTween = null;
    hitsDirty = true;
    lastInteraction = performance.now();
    wake();
  };

  el.canvas.addEventListener(
    'wheel',
    (event) => {
      event.preventDefault();
      const box = el.canvas.getBoundingClientRect();
      zoomAt(Math.exp(-event.deltaY * 0.0015), event.clientX - box.left, event.clientY - box.top);
    },
    { passive: false },
  );

  const pointers = new Map<number, { x: number; y: number }>();
  let pinch = 0;
  let moved = 0;
  el.canvas.addEventListener('pointerdown', (event) => {
    el.canvas.setPointerCapture(event.pointerId);
    pointers.set(event.pointerId, { x: event.clientX, y: event.clientY });
    dragging = true;
    moved = 0;
    if (pointers.size === 2) pinch = pinchDistance(pointers);
    lastInteraction = performance.now();
  });
  el.canvas.addEventListener('pointermove', (event) => {
    const previous = pointers.get(event.pointerId);
    if (!previous) {
      hover(event);
      return;
    }
    const dx = event.clientX - previous.x;
    const dy = event.clientY - previous.y;
    pointers.set(event.pointerId, { x: event.clientX, y: event.clientY });
    moved += Math.abs(dx) + Math.abs(dy);
    if (pointers.size === 2) {
      const distance = pinchDistance(pointers);
      const box = el.canvas.getBoundingClientRect();
      const middle = pinchMiddle(pointers);
      if (pinch > 0) zoomAt(distance / pinch, middle.x - box.left, middle.y - box.top);
      pinch = distance;
      return;
    }
    if (currentView() === 'globe') {
      const k = 0.35 / camera.zoom;
      camera.lon0 -= dx * k;
      camera.lat0 = clamp(camera.lat0 + dy * k, -80, 80);
    } else {
      camera.panX += dx;
      camera.panY -= dy;
    }
    cameraTween = null;
    hitsDirty = true;
    lastInteraction = performance.now();
    el.tip.hidden = true;
    wake();
  });
  const release = (event: PointerEvent) => {
    const wasClick = pointers.size === 1 && moved < 4;
    pointers.delete(event.pointerId);
    if (pointers.size < 2) pinch = 0;
    dragging = pointers.size > 0;
    if (wasClick && event.type === 'pointerup') select(event);
  };
  el.canvas.addEventListener('pointerup', release);
  el.canvas.addEventListener('pointercancel', release);
  el.canvas.addEventListener('pointerleave', () => {
    if (!dragging) el.tip.hidden = true;
  });
  el.canvas.addEventListener('dblclick', (event) => {
    const box = el.canvas.getBoundingClientRect();
    zoomAt(2, event.clientX - box.left, event.clientY - box.top);
  });

  el.stage.addEventListener('keydown', (event) => {
    const step = 60;
    const handled = (() => {
      switch (event.key) {
        case '+':
        case '=':
          zoomAt(1.4, size().width / 2, size().height / 2);
          return true;
        case '-':
          zoomAt(1 / 1.4, size().width / 2, size().height / 2);
          return true;
        case '0':
          goHome();
          return true;
        case 'ArrowLeft':
        case 'ArrowRight':
        case 'ArrowUp':
        case 'ArrowDown': {
          const dx = event.key === 'ArrowLeft' ? step : event.key === 'ArrowRight' ? -step : 0;
          const dy = event.key === 'ArrowUp' ? -step : event.key === 'ArrowDown' ? step : 0;
          if (currentView() === 'globe') {
            camera.lon0 -= dx * 0.2;
            camera.lat0 = clamp(camera.lat0 - dy * 0.2, -80, 80);
          } else {
            camera.panX += dx;
            camera.panY += dy;
          }
          hitsDirty = true;
          lastInteraction = performance.now();
          wake();
          return true;
        }
      }
      return false;
    })();
    if (handled) event.preventDefault();
  });

  root
    .querySelector('[data-zoom="in"]')
    ?.addEventListener('click', () => zoomAt(1.5, size().width / 2, size().height / 2));
  root
    .querySelector('[data-zoom="out"]')
    ?.addEventListener('click', () => zoomAt(1 / 1.5, size().width / 2, size().height / 2));
  root.querySelector('[data-zoom="home"]')?.addEventListener('click', () => goHome());

  function goHome(): void {
    tweenCamera({ ...HOME, lon0: camera.lon0, lat0: camera.lat0 });
  }

  function tweenCamera(target: Camera): void {
    cameraTween = { from: { ...camera }, to: target, started: performance.now() };
    lastInteraction = performance.now();
    wake();
  }

  // --- views ---------------------------------------------------------------------------------
  const setView = (next: View) => {
    if (next === to && mix >= 1) return;
    from = mix >= 0.5 ? to : from;
    to = next;
    mix = 0;
    morphStarted = performance.now();
    for (const button of el.views)
      button.setAttribute('aria-pressed', String(button.dataset['view'] === next));
    if (el.hint) el.hint.hidden = next !== 'core';
    el.tip.hidden = true;
    hitsDirty = true;
    tweenCamera({ ...HOME, lon0: camera.lon0, lat0: camera.lat0 });
    if (trails) fitRoute();
  };
  for (const button of el.views) {
    button.addEventListener('click', () => {
      const view = button.dataset['view'] as View | undefined;
      if (view && (VIEWS as readonly string[]).includes(view)) setView(view);
    });
  }

  // --- hover and selection -------------------------------------------------------------------
  const rebuildHits = () => {
    if (!overview || !at) return;
    const { width, height } = size();
    const count = overview.nodes.count;
    if (hitX.length !== count) {
      hitX = new Float32Array(count);
      hitY = new Float32Array(count);
    }
    grid.clear();
    const view = to;
    for (let i = count - 1; i >= 0; i--) {
      const [x, y, shown] = toScreen(
        at.lon[i]!,
        at.lat[i]!,
        at.coreR[i]!,
        view,
        view,
        1,
        camera,
        width,
        height,
      );
      hitX[i] = x;
      hitY[i] = y;
      if (shown < 0.5 || x < 0 || y < 0 || x > width || y > height) continue;
      const key = Math.floor(x / 16) * 4096 + Math.floor(y / 16);
      const cell = grid.get(key);
      if (cell) cell.push(i);
      else grid.set(key, [i]);
    }
    hitsDirty = false;
  };

  const nearest = (x: number, y: number): number => {
    if (hitsDirty) rebuildHits();
    let best = -1;
    let bestDistance = HIT_PX * HIT_PX;
    const cx = Math.floor(x / 16);
    const cy = Math.floor(y / 16);
    for (let gx = cx - 1; gx <= cx + 1; gx++) {
      for (let gy = cy - 1; gy <= cy + 1; gy++) {
        for (const i of grid.get(gx * 4096 + gy) ?? []) {
          const d = (hitX[i]! - x) ** 2 + (hitY[i]! - y) ** 2;
          // Among dots that overlap, the larger network (the smaller index) wins.
          if (d < bestDistance || (d === bestDistance && i < best)) {
            best = i;
            bestDistance = d;
          }
        }
      }
    }
    return best;
  };

  const describe = (i: number): string => {
    if (!overview) return '';
    const asn = overview.nodes.asn[i]!;
    const name = overview.names[i] ?? '';
    const country = overview.countries[overview.nodes.country[i]!] ?? '';
    const type = overview.types[overview.nodes.type[i]!] ?? '';
    return [`AS${asn}`, name, country, type].filter(Boolean).join(' · ');
  };

  function hover(event: PointerEvent): void {
    if (!overview || mix < 1) return;
    const box = el.canvas.getBoundingClientRect();
    const x = event.clientX - box.left;
    const y = event.clientY - box.top;
    const i = nearest(x, y);
    if (i < 0) {
      el.tip.hidden = true;
      el.canvas.style.cursor = '';
      return;
    }
    el.tip.textContent = describe(i);
    el.tip.hidden = false;
    el.tip.style.transform = `translate(${Math.round(x + 14)}px, ${Math.round(y + 14)}px)`;
    el.canvas.style.cursor = 'pointer';
  }

  let selecting: AbortController | null = null;
  async function select(event: PointerEvent): Promise<void> {
    if (!overview || !at) return;
    const box = el.canvas.getBoundingClientRect();
    const i = nearest(event.clientX - box.left, event.clientY - box.top);
    if (i < 0) return;
    selecting?.abort();
    selecting = new AbortController();
    const asn = overview.nodes.asn[i]!;
    try {
      const network = await getJSON<NetworkJSON>(`/api/net/as/${asn}`, selecting.signal);
      renderNetwork(el.cardBody, network, overview.totals.networks, texts, locale);
      el.card.hidden = false;
      const neighbours = [
        ...network.neighbours.providers,
        ...network.neighbours.peers,
        ...network.neighbours.customers,
      ]
        .map((n) => at?.byASN.get(n.asn))
        .filter((n): n is number => n !== undefined);
      renderer?.setLayer('focus', focusVertices(i, neighbours, at));
      wake();
    } catch (error) {
      if (error instanceof ApiError) {
        el.status.textContent =
          texts.errors[error.code as keyof MapTexts['errors']] ?? texts.errors.network;
      }
    }
  }
  root.querySelector('[data-network-close]')?.addEventListener('click', () => {
    el.card.hidden = true;
    renderer?.clearLayer('focus');
    wake();
  });

  // --- routes --------------------------------------------------------------------------------
  const me = visitor ? { ip: visitor.ip } : null;
  const url = new URL(location.href);
  const wanted = { from: url.searchParams.get('from') ?? '', to: url.searchParams.get('to') ?? '' };
  // Without it the visitor types the address: nothing is lost.
  if (visitor) {
    el.mine.textContent = [
      texts.form.mine,
      visitor.ip,
      visitor.asn ? `AS${visitor.asn}` : '',
      visitor.name,
    ]
      .filter(Boolean)
      .join(' · ');
    el.mine.hidden = false;
    if (!el.from.value && (wanted.from === '' || wanted.from === 'me')) el.from.value = visitor.ip;
  }
  if (wanted.from && wanted.from !== 'me') el.from.value = wanted.from;
  if (wanted.to) el.to.value = wanted.to;

  root.querySelector('[data-route-swap]')?.addEventListener('click', () => {
    [el.from.value, el.to.value] = [el.to.value, el.from.value];
  });
  for (const example of root.querySelectorAll<HTMLButtonElement>('[data-example]')) {
    example.addEventListener('click', () => {
      el.to.value = example.dataset['example'] ?? '';
      el.form.requestSubmit();
    });
  }

  let routing: AbortController | null = null;
  el.form.addEventListener('submit', (event) => {
    event.preventDefault();
    void trace(el.from.value.trim(), el.to.value.trim());
  });

  async function trace(source: string, target: string): Promise<void> {
    if (!source || !target) return;
    routing?.abort();
    routing = new AbortController();
    el.form.setAttribute('aria-busy', 'true');
    el.status.textContent = '';
    try {
      const query = new URLSearchParams({ from: source, to: target });
      const route = await getJSON<RouteJSON>(`/api/net/route?${query}`, routing.signal);
      const shared = new URL(location.href);
      shared.searchParams.set('from', me && source === me.ip ? 'me' : source);
      shared.searchParams.set('to', target);
      history.replaceState(null, '', shared);
      renderRoute(el.result, route, texts, locale);
      el.result.hidden = false;
      showRoute(route);
    } catch (error) {
      if (error instanceof DOMException && error.name === 'AbortError') return;
      const code = error instanceof ApiError ? error.code : 'network';
      el.status.textContent =
        texts.errors[code as keyof MapTexts['errors']] ?? texts.errors.network;
    } finally {
      el.form.removeAttribute('aria-busy');
    }
  }

  function showRoute(route: RouteJSON): void {
    if (!at) return;
    // The places the packet passes: the source, where each two networks meet, the destination.
    const firstMeet = route.hops[0]?.meet;
    const fromPlace =
      located(route.from.lon, route.from.lat) ??
      (firstMeet ? { lon: firstMeet.lon, lat: firstMeet.lat } : null);
    const lastMeet = route.hops.at(-2)?.meet;
    const toPlace =
      located(route.to.lon, route.to.lat) ??
      (lastMeet ? { lon: lastMeet.lon, lat: lastMeet.lat } : fromPlace);
    if (!fromPlace || !toPlace) return;
    const geo = [fromPlace];
    stops = [{ ...fromPlace, label: route.from.ip }];
    route.hops.forEach((hop, i) => {
      if (!hop.meet) return;
      const next = route.hops[i + 1];
      geo.push({ lon: hop.meet.lon, lat: hop.meet.lat });
      stops.push({
        lon: hop.meet.lon,
        lat: hop.meet.lat,
        label: hop.meet.name || (next ? `→ AS${next.asn}` : `AS${hop.asn}`),
      });
    });
    geo.push(toPlace);
    stops.push({ ...toPlace, label: route.to.ip });
    const networks = route.hops.map((hop, i) => {
      const index = at?.byASN.get(hop.asn);
      if (index !== undefined && at) return { lon: at.lon[index]!, coreR: at.coreR[index]! };
      const place = geo[Math.min(i + 1, geo.length - 1)]!;
      return { lon: place.lon, coreR: 0.97 };
    });
    trails = routeTrails(geo, networks);
    renderer?.setLayer('route', trails.geo);
    renderer?.setLayer('routeCore', trails.core);
    renderer?.setLayer('stops', trails.geoMarks);
    renderer?.setLayer('stopsCore', trails.coreMarks);
    routeStartedAt = performance.now();
    routeDuration = 1400 + 450 * route.hops.length;
    progress = reduced.matches ? 1 : 0;
    fitRoute();
    wake();
  }

  function fitRoute(): void {
    if (stops.length === 0) return;
    const { width, height } = size();
    if (to === 'globe') {
      // Turn the globe to the middle of the route.
      const middle = stops[Math.floor(stops.length / 2)]!;
      tweenCamera({
        zoom: 1,
        panX: 0,
        panY: 0,
        lon0: middle.lon,
        lat0: clamp(middle.lat, -60, 60),
      });
      return;
    }
    if (to === 'core') {
      tweenCamera({ ...HOME, lon0: camera.lon0, lat0: camera.lat0 });
      return;
    }
    let minX = Infinity;
    let maxX = -Infinity;
    let minY = Infinity;
    let maxY = -Infinity;
    for (const stop of stops) {
      const [x, y] = equalEarth(stop.lon, stop.lat);
      minX = Math.min(minX, x);
      maxX = Math.max(maxX, x);
      minY = Math.min(minY, y);
      maxY = Math.max(maxY, y);
    }
    const scale = viewScale('map', width, height);
    const spanX = Math.max((maxX - minX) * scale, 60);
    const spanY = Math.max((maxY - minY) * scale, 60);
    // A route inside one country keeps the country around it in view.
    const zoom = clamp(
      Math.min((width * 0.6) / Math.max(spanX, 160), (height * 0.6) / Math.max(spanY, 160)),
      1,
      3.5,
    );
    tweenCamera({
      zoom,
      panX: -((minX + maxX) / 2) * scale * zoom,
      panY: -((minY + maxY) / 2) * scale * zoom,
      lon0: camera.lon0,
      lat0: camera.lat0,
    });
  }

  // Labels of the route: small tags next to the places, placed after every frame.
  function placeLabels(): void {
    if (stops.length === 0 || to === 'core' || mix < 1) {
      for (const node of labelNodes) node.hidden = true;
      return;
    }
    const { width, height } = size();
    while (labelNodes.length < stops.length) {
      const node = document.createElement('span');
      node.className = 'map-label mono';
      el.labels.append(node);
      labelNodes.push(node);
    }
    // Places that fall on one spot of the screen get their labels one under the other.
    const taken: { x: number; y: number; rows: number }[] = [];
    labelNodes.forEach((node, i) => {
      const stop = stops[i];
      if (!stop || !trails) {
        node.hidden = true;
        return;
      }
      const [x, y, shown] = toScreen(stop.lon, stop.lat, 0, to, to, 1, camera, width, height);
      const reached = (trails.at[i] ?? 1) <= progress + 0.001;
      node.hidden = shown < 0.5 || !reached || x < 0 || y < 0 || x > width || y > height;
      if (node.hidden) return;
      let spot = taken.find((t) => Math.abs(t.x - x) < 30 && Math.abs(t.y - y) < 30);
      if (!spot) {
        spot = { x, y, rows: 0 };
        taken.push(spot);
      }
      node.textContent = stop.label;
      node.style.transform = `translate(${Math.round(spot.x + 12)}px, ${Math.round(spot.y - 24 + spot.rows * 22)}px)`;
      spot.rows++;
    });
  }

  if (el.to.value && el.from.value) void trace(el.from.value, el.to.value);

  // --- the page around -----------------------------------------------------------------------
  new ResizeObserver(() => {
    hitsDirty = true;
    wake();
  }).observe(el.stage);
  new IntersectionObserver(([entry]) => {
    visible = entry?.isIntersecting ?? true;
    if (visible) wake();
  }).observe(el.stage);
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden) wake();
  });
  const repaint = () => {
    renderer?.setPalette(readPalette());
    wake();
  };
  new MutationObserver(repaint).observe(document.documentElement, {
    attributes: true,
    attributeFilter: ['data-theme'],
  });
  matchMedia('(prefers-color-scheme: dark)').addEventListener('change', repaint);
  el.canvas.addEventListener('webglcontextlost', (event) => event.preventDefault());
  el.canvas.addEventListener('webglcontextrestored', () => location.reload());
  started = performance.now();
}

/** The dotted totals under the title: filled from the overview once it is here. */
function fillStats(root: HTMLElement, overview: Overview, locale: string): void {
  const number = new Intl.NumberFormat(locale);
  const set = (name: string, value: string) => {
    const node = root.querySelector<HTMLElement>(`[data-stat="${name}"]`);
    if (node) node.textContent = value;
  };
  set('networks', number.format(overview.totals.networks));
  set('links', number.format(overview.totals.links));
  set('exchanges', number.format(overview.exchanges.count));
  set(
    'updated',
    new Intl.DateTimeFormat(locale, { day: 'numeric', month: 'long' }).format(overview.built),
  );
}

function located(lon: number | null, lat: number | null): { lon: number; lat: number } | null {
  return lon === null || lat === null ? null : { lon, lat };
}

function pinchDistance(pointers: Map<number, { x: number; y: number }>): number {
  const [a, b] = [...pointers.values()];
  return a && b ? Math.hypot(a.x - b.x, a.y - b.y) : 0;
}

function pinchMiddle(pointers: Map<number, { x: number; y: number }>): { x: number; y: number } {
  const [a, b] = [...pointers.values()];
  return a && b ? { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 } : { x: 0, y: 0 };
}

const clamp = (value: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, value));
const lerp = (a: number, b: number, t: number) => a + (b - a) * t;
const ease = (t: number) => (t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2);

function lerpAngle(a: number, b: number, t: number): number {
  let d = b - a;
  while (d > 180) d -= 360;
  while (d < -180) d += 360;
  return a + d * t;
}
