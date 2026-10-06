import type {
  HealthResponse,
  HistoryResponse,
  HotspotsResponse,
  RisksResponse,
} from './types';

/**
 * Base path for every API call.
 *
 * The dashboard is served by the same Go process that exposes the API, so the
 * origin is always relative. An absolute URL would break the "single
 * executable, no server component" property of the static export.
 */
const BASE = '/api/v1';

/** Thrown for a non-2xx response so callers can render a real error. */
export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

/**
 * getJSON fetches and decodes one endpoint.
 *
 * The response is validated to be an object before it is returned, because a
 * 200 carrying an HTML error page would otherwise flow into the renderers as
 * `undefined` fields and produce a silently empty dashboard.
 */
async function getJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    signal,
    headers: { Accept: 'application/json' },
    cache: 'no-store',
  });

  if (!res.ok) {
    throw new ApiError(res.status, `GET ${path} returned ${res.status}`);
  }

  const data: unknown = await res.json();
  if (typeof data !== 'object' || data === null) {
    throw new ApiError(res.status, `GET ${path} did not return a JSON object`);
  }
  return data as T;
}

export const fetchHealth = (signal?: AbortSignal) =>
  getJSON<HealthResponse>('/health', signal);

export const fetchHistory = (signal?: AbortSignal) =>
  getJSON<HistoryResponse>('/history', signal);

export const fetchRisks = (signal?: AbortSignal) =>
  getJSON<RisksResponse>('/risks', signal);

export const fetchHotspots = (signal?: AbortSignal) =>
  getJSON<HotspotsResponse>('/hotspots', signal);

export const fetchOverview = async (signal?: AbortSignal) => {
  // One round trip for the overview: the three endpoints are independent, so
  // firing them together turns four sequential requests into one latency.
  const [health, risks, hotspots] = await Promise.all([
    fetchHealth(signal),
    fetchRisks(signal),
    fetchHotspots(signal),
  ]);
  return { health, risks, hotspots };
};