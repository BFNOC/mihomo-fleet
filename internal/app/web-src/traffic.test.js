import assert from "node:assert/strict";
import test from "node:test";

import {
  aggregateSeries,
  connectionSnapshot,
  countryFlag,
  createSeries,
  deriveRate,
  diffConnections,
  filterConnections,
  formatDuration,
  formatRate,
  localAddressLabel,
  pushSample,
  seriesField,
  seriesLatest,
  seriesPeak,
  sortConnections,
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

test("connection snapshot flattens mihomo metadata and tolerates a missing payload", () => {
  const [row] = connectionSnapshot({
    connections: [
      {
        id: "c1",
        chains: ["HK-01", "auto", "PROXY"],
        rule: "RuleSet",
        rulePayload: "gfw",
        upload: 120,
        download: 4096,
        start: "2026-07-28T04:00:00.000Z",
        metadata: {
          host: "example.com",
          destinationIP: "1.2.3.4",
          destinationPort: "443",
          sourceIP: "127.0.0.1",
          network: "tcp",
          type: "HTTPS",
          process: "curl",
        },
      },
    ],
  });
  assert.equal(row.host, "example.com");
  assert.equal(row.ip, "1.2.3.4");
  assert.equal(row.port, "443");
  assert.equal(row.network, "TCP");
  assert.equal(row.node, "HK-01");
  assert.equal(row.rulePayload, "gfw");
  assert.equal(row.start, Date.parse("2026-07-28T04:00:00.000Z"));
  assert.deepEqual(connectionSnapshot(null), []);
  assert.deepEqual(connectionSnapshot({ connections: "nope" }), []);
});

test("connection snapshot falls back to the sniffed host and blanks missing fields", () => {
  const [row] = connectionSnapshot({ connections: [{ metadata: { sniffHost: "sniffed.example" } }] });
  assert.equal(row.host, "sniffed.example");
  assert.equal(row.ip, "");
  assert.equal(row.node, "");
  assert.equal(row.start, 0);
  assert.equal(row.upload, 0);
});

test("per-connection rate differences cumulative counters by id", () => {
  const previous = new Map([["c1", { upload: 100, download: 1000 }]]);
  const rows = [
    { id: "c1", upload: 1100, download: 3000, up: 0, down: 0 },
    { id: "c2", upload: 50, download: 60, up: 0, down: 0 },
  ];
  const { totals } = diffConnections(previous, rows, 2000);
  assert.deepEqual({ up: rows[0].up, down: rows[0].down }, { up: 500, down: 1000 });
  // First sighting has no baseline, so it reports zero rather than its total.
  assert.deepEqual({ up: rows[1].up, down: rows[1].down }, { up: 0, down: 0 });
  assert.deepEqual(totals.get("c1"), { upload: 1100, download: 3000 });
  assert.deepEqual(totals.get("c2"), { upload: 50, download: 60 });
});

test("per-connection rate clamps reused ids and refuses a zero time delta", () => {
  const previous = new Map([["c1", { upload: 900, download: 900 }]]);
  const reused = [{ id: "c1", upload: 10, download: 0, up: 9, down: 9 }];
  diffConnections(previous, reused, 1800);
  assert.deepEqual({ up: reused[0].up, down: reused[0].down }, { up: 0, down: 0 });
  const stalled = [{ id: "c1", upload: 5000, download: 5000, up: 9, down: 9 }];
  diffConnections(previous, stalled, 0);
  assert.deepEqual({ up: stalled[0].up, down: stalled[0].down }, { up: 0, down: 0 });
});

test("connections sort by live rate first, then by cumulative bytes", () => {
  const rows = [
    { id: "idle-big", up: 0, down: 0, upload: 0, download: 9_000_000, start: 1 },
    { id: "busy", up: 0, down: 2000, upload: 0, download: 10, start: 2 },
    { id: "idle-small", up: 0, down: 0, upload: 0, download: 10, start: 3 },
  ];
  assert.deepEqual(sortConnections(rows).map((row) => row.id), ["busy", "idle-big", "idle-small"]);
  // Sorting must not disturb the caller's array, which is reused next tick.
  assert.deepEqual(rows.map((row) => row.id), ["idle-big", "busy", "idle-small"]);
});

test("connection filter matches host, ip, process, rule, chain and instance name", () => {
  const rows = [
    { host: "example.com", ip: "1.1.1.1", process: "curl", rule: "RuleSet", rulePayload: "gfw", chains: ["HK-01"], instanceName: "主线路" },
    { host: "", ip: "8.8.8.8", process: "dig", rule: "DIRECT", rulePayload: "", chains: ["DIRECT"], instanceName: "备用" },
  ];
  assert.equal(filterConnections(rows, "EXAMPLE").length, 1);
  assert.equal(filterConnections(rows, "8.8.8").length, 1);
  assert.equal(filterConnections(rows, "hk-01").length, 1);
  assert.equal(filterConnections(rows, "备用").length, 1);
  assert.equal(filterConnections(rows, "direct").length, 1);
  assert.equal(filterConnections(rows, "  ").length, 2);
  assert.equal(filterConnections(rows, "nothing").length, 0);
});

test("local address labels cover loopback, RFC1918, CGNAT and link-local", () => {
  assert.equal(localAddressLabel("127.0.0.1"), "本机");
  assert.equal(localAddressLabel("::1"), "本机");
  assert.equal(localAddressLabel("10.4.5.6"), "内网");
  assert.equal(localAddressLabel("192.168.1.10"), "内网");
  assert.equal(localAddressLabel("172.16.0.1"), "内网");
  assert.equal(localAddressLabel("172.31.255.254"), "内网");
  assert.equal(localAddressLabel("fd00::1"), "内网");
  assert.equal(localAddressLabel("100.64.0.1"), "运营商 NAT");
  assert.equal(localAddressLabel("169.254.1.1"), "链路本地");
  assert.equal(localAddressLabel("fe80::1"), "链路本地");
  // Public space, and neighbours of the private ranges, must stay lookupable.
  assert.equal(localAddressLabel("172.15.0.1"), "");
  assert.equal(localAddressLabel("172.32.0.1"), "");
  assert.equal(localAddressLabel("100.128.0.1"), "");
  assert.equal(localAddressLabel("192.169.0.1"), "");
  assert.equal(localAddressLabel("8.8.8.8"), "");
  assert.equal(localAddressLabel(""), "");
});

test("country flag maps ISO letters to regional indicators and rejects the rest", () => {
  assert.equal(countryFlag("US"), "\u{1F1FA}\u{1F1F8}");
  assert.equal(countryFlag("cn"), "\u{1F1E8}\u{1F1F3}");
  // The bundled database returns values like GOOGLE that are not ISO codes.
  assert.equal(countryFlag("GOOGLE"), "");
  assert.equal(countryFlag("U1"), "");
  assert.equal(countryFlag(""), "");
  assert.equal(countryFlag(null), "");
});

test("duration formatting steps from seconds to hours", () => {
  assert.equal(formatDuration(0), "0s");
  assert.equal(formatDuration(-5000), "0s");
  assert.equal(formatDuration(45_000), "45s");
  assert.equal(formatDuration(59_999), "59s");
  assert.equal(formatDuration(60_000), "1m");
  assert.equal(formatDuration(3_599_000), "59m");
  assert.equal(formatDuration(3_600_000), "1h0m");
  assert.equal(formatDuration(7_860_000), "2h11m");
});
