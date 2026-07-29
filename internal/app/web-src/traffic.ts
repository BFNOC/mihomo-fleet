export const trafficWindowSeconds = 60;

// A single up/down sample at one point in time, stored inside a TrafficSeries.
export interface TrafficSample {
  at: number;
  up: number;
  down: number;
}

// A ring buffer of TrafficSample, capped at `capacity` entries. Shared by the
// per-instance sampler and the fleet-wide aggregate in dashboard.ts.
export interface TrafficSeries {
  capacity: number;
  samples: TrafficSample[];
}

// pushSample's input is a raw poll result: every field is defensively
// re-validated with Number()/Math.max below (a restarted instance, a bad
// payload, or a stray `null` must not throw), so the fields stay `unknown`
// rather than claiming a shape the caller cannot actually guarantee.
export interface TrafficSampleInput {
  at?: unknown;
  up?: unknown;
  down?: unknown;
}

// The first/last timestamps covered by a series, used to label a trend chart.
export interface TrafficSpan {
  from: number;
  to: number;
}

// Cumulative byte counters as reported by mihomo's /connections endpoint at
// one point in time -- the input deriveRate differentiates into a TrafficRate.
export interface TrafficCounterSample {
  at: number;
  uploadTotal: number;
  downloadTotal: number;
}

export interface TrafficRate {
  up: number;
  down: number;
}

export function createSeries(capacity: number = trafficWindowSeconds): TrafficSeries {
  return { capacity: Math.max(1, Math.floor(capacity) || 1), samples: [] };
}

export function pushSample(series: TrafficSeries, sample: TrafficSampleInput | null | undefined): TrafficSeries {
  const at = Number(sample?.at) || 0;
  series.samples.push({
    at,
    up: Math.max(0, Number(sample?.up) || 0),
    down: Math.max(0, Number(sample?.down) || 0),
  });
  while (series.samples.length > series.capacity) series.samples.shift();
  return series;
}

export function seriesField(series: TrafficSeries | null | undefined, field: "up" | "down"): number[] {
  return (series?.samples || []).map((sample) => Number(sample[field]) || 0);
}

export function seriesPeak(series: TrafficSeries | null | undefined, field: "up" | "down"): number {
  return seriesField(series, field).reduce((peak, value) => (value > peak ? value : peak), 0);
}

export function seriesLatest(series: TrafficSeries | null | undefined): TrafficSample | null {
  const samples = series?.samples || [];
  // .at(-1) already types as TrafficSample | undefined regardless of
  // noUncheckedIndexedAccess, so it doubles as the empty-array check.
  return samples.at(-1) ?? null;
}

export function seriesSpan(series: TrafficSeries | null | undefined): TrafficSpan | null {
  const samples = series?.samples || [];
  const first = samples.at(0);
  const last = samples.at(-1);
  if (!first || !last) return null;
  return { from: first.at, to: last.at };
}

// Each instance streams on its own schedule, so a fleet total cannot just add
// index-to-index. Bucket every sample to its wall-clock second and sum the
// buckets, leaving gaps where an instance had not reported yet.
export function aggregateSeries(
  seriesList: ReadonlyArray<TrafficSeries | null | undefined> | null | undefined,
  capacity: number = trafficWindowSeconds,
): TrafficSeries {
  const buckets = new Map<number, TrafficSample>();
  for (const series of seriesList || []) {
    for (const sample of series?.samples || []) {
      const second = Math.floor(sample.at / 1000);
      const bucket = buckets.get(second) || { at: second * 1000, up: 0, down: 0 };
      bucket.up += Number(sample.up) || 0;
      bucket.down += Number(sample.down) || 0;
      buckets.set(second, bucket);
    }
  }
  const merged = createSeries(capacity);
  const ordered = [...buckets.keys()].sort((a, b) => a - b).slice(-merged.capacity);
  for (const second of ordered) {
    // `second` was just read back from buckets.keys(), so the entry always
    // exists; the guard is here only to satisfy noUncheckedIndexedAccess.
    const bucket = buckets.get(second);
    if (bucket) merged.samples.push(bucket);
  }
  return merged;
}

export interface SparklineOptions {
  width?: number;
  height?: number;
  max?: number;
}

export interface SparklinePoint {
  x: number;
  y: number;
}

export interface SparklineGeometry {
  line: string;
  area: string;
  points: SparklinePoint[];
  ceiling: number;
}

// Geometry only -- no DOM, so the shape is unit-testable. Callers drop the
// returned path strings straight into an <svg><path d="..."> pair.
export function sparklineGeometry(
  values: ReadonlyArray<number> | null | undefined,
  options: SparklineOptions = {},
): SparklineGeometry | null {
  const width = Number(options.width) || 0;
  const height = Number(options.height) || 0;
  const list = (values || []).map((value) => Math.max(0, Number(value) || 0));
  if (!list.length || width <= 0 || height <= 0) return null;

  const ceiling = Math.max(Number(options.max) || 0, ...list);
  const round = (value: number) => Number(value.toFixed(2));
  const yFor = (value: number) => round(ceiling > 0 ? height - (value / ceiling) * height : height);

  // A lone sample has no horizontal extent, so span it across the whole box.
  // Every downstream step then works on a uniform two-or-more point list.
  const points: SparklinePoint[] = list.length > 1
    ? list.map((value, index) => ({ x: round((index * width) / (list.length - 1)), y: yFor(value) }))
    // list.length is exactly 1 here (the `!list.length` check above already
    // ruled out empty), so the `?? 0` fallback never actually triggers -- it
    // only satisfies noUncheckedIndexedAccess.
    : [{ x: 0, y: yFor(list[0] ?? 0) }, { x: round(width), y: yFor(list[0] ?? 0) }];

  const baseline = round(height);
  const first = points[0];
  const last = points[points.length - 1];
  // points always has >=2 entries by construction above; this guard exists
  // only so the type checker can see that, without an unsafe assertion.
  if (!first || !last) return null;
  const line = points.map((point, index) => `${index === 0 ? "M" : "L"}${point.x} ${point.y}`).join(" ");
  const area = [
    `M${first.x} ${baseline}`,
    ...points.map((point) => `L${point.x} ${point.y}`),
    `L${last.x} ${baseline}`,
    "Z",
  ].join(" ");

  return { line, area, points, ceiling };
}

// /connections reports cumulative byte counters, so a rate is the delta over
// elapsed time. A restarted instance resets its counters to zero; without the
// regression guard that shows up as one enormous negative-turned-garbage spike
// that then pins the whole chart's ceiling for the next 60 seconds.
export function deriveRate(
  previous: TrafficCounterSample | null | undefined,
  current: TrafficCounterSample | null | undefined,
): TrafficRate {
  const zero: TrafficRate = { up: 0, down: 0 };
  if (!previous || !current) return zero;
  const elapsedMs = Number(current.at) - Number(previous.at);
  if (!Number.isFinite(elapsedMs) || elapsedMs <= 0) return zero;
  const seconds = elapsedMs / 1000;
  const delta = (field: "uploadTotal" | "downloadTotal") => {
    const before = Number(previous[field]) || 0;
    const after = Number(current[field]) || 0;
    if (after < before) return 0;
    return (after - before) / seconds;
  };
  return { up: delta("uploadTotal"), down: delta("downloadTotal") };
}

// The shape of mihomo's /connections response. It is untrusted network input,
// so `connections` stays `unknown` -- a malformed payload (missing field,
// wrong type) must be handled defensively rather than rejected outright by
// the type checker; normalizeConnection revalidates every field it reads.
export interface ConnectionsPayload {
  connections?: unknown;
}

// The normalized shape every connection row is rendered from, produced here
// from mihomo's raw /connections entries. diffConnections fills in up/down in
// place; sortConnections and filterConnections then operate on the result.
// `instanceName` is not set here -- the fleet-wide connection table (built in
// dashboard.ts) stamps it on afterwards so it can be searched alongside the
// mihomo fields.
export interface ConnectionRow {
  id: string;
  host: string;
  ip: string;
  port: string;
  sourceIP: string;
  network: string;
  kind: string;
  process: string;
  chains: string[];
  node: string;
  rule: string;
  rulePayload: string;
  upload: number;
  download: number;
  start: number;
  up: number;
  down: number;
  instanceName?: string;
}

// The same /connections payload the fleet chart derives its totals from also
// carries one entry per live connection. Normalizing here keeps mihomo's exact
// field names (and the defensive `?.` chains they need) out of the renderer.
export function connectionSnapshot(payload: ConnectionsPayload | null | undefined): ConnectionRow[] {
  const list = Array.isArray(payload?.connections) ? payload.connections : [];
  return list.map((raw: unknown) => normalizeConnection(raw));
}

// Narrows an unknown value to a plain-object property bag, or `{}` for
// anything else (null, a primitive, a differently-shaped payload). Every
// field pulled off the result is still individually re-validated below, so
// this only needs to rule out crashing on `.property` access.
function asRecord(value: unknown): Record<string, unknown> {
  return typeof value === "object" && value !== null ? (value as Record<string, unknown>) : {};
}

function normalizeConnection(raw: unknown): ConnectionRow {
  const record = asRecord(raw);
  const meta = asRecord(record.metadata);
  const chains = Array.isArray(record.chains) ? record.chains.map((value) => String(value)) : [];
  const text = (value: unknown) => String(value ?? "").trim();
  return {
    id: text(record.id),
    host: text(meta.host) || text(meta.sniffHost),
    ip: text(meta.destinationIP),
    port: text(meta.destinationPort),
    sourceIP: text(meta.sourceIP),
    network: text(meta.network).toUpperCase(),
    kind: text(meta.type),
    process: text(meta.process),
    // chains[0] is the node that actually carried the request; the rest are
    // the groups it was picked through, outermost last.
    chains,
    node: chains[0] || "",
    rule: text(record.rule),
    rulePayload: text(record.rulePayload),
    upload: Math.max(0, Number(record.upload) || 0),
    download: Math.max(0, Number(record.download) || 0),
    start: Date.parse(text(record.start)) || 0,
    up: 0,
    down: 0,
  };
}

// A cumulative upload/download total, keyed by connection id -- both the
// previous-tick baseline diffConnections reads and the map it hands back.
export interface ConnectionTotals {
  upload: number;
  download: number;
}

// The fields diffConnections needs from a row: enough to look up and update
// its counters. ConnectionRow satisfies this structurally, but the function
// only demands what it actually touches so callers (including the tests) can
// pass minimal row-like objects.
export interface ConnectionRateFields {
  id?: string;
  upload: number;
  download: number;
  up: number;
  down: number;
}

// Per-connection speed is the same cumulative-counter differencing deriveRate
// does for the fleet, keyed by connection id. A closed connection just stops
// appearing, so a counter that went backwards can only mean an id was reused --
// clamp to zero instead of emitting a negative rate. Rates are written onto the
// rows in place; the returned map is the next tick's baseline.
export function diffConnections<T extends ConnectionRateFields>(
  previous: Map<string, ConnectionTotals> | null | undefined,
  rows: T[] | null | undefined,
  elapsedMs: number,
): { rows: T[]; totals: Map<string, ConnectionTotals> } {
  const elapsed = Number(elapsedMs) || 0;
  const seconds = elapsed > 0 ? elapsed / 1000 : 0;
  const totals = new Map<string, ConnectionTotals>();
  for (const row of rows || []) {
    if (row.id) totals.set(row.id, { upload: row.upload, download: row.download });
    const before = row.id && seconds ? previous?.get(row.id) : null;
    if (!before) {
      row.up = 0;
      row.down = 0;
      continue;
    }
    row.up = row.upload > before.upload ? (row.upload - before.upload) / seconds : 0;
    row.down = row.download > before.download ? (row.download - before.download) / seconds : 0;
  }
  return { rows: rows || [], totals };
}

// The fields sortConnections needs to rank rows by activity, then by
// cumulative bytes, then by recency.
export interface ConnectionSortFields {
  up: number;
  down: number;
  upload: number;
  download: number;
  start?: number;
}

// "activity" is the default busiest-first ranking; the other keys are the
// user-facing sortable columns of the connections table.
export type ConnectionSortKey = "activity" | "up" | "down" | "duration";
export type ConnectionSortDirection = "asc" | "desc";

// Rate → bytes → recency. Shared by the default order and as the tie-breaker
// for the column sorts, so equal-valued rows keep a stable, meaningful order.
function compareActivity(a: ConnectionSortFields, b: ConnectionSortFields): number {
  const rate = (b.down + b.up) - (a.down + a.up);
  if (rate) return rate;
  const bytes = (b.download + b.upload) - (a.download + a.upload);
  if (bytes) return bytes;
  return (b.start || 0) - (a.start || 0);
}

// Busiest first by default: a fleet can hold thousands of connections and only
// the top of this list ever gets rendered, so idle keepalives must not crowd
// out the transfer someone is actually watching. Column sorts rank by the
// picked field only, falling back to activity on ties. A row without `start`
// has no duration at all, so it sorts after every row that has one in both
// directions rather than pretending to be the newest or the oldest.
export function sortConnections<T extends ConnectionSortFields>(
  rows: T[] | null | undefined,
  key: ConnectionSortKey = "activity",
  direction: ConnectionSortDirection = "desc",
): T[] {
  const sign = direction === "asc" ? -1 : 1;
  return [...(rows || [])].sort((a, b) => {
    let primary = 0;
    if (key === "up") primary = sign * (b.up - a.up);
    else if (key === "down") primary = sign * (b.down - a.down);
    else if (key === "duration") {
      if (!a.start || !b.start) primary = (b.start ? 1 : 0) - (a.start ? 1 : 0);
      // Older start = longer duration, so "desc" (longest first) is ascending start.
      else primary = sign * (a.start - b.start);
    }
    return primary || compareActivity(a, b);
  });
}

// The fields the search box can match against, gathered by connectionHaystack.
export interface ConnectionFilterFields {
  host?: string;
  ip?: string;
  port?: string;
  sourceIP?: string;
  process?: string;
  rule?: string;
  rulePayload?: string;
  network?: string;
  kind?: string;
  instanceName?: string;
  chains?: string[];
}

export function filterConnections<T extends ConnectionFilterFields>(
  rows: T[] | null | undefined,
  query: string,
): T[] {
  const needle = String(query || "").trim().toLowerCase();
  if (!needle) return rows || [];
  return (rows || []).filter((row) => connectionHaystack(row).includes(needle));
}

function connectionHaystack(row: ConnectionFilterFields): string {
  return [
    row.host,
    row.ip,
    row.port,
    row.sourceIP,
    row.process,
    row.rule,
    row.rulePayload,
    row.network,
    row.kind,
    row.instanceName,
    ...(row.chains || []),
  ].filter(Boolean).join(" ").toLowerCase();
}

// Private, loopback, link-local and CGNAT space is never in a country database,
// so the table labels it locally instead of spending a lookup on it.
export function localAddressLabel(ip: string): string {
  const value = String(ip || "").trim();
  if (!value) return "";
  if (value === "::1" || value.startsWith("127.")) return "本机";
  if (value.startsWith("169.254.") || /^fe80:/i.test(value)) return "链路本地";
  if (/^f[cd][0-9a-f]{2}:/i.test(value)) return "内网";
  const [a, b] = value.split(".");
  const first = Number(a);
  const second = Number(b);
  if (first === 10) return "内网";
  if (first === 192 && second === 168) return "内网";
  if (first === 172 && second >= 16 && second <= 31) return "内网";
  if (first === 100 && second >= 64 && second <= 127) return "运营商 NAT";
  return "";
}

// A regional indicator pair renders as a flag where the platform has one and as
// the two letters everywhere else, which is exactly the fallback we want.
export function countryFlag(code: string | null | undefined): string {
  const value = String(code || "").toUpperCase();
  if (!/^[A-Z]{2}$/.test(value)) return "";
  return String.fromCodePoint(...[...value].map((ch) => 0x1f1e6 + ch.charCodeAt(0) - 65));
}

export function formatDuration(ms: number): string {
  const total = Math.max(0, Math.floor((Number(ms) || 0) / 1000));
  if (total < 60) return `${total}s`;
  const minutes = Math.floor(total / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  return `${hours}h${minutes % 60}m`;
}

export interface FormattedRate {
  value: string;
  unit: string;
}

export function formatRate(bytesPerSecond: number): FormattedRate {
  const units = ["B/s", "kB/s", "MB/s", "GB/s"];
  let size = Math.max(0, Number(bytesPerSecond) || 0);
  let unit = 0;
  while (size >= 1000 && unit < units.length - 1) {
    size /= 1000;
    unit += 1;
  }
  const digits = unit === 0 || size >= 100 ? 0 : 1;
  // `unit` is clamped above to [0, units.length - 1], so this lookup always
  // hits; the fallback only exists to satisfy noUncheckedIndexedAccess.
  return { value: size.toFixed(digits), unit: units[unit] ?? "B/s" };
}
