// use-chain-candidates.ts pulls in vue and state.ts, so document/localStorage
// stubs must exist before the dynamic import (CLAUDE.md, "Verifying frontend
// changes"). fetch is stubbed to count POST /api/instances/chain-candidates.
import test from "node:test";
import assert from "node:assert/strict";

const g = globalThis as unknown as Record<string, unknown>;
g.document = { createElement: () => ({ style: {} }) };
g.localStorage = { getItem: () => null, setItem: () => {}, removeItem: () => {} };

let calls = 0;
g.fetch = async () => {
  calls += 1;
  return new Response(JSON.stringify({ candidates: [{ name: `n${calls}`, kind: "proxy" }] }), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
};

const { nextTick } = await import("vue");
const { store } = await import("./store.ts");
const { useChainCandidates } = await import("./views/shared/use-chain-candidates.ts");

const settle = async (): Promise<void> => {
  await nextTick();
  await new Promise((resolve) => setTimeout(resolve, 20));
};

test("candidates refetch when the selected profile is updated in place", async () => {
  store.profiles = [{ id: "p1", name: "p1", configPath: "", autoUpdate: false, createdAt: "t0", updatedAt: "t0" }];
  const { state } = useChainCandidates(() => "p1", () => "", () => true);
  await settle();
  assert.equal(calls, 1);
  assert.deepEqual(state.candidates.map((item) => item.name), ["n1"]);

  // Same id, new updatedAt: what a subscription refresh puts in the store.
  store.profiles = [{ ...store.profiles[0]!, updatedAt: "t1" }];
  await settle();
  assert.equal(calls, 2, "a profile update must trigger a new candidates request");
  assert.deepEqual(state.candidates.map((item) => item.name), ["n2"]);

  // A poll that reloads identical profile data must not refetch.
  store.profiles = [{ ...store.profiles[0]! }];
  await settle();
  assert.equal(calls, 2);
});
