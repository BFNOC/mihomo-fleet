import {
  aggregateSeries,
  connectionSnapshot,
  createSeries,
  deriveRate,
  diffConnections,
  localAddressLabel,
  pushSample,
  trafficWindowSeconds,
} from "./traffic.ts";
import type {
  ConnectionRow,
  ConnectionsPayload,
  ConnectionTotals,
  TrafficCounterSample,
  TrafficSeries,
} from "./traffic.ts";
import type { FleetInstance } from "./state.ts";

// The fast poll runs at 1800ms, so a 60s window is ~33 samples.
const sampleCapacity = Math.ceil((trafficWindowSeconds * 1000) / 1800) + 2;

// Per-instance rolling sampler state: the traffic-rate series plus enough of
// the last /connections snapshot to derive the next rate and render the
// connection table. This in-memory sampling history has no counterpart in
// FleetState/FleetInstance (the controller doesn't track it), so it is kept
// here, one entry per instance, for as long as pruneSamplers() (called every
// sampleFleet() tick, see below) lets it live -- deleted instances are pruned
// there, not on any per-instance removal hook. Exported so the Vue migration
// can reuse the exact shape instead of redeclaring it.
export interface DashboardSampler {
  series: TrafficSeries;
  previous: TrafficCounterSample | null;
  connections: number;
  reachable: boolean;
  connectionRows: ConnectionRow[];
  connectionTotals: Map<string, ConnectionTotals>;
  sampledAt: number;
}

// Raw JSON body of GET /api/mihomo/{id}/connections -- the controller's direct
// pass-through of mihomo's own /connections endpoint. Untrusted network input
// (same reasoning as traffic.ts's ConnectionsPayload, which this extends):
// `uploadTotal`/`downloadTotal` are only ever read through Number() below, so
// they stay `unknown` rather than claiming a shape the caller cannot actually
// guarantee.
export interface ConnectionsFetchPayload extends ConnectionsPayload {
  uploadTotal?: unknown;
  downloadTotal?: unknown;
}

export type FetchConnections = (instanceId: string) => Promise<ConnectionsFetchPayload | null | undefined>;

// One sampler per instance: the rolling rate series plus the previous
// cumulative counter reading the next rate is derived from.
const samplers = new Map<string, DashboardSampler>();

function emptySampler(): DashboardSampler {
  return {
    series: createSeries(sampleCapacity),
    previous: null,
    connections: 0,
    reachable: false,
    connectionRows: [],
    connectionTotals: new Map<string, ConnectionTotals>(),
    sampledAt: 0,
  };
}

function sampler(instanceId: string): DashboardSampler {
  let entry = samplers.get(instanceId);
  if (!entry) {
    entry = emptySampler();
    samplers.set(instanceId, entry);
  }
  return entry;
}

// A stopped or unreachable instance keeps no connection state: its rows are
// gone from the table and its counters must not seed the next rate it reports.
function resetConnectionState(entry: DashboardSampler): void {
  entry.previous = null;
  entry.connections = 0;
  entry.reachable = false;
  entry.connectionRows = [];
  entry.connectionTotals = new Map<string, ConnectionTotals>();
  entry.sampledAt = 0;
}

// Deleted instances would otherwise keep contributing to the fleet total
// forever, since nothing else ever clears their sampler.
export function pruneSamplers(instances: Pick<FleetInstance, "id">[] | null | undefined): void {
  const live = new Set((instances || []).map((item) => item.id));
  for (const id of [...samplers.keys()]) {
    if (!live.has(id)) samplers.delete(id);
  }
}

export function instanceSeries(instanceId: string): TrafficSeries | null {
  return samplers.get(instanceId)?.series || null;
}

export function instanceConnections(instanceId: string): number {
  const entry = samplers.get(instanceId);
  return entry?.reachable ? entry.connections : 0;
}

export function fleetSeries(instances: Pick<FleetInstance, "id">[] | null | undefined): TrafficSeries {
  const running = (instances || [])
    .map((item) => samplers.get(item.id)?.series)
    .filter((series): series is TrafficSeries => Boolean(series));
  return aggregateSeries(running, sampleCapacity);
}

export function fleetConnections(instances: Pick<FleetInstance, "id">[] | null | undefined): number {
  return (instances || []).reduce((total, item) => total + instanceConnections(item.id), 0);
}

// fleetConnectionRows's output row: a per-instance ConnectionRow stamped with
// the owning instance's id/name so the fleet-wide table can both display and
// search on it. `instanceName` is always populated here (falls back to the
// id), unlike ConnectionRow's own optional field of the same name.
export interface FleetConnectionRow extends ConnectionRow {
  instanceId: string;
  instanceName: string;
}

// Connections are stored per instance but read fleet-wide, so the owning
// instance's name has to travel with each row -- it is both a table column and
// a search term.
export function fleetConnectionRows(instances: Pick<FleetInstance, "id" | "name">[] | null | undefined): FleetConnectionRow[] {
  const rows: FleetConnectionRow[] = [];
  for (const item of instances || []) {
    const entry = samplers.get(item.id);
    if (!entry?.reachable) continue;
    for (const row of entry.connectionRows) {
      rows.push({ ...row, instanceId: item.id, instanceName: item.name || item.id });
    }
  }
  return rows;
}

// A stopped instance answers 409 from the proxy guard rather than returning
// empty data, so a rejection here is an expected state, not an error worth
// surfacing. Reset its counter baseline so the restart does not read as one
// giant delta.
export async function sampleInstance(instanceId: string, fetchConnections: FetchConnections, now: number): Promise<void> {
  const entry = sampler(instanceId);
  let payload: ConnectionsFetchPayload | null | undefined = null;
  try {
    payload = await fetchConnections(instanceId);
  } catch {
    resetConnectionState(entry);
    return;
  }
  const current: TrafficCounterSample = {
    at: now,
    uploadTotal: Number(payload?.uploadTotal) || 0,
    downloadTotal: Number(payload?.downloadTotal) || 0,
  };
  const rate = deriveRate(entry.previous, current);
  const rows = connectionSnapshot(payload);
  const { totals } = diffConnections(entry.connectionTotals, rows, entry.sampledAt ? now - entry.sampledAt : 0);
  entry.previous = current;
  entry.connectionRows = rows;
  entry.connectionTotals = totals;
  entry.sampledAt = now;
  entry.connections = rows.length;
  entry.reachable = true;
  pushSample(entry.series, { at: now, up: rate.up, down: rate.down });
}

export async function sampleFleet(
  instances: Pick<FleetInstance, "id" | "status">[] | null | undefined,
  fetchConnections: FetchConnections,
  now: number,
): Promise<void> {
  pruneSamplers(instances);
  const running = (instances || []).filter((item) => item.status === "running");
  for (const item of instances || []) {
    if (item.status !== "running") {
      const entry = samplers.get(item.id);
      if (entry) resetConnectionState(entry);
    }
  }
  await Promise.all(running.map((item) => sampleInstance(item.id, fetchConnections, now)));
}

// Country codes are resolved once per address and kept for the session: a
// connection's destination does not move between countries, and the table
// re-renders every 1.8s. A miss is cached as "" so a database that simply does
// not carry that address is not re-asked forever.
const geoCache = new Map<string, string>();
const geoPending = new Set<string>();
let geoAvailable = true;

// JSON body POST /api/geoip resolves to. `available: false` means the
// controller has no GeoIP database staged at all (a deployment choice, not a
// failed request) -- a rejected promise is the transport-error case instead,
// handled by requestGeo's `.catch`.
export interface GeoLookupResult {
  available?: boolean;
  countries?: Record<string, string>;
}

let geoFetch: ((ips: string[]) => Promise<GeoLookupResult>) | null = null;

export function setGeoResolver(fetchCountries: (ips: string[]) => Promise<GeoLookupResult>): void {
  geoFetch = fetchCountries;
}

export function requestGeo(rows: Pick<FleetConnectionRow, "ip">[]): void {
  if (!geoAvailable || !geoFetch) return;
  const wanted: string[] = [];
  for (const row of rows) {
    const ip = row.ip;
    if (!ip || geoCache.has(ip) || geoPending.has(ip) || localAddressLabel(ip)) continue;
    geoPending.add(ip);
    wanted.push(ip);
  }
  if (!wanted.length) return;
  geoFetch(wanted)
    .then((result) => {
      if (result && result.available === false) geoAvailable = false;
      const countries: Record<string, string> = result?.countries || {};
      for (const ip of wanted) geoCache.set(ip, countries[ip] || "");
    })
    .catch(() => {
      // Leave the addresses uncached so the next paint retries; a failed
      // lookup should not permanently blank the column.
    })
    .finally(() => {
      for (const ip of wanted) geoPending.delete(ip);
    });
}

// Read accessor for requestGeo()'s cache. A miss (not yet resolved, still
// pending, or genuinely unknown to the database) reads as "", matching the
// old geoCell()'s "no code yet" branch.
export function resolveGeo(ip: string): string {
  return geoCache.get(ip) || "";
}
