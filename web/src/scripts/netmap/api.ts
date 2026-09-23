// What the API of the map answers (api/internal/netmap/http.go).

export interface EndpointJSON {
  ip: string;
  asn: number;
  name: string;
  country: string;
  lat: number | null;
  lon: number | null;
  anycast?: boolean;
}

export interface MeetJSON {
  kind: 'ix' | 'facility' | 'guess';
  name?: string;
  city?: string;
  country?: string;
  lat: number;
  lon: number;
  speeds: [number, number]; // Mbit/s of the two networks' ports at the exchange point, 0 — not told
  ports: [number, number];
  rtt: number; // estimated round trip from the source to here, ms
}

export interface HopJSON {
  asn: number;
  name: string;
  country: string;
  type?: string;
  /** What the previous network is to this one. */
  rel?: 'customer' | 'provider' | 'peer' | 'unknown';
  known: boolean;
  meet?: MeetJSON;
}

export interface RouteJSON {
  from: EndpointJSON;
  to: EndpointJSON;
  rtt: number;
  kind: 'inferred' | 'observed';
  hops: HopJSON[];
  built: string;
}

export interface BriefJSON {
  asn: number;
  name: string;
  country: string;
  cone: number;
}

export interface NetworkJSON {
  asn: number;
  name: string;
  country: string;
  type?: string;
  cone: number;
  rank: number;
  customers: number;
  providers: number;
  peers: number;
  lat: number | null;
  lon: number | null;
  neighbours: { providers: BriefJSON[]; peers: BriefJSON[]; customers: BriefJSON[] };
  exchanges: { name: string; city: string; country: string; speed: number; ports: number }[];
}

/** An answer of the API that is not a success: `code` names the message to show. */
export class ApiError extends Error {
  readonly code: string;

  constructor(code: string) {
    super(code);
    this.code = code;
  }
}

/** GETs JSON; a failure becomes an ApiError with the code the API gave, or «network». */
export async function getJSON<T>(url: string, signal?: AbortSignal): Promise<T> {
  let response: Response;
  try {
    response = await fetch(url, {
      signal: signal ?? null,
      headers: { Accept: 'application/json' },
    });
  } catch (error) {
    if (error instanceof DOMException && error.name === 'AbortError') throw error;
    throw new ApiError('network');
  }
  if (!response.ok) {
    const body = (await response.json().catch(() => null)) as { error?: string } | null;
    throw new ApiError(body?.error ?? 'network');
  }
  return (await response.json()) as T;
}

/** A port speed for people: 400000 → «400G», 1000 → «1G», 100 → «100M». */
export function speed(mbps: number): string {
  if (mbps <= 0) return '';
  if (mbps >= 1000) {
    const gbps = mbps / 1000;
    return `${Number.isInteger(gbps) ? gbps : gbps.toFixed(1)}G`;
  }
  return `${mbps}M`;
}

/** Speeds a single port is made in, Mbit/s. */
const PORT_SPEEDS = [100, 1000, 10000, 25000, 40000, 50000, 100000, 200000, 400000, 800000];

/**
 * Ports of a network at an exchange point: «2×100G» when they split into ports of a real speed,
 * else the sum — 100G and 200G together are «300G», never «2×150G».
 */
export function ports(total: number, count: number): string {
  if (total <= 0) return '';
  if (count > 1 && total % count === 0 && PORT_SPEEDS.includes(total / count)) {
    return `${count}×${speed(total / count)}`;
  }
  return speed(total);
}

/** A delay: «0.4», «12», «143» (milliseconds). */
export function ms(value: number): string {
  return value < 10 ? value.toFixed(1) : String(Math.round(value));
}
