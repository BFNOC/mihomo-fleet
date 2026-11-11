// saveProfile(): payload rules that keep stale or wrong data off the wire.
import test from "node:test";
import assert from "node:assert/strict";

const g = globalThis as unknown as Record<string, unknown>;
g.document = { createElement: () => ({ style: {} }) };
g.localStorage = { getItem: () => null, setItem: () => {}, removeItem: () => {} };
g.window = { confirm: () => true };

const { store } = await import("./store.ts");
const { chrome, registerActions } = await import("./bridge.ts");
import type { SaveProfilePayload } from "./bridge.ts";
const ctx = await import("./views/profiles/profile-context.ts");
const { configEditorErrorText } = await import("./views/profiles/config-editor.ts");
const { saveProfile } = await import("./views/profiles/profile-operations.ts");

const sent: SaveProfilePayload[] = [];
registerActions({
  saveProfile: async (payload: SaveProfilePayload) => {
    sent.push(payload);
    return { id: "p1", name: payload.name || "A", configPath: "", autoUpdate: false, createdAt: "t", updatedAt: "t1" };
  },
  showMessage: () => 1,
});

test("creating a subscription profile without a URL is refused locally", async () => {
  sent.length = 0;
  chrome.profileBusy = false;
  store.profileCreating = true;
  store.profileCreateSource = "subscription";
  ctx.populateProfileForm(null);
  ctx.profileNameInput.value = "Sub";
  ctx.subscriptionUrlInput.value = "  ";
  await saveProfile();
  assert.equal(sent.length, 0);
  assert.equal(configEditorErrorText.value, "请填写订阅链接。");
});

test("a rename of a manual profile does not resend the clean YAML", async () => {
  sent.length = 0;
  store.profileCreating = false;
  store.profiles = [{ id: "p1", name: "A", configPath: "", autoUpdate: false, createdAt: "t", updatedAt: "t0" }];
  store.activeProfileId = "p1";
  store.view = "profiles";
  ctx.populateProfileForm(store.profiles[0]!);
  store.profileConfigOwnerId = "p1";
  store.profileConfigDirty = false;
  ctx.profileNameInput.value = "B";
  await saveProfile();
  assert.equal(sent.length, 1);
  assert.equal(sent[0]!.name, "B");
  assert.equal(sent[0]!.config, undefined, "clean YAML must not be resent over a possibly newer file");
});

test("the profile area's unsaved-changes check includes the create form", () => {
  store.editDirty = false;
  store.profileFormDirty = false;
  store.profileConfigDirty = false;
  store.createDirty = true;
  assert.equal(ctx.hasUnsavedChanges(), true);
  store.createDirty = false;
});
