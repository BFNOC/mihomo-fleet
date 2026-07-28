export const trafficWindowSeconds = 60;

export function createSeries(capacity = trafficWindowSeconds) {
  return { capacity: Math.max(1, Math.floor(capacity) || 1), samples: [] };
}

export function pushSample(series, sample) {
  const at = Number(sample?.at) || 0;
  series.samples.push({
    at,
    up: Math.max(0, Number(sample?.up) || 0),
    down: Math.max(0, Number(sample?.down) || 0),
  });
  while (series.samples.length > series.capacity) series.samples.shift();
  return series;
}

export function seriesField(series, field) {
  return (series?.samples || []).map((sample) => Number(sample[field]) || 0);
}

export function seriesPeak(series, field) {
  return seriesField(series, field).reduce((peak, value) => (value > peak ? value : peak), 0);
}

export function seriesLatest(series) {
  const samples = series?.samples || [];
  return samples.length ? samples[samples.length - 1] : null;
}

export function seriesSpan(series) {
  const samples = series?.samples || [];
  if (!samples.length) return null;
  return { from: samples[0].at, to: samples[samples.length - 1].at };
}

// Each instance streams on its own schedule, so a fleet total cannot just add
// index-to-index. Bucket every sample to its wall-clock second and sum the
// buckets, leaving gaps where an instance had not reported yet.
export function aggregateSeries(seriesList, capacity = trafficWindowSeconds) {
  const buckets = new Map();
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
  for (const second of ordered) merged.samples.push(buckets.get(second));
  return merged;
}

// Geometry only -- no DOM, so the shape is unit-testable. Callers drop the
// returned path strings straight into an <svg><path d="..."> pair.
export function sparklineGeometry(values, options = {}) {
  const width = Number(options.width) || 0;
  const height = Number(options.height) || 0;
  const list = (values || []).map((value) => Math.max(0, Number(value) || 0));
  if (!list.length || width <= 0 || height <= 0) return null;

  const ceiling = Math.max(Number(options.max) || 0, ...list);
  const round = (value) => Number(value.toFixed(2));
  const yFor = (value) => round(ceiling > 0 ? height - (value / ceiling) * height : height);

  // A lone sample has no horizontal extent, so span it across the whole box.
  // Every downstream step then works on a uniform two-or-more point list.
  const points = list.length > 1
    ? list.map((value, index) => ({ x: round((index * width) / (list.length - 1)), y: yFor(value) }))
    : [{ x: 0, y: yFor(list[0]) }, { x: round(width), y: yFor(list[0]) }];

  const baseline = round(height);
  const last = points[points.length - 1];
  const line = points.map((point, index) => `${index === 0 ? "M" : "L"}${point.x} ${point.y}`).join(" ");
  const area = [
    `M${points[0].x} ${baseline}`,
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
export function deriveRate(previous, current) {
  const zero = { up: 0, down: 0 };
  if (!previous || !current) return zero;
  const elapsedMs = Number(current.at) - Number(previous.at);
  if (!Number.isFinite(elapsedMs) || elapsedMs <= 0) return zero;
  const seconds = elapsedMs / 1000;
  const delta = (field) => {
    const before = Number(previous[field]) || 0;
    const after = Number(current[field]) || 0;
    if (after < before) return 0;
    return (after - before) / seconds;
  };
  return { up: delta("uploadTotal"), down: delta("downloadTotal") };
}

// The same /connections payload the fleet chart derives its totals from also
// carries one entry per live connection. Normalizing here keeps mihomo's exact
// field names (and the defensive `?.` chains they need) out of the renderer.
export function connectionSnapshot(payload) {
  const list = Array.isArray(payload?.connections) ? payload.connections : [];
  return list.map(normalizeConnection);
}

function normalizeConnection(raw) {
  const meta = raw?.metadata || {};
  const chains = Array.isArray(raw?.chains) ? raw.chains.map((value) => String(value)) : [];
  const text = (value) => String(value ?? "").trim();
  return {
    id: text(raw?.id),
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
    rule: text(raw?.rule),
    rulePayload: text(raw?.rulePayload),
    upload: Math.max(0, Number(raw?.upload) || 0),
    download: Math.max(0, Number(raw?.download) || 0),
    start: Date.parse(text(raw?.start)) || 0,
    up: 0,
    down: 0,
  };
}

// Per-connection speed is the same cumulative-counter differencing deriveRate
// does for the fleet, keyed by connection id. A closed connection just stops
// appearing, so a counter that went backwards can only mean an id was reused --
// clamp to zero instead of emitting a negative rate. Rates are written onto the
// rows in place; the returned map is the next tick's baseline.
export function diffConnections(previous, rows, elapsedMs) {
  const elapsed = Number(elapsedMs) || 0;
  const seconds = elapsed > 0 ? elapsed / 1000 : 0;
  const totals = new Map();
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

// Busiest first: a fleet can hold thousands of connections and only the top of
// this list ever gets rendered, so idle keepalives must not crowd out the
// transfer someone is actually watching.
export function sortConnections(rows) {
  return [...(rows || [])].sort((a, b) => {
    const rate = (b.down + b.up) - (a.down + a.up);
    if (rate) return rate;
    const bytes = (b.download + b.upload) - (a.download + a.upload);
    if (bytes) return bytes;
    return (b.start || 0) - (a.start || 0);
  });
}

export function filterConnections(rows, query) {
  const needle = String(query || "").trim().toLowerCase();
  if (!needle) return rows || [];
  return (rows || []).filter((row) => connectionHaystack(row).includes(needle));
}

function connectionHaystack(row) {
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
export function localAddressLabel(ip) {
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
export function countryFlag(code) {
  const value = String(code || "").toUpperCase();
  if (!/^[A-Z]{2}$/.test(value)) return "";
  return String.fromCodePoint(...[...value].map((ch) => 0x1f1e6 + ch.charCodeAt(0) - 65));
}

export function formatDuration(ms) {
  const total = Math.max(0, Math.floor((Number(ms) || 0) / 1000));
  if (total < 60) return `${total}s`;
  const minutes = Math.floor(total / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  return `${hours}h${minutes % 60}m`;
}

export function formatRate(bytesPerSecond) {
  const units = ["B/s", "kB/s", "MB/s", "GB/s"];
  let size = Math.max(0, Number(bytesPerSecond) || 0);
  let unit = 0;
  while (size >= 1000 && unit < units.length - 1) {
    size /= 1000;
    unit += 1;
  }
  const digits = unit === 0 || size >= 100 ? 0 : 1;
  return { value: size.toFixed(digits), unit: units[unit] };
}

export function formatRateText(bytesPerSecond) {
  const { value, unit } = formatRate(bytesPerSecond);
  return `${value} ${unit}`;
}
