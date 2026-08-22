import { api } from "../api.ts";
import { chrome, showMessage } from "../bridge.ts";
import { dismissNotice } from "../notifications.ts";
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

// The queue id of the error this module last raised via the catch below, so a
// later successful refresh can take that one entry back down. Errors never
// auto-dismiss (notifications.ts), so a poll failure would otherwise stay on
// screen forever after the backend recovered. Dismissing by id rather than by
// matching text is what keeps a message some other action raised in the
// meantime from being cleared by a poll succeeding underneath it. Reset to 0
// once acted on; ids are never reused, so a stale one cannot collide with a
// later entry.
let lastPollErrorId = 0;

/**
 * Re-pulls system + profiles + instances into the store.
 *
 * Never throws: a network failure is reported through the shared error banner
 * and reflected in the returned boolean instead, so a periodic poll failing
 * cannot reject into a caller that has no meaningful recovery.
 *
 * Returns false ONLY when this call actually failed to refresh and said so in
 * the banner. Being superseded by a newer refresh returns true: that newer
 * call owns writing the store and reporting its own outcome. BackupSection.vue
 * reads this to decide whether to warn that the list did not refresh, and a
 * newer refresh already being in flight is not something to warn about.
 */
export async function refresh(options: RefreshOptions = {}): Promise<boolean> {
  const seq = ++refreshSeq;
  try {
    const [system, profiles, list] = await Promise.all([
      api<FleetSystemStatus>("/api/system"),
      api<{ profiles?: FleetProfile[] }>("/api/profiles"),
      api<{ instances?: FleetInstance[] }>("/api/instances"),
    ]);
    // Superseded by a newer refresh: that call now owns writing the store and
    // reporting its own outcome, so this reports success rather than failure.
    // The distinction matters to BackupSection.vue, whose only use of the
    // return value is deciding whether to warn "the list did not refresh" --
    // a newer refresh being in flight is precisely the case where that warning
    // would be a false positive.
    if (seq !== refreshSeq) return true;
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
    // A poll failure never auto-dismisses (notifications.ts only expires
    // non-error entries), so this success has to clear it explicitly. By id,
    // so it can only ever remove the entry this module raised -- and a no-op
    // if the user already dismissed it by hand.
    if (lastPollErrorId) dismissNotice(lastPollErrorId);
    lastPollErrorId = 0;
    return true;
  } catch (err) {
    // Same reasoning as the superseded check in the try block above: a newer
    // refresh owns the outcome, and this stale call's failure is not the one
    // to report -- it never wrote the store and never shows a banner either.
    if (seq !== refreshSeq) return true;
    const message = err instanceof Error ? err.message : String(err);
    // Repeated identical failures merge into the one entry the queue already
    // holds (notifications.ts dedups on text+tone), so this id stays stable
    // across a whole outage rather than naming a card per failed poll.
    lastPollErrorId = showMessage(message, "error");
    return false;
  }
}
