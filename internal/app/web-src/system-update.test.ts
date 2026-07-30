import assert from "node:assert/strict";
import test from "node:test";

import {
  coreApplyDisabled,
  describeCoreChecksumNote,
  describeCoreStatus,
  describeGeoFile,
  describeGeoResult,
  formatBytes,
  formatGeoProgress,
  formatSpeed,
  geoApplyDisabled,
  geoFileDescription,
  geoFileLabel,
  geoProgressPercent,
  geoSummaryText,
} from "./views/system/system-update.ts";
import type { FleetCoreUpdateStatus, FleetGeoFileStatus, FleetGeoUpdateStatus } from "./state.ts";

function coreStatus(overrides: Partial<FleetCoreUpdateStatus> = {}): FleetCoreUpdateStatus {
  return { installed: true, updateAvailable: false, checksumAvailable: false, ...overrides };
}

test("describeCoreStatus reports not-installed before anything else", () => {
  assert.match(describeCoreStatus(coreStatus({ installed: false })), /未检测到 mihomo/);
});

test("describeCoreStatus surfaces a check error", () => {
  assert.equal(describeCoreStatus(coreStatus({ checkError: "network down" })), "检测失败：network down");
});

test("describeCoreStatus reports up to date", () => {
  const text = describeCoreStatus(coreStatus({ currentVersion: "1.19.29", latestVersion: "1.19.29", updateAvailable: false }));
  assert.match(text, /已是最新版本/);
  assert.match(text, /1\.19\.29/);
});

test("describeCoreStatus names the target version when an update is available", () => {
  const text = describeCoreStatus(coreStatus({ currentVersion: "1.19.28", latestVersion: "1.19.29", updateAvailable: true }));
  assert.match(text, /1\.19\.29/);
  assert.match(text, /1\.19\.28/);
});

test("describeCoreChecksumNote is empty unless an update is available with no checksum", () => {
  assert.equal(describeCoreChecksumNote(coreStatus({ updateAvailable: false })), "");
  assert.equal(describeCoreChecksumNote(coreStatus({ updateAvailable: true, checksumAvailable: true })), "");
  assert.notEqual(describeCoreChecksumNote(coreStatus({ updateAvailable: true, checksumAvailable: false })), "");
  assert.equal(describeCoreChecksumNote(coreStatus({ installed: false, updateAvailable: true })), "");
});

test("coreApplyDisabled requires installed + update available + checksum available + not busy", () => {
  assert.equal(coreApplyDisabled(coreStatus({ updateAvailable: true, checksumAvailable: true }), false), false);
  assert.equal(coreApplyDisabled(coreStatus({ updateAvailable: true, checksumAvailable: true }), true), true);
  assert.equal(coreApplyDisabled(coreStatus({ updateAvailable: true, checksumAvailable: false }), false), true);
  assert.equal(coreApplyDisabled(coreStatus({ updateAvailable: false, checksumAvailable: true }), false), true);
  assert.equal(coreApplyDisabled(coreStatus({ installed: false, updateAvailable: true, checksumAvailable: true }), false), true);
});

function geoFile(overrides: Partial<FleetGeoFileStatus> = {}): FleetGeoFileStatus {
  return { name: "GeoIP.dat", present: true, checksumAvailable: true, updateAvailable: false, ...overrides };
}

test("geoFileLabel maps known canonical names and falls back for unknown ones", () => {
  assert.equal(geoFileLabel("GeoIP.dat"), "GeoIP 规则库");
  assert.equal(geoFileLabel("Something.new"), "Something.new");
});

test("describeGeoFile covers missing/unverifiable/up-to-date/updatable", () => {
  assert.match(describeGeoFile(geoFile({ present: false, checksumAvailable: true })), /可下载/);
  assert.match(describeGeoFile(geoFile({ present: false, checksumAvailable: false })), /未发布校验和/);
  assert.match(describeGeoFile(geoFile({ present: true, checksumAvailable: false })), /无法确认/);
  assert.match(describeGeoFile(geoFile({ present: true, checksumAvailable: true, updateAvailable: true })), /有更新/);
  assert.match(describeGeoFile(geoFile({ present: true, checksumAvailable: true, updateAvailable: false })), /已是最新/);
});

function geoStatus(files: FleetGeoFileStatus[], checkError?: string): FleetGeoUpdateStatus {
  return { files, checkError };
}

test("geoApplyDisabled is true when nothing is actionable or a request is in flight", () => {
  assert.equal(geoApplyDisabled(geoStatus([geoFile({ updateAvailable: true })]), false), false);
  assert.equal(geoApplyDisabled(geoStatus([geoFile({ updateAvailable: true })]), true), true);
  assert.equal(geoApplyDisabled(geoStatus([geoFile({ updateAvailable: false })]), false), true);
  // Missing locally but unverifiable upstream: still nothing safe to do.
  assert.equal(geoApplyDisabled(geoStatus([geoFile({ present: false, checksumAvailable: false })]), false), true);
  // Missing locally and verifiable: actionable.
  assert.equal(geoApplyDisabled(geoStatus([geoFile({ present: false, checksumAvailable: true })]), false), false);
});

test("geoSummaryText reports the check error, empty state, and counts", () => {
  assert.match(geoSummaryText(geoStatus([], "network down")), /network down/);
  assert.match(geoSummaryText(geoStatus([])), /暂无/);
  assert.match(geoSummaryText(geoStatus([geoFile({ updateAvailable: false })])), /均已是最新/);
  assert.match(geoSummaryText(geoStatus([geoFile({ updateAvailable: true }), geoFile({ updateAvailable: false })])), /共 2 个文件，1 个可更新/);
});

test("describeGeoResult reports updated files, errors, or a no-op", () => {
  assert.match(describeGeoResult(["GeoIP.dat", "GeoSite.dat"], undefined), /GeoIP 规则库/);
  assert.match(describeGeoResult(["GeoIP.dat", "GeoSite.dat"], undefined), /GeoSite 规则库/);
  assert.match(describeGeoResult(undefined, ["ASN.mmdb: refusing unverified update"]), /ASN\.mmdb/);
  assert.equal(describeGeoResult(undefined, undefined), "没有文件需要更新。");
});

test("geoFileDescription maps known canonical names and falls back to empty for unknown ones", () => {
  assert.equal(geoFileDescription("GeoIP.dat"), "mihomo 规则引擎 GEOIP 匹配");
  assert.equal(geoFileDescription("GeoSite.dat"), "mihomo 规则引擎 GEOSITE 匹配");
  assert.equal(geoFileDescription("Country.mmdb"), "连接列表国家代码解析");
  assert.equal(geoFileDescription("ASN.mmdb"), "mihomo 规则引擎 IP-ASN 匹配");
  assert.equal(geoFileDescription("Something.new"), "");
});

test("formatBytes renders whole bytes with no decimal and larger units with one", () => {
  assert.equal(formatBytes(0), "0 B");
  assert.equal(formatBytes(-5), "0 B");
  assert.equal(formatBytes(512), "512 B");
  assert.equal(formatBytes(1024), "1.0 KB");
  assert.equal(formatBytes(1536), "1.5 KB");
  assert.equal(formatBytes(1_258_291), "1.2 MB");
  assert.equal(formatBytes(6 * 1024 * 1024), "6.0 MB");
  assert.equal(formatBytes(2 * 1024 * 1024 * 1024), "2.0 GB");
});

test("formatSpeed reuses formatBytes and appends /s", () => {
  assert.equal(formatSpeed(0), "0 B/s");
  assert.equal(formatSpeed(1_048_576), "1.0 MB/s");
  assert.equal(formatSpeed(345 * 1024), "345.0 KB/s");
});

test("geoProgressPercent clamps to 0-100 and treats an unknown total as 0", () => {
  assert.equal(geoProgressPercent(50, 100), 50);
  assert.equal(geoProgressPercent(100, 100), 100);
  assert.equal(geoProgressPercent(150, 100), 100);
  assert.equal(geoProgressPercent(10, 0), 0);
});

test("formatGeoProgress summarizes file, position, size, and speed on one line", () => {
  const line = formatGeoProgress({
    file: "GeoSite.dat",
    downloaded: 4_400_000,
    totalSize: 6_600_000,
    speed: 1_100_000,
    index: 1,
    total: 4,
  });
  assert.match(line, /GeoSite 规则库/);
  assert.match(line, /\(2\/4\)|（2\/4）/);
  assert.match(line, /4\.2 MB \/ 6\.3 MB/);
  assert.match(line, /1\.0 MB\/s/);
});

test("formatGeoProgress omits the '/ total' size half when totalSize is unknown", () => {
  const line = formatGeoProgress({ file: "ASN.mmdb", downloaded: 1024, totalSize: 0, speed: 0, index: 0, total: 4 });
  assert.match(line, /1\.0 KB/);
  assert.doesNotMatch(line, / \/ /);
});
