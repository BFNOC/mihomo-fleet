import { api } from "../../api.ts";
import type { FleetInstance, FleetState } from "../../state.ts";

/**
 * Re-fetches GET /api/instances and merges the result into `state.instances`,
 * reconciling `state.activeId` the same way app.ts's refresh() does (falls
 * back to the first instance if the active one disappeared, e.g. after a
 * delete). Every action in this view that needs a post-write refresh
 * (start/stop/restart/clone/delete/save-basics) only ever changes instance
 * data, so this stays scoped to the instances list rather than re-fetching
 * system/profiles too -- those already refresh on their own cadence via
 * app.ts's still-running poll loop.
 */
export async function refreshInstancesList(state: FleetState): Promise<void> {
  try {
    const payload = await api<{ instances?: FleetInstance[] }>("/api/instances");
    state.instances = payload.instances || state.instances;
    if (!state.activeId && state.instances.length) state.activeId = state.instances[0]!.id;
    if (state.activeId && !state.instances.some((item) => item.id === state.activeId)) {
      state.activeId = state.instances[0]?.id || "";
    }
  } catch (err) {
    console.warn("Unable to refresh instance list.", err);
  }
}
