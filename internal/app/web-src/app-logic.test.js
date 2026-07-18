import assert from "node:assert/strict";
import test from "node:test";

import {
  canClearSavedConfig,
  createActionGate,
  profileOptionLabel,
  shouldApplyConfigLoad,
  shouldApplyCreateProfileConfig,
} from "./app-logic.js";

test("action gate rejects duplicate submissions until released", () => {
  const gate = createActionGate();
  assert.equal(gate.begin(), true);
  assert.equal(gate.begin(), false);
  assert.equal(gate.isRunning(), true);
  gate.end();
  assert.equal(gate.begin(), true);
});

test("create profile config accepts only the latest selected profile response", () => {
  assert.equal(shouldApplyCreateProfileConfig({ requestSeq: 3, currentSeq: 3, requestedProfileId: "a", currentProfileId: "a" }), true);
  assert.equal(shouldApplyCreateProfileConfig({ requestSeq: 2, currentSeq: 3, requestedProfileId: "a", currentProfileId: "a" }), false);
  assert.equal(shouldApplyCreateProfileConfig({ requestSeq: 3, currentSeq: 3, requestedProfileId: "a", currentProfileId: "b" }), false);
});

test("config load cannot overwrite dirty or mismatched editor context", () => {
  const base = { requestedInstanceId: "one", requestedProfileId: "profile-a", activeInstanceId: "one", activeProfileId: "profile-a" };
  assert.equal(shouldApplyConfigLoad({ ...base, dirty: false }), true);
  assert.equal(shouldApplyConfigLoad({ ...base, dirty: true }), false);
  assert.equal(shouldApplyConfigLoad({ ...base, activeInstanceId: "two", dirty: false }), false);
  assert.equal(shouldApplyConfigLoad({ ...base, activeProfileId: "profile-b", dirty: false }), false);
});

test("save response clears dirty only for the exact editor version and context", () => {
  const base = {
    savedInstanceId: "one",
    savedProfileId: "profile-a",
    savedVersion: 4,
    activeInstanceId: "one",
    activeProfileId: "profile-a",
    currentVersion: 4,
  };
  assert.equal(canClearSavedConfig(base), true);
  assert.equal(canClearSavedConfig({ ...base, currentVersion: 5 }), false);
  assert.equal(canClearSavedConfig({ ...base, activeProfileId: "profile-b" }), false);
});

test("profile option label exposes stable identity and reference count", () => {
  assert.equal(profileOptionLabel({ id: "local-3", name: "local配置档" }, 1), "local配置档 · local-3 · 1 实例");
  assert.equal(profileOptionLabel({ id: "local-2", name: "local配置档" }, 0), "local配置档 · local-2 · 未使用");
});
