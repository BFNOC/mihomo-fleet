import { store } from "../../store.ts";
import { actions, chrome } from "../../bridge.ts";
import type { SaveProfilePayload } from "../../bridge.ts";
import { profileReferenceCount } from "../../state.ts";
import type { FleetProfile, ProfileCreateSource } from "../../state.ts";
import { canClearSavedProfileConfig } from "../../app-logic.ts";
import {
  activeProfile,
  advanceProfileContext,
  captureOperationContext,
  clearProfileFormDirty,
  operationContextMatches,
  populateProfileForm,
  profileNameInput,
  subscriptionAutoUpdateInput,
  subscriptionIntervalInput,
  subscriptionUrlInput,
} from "./profile-context.ts";
import {
  deleting,
  editorRef,
  loadProfileConfig,
  refreshingSub,
  resetConfigEditor,
  saving,
  setConfigEditorError,
} from "./config-editor.ts";
import { selectProfile } from "./profile-navigation.ts";

// Optional field controller.go's "refresh" and URL-changing "save" responses
// carry (store.go's SelectionReconciliation), present only when the fresh
// subscription content dropped a node an instance had selected and the
// backend silently re-picked a replacement -- previously a user could only
// discover that days later by noticing a port's exit country had changed.
// bridge.ts's saveProfile()/refreshSubscriptionProfile() are typed as
// returning plain FleetProfile (bridge.ts is owned elsewhere, not widened
// here), so this is read off the same response object via a cast at each
// call site below rather than a bridge.ts change. Treated as absent-by-default
// throughout: an older/unpatched backend simply omits the field, and
// `describeSelectionChanges` below already no-ops on undefined/empty.
interface SelectionReconciliation {
  instanceId: string;
  instanceName: string;
  group: string;
  vanishedProxy: string;
  replacementGroup?: string;
  replacementProxy?: string;
}

type ProfileWithSelectionChanges = FleetProfile & { selectionChanges?: SelectionReconciliation[] };

function describeSelectionChanges(changes: SelectionReconciliation[] | undefined): string {
  if (!changes?.length) return "";
  const lines = changes.map((change) => {
    const target = change.replacementProxy
      ? `已自动切换到「${change.replacementGroup} / ${change.replacementProxy}」`
      : "该实例已没有可用的已选节点，需要手动重新选择";
    return `实例「${change.instanceName}」的「${change.group}」原节点「${change.vanishedProxy}」已从订阅中消失，${target}`;
  });
  return ` ${lines.join("；")}。`;
}

// The three profile mutations. services/profiles.ts owns the network call and
// the mutual-exclusion gate; this module owns the pre/post-flight orchestration
// that needs the editor handle and the dirty/version bookkeeping.
//
// These three stay in one file deliberately. Each is a single interlocking
// sequence -- capture context, await, re-check context, then mutate the store in
// a specific order -- and the guard, the ordering and the bookkeeping cannot be
// read or reviewed apart from each other. The last time the delete path's
// ordering was split across a module boundary, a refresh landed on the wrong
// side of the await and silently broke the guard on every successful delete.

export async function saveProfile(): Promise<void> {
  const profile = activeProfile.value;
  if (!profile && !store.profileCreating) return;
  if (chrome.profileBusy) return;
  const creating = store.profileCreating;
  // Guarded by the check above: `profile || store.profileCreating`, so every
  // `profile!` below only runs on the `!creating` branch, where that guard
  // proves `profile` non-null.
  const source: ProfileCreateSource = creating ? store.profileCreateSource : (profile!.subscriptionUrl ? "subscription" : "manual");
  const savedProfileId = creating ? "__new__" : profile!.id;
  const savedConfigVersion = editorRef.value?.getVersion() ?? 0;
  const savedFormVersion = store.profileFormVersion;
  const configMayChange = source === "manual"
    ? store.profileConfigDirty
    : !creating && subscriptionUrlInput.value.trim() !== profile!.subscriptionUrl;

  if (store.profileFormOwnerId !== savedProfileId || (source === "manual" && store.profileConfigOwnerId !== savedProfileId)) {
    setConfigEditorError("配置档已变化，请重新选择后再保存。");
    return;
  }
  setConfigEditorError("");
  const operationContext = captureOperationContext(savedProfileId);
  const payload: SaveProfilePayload = {
    creating,
    profileId: creating ? "" : profile!.id,
    name: profileNameInput.value.trim(),
    source,
  };
  if (source === "subscription") {
    payload.subscriptionUrl = subscriptionUrlInput.value.trim();
    payload.autoUpdate = subscriptionAutoUpdateInput.value;
    payload.updateIntervalMinutes = Number(subscriptionIntervalInput.value) || 0;
  } else {
    payload.config = editorRef.value?.getValue() ?? "";
  }
  saving.value = true;
  try {
    const saved = await actions.saveProfile(payload) as ProfileWithSelectionChanges;
    if (!operationContextMatches(operationContext)) return;
    if (creating) advanceProfileContext();
    store.profileCreating = false;
    store.activeProfileId = saved.id;
    store.profileFormOwnerId = saved.id;
    store.profileConfigOwnerId = saved.id;
    const sameFormVersion = store.profileFormVersion === savedFormVersion;
    const sameConfigVersion = creating
      ? savedConfigVersion === (editorRef.value?.getVersion() ?? 0)
      : canClearSavedProfileConfig({
        savedProfileId,
        savedVersion: savedConfigVersion,
        activeProfileId: store.activeProfileId,
        currentVersion: editorRef.value?.getVersion() ?? 0,
      });
    if (sameFormVersion && sameConfigVersion) {
      clearProfileFormDirty();
      setConfigEditorError("");
      populateProfileForm(saved);
    }
    actions.showMessage(creating
      ? "配置档已创建。"
      : configMayChange
        ? `配置档已保存，引用它的运行中实例需要重启后生效。${describeSelectionChanges(saved.selectionChanges)}`
        : "配置档已保存。");
    if (source === "subscription" && sameFormVersion && store.view === "profiles" && !store.profileCreating && store.activeProfileId === saved.id) {
      await loadProfileConfig(saved.id);
    }
  } catch (err) {
    if (operationContextMatches(operationContext)) {
      const message = err instanceof Error ? err.message : String(err);
      setConfigEditorError(message);
      actions.showMessage(message, "error");
    }
  } finally {
    saving.value = false;
  }
}

export async function deleteProfile(): Promise<void> {
  const profile = activeProfile.value;
  if (!profile) return;
  const refCount = profileReferenceCount(store, profile.id);
  if (refCount > 0) {
    actions.showMessage(`该配置档仍被 ${refCount} 个实例引用，无法删除。`, "error");
    return;
  }
  if (!window.confirm(`确定删除配置档 ${profile.name}？此操作不可撤销。`)) return;
  if (chrome.profileBusy) return;
  const operationContext = captureOperationContext(profile.id);
  deleting.value = true;
  try {
    // actions.deleteProfile() is the network call and nothing else, so the guard
    // below still sees the pre-delete store -- which is the only state it can
    // meaningfully compare against. Every mutation the delete implies happens
    // after it, in this order: drop the row, move the selection, clear the
    // editor, report, and only then re-poll. Re-polling first would move
    // activeProfileId itself and defeat the guard.
    await actions.deleteProfile(profile.id);
    if (!operationContextMatches(operationContext)) return;
    advanceProfileContext();
    store.profiles = store.profiles.filter((item) => item.id !== profile.id);
    store.activeProfileId = store.profiles[0]?.id || "";
    store.profileFormDirty = false;
    resetConfigEditor();
    actions.showMessage("配置档已删除。");
    await actions.refreshFleet({ forceInstances: true });
    if (store.view === "profiles" && store.activeProfileId) {
      selectProfile(store.activeProfileId, { force: true, allowBusy: true });
    }
  } catch (err) {
    if (operationContextMatches(operationContext)) {
      const message = err instanceof Error ? err.message : String(err);
      actions.showMessage(message, "error");
      await actions.refreshFleet({ forceInstances: true });
    }
  } finally {
    deleting.value = false;
  }
}

export async function refreshSubscription(): Promise<void> {
  const profile = activeProfile.value;
  if (!profile || store.profileFormDirty) {
    actions.showMessage("请先保存订阅设置，再立即更新。", "error");
    return;
  }
  if (chrome.profileBusy) return;
  const operationContext = captureOperationContext(profile.id);
  refreshingSub.value = true;
  try {
    const refreshed = await actions.refreshSubscriptionProfile(profile.id) as ProfileWithSelectionChanges;
    if (!operationContextMatches(operationContext)) return;
    actions.showMessage(`订阅已更新。运行中的实例需要重启后使用新的缓存配置。${describeSelectionChanges(refreshed.selectionChanges)}`);
    populateProfileForm(refreshed);
    await loadProfileConfig(refreshed.id);
  } catch (err) {
    if (operationContextMatches(operationContext)) {
      const message = err instanceof Error ? err.message : String(err);
      actions.showMessage(message, "error");
    }
  } finally {
    refreshingSub.value = false;
  }
}
