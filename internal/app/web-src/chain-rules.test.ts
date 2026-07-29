import assert from "node:assert/strict";
import test from "node:test";

import {
  addChainMember,
  chainProblem,
  defaultChain,
  moveChainMember,
  removeChainMember,
  unusedCandidates,
} from "./chain-rules.ts";
import type { ChainCandidate } from "./chain-rules.ts";

const candidates: ChainCandidate[] = [
  { name: "节点选择", kind: "group" },
  { name: "local-hop", kind: "local" },
  { name: "HK-01", kind: "profile" },
];

test("reordering keeps every member and refuses to fall off an end", () => {
  assert.deepEqual(moveChainMember(["a", "b", "c"], 2, -1), ["a", "c", "b"]);
  assert.deepEqual(moveChainMember(["a", "b", "c"], 0, -1), ["a", "b", "c"]);
  assert.deepEqual(moveChainMember(["a", "b", "c"], 2, 1), ["a", "b", "c"]);
  assert.deepEqual(removeChainMember(["a", "b"], 0), ["b"]);
  assert.deepEqual(removeChainMember(["a", "b"], 9), ["a", "b"]);
});

test("adding refuses blanks and duplicates", () => {
  assert.deepEqual(addChainMember(["a"], " b "), ["a", "b"]);
  assert.deepEqual(addChainMember(["a"], "a"), ["a"]);
  assert.deepEqual(addChainMember(["a"], "   "), ["a"]);
});

test("unusedCandidates hides what the chain already holds", () => {
  assert.deepEqual(
    unusedCandidates(candidates, ["local-hop"]).map((entry) => entry.name),
    ["节点选择", "HK-01"],
  );
});

test("defaultChain matches buildGlobalChainPlan's empty-chain fallback", () => {
  assert.deepEqual(defaultChain(candidates), ["local-hop", "节点选择"]);
  // No profile proxy and no provider: the local node has nothing to chain into.
  assert.deepEqual(defaultChain([candidates[0], candidates[1]] as ChainCandidate[]), ["节点选择"]);
  assert.deepEqual(defaultChain([candidates[0], candidates[1]] as ChainCandidate[], ["prov"]), ["local-hop", "节点选择"]);
});

test("chainProblem reproduces each backend refusal", () => {
  assert.equal(chainProblem(["local-hop", "节点选择"], candidates), "");
  assert.match(chainProblem(["代理链"], candidates), /不能引用 代理链/);
  assert.match(chainProblem(["local-hop", "local-hop"], candidates), /重复引用/);
  assert.match(chainProblem(["ghost"], candidates), /不存在的节点或组/);
  // 节点选择 with every proxy pinned into the chain and no provider to fall back on.
  assert.match(chainProblem(["local-hop", "HK-01", "节点选择"], candidates), /没有可选择的节点/);
  assert.equal(chainProblem(["local-hop", "HK-01", "节点选择"], candidates, ["prov"]), "");
  assert.equal(chainProblem([], candidates), "");
});
