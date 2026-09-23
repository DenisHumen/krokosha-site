// The map of /map: the decoder of the overview against the file the Go side writes for its test
// world (api/internal/netmap, TestOverviewGolden), the land, the projections, the trails of a route.

import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { ms, ports, speed } from '../../src/scripts/netmap/api.ts';
import { readLand, readOverview } from '../../src/scripts/netmap/data.ts';
import { packetAt, routeTrails } from '../../src/scripts/netmap/layers.ts';
import { plural } from '../../src/scripts/netmap/panel.ts';
import {
  coreRadius,
  distanceKm,
  equalEarth,
  greatCircle,
  orthographic,
  toScreen,
  viewScale,
  HOME,
} from '../../src/scripts/netmap/projection.ts';

const bytes = (path: string): ArrayBuffer => {
  const file = readFileSync(new URL(path, import.meta.url));
  return file.buffer.slice(file.byteOffset, file.byteOffset + file.byteLength);
};

describe('overview', () => {
  const overview = readOverview(bytes('../fixtures/netmap-overview.bin'));

  it('reads the test world of the Go side', () => {
    expect(overview.version).toBe(1);
    expect(overview.built.toISOString()).toBe('2026-09-23T03:00:00.000Z');
    expect(overview.totals).toEqual({ links: 15, networks: 13, prefixes4: 4, prefixes6: 0 });
    // Placed networks only, the largest first.
    expect(overview.nodes.count).toBe(8);
    expect([...overview.nodes.asn.slice(0, 2)]).toEqual([1000, 2000]);
    expect(overview.names).toHaveLength(8);
  });

  it('keeps every network together with its place and name', () => {
    const i = [...overview.nodes.asn].indexOf(6000);
    expect(i).toBeGreaterThan(-1);
    expect(overview.nodes.lat[i]).toBe(5045);
    expect(overview.nodes.lon[i]).toBe(3052);
    expect(overview.countries[overview.nodes.country[i]!]).toBe('UA');
    expect(overview.names[i]).toBe('SIX');
    const seven = [...overview.nodes.asn].indexOf(7000);
    expect(overview.types[overview.nodes.type[seven]!]).toBe('Content');
  });

  it('has the exchange points, bundles and the links of the core', () => {
    expect(overview.exchanges.count).toBe(1);
    expect(overview.exchanges.names[0]).toEqual(['TEST-IX', 'Frankfurt', 'DE']);
    expect(overview.exchanges.capacity[0]).toBe(300);
    expect(overview.exchanges.members[0]).toBe(2);
    expect(overview.core.size).toBe(8);
    expect(overview.core.count).toBe(10);
    for (let k = 0; k < overview.core.count; k++) {
      expect(overview.core.a[k]).toBeLessThan(overview.core.b[k]!);
    }
    expect(overview.bundles.count).toBeGreaterThan(0);
  });

  it('refuses what is not an overview', () => {
    expect(() => readOverview(new ArrayBuffer(10))).toThrow();
    const wrong = bytes('../fixtures/netmap-overview.bin');
    new Uint8Array(wrong)[0] = 0;
    expect(() => readOverview(wrong)).toThrow(/not an overview/);
  });
});

describe('land', () => {
  it('is the dot matrix of the world, without Antarctica', () => {
    const points = readLand(bytes('../../public/netmap/land.bin'));
    expect(points.length / 2).toBe(15496);
    for (let i = 0; i < points.length; i += 2) {
      expect(Math.abs(points[i]!)).toBeLessThan(180);
      expect(points[i + 1]!).toBeGreaterThan(-60);
      expect(points[i + 1]!).toBeLessThan(84);
    }
  });
});

describe('projections', () => {
  it('Equal Earth: the middle, the edge, the north', () => {
    const [x0, y0] = equalEarth(0, 0);
    expect(x0).toBeCloseTo(0);
    expect(y0).toBeCloseTo(-0.04);
    expect(equalEarth(180, 0)[0]).toBeCloseTo(1, 3);
    expect(equalEarth(-180, 0)[0]).toBeCloseTo(-1, 3);
    expect(equalEarth(0, 84)[1]).toBeCloseTo(0.44, 2);
  });

  it('the globe shows its front and hides its back', () => {
    const [x, y, z] = orthographic(30, 50, 30, 50);
    expect(x).toBeCloseTo(0);
    expect(y).toBeCloseTo(0);
    expect(z).toBeCloseTo(1);
    expect(orthographic(-150, -50, 30, 50)[2]).toBeCloseTo(-1);
  });

  it('the core puts the giants in the middle', () => {
    expect(coreRadius(17, 3356)).toBeLessThan(0.1);
    expect(coreRadius(1, 64500)).toBeGreaterThan(0.9);
  });

  it('screen positions: the middle of the map in the middle of the canvas', () => {
    const [x, y, shown] = toScreen(0, 0, 1, 'map', 'map', 1, HOME, 1000, 600);
    expect(x).toBeCloseTo(500);
    expect(y).toBeCloseTo(300 + 0.04 * viewScale('map', 1000, 600));
    expect(shown).toBe(1);
    expect(toScreen(-150, -50, 1, 'globe', 'globe', 1, HOME, 1000, 600)[2]).toBe(0);
  });

  it('great circles and distances', () => {
    const points = greatCircle(30.52, 50.45, 2.35, 48.85, 8);
    expect(points).toHaveLength(18);
    expect(points[0]).toBeCloseTo(30.52);
    expect(points[17]).toBeCloseTo(48.85);
    expect(distanceKm(30.52, 50.45, 2.35, 48.85)).toBeGreaterThan(1990);
    expect(distanceKm(30.52, 50.45, 2.35, 48.85)).toBeLessThan(2060);
  });
});

describe('the trails of a route', () => {
  const stops = [
    { lon: 30.52, lat: 50.45 }, // Kyiv
    { lon: 8.68, lat: 50.11 }, // Frankfurt
    { lon: -74, lat: 40.71 }, // New York
  ];
  const networks = [
    { lon: 30, coreR: 0.95 },
    { lon: -80, coreR: 0.1 },
  ];
  const trails = routeTrails(stops, networks);

  it('share one way from 0 to 1, in order', () => {
    expect(trails.at[0]).toBe(0);
    expect(trails.at.at(-1)).toBeCloseTo(1);
    expect(trails.at[1]).toBeLessThan(0.3); // Kyiv–Frankfurt is short beside the ocean
    for (const trail of [trails.geo, trails.core]) {
      let last = -1;
      for (let i = 4; i < trail.length; i += 5) {
        expect(trail[i]).toBeGreaterThanOrEqual(last);
        last = trail[i]!;
      }
    }
  });

  it('put the packet between the dots it passes', () => {
    const packet = packetAt(trails.geo, 0.5);
    expect(packet).toHaveLength(10);
    expect(packet[0]).toBeLessThan(8.68); // past Frankfurt, over the ocean
    expect(packet[0]).toBeGreaterThan(-74);
    expect(packetAt(new Float32Array(0), 0.5)).toHaveLength(0);
  });
});

describe('words and numbers', () => {
  it('speeds of ports', () => {
    expect(speed(400000)).toBe('400G');
    expect(speed(1000)).toBe('1G');
    expect(speed(100)).toBe('100M');
    expect(speed(2500)).toBe('2.5G');
    expect(ports(200000, 2)).toBe('2×100G');
    expect(ports(300000, 2)).toBe('300G');
    expect(ports(0, 0)).toBe('');
  });

  it('delays', () => {
    expect(ms(0.44)).toBe('0.4');
    expect(ms(12.3)).toBe('12');
    expect(ms(143.6)).toBe('144');
  });

  it('plural forms', () => {
    const ru = ['сеть', 'сети', 'сетей'];
    expect(plural(ru, 1, 'ru')).toBe('сеть');
    expect(plural(ru, 3, 'ru')).toBe('сети');
    expect(plural(ru, 5, 'ru')).toBe('сетей');
    expect(plural(ru, 21, 'ru')).toBe('сеть');
    expect(plural(['network', 'networks'], 1, 'en')).toBe('network');
    expect(plural(['network', 'networks'], 4, 'en')).toBe('networks');
  });
});
