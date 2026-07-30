import assert from "node:assert/strict";
import test from "node:test";

import { describeImportItem, exportFilename, summarizeImportResult } from "./views/system/backup.ts";
import type { FleetImportItemResult, FleetImportResult } from "./state.ts";

function item(overrides: Partial<FleetImportItemResult> = {}): FleetImportItemResult {
  return { originalName: "HK", name: "HK", id: "hk", ...overrides };
}

test("exportFilename formats a local timestamp", () => {
  const date = new Date(2026, 6, 30, 9, 5, 3); // 2026-07-30 09:05:03 local
  assert.equal(exportFilename(date), "mihomo-fleet-backup-20260730-090503.json");
});

test("exportFilename zero-pads every component", () => {
  const date = new Date(2026, 0, 1, 0, 0, 0);
  assert.equal(exportFilename(date), "mihomo-fleet-backup-20260101-000000.json");
});

test("describeImportItem reports a plain name when nothing changed", () => {
  assert.equal(describeImportItem("instance", item()), "HK");
});

test("describeImportItem reports a rename", () => {
  const text = describeImportItem("profile", item({ name: "HK (2)", renamed: true }));
  assert.equal(text, "HK (2)（原名“HK”重名，已重命名）");
});

test("describeImportItem reports a reallocated port for an instance", () => {
  const text = describeImportItem("instance", item({ portReallocated: true, mixedPort: 28002, controllerPort: 29002 }));
  assert.equal(text, "HK（端口冲突，已重新分配为 28002/29002）");
});

test("describeImportItem ignores portReallocated for a profile", () => {
  const text = describeImportItem("profile", item({ portReallocated: true, mixedPort: 28002, controllerPort: 29002 }));
  assert.equal(text, "HK");
});

test("describeImportItem combines a rename and a reallocation", () => {
  const text = describeImportItem(
    "instance",
    item({ name: "HK (2)", renamed: true, portReallocated: true, mixedPort: 28002, controllerPort: 29002 }),
  );
  assert.equal(text, "HK (2)（原名“HK”重名，已重命名；端口冲突，已重新分配为 28002/29002）");
});

test("summarizeImportResult leads with a count line and one line per item", () => {
  const result: FleetImportResult = {
    profiles: [item({ originalName: "Main", name: "Main", id: "p1" })],
    instances: [item({ originalName: "HK", name: "HK (2)", id: "i1", renamed: true })],
  };
  assert.deepEqual(summarizeImportResult(result), [
    "已导入 1 个配置档、1 个实例。",
    "配置档：Main",
    "实例：HK (2)（原名“HK”重名，已重命名）",
  ]);
});

test("summarizeImportResult reports the count line even for an empty bundle", () => {
  assert.deepEqual(summarizeImportResult({ profiles: [], instances: [] }), ["已导入 0 个配置档、0 个实例。"]);
});
