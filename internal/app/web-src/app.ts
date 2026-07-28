// Boot and wiring, nothing else. Every behaviour this file used to implement
// now lives in services/ next to the state it touches; what is left is the
// order those pieces have to be switched on in.
//
// Loaded as a dynamic import from main.ts, after the Vue shells are mounted --
// refresh() runs at module scope below, so mounting first means the first paint
// is the real UI rather than an empty skeleton.
import "./styles.css";
import { registerActions, showMessage } from "./bridge.ts";
import { setGeoResolver } from "./dashboard.ts";
import type { GeoLookupResult } from "./dashboard.ts";
import { api } from "./api.ts";
import { copyProxyValue } from "./services/clipboard.ts";
import { refresh } from "./services/fleet-refresh.ts";
import { startPolling } from "./services/polling.ts";
import {
  createInstance,
  cancelCreate,
  runBulkAction,
  suggestPorts,
} from "./services/instances.ts";
import {
  closeDashboard,
  hasUnsavedChanges,
  openDashboard,
  selectInstance,
  showCreate,
} from "./services/navigation.ts";
import {
  deleteProfile,
  fetchProfileConfig,
  refreshSubscriptionProfile,
  saveProfile,
} from "./services/profiles.ts";

// Country lookups run against the local database the controller already stages
// for mihomo, so no destination address ever leaves the machine.
setGeoResolver((ips) => api<GeoLookupResult>("/api/geoip", { method: "POST", body: JSON.stringify({ ips }) }));

window.addEventListener("beforeunload", (event) => {
  if (!hasUnsavedChanges()) return;
  event.preventDefault();
  event.returnValue = "";
});

// Fills in bridge.ts's action table so the Vue components can call back into
// this layer.
//
// openProfileManager/closeProfileManager are deliberately absent: bridge.ts's
// OWNERSHIP RULE reserves those two keys for ProfileManagerView.vue, which
// registers them itself. main.ts mounts that component before this module's
// dynamic import runs, so this call (which runs last) would otherwise silently
// clobber them back to no-ops.
registerActions({
  selectInstance,
  showCreate,
  openDashboard,
  closeDashboard,
  startAll: () => runBulkAction("start-all"),
  stopAll: () => runBulkAction("stop-all"),
  copyProxyValue,
  showMessage,
  dismissMessage: () => showMessage(""),
  createInstance,
  suggestPorts,
  cancelCreate,
  saveProfile,
  deleteProfile,
  refreshFleet: refresh,
  refreshSubscriptionProfile,
  fetchProfileConfig,
});

refresh();
startPolling();
