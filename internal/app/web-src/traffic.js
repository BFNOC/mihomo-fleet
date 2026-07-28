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
