// C12: after leaving and returning to an instance, an older latency request
// still in flight must not overwrite the newer request's result.
import test from "node:test";
import assert from "node:assert/strict";

const g = globalThis as unknown as Record<string, unknown>;
g.document = { createElement: () => ({ style: {} }) };
g.localStorage = { getItem: () => null, setItem: () => {}, removeItem: () => {} };

const pending: Array<(delay: number) => void> = [];
g.fetch = () => new Promise<Response>((resolve) => {
  pending.push((delay) => resolve(new Response(JSON.stringify({ delays: { n: delay } }), {
    status: 200, headers: { "Content-Type": "application/json" },
  })));
});

const { createState, latencyResult } = await import("./state.ts");
const { createLatencyController } = await import("./latency.ts");
import type { FleetInstance, FleetProxyGroup } from "./state.ts";

test("the newer group latency result survives an older one landing last", async () => {
  const state = createState();
  const instance = { id: "i1", name: "i1", status: "running" } as FleetInstance;
  state.instances = [instance];
  state.activeId = "i1";
  const controller = createLatencyController({
    state,
    el: { latencyUrl: { value: "" }, latencyTimeout: { value: "" } } as never,
    getActive: () => instance,
    showMessage: () => {},
  });
  const group = { name: "G", all: ["n"] } as FleetProxyGroup;

  const first = controller.testGroupLatency(group, "url");
  state.latencyRunning.clear(); // navigation away and back clears the flags
  const second = controller.testGroupLatency(group, "url");
  assert.equal(pending.length, 2);
  pending[1]!(9);
  await second;
  pending[0]!(99);
  await first;
  assert.equal(latencyResult(state, "i1", "G", "n", "url")?.delay, 9);
});
