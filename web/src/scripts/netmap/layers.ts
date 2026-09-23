// Vertices of the layers of the map, [lon, lat, coreR, a, b] each (renderer.ts): built once from
// the overview, or for every route.

import type { Overview } from './data.ts';
import { coreRadius, distanceKm, greatCircle } from './projection.ts';

/** A layer that the core view does not use sits at this radius (the renderer fades it out). */
const NO_CORE = -1;

/** The land as a grid of dots. */
export function landVertices(points: Float32Array): Float32Array {
  const out = new Float32Array((points.length / 2) * 5);
  for (let i = 0, o = 0; i < points.length; i += 2, o += 5) {
    out.set([points[i]!, points[i + 1]!, NO_CORE, 2, 1], o);
  }
  return out;
}

/** A small offset, the same for a network every time: networks of one city spread into a cloud. */
export function scatter(asn: number): [number, number] {
  const h = Math.imul(asn ^ 0x9e3779b9, 2246822519) >>> 0;
  const angle = ((h % 3600) / 3600) * Math.PI * 2;
  const radius = 0.08 + (((h >>> 12) % 1000) / 1000) * 0.35;
  return [radius * Math.cos(angle), radius * Math.sin(angle)];
}

/** Where every network of the overview is drawn: lon, lat (with its offset) and its core radius. */
export interface Places {
  lon: Float32Array;
  lat: Float32Array;
  coreR: Float32Array;
  /** Index of a network in the overview by its number. */
  byASN: Map<number, number>;
}

export function places(overview: Overview): Places {
  const { count, asn, lat, lon, size } = overview.nodes;
  const out: Places = {
    lon: new Float32Array(count),
    lat: new Float32Array(count),
    coreR: new Float32Array(count),
    byASN: new Map(),
  };
  for (let i = 0; i < count; i++) {
    const [dLon, dLat] = size[i]! > 6 ? [0, 0] : scatter(asn[i]!); // large networks stay in place
    out.lon[i] = lon[i]! / 100 + dLon;
    out.lat[i] = Math.max(-89, Math.min(89, lat[i]! / 100 + dLat));
    out.coreR[i] = coreRadius(size[i]!, asn[i]!);
    out.byASN.set(asn[i]!, i);
  }
  return out;
}

/** Size of a network's dot in CSS pixels, from the bits of its cone. */
export function nodeSize(sizeBits: number): number {
  return 1.2 + Math.pow(Math.max(0, sizeBits - 1), 1.3) * 0.12;
}

export function nodeVertices(overview: Overview, at: Places): Float32Array {
  const { count, size } = overview.nodes;
  const out = new Float32Array(count * 5);
  // Drawn smallest first: the giants lie on top.
  for (let i = count - 1, o = 0; i >= 0; i--, o += 5) {
    const bits = size[i]!;
    out.set([at.lon[i]!, at.lat[i]!, at.coreR[i]!, nodeSize(bits), Math.min(1, bits / 12)], o);
  }
  return out;
}

export function exchangeVertices(overview: Overview): Float32Array {
  const { count, lat, lon, capacity, members } = overview.exchanges;
  const out = new Float32Array(count * 5);
  for (let i = count - 1, o = 0; i >= 0; i--, o += 5) {
    const gbps = capacity[i]!;
    const px = 3 + Math.log2(1 + gbps) * 0.75;
    out.set([lon[i]! / 100, lat[i]! / 100, NO_CORE, px, Math.min(1, members[i]! / 300)], o);
  }
  return out;
}

/**
 * Line segments of an arc along the great circle, cut where it crosses the date line: the flat map
 * would otherwise draw it across the whole world. `t` runs 0…1 along the arc.
 */
function pushArc(
  out: number[],
  lon1: number,
  lat1: number,
  lon2: number,
  lat2: number,
  coreR1: number,
  coreR2: number,
  alpha: number,
): void {
  const km = distanceKm(lon1, lat1, lon2, lat2);
  const steps = Math.max(2, Math.min(40, Math.round(km / 250)));
  const points = greatCircle(lon1, lat1, lon2, lat2, steps);
  for (let i = 0; i < steps; i++) {
    const a = [points[2 * i]!, points[2 * i + 1]!] as const;
    const b = [points[2 * i + 2]!, points[2 * i + 3]!] as const;
    if (Math.abs(a[0] - b[0]) > 180) continue;
    const ta = i / steps;
    const tb = (i + 1) / steps;
    const ra = coreR1 + (coreR2 - coreR1) * ta;
    const rb = coreR1 + (coreR2 - coreR1) * tb;
    out.push(a[0], a[1], ra, ta, alpha, b[0], b[1], rb, tb, alpha);
  }
}

/** The busiest bundles of links: brighter the more links they carry. */
export function bundleVertices(overview: Overview): Float32Array {
  const { count, lat1, lon1, lat2, lon2, links } = overview.bundles;
  const out: number[] = [];
  const top = Math.log(1 + (links[0] ?? 1));
  for (let i = 0; i < count; i++) {
    const alpha = 0.012 + 0.22 * Math.pow(Math.log(1 + links[i]!) / top, 2.4);
    pushArc(
      out,
      lon1[i]! / 100,
      lat1[i]! / 100,
      lon2[i]! / 100,
      lat2[i]! / 100,
      NO_CORE,
      NO_CORE,
      alpha,
    );
  }
  return new Float32Array(out);
}

/** Links among the largest networks, straight in the core view. Transit is brighter than peering. */
export function coreVertices(overview: Overview, at: Places): Float32Array {
  const { count, a, b, rel } = overview.core;
  const out = new Float32Array(count * 10);
  for (let i = 0, o = 0; i < count; i++, o += 10) {
    const x = a[i]!;
    const y = b[i]!;
    const alpha = rel[i] === 0 || rel[i] === 2 ? 0.04 : 0.09;
    out.set(
      [
        at.lon[x]!,
        at.lat[x]!,
        at.coreR[x]!,
        0,
        alpha,
        at.lon[y]!,
        at.lat[y]!,
        at.coreR[y]!,
        1,
        alpha,
      ],
      o,
    );
  }
  return out;
}

/** Links of one network to some of its neighbours: arcs to where they are. */
export function focusVertices(from: number, to: number[], at: Places): Float32Array {
  const out: number[] = [];
  for (const neighbour of to) {
    pushArc(
      out,
      at.lon[from]!,
      at.lat[from]!,
      at.lon[neighbour]!,
      at.lat[neighbour]!,
      at.coreR[from]!,
      at.coreR[neighbour]!,
      0.8,
    );
  }
  return new Float32Array(out);
}

/** A place a route passes on the globe. */
export interface Stop {
  lon: number;
  lat: number;
}

/** A network of a route in the core view: its angle (longitude) and radius. */
export interface CoreStop {
  lon: number;
  coreR: number;
}

/** The two trails of a route, drawn in step: the same share of the way on both. */
export interface Trails {
  geo: Float32Array; // dots along the arcs between the places the route passes
  core: Float32Array; // dots between the networks, for the core view
  geoMarks: Float32Array;
  coreMarks: Float32Array;
  at: number[]; // the share of the way at every place of `geo`
}

/**
 * The trails of a route. The places: the source, where each two networks meet, the destination —
 * one more than the networks. The packet leaves network i for network i+1 at place i+1, so in the
 * core view it stands on network i at share at[i], and on the last one from at[n-1] to the end.
 * `b` of every dot is its share of the way: the renderer lights what the packet has passed.
 */
export function routeTrails(stops: Stop[], networks: CoreStop[]): Trails {
  const legs: number[] = [];
  for (let i = 1; i < stops.length; i++) {
    const p = stops[i - 1]!;
    const q = stops[i]!;
    legs.push(Math.max(distanceKm(p.lon, p.lat, q.lon, q.lat), 120)); // even a hop inside a city shows
  }
  const total = legs.reduce((sum, km) => sum + km, 0) || 1;
  const geo: number[] = [];
  const at = [0];
  let done = 0;
  for (let i = 1; i < stops.length; i++) {
    const p = stops[i - 1]!;
    const q = stops[i]!;
    const leg = legs[i - 1]!;
    const steps = Math.max(24, Math.min(420, Math.round(leg / 18)));
    const points = greatCircle(p.lon, p.lat, q.lon, q.lat, steps);
    for (let s = 0; s < steps; s++) {
      geo.push(
        points[2 * s]!,
        points[2 * s + 1]!,
        NO_CORE,
        3.4,
        (done + (leg * s) / steps) / total,
      );
    }
    done += leg;
    at.push(done / total);
  }
  const last = stops.at(-1);
  if (last) geo.push(last.lon, last.lat, NO_CORE, 3.4, 1);

  const core: number[] = [];
  for (let i = 1; i < networks.length; i++) {
    const p = networks[i - 1]!;
    const q = networks[i]!;
    let turn = q.lon - p.lon; // the shorter way round
    if (turn > 180) turn -= 360;
    if (turn < -180) turn += 360;
    const t0 = at[i - 1] ?? 0;
    const t1 = at[i] ?? 1;
    const steps = 90;
    for (let s = 0; s < steps; s++) {
      const f = s / steps;
      core.push(p.lon + turn * f, 0, p.coreR + (q.coreR - p.coreR) * f, 3.4, t0 + (t1 - t0) * f);
    }
  }
  const final = networks.at(-1);
  if (final) core.push(final.lon, 0, final.coreR, 3.4, at[networks.length - 1] ?? 1);

  const geoMarks = new Float32Array(stops.length * 5);
  stops.forEach((stop, i) => geoMarks.set([stop.lon, stop.lat, NO_CORE, 11, at[i]!], i * 5));
  const coreMarks = new Float32Array(networks.length * 5);
  networks.forEach((n, i) => coreMarks.set([n.lon, 0, n.coreR, 11, at[i] ?? 1], i * 5));
  return { geo: new Float32Array(geo), core: new Float32Array(core), geoMarks, coreMarks, at };
}

/** Where the packet is when a share of the way is done: between two dots of a trail. */
export function packetAt(trail: Float32Array, share: number): Float32Array {
  const count = trail.length / 5;
  if (count === 0) return new Float32Array(0);
  let i = 0;
  while (i < count - 1 && trail[(i + 1) * 5 + 4]! <= share) i++;
  const j = Math.min(i + 1, count - 1);
  const t0 = trail[i * 5 + 4]!;
  const t1 = trail[j * 5 + 4]!;
  const f = t1 > t0 ? Math.min(1, Math.max(0, (share - t0) / (t1 - t0))) : 0;
  const mix = (k: number) => trail[i * 5 + k]! + (trail[j * 5 + k]! - trail[i * 5 + k]!) * f;
  const lon = Math.abs(trail[j * 5]! - trail[i * 5]!) > 180 ? trail[i * 5]! : mix(0);
  // A bright head and a soft halo around it.
  return new Float32Array([lon, mix(1), mix(2), 8, 2, lon, mix(1), mix(2), 24, 2]);
}
