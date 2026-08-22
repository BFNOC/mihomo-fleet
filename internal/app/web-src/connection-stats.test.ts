import test from "node:test";
import assert from "node:assert/strict";

import { aggregateConnections, totalConnectionStats } from "./connection-stats.ts";
import type { FleetConnectionRow } from "./dashboard.ts";

function row(overrides: Partial<FleetConnectionRow>): FleetConnectionRow {
  return {
    id: "c1",
    instanceId: "i1",
    instanceName: "HK",
    host: "",
    ip: "203.0.113.9",
    port: "443",
    sourceIP: "127.0.0.1",
    process: "",
    network: "tcp",
    kind: "HTTPS",
    chains: [],
    node: "",
    rule: "",
    rulePayload: "",
    upload: 0,
    download: 0,
    up: 0,
    down: 0,
    start: 0,
    ...overrides,
  } as FleetConnectionRow;
}

test("rules are grouped by name AND payload", () => {
  const groups = aggregateConnections(
    [
      row({ rule: "RuleSet", rulePayload: "cn", download: 10 }),
      row({ rule: "RuleSet", rulePayload: "cn", download: 5 }),
      row({ rule: "RuleSet", rulePayload: "proxy", download: 1 }),
    ],
    "rule",
  );
  // Collapsing on the rule name alone would answer "RuleSet: 3" and hide which
  // set matched -- exactly what this view exists to show.
  assert.equal(groups.length, 2);
  assert.deepEqual(
    groups.map((group) => [group.label, group.detail, group.connections, group.download]),
    [
      ["RuleSet", "cn", 2, 15],
      ["RuleSet", "proxy", 1, 1],
    ],
  );
});

test("nodes group by the exit that carried the connection", () => {
  const groups = aggregateConnections(
    [
      row({ node: "HK-01", up: 3 }),
      row({ node: "HK-01", up: 1 }),
      row({ node: "JP-02", up: 2 }),
    ],
    "node",
  );
  assert.deepEqual(
    groups.map((group) => [group.label, group.connections, group.up]),
    [
      ["HK-01", 2, 4],
      ["JP-02", 1, 2],
    ],
  );
});

test("groups sort by live rate first, then cumulative bytes", () => {
  const groups = aggregateConnections(
    [
      row({ rule: "idle-but-fat", download: 9_000_000 }),
      row({ rule: "busy-now", down: 500 }),
      row({ rule: "idle-and-thin", download: 10 }),
    ],
    "rule",
  );
  assert.deepEqual(
    groups.map((group) => group.label),
    ["busy-now", "idle-but-fat", "idle-and-thin"],
  );
});

test("identical groups keep a stable order instead of flipping per tick", () => {
  const first = aggregateConnections([row({ rule: "B" }), row({ rule: "A" })], "rule");
  const second = aggregateConnections([row({ rule: "A" }), row({ rule: "B" })], "rule");
  assert.deepEqual(
    first.map((group) => group.label),
    second.map((group) => group.label),
  );
});

test("a missing rule or node falls back to a placeholder rather than an empty label", () => {
  const rules = aggregateConnections([row({ rule: "", rulePayload: "" })], "rule");
  assert.equal(rules[0]?.label, "—");
  const nodes = aggregateConnections([row({ node: "" })], "node");
  assert.equal(nodes[0]?.label, "—");
});

test("negative counters from a reset connection cannot pull a total below zero", () => {
  const groups = aggregateConnections([row({ rule: "A", download: -50, down: -5 })], "rule");
  assert.equal(groups[0]?.download, 0);
  assert.equal(groups[0]?.down, 0);
});

test("totals sum every group, not just the shown ones", () => {
  const groups = aggregateConnections(
    [row({ rule: "A", up: 1, down: 2 }), row({ rule: "B", up: 3, down: 4 })],
    "rule",
  );
  assert.deepEqual(totalConnectionStats(groups), { groups: 2, connections: 2, up: 4, down: 6 });
});

test("an empty snapshot aggregates to nothing rather than throwing", () => {
  assert.deepEqual(aggregateConnections([], "rule"), []);
  assert.deepEqual(totalConnectionStats([]), { groups: 0, connections: 0, up: 0, down: 0 });
});
