import { api } from "../api.ts";
import { createActionGate } from "../app-logic.ts";
import type { CreateInstancePayload } from "../bridge.ts";
import { showMessage } from "../bridge.ts";
import { formatBatchMessage } from "../format.ts";
import type { BatchActionPayload } from "../format.ts";
import { store } from "../store.ts";
import type { FleetInstance } from "../state.ts";
import { refresh } from "./fleet-refresh.ts";
import { clearActiveDetailCache } from "./navigation.ts";

// Instance lifecycle: create one, or act on the whole fleet at once.
//
// This gate is separate from the three in profile-gates.ts and deliberately
// does NOT feed chrome.profileBusy -- creating an instance has no reason to
// disable the profile editor. (The pre-Vue code called a global render() here,
// which is why a syncProfileBusy() call used to sit in createInstance; it
// recomputed the flag from gates this one is not part of, so it never changed
// anything.)
const createGate = createActionGate();

export async function createInstance(payload: CreateInstancePayload): Promise<void> {
  if (!createGate.begin()) return;
  try {
    const created = await api<FleetInstance>("/api/instances", {
      method: "POST",
      body: JSON.stringify(payload),
    });
    store.activeId = created.id;
    localStorage.setItem("activeInstance", created.id);
    store.creating = false;
    clearActiveDetailCache();
    showMessage("实例已创建。");
    await refresh();
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    showMessage(message, "error");
  } finally {
    createGate.end();
  }
}

/** Shape of the JSON body GET /api/ports/suggest returns. */
export interface SuggestedPorts {
  mixedPort?: number;
  controllerPort?: number;
}

// Suggestions are a convenience, not a requirement -- the backend picks its own
// ports when the form submits them empty, so a failure here is logged and
// swallowed rather than surfaced.
export async function suggestPorts(): Promise<SuggestedPorts> {
  try {
    return await api<SuggestedPorts>("/api/ports/suggest");
  } catch (err) {
    console.warn("Unable to load suggested ports.", err);
    return {};
  }
}

export function cancelCreate(): void {
  store.creating = false;
}

// store.bulkRunning is reactive, so setting it is the whole of what the pre-Vue
// updateBulkControls() used to push into the DOM: EmptyPanel.vue and
// InstanceDetail.vue read it and disable themselves.
export async function runBulkAction(action: string): Promise<void> {
  try {
    store.bulkRunning = true;
    const payload = await api<BatchActionPayload>(`/api/instances?action=${encodeURIComponent(action)}`, { method: "POST" });
    store.instances = payload.instances || store.instances;
    showMessage(formatBatchMessage(action, payload), payload.failed ? "error" : "info");
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    showMessage(message, "error");
    await refresh({ forceInstances: true });
  } finally {
    store.bulkRunning = false;
  }
}
