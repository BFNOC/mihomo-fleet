// profile-sync.ts: when the open profile changes underneath the editor (same
// id, new stamp), a clean form/editor resyncs and a dirty one is warned, never
// overwritten. Our own in-flight operation (chrome.profileBusy) is left alone.
import test from "node:test";
import assert from "node:assert/strict";

const g = globalThis as unknown as Record<string, unknown>;
g.document = { createElement: () => ({ style: {} }) };
g.localStorage = { getItem: () => null, setItem: () => {}, removeItem: () => {} };

const { nextTick } = await import("vue");
const { store } = await import("./store.ts");
const { chrome, registerActions } = await import("./bridge.ts");
const { populateProfileForm, profileNameInput } = await import("./views/profiles/profile-context.ts");
const { watchActiveProfileUpdates } = await import("./views/profiles/profile-sync.ts");
import type { FleetProfile } from "./state.ts";

const fetched: string[] = [];
const messages: string[] = [];
registerActions({
  fetchProfileConfig: async (id: string) => { fetched.push(id); return "mode: rule\n"; },
  showMessage: (text: string) => { messages.push(text); return 1; },
});

const base: FleetProfile = {
  id: "p1", name: "Old", configPath: "", autoUpdate: false, createdAt: "t0", updatedAt: "t0",
};

async function setup(profile: FleetProfile = base): Promise<void> {
  chrome.profileBusy = true; // the reset itself must not count as an update
  store.profileCreating = false;
  store.profiles = [{ ...profile }];
  store.activeProfileId = "p1";
  populateProfileForm(store.profiles[0]!);
  store.profileConfigOwnerId = "p1";
  store.profileFormDirty = false;
  store.profileConfigDirty = false;
  await nextTick();
  chrome.profileBusy = false;
  fetched.length = 0;
  messages.length = 0;
}

async function update(patch: Partial<FleetProfile>): Promise<void> {
  store.profiles = [{ ...store.profiles[0]!, ...patch }];
  await nextTick();
}

watchActiveProfileUpdates();

test("clean form and editor resync on a same-id update", async () => {
  await setup();
  await update({ name: "New", updatedAt: "t1" });
  assert.equal(profileNameInput.value, "New");
  assert.deepEqual(fetched, ["p1"], "clean manual YAML must reload");
  assert.equal(messages.length, 0);
});

test("dirty form and editor are kept and warned about", async () => {
  await setup();
  store.profileFormDirty = true;
  store.profileConfigDirty = true;
  profileNameInput.value = "Mine";
  await update({ name: "New", updatedAt: "t2" });
  assert.equal(profileNameInput.value, "Mine");
  assert.deepEqual(fetched, []);
  assert.equal(messages.length, 2);
});

test("our own operation in flight is not treated as a remote change", async () => {
  await setup();
  chrome.profileBusy = true;
  await update({ name: "New", updatedAt: "t3" });
  assert.equal(profileNameInput.value, "Old");
  assert.deepEqual(fetched, []);
});

test("a failed subscription fetch does not re-download the YAML", async () => {
  await setup({ ...base, subscriptionUrl: "https://x", lastUpdatedAt: "s0" });
  await update({ updatedAt: "t4", lastUpdateError: "timeout" });
  assert.deepEqual(fetched, []);
  await update({ updatedAt: "t5", lastUpdatedAt: "s1" });
  assert.deepEqual(fetched, ["p1"]);
});
