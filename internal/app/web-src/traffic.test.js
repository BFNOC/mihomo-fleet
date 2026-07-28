import assert from "node:assert/strict";
import test from "node:test";

import {
  aggregateSeries,
  createSeries,
  deriveRate,
  formatRate,
  pushSample,
  seriesField,
  seriesLatest,
  seriesPeak,
  sparklineGeometry,
} from "./traffic.js";

test("series drops the oldest sample once capacity is reached", () => {
  const series = createSeries(3);
  for (let index = 0; index < 5; index += 1) {
    pushSample(series, { at: index * 1000, up: index, down: index * 2 });
  }
  assert.equal(series.samples.length, 3);
  assert.deepEqual(seriesField(series, "up"), [2, 3, 4]);
  assert.deepEqual(seriesLatest(series), { at: 4000, up: 4, down: 8 });
});

test("series clamps negative and non-numeric payloads to zero", () => {
  const series = createSeries(4);
  pushSample(series, { at: 1000, up: -50, down: "1200" });
  pushSample(series, { at: 2000, up: undefined, down: null });
  assert.deepEqual(seriesField(series, "up"), [0, 0]);
  assert.deepEqual(seriesField(series, "down"), [1200, 0]);
  assert.equal(seriesPeak(series, "down"), 1200);
});

test("fleet aggregate sums by wall-clock second, not by sample index", () => {
  const first = createSeries(10);
  pushSample(first, { at: 1000, up: 10, down: 100 });
  pushSample(first, { at: 2000, up: 20, down: 200 });
  const second = createSeries(10);
  // Offset within the same second: must land in the 1000ms bucket, not a new one.
  pushSample(second, { at: 1400, up: 5, down: 50 });
  pushSample(second, { at: 3000, up: 7, down: 70 });

  const merged = aggregateSeries([first, second], 10);
  assert.deepEqual(merged.samples, [
    { at: 1000, up: 15, down: 150 },
    { at: 2000, up: 20, down: 200 },
    { at: 3000, up: 7, down: 70 },
  ]);
});

test("fleet aggregate keeps only the newest samples within capacity", () => {
  const series = createSeries(10);
  for (let index = 1; index <= 6; index += 1) pushSample(series, { at: index * 1000, up: index, down: 0 });
  const merged = aggregateSeries([series], 2);
  assert.deepEqual(seriesField(merged, "up"), [5, 6]);
});

test("fleet aggregate of no running instances yields an empty series", () => {
  assert.deepEqual(aggregateSeries([], 10).samples, []);
  assert.deepEqual(aggregateSeries([createSeries(5)], 10).samples, []);
});

test("sparkline maps values onto the box and pins the peak to the top edge", () => {
  const geometry = sparklineGeometry([0, 50, 100], { width: 100, height: 20 });
  assert.equal(geometry.ceiling, 100);
  assert.equal(geometry.line, "M0 20 L50 10 L100 0");
  assert.equal(geometry.area, "M0 20 L0 20 L50 10 L100 0 L100 20 Z");
});

test("sparkline honours an explicit ceiling so paired charts share a scale", () => {
  const geometry = sparklineGeometry([25, 50], { width: 100, height: 20, max: 100 });
  assert.equal(geometry.ceiling, 100);
  assert.equal(geometry.line, "M0 15 L100 10");
});

test("sparkline ceiling still grows when a value exceeds the requested max", () => {
  const geometry = sparklineGeometry([0, 400], { width: 100, height: 20, max: 100 });
  assert.equal(geometry.ceiling, 400);
});

test("sparkline renders an all-zero window as a flat baseline instead of dividing by zero", () => {
  const geometry = sparklineGeometry([0, 0, 0], { width: 100, height: 20 });
  assert.equal(geometry.ceiling, 0);
  assert.equal(geometry.line, "M0 20 L50 20 L100 20");
  assert.ok(geometry.points.every((point) => Number.isFinite(point.y)));
});

test("sparkline spans a single sample across the whole box so the stroke is visible", () => {
  const geometry = sparklineGeometry([80], { width: 100, height: 20 });
  assert.equal(geometry.line, "M0 0 L100 0");
  assert.equal(geometry.area, "M0 20 L0 0 L100 0 L100 20 Z");
});

test("sparkline returns null for an empty window or a collapsed box", () => {
  assert.equal(sparklineGeometry([], { width: 100, height: 20 }), null);
  assert.equal(sparklineGeometry([1, 2], { width: 0, height: 20 }), null);
  assert.equal(sparklineGeometry([1, 2], { width: 100, height: 0 }), null);
});

test("rate derives bytes per second from cumulative counters over elapsed time", () => {
  const previous = { at: 1000, uploadTotal: 1000, downloadTotal: 5000 };
  const current = { at: 3000, uploadTotal: 3000, downloadTotal: 15000 };
  assert.deepEqual(deriveRate(previous, current), { up: 1000, down: 5000 });
});

test("rate treats a counter reset from an instance restart as zero, not a spike", () => {
  const previous = { at: 1000, uploadTotal: 900_000, downloadTotal: 900_000 };
  const current = { at: 2800, uploadTotal: 120, downloadTotal: 0 };
  assert.deepEqual(deriveRate(previous, current), { up: 0, down: 0 });
});

test("rate refuses to divide by a zero or backwards time delta", () => {
  const sample = { at: 2000, uploadTotal: 10, downloadTotal: 10 };
  assert.deepEqual(deriveRate({ ...sample, at: 2000 }, sample), { up: 0, down: 0 });
  assert.deepEqual(deriveRate({ ...sample, at: 5000 }, sample), { up: 0, down: 0 });
  assert.deepEqual(deriveRate(null, sample), { up: 0, down: 0 });
  assert.deepEqual(deriveRate(sample, null), { up: 0, down: 0 });
});

test("rate formatting switches unit and precision by magnitude", () => {
  assert.deepEqual(formatRate(0), { value: "0", unit: "B/s" });
  assert.deepEqual(formatRate(999), { value: "999", unit: "B/s" });
  assert.deepEqual(formatRate(22600), { value: "22.6", unit: "kB/s" });
  assert.deepEqual(formatRate(132000), { value: "132", unit: "kB/s" });
  assert.deepEqual(formatRate(5_400_000), { value: "5.4", unit: "MB/s" });
});
