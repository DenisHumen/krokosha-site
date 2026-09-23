// Where a point of the map goes on the screen, in the three views of /map. The same formulas are
// written in GLSL for the drawing (renderer.ts): these serve the pointer, the labels and the packet.
//
//   map    the Equal Earth projection (Šavrič, Patterson, Jenny, 2018): the world as it is, flat,
//          continents in their true sizes
//   core   a disc after CAIDA's picture of the internet's core: the angle is the longitude of a
//          network, the distance from the middle — how small it is; the giants are in the middle
//   globe  the Earth seen from space, turning

export const VIEWS = ['map', 'core', 'globe'] as const;
export type View = (typeof VIEWS)[number];

/** Half the width of the Equal Earth world, to bring it to [-1, 1]. */
export const EQUAL_EARTH_WIDTH = 2.7066;
/**
 * The land drawn goes from 84° north to 60° south: y from 0.48 to -0.40. The map is moved down by
 * the middle of that, 0.04, to stand in the middle of the canvas; MAP_HEIGHT is its height.
 */
export const MAP_SHIFT = 0.04;
export const MAP_HEIGHT = 0.88;

const A1 = 1.340264;
const A2 = -0.081106;
const A3 = 0.000893;
const A4 = 0.003796;
const M = Math.sqrt(3) / 2;
const RAD = Math.PI / 180;

/** Equal Earth: x in [-1, 1], y in about [-0.44, 0.44] for the land drawn, north up. */
export function equalEarth(lon: number, lat: number): [number, number] {
  const lambda = lon * RAD;
  const theta = Math.asin(M * Math.sin(lat * RAD));
  const t2 = theta * theta;
  const t6 = t2 * t2 * t2;
  const x = (lambda * Math.cos(theta)) / (M * (A1 + 3 * A2 * t2 + t6 * (7 * A3 + 9 * A4 * t2)));
  const y = theta * (A1 + A2 * t2 + t6 * (A3 + A4 * t2));
  return [x / EQUAL_EARTH_WIDTH, y / EQUAL_EARTH_WIDTH - MAP_SHIFT];
}

/** The globe turned so that (lon0, lat0) faces the viewer: x, y on the unit disc, z > 0 in front. */
export function orthographic(
  lon: number,
  lat: number,
  lon0: number,
  lat0: number,
): [number, number, number] {
  const phi = lat * RAD;
  const phi0 = lat0 * RAD;
  const d = (lon - lon0) * RAD;
  const x = Math.cos(phi) * Math.sin(d);
  const y = Math.cos(phi0) * Math.sin(phi) - Math.sin(phi0) * Math.cos(phi) * Math.cos(d);
  const z = Math.sin(phi0) * Math.sin(phi) + Math.cos(phi0) * Math.cos(phi) * Math.cos(d);
  return [x, y, z];
}

/** The largest cone, in bits, the core view expects; bigger ones sit in the very middle. */
export const CORE_BITS = 17;

/**
 * Distance from the middle of the core view: the largest networks at 0.08, networks without
 * customers on the rim, spread over a thin ring by their number so that they do not stack.
 */
export function coreRadius(sizeBits: number, asn: number): number {
  const share = Math.min(Math.max((sizeBits - 1) / (CORE_BITS - 1), 0), 1);
  const spread = ((Math.imul(asn, 2654435761) >>> 0) % 1000) / 1000;
  return 0.08 + 0.86 * Math.pow(1 - share, 1.35) + 0.06 * spread * (1 - share);
}

/** A point of the core view: the angle of the longitude, the given radius. North of the page is 0°. */
export function core(lon: number, radius: number): [number, number] {
  const angle = lon * RAD;
  return [radius * Math.sin(angle), radius * Math.cos(angle)];
}

/** How the camera looks: zoom, a shift in pixels, and the side of the globe that faces the viewer. */
export interface Camera {
  zoom: number;
  panX: number;
  panY: number;
  lon0: number;
  lat0: number;
}

export const HOME: Camera = { zoom: 1, panX: 0, panY: 0, lon0: 15, lat0: 30 };

/** Pixels per world unit of a view in a canvas of this size (CSS pixels). */
export function viewScale(view: View, width: number, height: number): number {
  if (view === 'map') return Math.min((width / 2) * 0.96, (height * 0.94) / MAP_HEIGHT);
  return (Math.min(width, height) / 2) * 0.9;
}

/**
 * A point in CSS pixels from the top left of the canvas, with how visible it is (0 — behind the
 * globe or out of the view), blended between two views by `mix` exactly like the shaders do.
 */
export function toScreen(
  lon: number,
  lat: number,
  coreR: number,
  from: View,
  to: View,
  mix: number,
  camera: Camera,
  width: number,
  height: number,
): [x: number, y: number, visible: number] {
  const a = place(from, lon, lat, coreR, camera);
  const b = place(to, lon, lat, coreR, camera);
  const scale = viewScale(from, width, height) * (1 - mix) + viewScale(to, width, height) * mix;
  const x = (a[0] * (1 - mix) + b[0] * mix) * scale * camera.zoom + camera.panX;
  const y = (a[1] * (1 - mix) + b[1] * mix) * scale * camera.zoom + camera.panY;
  return [width / 2 + x, height / 2 - y, a[2] * (1 - mix) + b[2] * mix];
}

function place(
  view: View,
  lon: number,
  lat: number,
  coreR: number,
  camera: Camera,
): [number, number, number] {
  switch (view) {
    case 'map': {
      const [x, y] = equalEarth(lon, lat);
      return [x, y, 1];
    }
    case 'core': {
      const [x, y] = core(lon, coreR);
      return [x, y, 1];
    }
    case 'globe': {
      const [x, y, z] = orthographic(lon, lat, camera.lon0, camera.lat0);
      return [x, y, z > -0.02 ? 1 : 0];
    }
  }
}

/** Points along the great circle between two places, both ends included: [lon, lat] pairs. */
export function greatCircle(
  lon1: number,
  lat1: number,
  lon2: number,
  lat2: number,
  steps: number,
): number[] {
  const p1 = toVector(lon1, lat1);
  const p2 = toVector(lon2, lat2);
  const dot = Math.min(1, Math.max(-1, p1[0] * p2[0] + p1[1] * p2[1] + p1[2] * p2[2]));
  const omega = Math.acos(dot);
  const out: number[] = [];
  for (let i = 0; i <= steps; i++) {
    const t = i / steps;
    let v: [number, number, number];
    if (omega < 1e-6) {
      v = p1;
    } else {
      const a = Math.sin((1 - t) * omega) / Math.sin(omega);
      const b = Math.sin(t * omega) / Math.sin(omega);
      v = [a * p1[0] + b * p2[0], a * p1[1] + b * p2[1], a * p1[2] + b * p2[2]];
    }
    out.push(Math.atan2(v[1], v[0]) / RAD, Math.asin(Math.max(-1, Math.min(1, v[2]))) / RAD);
  }
  return out;
}

function toVector(lon: number, lat: number): [number, number, number] {
  const phi = lat * RAD;
  const lambda = lon * RAD;
  return [Math.cos(phi) * Math.cos(lambda), Math.cos(phi) * Math.sin(lambda), Math.sin(phi)];
}

/** Great-circle distance in kilometres. */
export function distanceKm(lon1: number, lat1: number, lon2: number, lat2: number): number {
  const p1 = toVector(lon1, lat1);
  const p2 = toVector(lon2, lat2);
  const dot = Math.min(1, Math.max(-1, p1[0] * p2[0] + p1[1] * p2[1] + p1[2] * p2[2]));
  return Math.acos(dot) * 6371;
}
