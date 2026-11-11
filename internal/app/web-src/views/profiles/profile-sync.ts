import { watch } from "vue";
import { store } from "../../store.ts";
import { actions, chrome } from "../../bridge.ts";
import type { FleetProfile } from "../../state.ts";
import { activeProfile, populateProfileForm } from "./profile-context.ts";
import { loadProfileConfig } from "./config-editor.ts";

// Keeps the open profile's form and YAML editor in step with the store when the
// same profile changes underneath them: a scheduled subscription refresh, or a
// save from another browser tab, both land via the fleet poll with the same id.
// Before this, both stayed on whatever was loaded at selection time, and the
// stale copy could then be saved back over the newer one.
//
// Selection changes are not handled here -- profile-navigation.ts reloads on
// those. Our own save/refresh is skipped via chrome.profileBusy: those paths
// already repopulate the form and reload the YAML in their own fixed order.

// The YAML of a subscription profile changes only when a fetch is applied,
// which always stamps lastUpdatedAt; updatedAt also moves on every failed
// fetch (SetProfileUpdateError) and would re-download a possibly multi-MB file
// for nothing. A manual profile's YAML can only change through an edit, which
// stamps updatedAt.
function configStamp(profile: FleetProfile): string {
  return profile.subscriptionUrl ? profile.lastUpdatedAt ?? "" : profile.updatedAt;
}

interface Snapshot {
  id: string;
  form: string;
  config: string;
}

function snapshot(): Snapshot | null {
  const profile = activeProfile.value;
  return profile ? { id: profile.id, form: profile.updatedAt, config: configStamp(profile) } : null;
}

function syncForm(profile: FleetProfile): void {
  if (store.profileFormOwnerId !== profile.id) return;
  if (!store.profileFormDirty) {
    populateProfileForm(profile);
    return;
  }
  actions.showMessage(`配置档「${profile.name}」的设置已在别处更新。你有未保存的修改，保存时只会提交你改过的项。`, "error");
}

function syncConfig(profile: FleetProfile): void {
  if (store.profileConfigOwnerId !== profile.id) return;
  if (!store.profileConfigDirty) {
    void loadProfileConfig(profile.id);
    return;
  }
  actions.showMessage(`配置档「${profile.name}」的 YAML 已在别处更新。你有未保存的修改，保存会被拒绝，以免覆盖对方的版本。`, "error");
}

/** Called once from ProfileManagerView.vue's setup; stops with that component. */
export function watchActiveProfileUpdates(): void {
  watch(snapshot, (next, prev) => {
    if (!next || !prev || next.id !== prev.id) return;
    if (store.profileCreating || chrome.profileBusy) return;
    const profile = activeProfile.value;
    if (!profile) return;
    if (next.form !== prev.form) syncForm(profile);
    if (next.config !== prev.config) syncConfig(profile);
  });
}
