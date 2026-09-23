// Makes public/netmap/land.bin — the land of the world as a dot matrix, for the /map page.
//
//   node scripts/gen-land.mjs path/to/ne_110m_land.geojson
//
// The source is Natural Earth, 1:110m physical «land» (public domain):
// https://github.com/nvkelso/natural-earth-vector/blob/master/geojson/ne_110m_land.geojson
// It is not needed for building the site: the result is committed. Run it again only to change
// the grid.
//
// The file is a bit mask over a grid of whole degrees, a cell for every degree of latitude from
// +84 down to -60 (no Antarctica: nobody routes through it) and of longitude from -180 east:
//
//   "KLD1", u16 columns, u16 rows, i16 north, i16 west (degrees), then columns × rows bits,
//   row by row from the north, the lowest bit of a byte first. A set bit — land at the middle of
//   the cell.

import { readFileSync, writeFileSync } from 'node:fs';

const source = process.argv[2];
if (!source) {
  console.error('usage: node scripts/gen-land.mjs ne_110m_land.geojson');
  process.exit(2);
}

const STEP = 1;
const NORTH = 84;
const SOUTH = -60;
const WEST = -180;
const columns = 360 / STEP;
const rows = (NORTH - SOUTH) / STEP;

/** Polygons as lists of rings, each ring a list of [lon, lat], with a bounding box. */
const polygons = [];
for (const feature of JSON.parse(readFileSync(source, 'utf8')).features) {
  const { type, coordinates } = feature.geometry;
  const list = type === 'Polygon' ? [coordinates] : type === 'MultiPolygon' ? coordinates : [];
  for (const rings of list) {
    let [west, south, east, north] = [180, 90, -180, -90];
    for (const [lon, lat] of rings[0]) {
      west = Math.min(west, lon);
      east = Math.max(east, lon);
      south = Math.min(south, lat);
      north = Math.max(north, lat);
    }
    polygons.push({ rings, west, south, east, north });
  }
}

/** Even–odd rule: a point is inside when a ray from it crosses the rings an odd number of times. */
function inside(lon, lat, rings) {
  let crossings = 0;
  for (const ring of rings) {
    for (let i = 0, j = ring.length - 1; i < ring.length; j = i++) {
      const [xi, yi] = ring[i];
      const [xj, yj] = ring[j];
      if (yi > lat !== yj > lat && lon < ((xj - xi) * (lat - yi)) / (yj - yi) + xi) crossings++;
    }
  }
  return crossings % 2 === 1;
}

const bits = new Uint8Array(Math.ceil((columns * rows) / 8));
let land = 0;
for (let row = 0; row < rows; row++) {
  const lat = NORTH - (row + 0.5) * STEP;
  for (let column = 0; column < columns; column++) {
    const lon = WEST + (column + 0.5) * STEP;
    const hit = polygons.some(
      (p) =>
        lon >= p.west &&
        lon <= p.east &&
        lat >= p.south &&
        lat <= p.north &&
        inside(lon, lat, p.rings),
    );
    if (hit) {
      const index = row * columns + column;
      bits[index >> 3] |= 1 << (index & 7);
      land++;
    }
  }
}

const header = new DataView(new ArrayBuffer(12));
[0x4b, 0x4c, 0x44, 0x31].forEach((byte, i) => header.setUint8(i, byte)); // "KLD1"
header.setUint16(4, columns, true);
header.setUint16(6, rows, true);
header.setInt16(8, NORTH, true);
header.setInt16(10, WEST, true);
const out = new Uint8Array(12 + bits.length);
out.set(new Uint8Array(header.buffer), 0);
out.set(bits, 12);
writeFileSync(new URL('../public/netmap/land.bin', import.meta.url), out);
console.log(`land.bin: ${columns}×${rows} cells, ${land} of them land, ${out.length} bytes`);
