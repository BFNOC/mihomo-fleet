import { api } from "../api.ts";
import { showMessage } from "../bridge.ts";
import { store } from "../store.ts";
import type { FleetInstance, FleetProfile, FleetSystemStatus } from "../state.ts";
import { syncProfileBusy } from "./profile-gates.ts";

/** Options accepted by refresh(). */
export interface RefreshOptions {
  forceInstances?: boolean;
}

// Only the newest call may write. An in-flight refresh whose sequence number
// has been superseded drops its response instead of clobbering fresher data.
let refreshSeq = 0;

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
    store.profiles = profiles.profiles || [];
    if (!store.profileCreating && store.activeProfileId && !store.profiles.some((profile) => profile.id === store.activeProfileId)) {
      store.activeProfileId = store.profiles[0]?.id || "";
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
  } catch (err) {
    if (seq !== refreshSeq) return;
    const message = err instanceof Error ? err.message : String(err);
    showMessage(message, "error");
  }
}
