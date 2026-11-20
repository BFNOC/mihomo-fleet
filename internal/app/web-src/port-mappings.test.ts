import test from "node:test";
import assert from "node:assert/strict";

import {
  mappingsFromRows,
  portMappingProblem,
  portMappingSummary,
  portMappingTargetOptions,
  rowsFromMappings,
} from "./port-mappings.ts";
import { localizedMessage } from "./messages.ts";

test("rows round-trip to the PUT shape with trimmed values", () => {
  const rows = rowsFromMappings([{ port: 10001, proxy: "us" }]);
  assert.deepEqual(rows, [{ port: "10001", proxy: "us" }]);
  // The target is sent as typed: mihomo matches names exactly, padding included.
  assert.deepEqual(mappingsFromRows([{ port: " 10002 ", proxy: " sg " }]), [{ port: 10002, proxy: " sg " }]);
  assert.deepEqual(rowsFromMappings(undefined), []);
});

test("portMappingProblem names the first bad row", () => {
  const ok = [{ port: "10001", proxy: "us" }, { port: "10002", proxy: "sg" }];
  assert.equal(portMappingProblem(ok, 28000, 29000), "");
  assert.match(portMappingProblem([{ port: "0", proxy: "us" }], 28000, 29000), /第 1 条.*1–65535/);
  assert.match(portMappingProblem([{ port: "10.5", proxy: "us" }], 28000, 29000), /1–65535/);
  assert.match(portMappingProblem([{ port: "10001", proxy: " " }], 28000, 29000), /未选择目标节点/);
  assert.match(portMappingProblem([...ok, { port: "10001", proxy: "jp" }], 28000, 29000), /端口 10001 在第 1 条和第 3 条/);
  assert.match(portMappingProblem([{ port: "28000", proxy: "us" }], 28000, 29000), /混合端口/);
  assert.match(portMappingProblem([{ port: "29000", proxy: "us" }], 28000, 29000), /控制端口/);
  // Blank instance ports (create form, auto-allocated) never collide.
  assert.equal(portMappingProblem([{ port: "28000", proxy: "us" }], 0, 0), "");
});

test("target options follow the mode's final proxies and groups", () => {
  const candidates = [
    { name: "节点选择", kind: "group" },
    { name: "local-a", kind: "local" },
    { name: "sub-a", kind: "profile" },
  ];
  // Profile proxies come from the raw-name list, not the trimmed chain candidates.
  assert.deepEqual(portMappingTargetOptions("global-chain", candidates, ["G"], [" sub-a "]), ["节点选择", "local-a", " sub-a "]);
  assert.deepEqual(portMappingTargetOptions("rule", candidates, ["G"], ["sub-a"]), ["sub-a", "G"]);
});

test("summary text", () => {
  assert.equal(portMappingSummary(undefined), "无");
  assert.equal(portMappingSummary([{ port: 10001, proxy: "us" }, { port: 10002, proxy: "sg" }]), "10001 → us，10002 → sg");
});

test("backend port mapping errors are localized with their actual values", () => {
  assert.equal(
    localizedMessage("stop the instance before changing port mappings (running: 10001 -> us; requested: 10001 -> sg)"),
    "修改端口映射前请先停止该实例。运行中：10001 -> us；请求：10001 -> sg",
  );
  assert.equal(
    localizedMessage('port mapping 10001 -> "us-globe" references unknown proxy or group; available: us-globel, sg-globel'),
    "端口映射 10001 的目标 us-globe 不存在。可选：us-globel, sg-globel",
  );
  assert.match(
    localizedMessage('port mapping 10001 -> "x" references unknown proxy or group; available: G; proxy-provider nodes cannot be mapped directly, map a proxy group instead'),
    /可选：G。订阅 provider 里的节点不能直接映射/,
  );
  assert.equal(
    localizedMessage('port mapping port 10001 (-> "us") is unavailable: held by instance "B" mixed proxy port'),
    '端口映射端口 10001（→ us）不可用：held by instance "B" mixed proxy port',
  );
  assert.equal(
    localizedMessage('port mapping port 10001 (-> "us") is already in use on 127.0.0.1: listen tcp 127.0.0.1:10001: bind: address already in use'),
    "端口映射端口 10001（→ us）在 127.0.0.1 上已被占用：listen tcp 127.0.0.1:10001: bind: address already in use",
  );
  assert.match(
    localizedMessage('port mappings not listening after 15s: 10001 -> "us" on 127.0.0.1:10001 (connection refused)'),
    /^端口映射在 15s 内没有开始监听，已停止本次启动：10001 -> "us"/,
  );
  assert.equal(localizedMessage("port mapping port 10001 is duplicated (#1 and #2)"), "端口 10001 在第 1 条和第 2 条映射里重复。");
  assert.equal(
    localizedMessage('create instance "x": unable to allocate a port for port mapping 10001 -> "us" (held by y)'),
    "创建实例 x 失败：无法为端口映射 10001（→ us）分配新端口：held by y",
  );
  assert.match(localizedMessage('port mappings cannot listen on proxy bind "0.0.0.0,::1": the wildcard 0.0.0.0 would also open every address of the other family than ::1; use only wildcards or only specific addresses'), /^代理绑定地址 0.0.0.0,::1 无法用于端口映射/);
  assert.match(localizedMessage('instance "A" has port mappings managed by the fleet; change its config through the fleet instead of PUT /configs'), /^实例 A 的端口映射由 Fleet 管理/);
  assert.equal(
    localizedMessage("stop the instance before changing port mapping udp (running udp=true, requested udp=false)"),
    "这次修改会让端口映射的 UDP 从 true 变为 false，请先停止该实例。",
  );
  assert.match(localizedMessage("mihomo could not bind a port mapping: Listener fleet-map-10001 listen err: x"), /^mihomo 没能监听端口映射.*Listener fleet-map-10001/);
  assert.equal(
    localizedMessage('config would break instance "B": port mapping 10001 -> "G" references unknown proxy or group; available: a'),
    "这份配置会让实例 B 的端口映射失效，未保存：端口映射 10001 的目标 G 不存在。可选：a",
  );
  assert.equal(
    localizedMessage("reload cannot apply a port mapping change (running: 10001 -> us; saved: 10001 -> jp); restart the instance instead"),
    "端口映射有变更，热重载无法应用，请重启实例。运行中：10001 -> us；已保存：10001 -> jp",
  );
  assert.match(
    localizedMessage("reload cannot apply a port mapping udp change (running udp=false, saved udp=true); restart the instance instead"),
    /运行中 false，已保存 true/,
  );
  assert.equal(
    localizedMessage('instance "A": port mapping #1: port 0 is out of range (1-65535)'),
    "实例 A：第 1 条端口映射的端口 0 超出范围（1–65535）。",
  );
});

test("the create form sends every mapping row, untouched", async () => {
  const { blankCreateForm, createPayload } = await import("./views/create/create-form.ts");
  const form = { ...blankCreateForm("p1"), portMappings: [{ port: "10001", proxy: "us" }, { port: "10002", proxy: " sg " }] };
  assert.deepEqual(createPayload(form).portMappings, [{ port: 10001, proxy: "us" }, { port: 10002, proxy: " sg " }]);
  assert.deepEqual(createPayload(blankCreateForm("p1")).portMappings, []);
});
