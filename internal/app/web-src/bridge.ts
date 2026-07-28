import { reactive } from "vue";
import type { FleetProfile } from "./state.ts";

// Action registry bridging Vue components to the behaviour still implemented in
// app.ts. Components import `actions` and call `actions.selectInstance(id)`;
// app.ts fills the table during boot via registerActions().
//
// This exists to keep the dependency one-way. Components must not import app.ts
// directly: app.ts touches the DOM at module scope, so importing it from a
// component would run it before Vue has mounted anything.
//
// Every entry is a no-op until app.ts registers it, so a component rendered
// during the boot gap cannot throw.
const noop = () => {};

// Each entry carries its own explicit signature. Inferring them all from `noop`
// would type every action as `() => void`, so `actions.selectInstance(id)` would
// fail to compile with "Expected 0 arguments, but got 1" -- pushing every call
// site into a cast that silently defeats the checking this table exists for.
//
// Return types are `void` even though app.ts's implementations return boolean:
// callers here never use the result, and `() => boolean` is assignable to
// `() => void`, so registration still typechecks.
// Payload for saveProfile. The profiles view owns the CodeMirror handle, so it
// passes the YAML in rather than app.ts reading it back out of an editor it can
// no longer reach.
export interface SaveProfilePayload {
  creating: boolean;
  profileId: string;
  name: string;
  source: "manual" | "subscription";
  config?: string;
  subscriptionUrl?: string;
  autoUpdate?: boolean;
  updateIntervalMinutes?: number;
}

export interface CreateInstancePayload {
  name: string;
  profileId: string;
  mixedPort: number;
  proxyBind: string;
  controllerPort: number;
  mode: string;
  localProxies: string;
  chain: string[];
}

export interface FleetActions {
  selectInstance: (id: string) => void;
  showCreate: () => void;
  openDashboard: () => void;
  closeDashboard: () => void;
  openProfileManager: (profileId?: string) => void;
  closeProfileManager: () => void;
  startAll: () => void;
  stopAll: () => void;
  copyProxyValue: (value: string, success: string | undefined) => void;
  showMessage: (text: string, tone?: BannerTone) => void;
  dismissMessage: () => void;

  // Create view. These take their payload as an argument because the form
  // fields now live in component state; the app.ts originals read them straight
  // off el.createName/el.createProfile/... and would submit empty data if
  // called unchanged.
  createInstance: (input: CreateInstancePayload) => Promise<void>;
  suggestPorts: () => Promise<{ mixedPort?: number; controllerPort?: number }>;
  cancelCreate: () => void;

  // Profiles view. app.ts keeps only the gated fetch (mutual exclusion via its
  // existing ActionGate objects, which still drive chrome.profileBusy); the
  // component owns everything that touches the editor.
  saveProfile: (payload: SaveProfilePayload) => Promise<FleetProfile>;
  /**
   * Network DELETE only -- deliberately does NOT touch store.profiles or
   * re-poll. ProfileManagerView.vue's post-delete guard compares
   * store.profileFormOwnerId against store.activeProfileId, and refreshFleet()
   * moves activeProfileId off a profile that no longer exists, so a refresh on
   * this side of the await makes that guard fail every time. The caller owns
   * the ordering; see the delete path in ProfileManagerView.vue.
   */
  deleteProfile: (profileId: string) => Promise<void>;
  /** Re-pulls system + profiles + instances into the store. */
  refreshFleet: (options?: { forceInstances?: boolean }) => Promise<void>;
  refreshSubscriptionProfile: (profileId: string) => Promise<FleetProfile>;
  fetchProfileConfig: (profileId: string) => Promise<string>;
}

// A key is owned by exactly ONE registrant. app.ts must not list any key a
// component provides, and no two components may provide the same key.
//
// This is not style. registerActions() is Object.assign, so last write wins,
// and main.ts mounts every component synchronously BEFORE it dynamically
// imports app.ts -- so app.ts always registers last and would silently clobber
// a component's version. The failure is invisible: no error, no warning, the
// button just stops doing anything.
//
// Currently component-owned (must stay out of app.ts's registerActions call):
//   openProfileManager, closeProfileManager  -- views/profiles/ProfileManagerView.vue
//     (both need direct access to that component's private CodeMirror handle)
export const actions: FleetActions = {
  selectInstance: noop,
  showCreate: noop,
  openDashboard: noop,
  closeDashboard: noop,
  openProfileManager: noop,
  closeProfileManager: noop,
  startAll: noop,
  stopAll: noop,
  copyProxyValue: noop,
  showMessage: noop,
  dismissMessage: noop,
  // Async actions resolve rather than no-op so a caller that awaits one during
  // the boot gap gets a settled promise instead of hanging forever.
  createInstance: async () => {},
  suggestPorts: async () => ({}),
  cancelCreate: noop,
  saveProfile: () => Promise.reject(new Error("saveProfile called before app.ts registered it")),
  deleteProfile: async () => {},
  refreshFleet: async () => {},
  refreshSubscriptionProfile: () =>
    Promise.reject(new Error("refreshSubscriptionProfile called before app.ts registered it")),
  fetchProfileConfig: async () => "",
};

export function registerActions(table: Partial<FleetActions>): void {
  Object.assign(actions, table);
}

// Transient UI state that belongs to the chrome rather than to the domain state
// in store.ts: the message banner's current text and severity.
//
// reactive() is load-bearing, not decoration. The banner is written by app.ts's
// showMessage() and read by the Vue chrome; a plain object would let the write
// land with no re-render, so the banner would silently never update.
export type BannerTone = "info" | "error";

export const banner = reactive<{ text: string; tone: BannerTone }>({
  text: "",
  tone: "info",
});

// Derived chrome state that the shell renders from but that does NOT live in
// FleetState, so `store` alone cannot expose it to components.
//
// `profileBusy` mirrors app.ts's profileOperationRunning(), which reads three
// ActionGate objects held in app.ts module scope. Those gates are plain
// closures outside the reactive graph, so a component reading them directly
// would never re-render. app.ts pushes the derived value in from render() --
// render() already runs on every state change, so it is the natural sync point.
//
// Keep this object minimal. Anything that genuinely belongs to the domain
// belongs in state.ts/FleetState instead, where it is reactive for free.
// `trafficTick` exists because dashboard.ts's sampling history is a plain
// module-scope Map, not reactive state. A computed() calling instanceSeries()
// or fleetConnectionRows() reads nothing Vue tracks, so it would evaluate once
// and never invalidate -- the charts would silently freeze with no error.
// app.ts bumps this right after each sampleFleet() lands, giving those
// computeds one real dependency to hang off.
export const chrome = reactive<{ profileBusy: boolean; trafficTick: number }>({
  profileBusy: false,
  trafficTick: 0,
});
