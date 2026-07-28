import { nextTick } from "vue";
import { store } from "../../store.ts";
import { chrome, registerActions } from "../../bridge.ts";
import { activeInstance, profileById } from "../../state.ts";
import type { ProfileCreateSource } from "../../state.ts";
import {
  advanceProfileContext,
  clearProfileFormDirty,
  confirmDiscardChanges,
  markProfileFormDirty,
  populateProfileForm,
  profileNameInputRef,
} from "./profile-context.ts";
import { editorRef, loadDefaultConfig, loadProfileConfig, resetConfigEditor } from "./config-editor.ts";

// Moving between profiles. All four functions need both confirmDiscardChanges()
// and direct editor calls, which is why they could not be split the way
// save/delete/refresh were -- those are network calls whose gates live in
// services/profiles.ts, these are not.
//
// Each returns false when the user declined a discard prompt.

export function startNewProfile(): boolean {
  if (chrome.profileBusy) return false;
  if (!confirmDiscardChanges("新建配置档")) return false;
  advanceProfileContext();
  store.editDirty = false;
  store.view = "profiles";
  store.profileCreating = true;
  store.activeProfileId = "";
  store.profileCreateSource = "manual";
  store.profileFormVersion = 0;
  resetConfigEditor();
  populateProfileForm(null);
  loadDefaultConfig();
  clearProfileFormDirty();
  void nextTick(() => profileNameInputRef.value?.focus());
  return true;
}

export interface SelectProfileOptions {
  force?: boolean;
  allowBusy?: boolean;
}

export function selectProfile(profileId: string, options: SelectProfileOptions = {}): boolean {
  if (chrome.profileBusy && !options.allowBusy) return false;
  if (!options.force && !store.profileCreating && store.activeProfileId === profileId) {
    store.view = "profiles";
    if (store.profileConfigOwnerId !== profileId) void loadProfileConfig(profileId);
    return true;
  }
  if (!options.force && store.activeProfileId !== profileId && !confirmDiscardChanges("切换配置档")) return false;
  const profile = profileById(store, profileId);
  if (!profile) return false;
  advanceProfileContext();
  store.view = "profiles";
  store.profileCreating = false;
  store.activeProfileId = profile.id;
  store.profileFormVersion = 0;
  populateProfileForm(profile);
  clearProfileFormDirty();
  void loadProfileConfig(profile.id);
  return true;
}

export function openProfileManager(profileId = ""): boolean {
  if (chrome.profileBusy) return false;
  if (store.view !== "profiles" && !confirmDiscardChanges("打开配置档管理")) return false;
  store.editDirty = false;
  store.view = "profiles";
  store.creating = false;
  const targetId = profileId || store.activeProfileId || activeInstance(store)?.profileId || store.profiles[0]?.id || "";
  if (targetId) return selectProfile(targetId, { force: true });
  return startNewProfile();
}

export function closeProfileManager(): boolean {
  if (chrome.profileBusy) return false;
  if (!confirmDiscardChanges("返回实例")) return false;
  advanceProfileContext();
  store.view = "instances";
  store.profileCreating = false;
  store.profileFormDirty = false;
  resetConfigEditor();
  return true;
}

export function setProfileCreateSource(source: ProfileCreateSource): void {
  if (!store.profileCreating || store.profileCreateSource === source) return;
  store.profileCreateSource = source;
  markProfileFormDirty();
  editorRef.value?.setReadOnly(source === "subscription");
}

// Claims these two keys per bridge.ts's OWNERSHIP RULE -- app.ts must not list
// them. Registering at module-import time (this module is reached through
// ProfileManagerView.vue, which main.ts imports statically) keeps that ordering
// deterministic: it always happens before app.ts's dynamic import runs.
registerActions({ openProfileManager, closeProfileManager });
