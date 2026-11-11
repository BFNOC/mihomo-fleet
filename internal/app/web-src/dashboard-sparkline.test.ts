// The per-instance sparkline froze because sampleFor() handed DashboardSparkline
// the sampler's own series object, mutated in place, so the prop identity never
// changed and its computeds never re-ran. This drives the real heartbeat
// (useDashboardHeartbeat needs onMounted, hence the no-op custom renderer) and
// checks that a downstream computed over sampleFor(id).series sees each sample.
import test from "node:test";
import assert from "node:assert/strict";

const g = globalThis as unknown as Record<string, unknown>;
g.document = { createElement: () => ({ style: {} }), visibilityState: "visible" };
g.localStorage = { getItem: () => null, setItem: () => {}, removeItem: () => {} };

const { computed, createRenderer, defineComponent, nextTick } = await import("vue");
const { store } = await import("./store.ts");
const { chrome } = await import("./bridge.ts");
const { sampleInstance } = await import("./dashboard.ts");
const { sampleFor, useDashboardHeartbeat } = await import("./views/dashboard/dashboard-data.ts");

type Node = { children: Node[]; parent: Node | null };
const node = (): Node => ({ children: [], parent: null });
const { createApp } = createRenderer<Node, Node>({
  createElement: node,
  createText: node,
  createComment: node,
  setText: () => {},
  setElementText: () => {},
  patchProp: () => {},
  insert: (child, parent) => { child.parent = parent; parent.children.push(child); },
  remove: (child) => {
    const parent = child.parent;
    if (parent) parent.children.splice(parent.children.indexOf(child), 1);
  },
  parentNode: (child) => child.parent,
  nextSibling: () => null,
});

test("per-instance series seen by a computed advances on every heartbeat", async () => {
  store.view = "dashboard";
  store.instances = [{ id: "i1", name: "i1", status: "running" } as (typeof store.instances)[number]];
  createApp(defineComponent({ setup() { useDashboardHeartbeat(); return () => null; } })).mount(node());

  // Same shape as parent -> DashboardSparkline: the parent passes `series` as a
  // prop (an identity Vue compares), the child computes only from that prop.
  const prop = computed(() => sampleFor("i1").series);
  const length = computed(() => prop.value.samples.length);
  assert.equal(length.value, 0);

  let total = 0;
  for (let tick = 1; tick <= 3; tick += 1) {
    total += 1000;
    await sampleInstance("i1", async () => ({ uploadTotal: total, downloadTotal: total, connections: [] }), tick * 1000);
    chrome.trafficTick += 1;
    await nextTick();
    assert.equal(length.value, tick, `sample ${tick} must reach the chart`);
  }
});

test("an older connections sample finishing last does not overwrite a newer one", async () => {
  const { instanceConnections, instanceSeries } = await import("./dashboard.ts");
  let releaseOld: (value: unknown) => void = () => {};
  const old = sampleInstance("i2", () => new Promise((resolve) => { releaseOld = resolve; }) as never, 1000);
  await sampleInstance("i2", async () => ({ uploadTotal: 10, downloadTotal: 10, connections: [] }), 2000);
  releaseOld({ uploadTotal: 5, downloadTotal: 5, connections: [{ id: "c1", metadata: {} }] });
  await old;
  assert.equal(instanceConnections("i2"), 0, "the closed connection must not come back");
  const ats = instanceSeries("i2")!.samples.map((sample) => sample.at);
  assert.deepEqual(ats, [...ats].sort((a, b) => a - b), "timestamps must stay ordered");
});
