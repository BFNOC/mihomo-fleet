import { actions, showMessage } from "../bridge.ts";
import { store } from "../store.ts";
import { clearLatencyStateForInstance } from "../state.ts";
import { profileOperationRunning } from "./profile-gates.ts";
import { sampleFleetTraffic } from "./polling.ts";

// Moving between views, plus the bookkeeping each move implies. Every function
// here returns a boolean: false means the user declined a discard prompt and
// the caller's view change must not happen.

/**
 * store.editDirty is the instance edit form (OverviewTab.vue); the two profile
 * flags belong to ProfileManagerView.vue. None of the three is set from this
 * module, but beforeunload and every guard below need the combined signal.
 */
export function hasUnsavedChanges(): boolean {
  return store.editDirty || store.profileFormDirty || store.profileConfigDirty;
}

export function confirmDiscardChanges(action: string): boolean {
  if (!hasUnsavedChanges()) return true;
  return window.confirm(`有未保存的修改。确定放弃并${action}吗？`);
}

/** Drops everything scoped to the instance being navigated away from. */
export function clearActiveDetailCache(): void {
  store.editInstanceId = "";
  store.editDirty = false;
  store.editVersion = 0;
  store.proxyGroups = [];
  store.proxyApply = false;
  store.latencyBatchRunning = false;
  store.latencyBatchToken += 1;
}

export function selectInstance(id: string): boolean {
  if (store.activeId !== id || store.view === "profiles") {
    if (!confirmDiscardChanges("切换实例")) {
      return false;
    }
    if (store.view === "profiles") {
      // The editor itself (CodeMirror instance + its owner/dirty bookkeeping)
      // belongs to ProfileManagerView.vue, which re-syncs its display the next
      // time its own navigation functions run. This module has no handle to
      // reach into it, so it only clears the reactive flags hasUnsavedChanges()
      // and beforeunload read.
      store.profileFormDirty = false;
      store.profileConfigDirty = false;
    }
    clearLatencyStateForInstance(store, store.activeId);
    clearActiveDetailCache();
  }
  store.activeId = id;
  store.view = "instances";
  store.creating = false;
  localStorage.setItem("activeInstance", id);
  return true;
}

export function showCreate(): boolean {
  if (!store.profiles.length) {
    // openProfileManager is owned by ProfileManagerView.vue (see bridge.ts's
    // ownership rule), so it is reached through the shared action table rather
    // than a local function.
    actions.openProfileManager();
    showMessage("请先创建配置档，再创建引用它的实例。", "error");
    return false;
  }
  if (!confirmDiscardChanges("新建实例")) return false;
  if (store.view === "profiles") {
    // Matches selectInstance's (~47-48) and openDashboard's (~85-86) discard
    // paths: the profile editor's dirty flags are not this module's state, but
    // hasUnsavedChanges()/beforeunload read them, so leaving them set after the
    // user already agreed to discard makes every later navigation re-prompt
    // "有未保存的修改…" for a discard that already happened.
    store.profileFormDirty = false;
    store.profileConfigDirty = false;
  }
  if (hasUnsavedChanges()) clearActiveDetailCache();
  store.view = "instances";
  store.creating = true;
  showMessage("");
  return true;
}

// The dashboard is read-only, so leaving the workbench for it cannot lose edits
// and needs no discard prompt. Coming back does, because the profile editor may
// still be mid-operation.
export function openDashboard(): boolean {
  if (profileOperationRunning()) return false;
  if (store.view === "profiles" && !confirmDiscardChanges("打开总览")) return false;
  if (store.view === "profiles") {
    store.profileCreating = false;
    store.profileFormDirty = false;
    store.profileConfigDirty = false;
  }
  store.view = "dashboard";
  sampleFleetTraffic();
  return true;
}

export function closeDashboard(): boolean {
  store.view = "instances";
  return true;
}
