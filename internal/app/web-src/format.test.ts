import assert from "node:assert/strict";
import test from "node:test";

import {
  chainFromText,
  currentLatencyTarget,
  documentTitle,
  formatBatchMessage,
  formatBytes,
  proxyCopyActionGroups,
  proxyEndpointText,
  proxyPort,
  restartEvidenceText,
  selectionSummary,
  shortMihomoVersion,
  splitProxyLabel,
} from "./format.ts";
import { localizedMessage } from "./messages.ts";

test("documentTitle falls back to the bare name before the version lands", () => {
  const idle = { up: 0, down: 0, running: 0, hidden: false };
  assert.equal(documentTitle({ ...idle, appVersion: "" }), "MF");
  assert.equal(documentTitle({ ...idle, appVersion: "1.4.5" }), "MF v1.4.5");
  assert.equal(documentTitle({ ...idle, appVersion: "dev" }), "MF vdev");
});

test("documentTitle shows rates while the tab is visible", () => {
  assert.equal(
    documentTitle({ up: 1_200_000, down: 318_000, running: 3, appVersion: "1.4.5", hidden: false }),
    "↑1.2 MB/s ↓318 kB/s · MF v1.4.5",
  );
  // Kept even at zero: collapsing to the plain name on one idle sample would
  // flip the title's format back and forth.
  assert.equal(
    documentTitle({ up: 0, down: 0, running: 1, appVersion: "1.4.5", hidden: false }),
    "↑0 B/s ↓0 B/s · MF v1.4.5",
  );
});

test("documentTitle swaps rates for the instance count while hidden", () => {
  // polling.ts stops sampling on a backgrounded tab, so the rates below are
  // stale by construction and must not reach the title.
  assert.equal(
    documentTitle({ up: 1_200_000, down: 318_000, running: 3, appVersion: "1.4.5", hidden: true }),
    "3 个实例运行中 · MF v1.4.5",
  );
  assert.equal(
    documentTitle({ up: 0, down: 0, running: 0, appVersion: "1.4.5", hidden: true }),
    "MF v1.4.5",
  );
});

test("proxyPort accepts only valid tcp ports", () => {
  assert.equal(proxyPort(7890), 7890);
  assert.equal(proxyPort("8080"), 8080);
  assert.equal(proxyPort(0), 0);
  assert.equal(proxyPort("nope"), 0);
  assert.equal(proxyPort(70000), 0);
});

test("proxyEndpointText joins multi-bind endpoints", () => {
  assert.equal(
    proxyEndpointText({ mixedPort: 7890, proxyBind: "127.0.0.1,192.168.1.2" }),
    "127.0.0.1:7890，192.168.1.2:7890",
  );
  assert.equal(proxyEndpointText({ mixedPort: 0 }), "端口未分配");
});

test("proxyCopyActionGroups yields one single-endpoint row per bind address", () => {
  const groups = proxyCopyActionGroups({ name: "A", mixedPort: 7890, proxyBind: "127.0.0.1,192.168.1.2" });
  assert.deepEqual(groups.map((group) => group.host), ["127.0.0.1", "192.168.1.2"]);
  const second = groups[1]!.actions;
  assert.equal(second.find((action) => action.id === "addr")?.value, "192.168.1.2:7890");
  assert.equal(second.find((action) => action.id === "http")?.value, "http://192.168.1.2:7890");
  assert.equal(second.find((action) => action.id === "socks")?.value, "socks5://192.168.1.2:7890");
  assert.match(second.find((action) => action.id === "env")?.value ?? "", /^export HTTP_PROXY='http:\/\/192\.168\.1\.2:7890'\n/);
  assert.equal(second[0]!.message, "已复制 A 地址（192.168.1.2）。");
  for (const group of groups) {
    for (const action of group.actions) assert.ok(!action.value.includes("\n") || action.id === "env");
  }

  const single = proxyCopyActionGroups({ name: "A", mixedPort: 7890 });
  assert.equal(single.length, 1);
  assert.equal(single[0]!.host, "");
  assert.equal(single[0]!.actions[0]!.message, "已复制 A 地址。");

  const none = proxyCopyActionGroups({ name: "A", mixedPort: 0 });
  assert.equal(none.length, 1);
  assert.ok(none[0]!.actions.every((action) => action.value === ""));
});

test("splitProxyLabel peels longest matching source prefix", () => {
  assert.deepEqual(
    splitProxyLabel("hk-go - Tokyo 01", ["hk-go", "hk"]),
    { source: "hk-go", name: "Tokyo 01" },
  );
  assert.deepEqual(splitProxyLabel("DIRECT", ["hk-go"]), { source: "", name: "DIRECT" });
});

test("currentLatencyTarget skips built-ins and nested groups", () => {
  const groups = [
    { name: "Proxy", now: "JP-1", all: ["JP-1", "US-1"] },
    { name: "GLOBAL", now: "Proxy", all: ["Proxy"] },
  ];
  assert.equal(currentLatencyTarget(groups[0], groups), "JP-1");
  assert.equal(currentLatencyTarget(groups[1], groups), "");
  assert.equal(currentLatencyTarget({ name: "X", now: "DIRECT" }, groups), "");
});

test("selection and chain helpers stay readable", () => {
  assert.equal(selectionSummary({ selectedProxies: { Proxy: "JP" } }), "Proxy -> JP");
  assert.deepEqual(chainFromText("a\nb\n\nc"), ["a", "b", "c"]);
  assert.equal(formatBytes(1536), "1.5 KB");
});

test("localized batch and error messages stay stable", () => {
  assert.equal(localizedMessage("group and proxy are required"), "必须选择节点组和节点。");
  assert.equal(
    formatBatchMessage("start-all", { total: 2, success: 1, failed: 1, errors: [{ name: "a", error: "method not allowed" }] }),
    "批量启动完成：成功 1/2，失败 1。 a: 请求方法不允许。",
  );
});

test("restartEvidenceText reports nothing for a never-crashed instance", () => {
  assert.equal(restartEvidenceText({}), "");
  assert.equal(restartEvidenceText({ restartCount: 0, lastExitReason: "" }), "");
});

test("restartEvidenceText combines restart count and localized last-exit reason", () => {
  assert.equal(
    restartEvidenceText({ restartCount: 2, lastExitReason: "", lastExitAt: "" }),
    "已自动重启 2 次",
  );
  const text = restartEvidenceText({
    restartCount: 3,
    lastExitReason: "signal: terminated",
    lastExitAt: "2026-07-30T00:00:00Z",
  });
  assert.ok(text.startsWith("已自动重启 3 次 · 最近异常退出：signal: terminated（"), text);
  // A pure exit-reason-only case (no restartCount yet, e.g. an exhausted
  // watchdog that gave up) still reports the reason.
  assert.ok(restartEvidenceText({ restartCount: 0, lastExitReason: "exit status 1" }).includes("exit status 1"));
});

test("mihomo version keeps only the build number from the banner", () => {
  assert.equal(
    shortMihomoVersion("Mihomo Meta v1.19.29 darwin arm64 with go1.26.5 Sat Jul 18 12:19:57 UTC 2026 Use tags: with_gvisor"),
    "1.19.29",
  );
  // The go toolchain version has the same shape and must not win.
  assert.equal(shortMihomoVersion("Clash Meta go1.24.1 v1.18.0 linux amd64"), "1.18.0");
  assert.equal(shortMihomoVersion("v1.19.29"), "1.19.29");
  assert.equal(shortMihomoVersion("Mihomo Meta v1.20.0-alpha.3 darwin"), "1.20.0-alpha.3");
  assert.equal(shortMihomoVersion("Alpha-g1234abc"), "Alpha-g1234abc");
  assert.equal(shortMihomoVersion(""), "");
  assert.equal(shortMihomoVersion(null), "");
});
