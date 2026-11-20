import test from "node:test";
import assert from "node:assert/strict";

import {
  changedEditBody,
  editFieldsFromInstance,
  editPutBody,
  resyncEditFields,
} from "./views/detail/edit-fields.ts";
import type { EditFields } from "./views/detail/edit-fields.ts";
import type { FleetInstance } from "./state.ts";

const instance = {
  id: "i1", name: "A", profileId: "p1", mode: "global-chain", mixedPort: 7890, proxyBind: "",
  controllerPort: 9090, localProxies: "- {name: a}", configOverride: "", chain: ["a", "节点选择"],
  chainUdp: false, autoRestart: true,
} as unknown as FleetInstance;

const base: EditFields = editFieldsFromInstance(instance);

test("an untouched form sends nothing; one edit sends one key", () => {
  assert.deepEqual(changedEditBody({ ...base }, base), {});
  assert.deepEqual(changedEditBody({ ...base, name: "B" }, base), { name: "B" });
  assert.deepEqual(changedEditBody({ ...base, chain: ["节点选择", "a"] }, base), { chain: ["节点选择", "a"] });
});

test("switching to rule mode keeps the chain-only fields stored", () => {
  assert.deepEqual(changedEditBody({ ...base, mode: "rule" }, base), { mode: "rule" });
  // Back to global-chain from a rule-mode baseline that still holds them: nothing to resend.
  const ruleBase = { ...base, mode: "rule" };
  assert.deepEqual(changedEditBody({ ...ruleBase, mode: "global-chain" }, ruleBase), { mode: "global-chain" });
});

test("a profile fallback on the form counts as a change against a dangling baseline", () => {
  const dangling = { ...base, profileId: "gone" };
  assert.deepEqual(changedEditBody({ ...dangling, profileId: "p1" }, dangling), { profileId: "p1" });
});

test("resync takes new server values for untouched fields and keeps edits", () => {
  const form = { ...base, name: "Mine" };
  const next = { ...base, configOverride: "dns: {}", name: "Theirs" };
  const { form: merged, conflicts } = resyncEditFields(form, base, next);
  assert.equal(merged.configOverride, "dns: {}", "untouched field follows the server");
  assert.equal(merged.name, "Mine", "edited field is kept");
  assert.deepEqual(conflicts, ["name"]);
  // After resync the save sends only the user's edit -- not the stale override.
  assert.deepEqual(changedEditBody(merged, next), { name: "Mine" });
});

test("no conflict when the server changed to what the user typed, or not at all", () => {
  assert.deepEqual(resyncEditFields({ ...base, name: "B" }, base, { ...base, name: "B" }).conflicts, []);
  assert.deepEqual(resyncEditFields({ ...base, name: "B" }, base, { ...base }).conflicts, []);
});

test("the full body matches the pre-change PUT shape", () => {
  assert.deepEqual(editPutBody(base), {
    name: "A", profileId: "p1", mixedPort: 7890, proxyBind: "127.0.0.1", controllerPort: 9090,
    mode: "global-chain", localProxies: "- {name: a}", configOverride: "", chain: ["a", "节点选择"],
    chainUdp: false, portMappings: [], autoRestart: true,
  });
});

const mapped = {
  ...instance, portMappings: [{ port: 10001, proxy: "us" }, { port: 10002, proxy: "sg" }],
} as unknown as FleetInstance;
const mappedBase: EditFields = editFieldsFromInstance(mapped);

test("port mappings: a fresh poll copy is not an edit, a changed row is", () => {
  // Same content, new objects -- what every poll delivers.
  const polled = editFieldsFromInstance({ ...mapped, portMappings: [{ port: 10001, proxy: "us" }, { port: 10002, proxy: "sg" }] } as FleetInstance);
  assert.deepEqual(changedEditBody(polled, mappedBase), {});
  assert.deepEqual(resyncEditFields(polled, mappedBase, polled).conflicts, []);
  const edited = { ...mappedBase, portMappings: [{ port: "10001", proxy: "jp" }, { port: "10002", proxy: "sg" }] };
  const loaded = [{ port: 10001, proxy: "us" }, { port: 10002, proxy: "sg" }];
  // The list is sent whole, with the list it was based on for the server to check.
  assert.deepEqual(changedEditBody(edited, mappedBase), {
    portMappings: [{ port: 10001, proxy: "jp" }, { port: 10002, proxy: "sg" }],
    expectedPortMappings: loaded,
  });
  assert.deepEqual(changedEditBody({ ...mappedBase, portMappings: [] }, mappedBase), { portMappings: [], expectedPortMappings: loaded });
  assert.deepEqual(changedEditBody({ ...mappedBase, name: "B" }, mappedBase), { name: "B" }, "no expectation without a mapping change");
});

test("port mappings: resync follows the server when untouched and flags a double edit", () => {
  const server = { ...mappedBase, portMappings: [{ port: "10001", proxy: "uk" }] };
  assert.deepEqual(resyncEditFields({ ...mappedBase }, mappedBase, server).form.portMappings, server.portMappings);
  const mine = { ...mappedBase, portMappings: [{ port: "10003", proxy: "nz" }] };
  assert.deepEqual(resyncEditFields(mine, mappedBase, server).conflicts, ["portMappings"]);
});
