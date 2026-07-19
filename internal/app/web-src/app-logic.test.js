import assert from "node:assert/strict";
import test from "node:test";

import {
  canClearSavedProfileConfig,
  createActionGate,
  profileOptionLabel,
  shouldApplyProfileOperation,
  shouldApplyProfileConfigLoad,
} from "./app-logic.js";

test("action gate rejects duplicate submissions until released", () => {
  const gate = createActionGate();
  assert.equal(gate.begin(), true);
  assert.equal(gate.begin(), false);
  assert.equal(gate.isRunning(), true);
  gate.end();
  assert.equal(gate.begin(), true);
});

test("profile config load cannot overwrite dirty or stale editor context", () => {
  const base = { requestSeq: 3, currentSeq: 3, requestedProfileId: "profile-a", activeProfileId: "profile-a" };
  assert.equal(shouldApplyProfileConfigLoad({ ...base, dirty: false }), true);
  assert.equal(shouldApplyProfileConfigLoad({ ...base, dirty: true }), false);
  assert.equal(shouldApplyProfileConfigLoad({ ...base, currentSeq: 4, dirty: false }), false);
  assert.equal(shouldApplyProfileConfigLoad({ ...base, activeProfileId: "profile-b", dirty: false }), false);
});

test("profile save clears dirty only for the exact editor version and context", () => {
  const base = {
    savedProfileId: "profile-a",
    savedVersion: 4,
    activeProfileId: "profile-a",
    currentVersion: 4,
  };
  assert.equal(canClearSavedProfileConfig(base), true);
  assert.equal(canClearSavedProfileConfig({ ...base, currentVersion: 5 }), false);
  assert.equal(canClearSavedProfileConfig({ ...base, activeProfileId: "profile-b" }), false);
});

test("profile async response only applies to the context that started it", () => {
  const base = {
    requestContextSeq: 8,
    currentContextSeq: 8,
    requestedProfileId: "profile-a",
    activeProfileId: "profile-a",
    view: "profiles",
  };
  assert.equal(shouldApplyProfileOperation(base), true);
  assert.equal(shouldApplyProfileOperation({ ...base, currentContextSeq: 9 }), false);
  assert.equal(shouldApplyProfileOperation({ ...base, activeProfileId: "profile-b" }), false);
  assert.equal(shouldApplyProfileOperation({ ...base, view: "instances" }), false);
  assert.equal(shouldApplyProfileOperation({ ...base, requestedProfileId: "" }), false);
  assert.equal(shouldApplyProfileOperation({
    ...base,
    requestedProfileId: "__new__",
    activeProfileId: "__new__",
  }), true);
});

test("profile option label stays compact while exposing reference count", () => {
  assert.equal(profileOptionLabel({ id: "local-3", name: "local配置档" }, 1), "local配置档 · 1 个实例");
  assert.equal(profileOptionLabel({ id: "local-2", name: "local配置档" }, 0), "local配置档 · 未使用");
});
