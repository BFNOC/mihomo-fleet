import { api } from "../api.ts";
import type { SaveProfilePayload } from "../bridge.ts";
import { store } from "../store.ts";
import type { FleetProfile } from "../state.ts";
import { refresh } from "./fleet-refresh.ts";
import {
  deleteProfileGate,
  profileOperationRunning,
  refreshSubscriptionGate,
  saveProfileGate,
  syncProfileBusy,
} from "./profile-gates.ts";

// Thin network actions. ProfileManagerView.vue owns the editor, the
// dirty/version bookkeeping, and the operation-context guard; this module keeps
// only what genuinely cannot move across the bridge -- the mutual-exclusion
// gates that also drive chrome.profileBusy -- plus the request and the
// store.profiles upsert.

/** Request body POST/PUT /api/profiles(/:id) accepts; see saveProfile(). */
interface SaveProfileBody {
  name: string;
  subscriptionUrl?: string;
  autoUpdate?: boolean;
  updateIntervalMinutes?: number;
  config?: string;
}

// Every mutation below opens with this. Returning (rather than queueing) is
// deliberate: the UI disables its own controls off chrome.profileBusy, so
// reaching here at all means two operations raced.
function beginProfileOperation(gate: { begin: () => boolean }): void {
  if (profileOperationRunning() || !gate.begin()) {
    throw new Error("配置档操作正在进行，请稍候。");
  }
  syncProfileBusy();
}

function endProfileOperation(gate: { end: () => void }): void {
  gate.end();
  syncProfileBusy();
}

export async function saveProfile(payload: SaveProfilePayload): Promise<FleetProfile> {
  beginProfileOperation(saveProfileGate);
  try {
    const body: SaveProfileBody = { name: payload.name };
    if (payload.source === "subscription") {
      body.subscriptionUrl = payload.subscriptionUrl;
      body.autoUpdate = payload.autoUpdate;
      body.updateIntervalMinutes = payload.updateIntervalMinutes;
    } else {
      body.config = payload.config;
    }
    const saved = await api<FleetProfile>(payload.creating ? "/api/profiles" : `/api/profiles/${payload.profileId}`, {
      method: payload.creating ? "POST" : "PUT",
      body: JSON.stringify(body),
    });
    store.profiles = payload.creating
      ? [...store.profiles, saved]
      : store.profiles.map((item) => (item.id === saved.id ? saved : item));
    await refresh();
    return saved;
  } finally {
    endProfileOperation(saveProfileGate);
  }
}

// Network + gate only. The store bookkeeping that used to live here (dropping
// the row, moving activeProfileId, re-polling) belongs to the caller, because
// refresh() reassigns store.activeProfileId when the active profile vanishes --
// and that is exactly the field ProfileManagerView.vue's post-await guard
// compares against, so doing it here made the guard fail on every successful
// delete: the row disappeared but the form kept showing the deleted profile and
// no "已删除" message ever appeared. See bridge.ts's note on this key.
export async function deleteProfile(profileId: string): Promise<void> {
  beginProfileOperation(deleteProfileGate);
  try {
    await api(`/api/profiles/${profileId}`, { method: "DELETE" });
  } finally {
    endProfileOperation(deleteProfileGate);
  }
}

export async function refreshSubscriptionProfile(profileId: string): Promise<FleetProfile> {
  beginProfileOperation(refreshSubscriptionGate);
  try {
    const refreshed = await api<FleetProfile>(`/api/profiles/${profileId}/refresh`, { method: "POST" });
    store.profiles = store.profiles.map((item) => (item.id === refreshed.id ? refreshed : item));
    await refresh();
    return refreshed;
  } finally {
    endProfileOperation(refreshSubscriptionGate);
  }
}

// Not gated: a plain read with no mutual-exclusion concern, and
// ProfileManagerView.vue already sequences its own calls (profileConfigLoadSeq).
export async function fetchProfileConfig(profileId: string): Promise<string> {
  const payload = await api<{ config?: string }>(`/api/profiles/${profileId}/config`);
  return payload.config || "";
}
