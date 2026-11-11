// F4: a post-action instance-list write must not be reverted by a poll that
// was already in flight when the action finished.
import test from "node:test";
import assert from "node:assert/strict";

const g = globalThis as unknown as Record<string, unknown>;
g.document = { createElement: () => ({ style: {} }), hidden: false };
g.localStorage = { getItem: () => null, setItem: () => {}, removeItem: () => {} };

let releasePoll: () => void = () => {};
let instancesCalls = 0;
g.fetch = async (path: string) => {
  const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  if (path === "/api/instances") {
    instancesCalls += 1;
    if (instancesCalls === 1) {
      await new Promise<void>((resolve) => { releasePoll = resolve; });
      return json({ instances: [{ id: "i1", name: "old", status: "stopped" }] });
    }
    return json({ instances: [{ id: "i1", name: "new", status: "running" }] });
  }
  if (path === "/api/profiles") return json({ profiles: [] });
  return json({});
};

const { store } = await import("./store.ts");
const { refresh } = await import("./services/fleet-refresh.ts");
const { refreshInstancesList } = await import("./views/detail/instance-refresh.ts");

test("an older poll resolving after a post-action reload is dropped", async () => {
  const poll = refresh({ periodic: true });
  await new Promise((resolve) => setTimeout(resolve, 0));
  await refreshInstancesList(store);
  assert.equal(store.instances[0]?.name, "new");
  releasePoll();
  await poll;
  assert.equal(store.instances[0]?.name, "new", "stale poll must not revert the post-action list");
});
