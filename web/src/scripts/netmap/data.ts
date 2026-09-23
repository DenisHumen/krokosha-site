// The data of the /map page: the overview the server builds every night (api/internal/netmap,
// overview.go — the layout of the file is described there) and the land of the world as a dot
// matrix (scripts/gen-land.mjs). Both are read into typed arrays over the downloaded bytes.

export interface Overview {
  version: number;
  built: Date;
  /** Networks with a place on the map, the largest first. */
  nodes: {
    count: number;
    asn: Uint32Array;
    lat: Int16Array; // hundredths of a degree
    lon: Int16Array;
    size: Uint8Array; // bits of the customer cone: 1 — a network alone, 17 — the largest
    degree: Uint8Array; // bits of the number of links
    type: Uint8Array; // index in `types`
    country: Uint8Array; // index in `countries`
  };
  exchanges: {
    count: number;
    lat: Int16Array;
    lon: Int16Array;
    members: Uint16Array;
    capacity: Uint32Array; // Gbit/s
    names: [name: string, city: string, country: string][];
  };
  /** The busiest bundles of links between cells of the map. */
  bundles: {
    count: number;
    lat1: Int16Array;
    lon1: Int16Array;
    lat2: Int16Array;
    lon2: Int16Array;
    links: Uint32Array;
  };
  /** Links among the `size` largest networks, for the view «core». */
  core: { size: number; count: number; a: Uint16Array; b: Uint16Array; rel: Int8Array };
  /** Names of the first networks (the rest are asked for when needed). */
  names: string[];
  countries: string[];
  types: string[];
  totals: { links: number; networks: number; prefixes4: number; prefixes6: number };
}

const MAGIC = 0x314d4e4b; // "KNM1" read as a little-endian number
export const OVERVIEW_VERSION = 1;

/** Reads an overview; throws on a file of another kind or version. */
export function readOverview(buffer: ArrayBuffer): Overview {
  if (buffer.byteLength < 64) throw new Error('overview: too short');
  const header = new Uint32Array(buffer, 0, 16);
  const [magic, version, built, n, x, b, c, coreSize, named, links, networks, v4, v6, textLength] =
    header;
  if (magic !== MAGIC) throw new Error('overview: not an overview file');
  if (version !== OVERVIEW_VERSION) throw new Error(`overview: version ${version}`);

  let at = 64;
  const take = <T>(make: (offset: number) => T, bytes: number): T => {
    const view = make(at);
    at += bytes;
    return view;
  };
  const align = () => {
    at = (at + 3) & ~3;
  };

  const nodes = {
    count: n!,
    asn: take((o) => new Uint32Array(buffer, o, n), 4 * n!),
    lat: take((o) => new Int16Array(buffer, o, n), 2 * n!),
    lon: take((o) => new Int16Array(buffer, o, n), 2 * n!),
    size: take((o) => new Uint8Array(buffer, o, n), n!),
    degree: take((o) => new Uint8Array(buffer, o, n), n!),
    type: take((o) => new Uint8Array(buffer, o, n), n!),
    country: take((o) => new Uint8Array(buffer, o, n), n!),
  };
  align();
  const xLat = take((o) => new Int16Array(buffer, o, x), 2 * x!);
  const xLon = take((o) => new Int16Array(buffer, o, x), 2 * x!);
  const members = take((o) => new Uint16Array(buffer, o, x), 2 * x!);
  align();
  const capacity = take((o) => new Uint32Array(buffer, o, x), 4 * x!);

  const lat1 = take((o) => new Int16Array(buffer, o, b), 2 * b!);
  const lon1 = take((o) => new Int16Array(buffer, o, b), 2 * b!);
  const lat2 = take((o) => new Int16Array(buffer, o, b), 2 * b!);
  const lon2 = take((o) => new Int16Array(buffer, o, b), 2 * b!);
  align();
  const bundleLinks = take((o) => new Uint32Array(buffer, o, b), 4 * b!);

  const coreA = take((o) => new Uint16Array(buffer, o, c), 2 * c!);
  const coreB = take((o) => new Uint16Array(buffer, o, c), 2 * c!);
  const coreRel = take((o) => new Int8Array(buffer, o, c), c!);
  align();
  if (at + textLength! > buffer.byteLength) throw new Error('overview: cut short');
  const text = JSON.parse(new TextDecoder().decode(new Uint8Array(buffer, at, textLength))) as {
    countries: string[];
    names: string[];
    exchanges: [string, string, string][];
    types: string[];
  };
  if (text.names.length !== named) throw new Error('overview: names do not match');

  return {
    version: version!,
    built: new Date(built! * 1000),
    nodes,
    exchanges: { count: x!, lat: xLat, lon: xLon, members, capacity, names: text.exchanges },
    bundles: { count: b!, lat1, lon1, lat2, lon2, links: bundleLinks },
    core: { size: coreSize!, count: c!, a: coreA, b: coreB, rel: coreRel },
    names: text.names,
    countries: text.countries,
    types: text.types,
    totals: { links: links!, networks: networks!, prefixes4: v4!, prefixes6: v6! },
  };
}

/** Land of the world: the middles of the land cells, as [lon, lat] pairs. */
export function readLand(buffer: ArrayBuffer): Float32Array {
  const view = new DataView(buffer);
  if (buffer.byteLength < 12 || view.getUint32(0, true) !== 0x31444c4b) {
    throw new Error('land: not a land file'); // "KLD1"
  }
  const columns = view.getUint16(4, true);
  const rows = view.getUint16(6, true);
  const north = view.getInt16(8, true);
  const west = view.getInt16(10, true);
  const step = 360 / columns;
  const bits = new Uint8Array(buffer, 12);
  const points: number[] = [];
  for (let row = 0; row < rows; row++) {
    for (let column = 0; column < columns; column++) {
      const index = row * columns + column;
      if ((bits[index >> 3]! >> (index & 7)) & 1) {
        points.push(west + (column + 0.5) * step, north - (row + 0.5) * step);
      }
    }
  }
  return new Float32Array(points);
}

/** The server's manifest of the newest overview. */
export interface Manifest {
  file: string;
  version: number;
  built: string;
  networks: number;
  links: number;
  exchanges: number;
  bytes: number;
}
