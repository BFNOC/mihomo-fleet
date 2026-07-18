import assert from "node:assert/strict";
import test from "node:test";

import {
  chainFromText,
  currentLatencyTarget,
  formatBatchMessage,
  formatBytes,
  proxyEndpointText,
  proxyPort,
  selectionSummary,
  splitProxyLabel,
} from "./format.js";
import { localizedMessage } from "./i18n.js";

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
