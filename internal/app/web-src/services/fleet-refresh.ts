import { api } from "../api.ts";
import { banner, chrome, showMessage } from "../bridge.ts";
import { store } from "../store.ts";
import type { FleetInstance, FleetProfile, FleetSystemStatus } from "../state.ts";
import { syncProfileBusy } from "./profile-gates.ts";

/** Options accepted by refresh(). */
export interface RefreshOptions {
  forceInstances?: boolean;
  /**
   * Set ONLY by services/polling.ts's periodic tick, never by an explicit
   * refresh (services/instances.ts, services/profiles.ts, or the post-op
   * refreshFleet() calls in views/profiles/profile-operations.ts).
   *
   * A profile mutation (save/delete/refresh-subscription) holds
   * chrome.profileBusy for the whole network round trip. If the periodic
   * poll's own GET /api/profiles lands inside that window, it can observe the
   * mutation's server-side effect (e.g. the row already deleted) before the
   * mutation's own response resolves locally, and reassign
   * store.activeProfileId out from under it. That fails operationContextMatches()
   * in profile-operations.ts and skips its entire success branch -- no
   * "配置档已删除" message, editor still showing the deleted profile. See that
   * file's guard comment for the full contract.
   *
   * The explicit refreshes above must NOT set this: they intentionally run
   * once the gate has already cleared (or, for saveProfile/refreshSubscription,
   * are themselves part of the one operation currently holding the gate) and
   * need the fresh profile list, not a frozen one.
   */
  periodic?: boolean;
}

// Only the newest call may write. An in-flight refresh whose sequence number
// has been superseded drops its response instead of clobbering fresher data.
let refreshSeq = 0;

// The exact text this module last wrote into the error banner via the catch
// below, so a later successful refresh can clear it -- but only if it is
// still the banner on screen. Error banners never auto-dismiss
// (MessageBanner.vue), so a poll failure would otherwise stick around forever
// even after the backend recovers. Cleared back to "" once acted on, so a
// success right after a fresh (non-poll) message never touches it.
let lastPollErrorText = "";

/** Re-pulls system + profiles + instances into the store. */
export async function refresh(options: RefreshOptions = {}): Promise<void> {
  const seq = ++refreshSeq;
  try {
    const [system, profiles, list] = await Promise.all([
      api<FleetSystemStatus>("/api/system"),
      api<{ profiles?: FleetProfile[] }>("/api/profiles"),
      api<{ instances?: FleetInstance[] }>("/api/instances"),
    ]);
    if (seq !== refreshSeq) return;
    store.system = system;
    // See RefreshOptions.periodic's doc: only the periodic poll skips this
    // while a profile operation is in flight, so it cannot race that
    // operation's own guard. Every explicit caller still applies the fresh
    // list immediately, including while chrome.profileBusy is true for its
    // own operation (saveProfile/refreshSubscriptionProfile in
    // services/profiles.ts both refresh before their gate is released).
    if (!(options.periodic && chrome.profileBusy)) {
      store.profiles = profiles.profiles || [];
      if (!store.profileCreating && store.activeProfileId && !store.profiles.some((profile) => profile.id === store.activeProfileId)) {
        store.activeProfileId = store.profiles[0]?.id || "";
      }
    }
    if (!store.bulkRunning || options.forceInstances) {
      store.instances = list.instances || [];
      // `store.instances.length` just guarded a non-empty array, so index 0 is
      // always present; the assertion only documents that to noUncheckedIndexedAccess.
      if (!store.activeId && store.instances.length) store.activeId = store.instances[0]!.id;
      if (store.activeId && !store.instances.some((item) => item.id === store.activeId)) {
        store.activeId = store.instances[0]?.id || "";
      }
    }
    localStorage.setItem("activeInstance", store.activeId);
    syncProfileBusy();
    // A poll failure never auto-dismisses (MessageBanner.vue only auto-clears
    // non-error banners), so this success has to clear it explicitly -- but
    // only if it is still exactly the banner this module wrote; some other
    // action may have written its own message in the meantime and that must
    // survive a poll succeeding underneath it.
    if (lastPollErrorText && banner.tone === "error" && banner.text === lastPollErrorText) {
      showMessage("");
    }
    lastPollErrorText = "";
  } catch (err) {
    if (seq !== refreshSeq) return;
    const message = err instanceof Error ? err.message : String(err);
    showMessage(message, "error");
    lastPollErrorText = message;
  }
}
