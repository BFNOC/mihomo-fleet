import "./styles.css";
import { fastPollIntervalMs, instanceModes, slowPollIntervalMs } from "./constants.ts";
import { api, writeClipboard } from "./api.ts";
import { sampleFleet, setGeoResolver } from "./dashboard.ts";
import type { ConnectionsFetchPayload, GeoLookupResult } from "./dashboard.ts";
import { bindElements } from "./dom.ts";
import { formatBatchMessage } from "./format.ts";
import type { BatchActionPayload } from "./format.ts";
import { clearLatencyStateForInstance } from "./state.ts";
import type { FleetInstance, FleetProfile, FleetSystemStatus } from "./state.ts";
import { actions, banner, chrome, registerActions } from "./bridge.ts";
import type { CreateInstancePayload, SaveProfilePayload } from "./bridge.ts";
import { store } from "./store.ts";
import { createActionGate } from "./yaml-editor.ts";

// Aliased (not reassigned -- `state` stays `const`) to the same reactive
// object store.ts wraps in reactive(createState()). Vue's chrome components
// read that object directly, so mutating fields on `state` here is what
// makes their re-render happen with no explicit render() call needed on
// their side. See store.ts for the contract.
const state = store;
const el = bindElements();

const createGate = createActionGate();
const saveProfileGate = createActionGate();
const deleteProfileGate = createActionGate();
const refreshSubscriptionGate = createActionGate();
let refreshSeq = 0;
let slowPollTimer: ReturnType<typeof setTimeout> | null = null;
let fastPollTimer: ReturnType<typeof setTimeout> | null = null;

function profileOperationRunning(): boolean {
  return saveProfileGate.isRunning()
    || deleteProfileGate.isRunning()
    || refreshSubscriptionGate.isRunning();
}

// Mirrors state.editDirty (instance edit form, owned by OverviewTab.vue) plus
// the two profile-editor dirty flags (owned by ProfileManagerView.vue) that
// replaced the old configEditor DOM dataset. beforeunload and the surviving
// navigation guards below (confirmDiscardChanges) both still need this one
// combined signal even though none of the three flags is set from this file
// anymore.
function hasUnsavedChanges(): boolean {
  return state.editDirty || state.profileFormDirty || state.profileConfigDirty;
}

function confirmDiscardChanges(action: string): boolean {
  if (!hasUnsavedChanges()) return true;
  return window.confirm(`有未保存的修改。确定放弃并${action}吗？`);
}

// Writes the raw text into the reactive banner; MessageBanner.vue owns both
// the localizedMessage() translation and the 6s auto-dismiss timer now, so
// neither happens here (see bridge.ts's `banner` and the component for why).
function showMessage(text: string, kind: string = "info"): void {
  banner.text = text;
  banner.tone = kind === "error" ? "error" : "info";
}

/** Options accepted by refresh(); see call sites below and in bindEvents(). */
interface RefreshOptions {
  forceInstances?: boolean;
  periodic?: boolean;
}

async function refresh(options: RefreshOptions = {}): Promise<void> {
  const seq = ++refreshSeq;
  try {
    const [system, profiles, list] = await Promise.all([
      api<FleetSystemStatus>("/api/system"),
      api<{ profiles?: FleetProfile[] }>("/api/profiles"),
      api<{ instances?: FleetInstance[] }>("/api/instances"),
    ]);
    if (seq !== refreshSeq) return;
    state.system = system;
    state.profiles = profiles.profiles || [];
    if (!state.profileCreating && state.activeProfileId && !state.profiles.some((profile) => profile.id === state.activeProfileId)) {
      state.activeProfileId = state.profiles[0]?.id || "";
    }
    if (!state.bulkRunning || options.forceInstances) {
      state.instances = list.instances || [];
      // `state.instances.length` just guarded a non-empty array, so index 0 is
      // always present; the assertion only documents that to noUncheckedIndexedAccess.
      if (!state.activeId && state.instances.length) state.activeId = state.instances[0]!.id;
      if (state.activeId && !state.instances.some((item) => item.id === state.activeId)) {
        state.activeId = state.instances[0]?.id || "";
      }
    }
    localStorage.setItem("activeInstance", state.activeId);
    render();
    await refreshActiveDetails({ skipFast: options.periodic });
  } catch (err) {
    if (seq !== refreshSeq) return;
    const message = err instanceof Error ? err.message : String(err);
    showMessage(message, "error");
  }
}

function render(): void {
  // ActionGate objects backing profileOperationRunning() live outside the
  // reactive graph (plain closures in this module), so a component reading
  // them directly would never re-render; render() is the sync point since it
  // already runs on every state change (see bridge.ts's `chrome`).
  chrome.profileBusy = profileOperationRunning();
  updateBulkControls();
  renderPanels();
}

async function copyProxyValue(value: string, success: string | undefined): Promise<void> {
  try {
    await writeClipboard(value);
    showMessage(success || "");
  } catch (err) {
    console.warn("Unable to copy proxy value.", err);
    showMessage("复制失败，请检查浏览器剪贴板权限。", "error");
  }
}

function updateBulkControls(): void {
  // el.emptyCreate.disabled used to be pushed from here; EmptyPanel.vue now
  // computes its own `disabled` straight from store.bulkRunning. Kept as a
  // (currently empty) function rather than deleted, since render()/
  // runBulkAction() still call it and may grow other bulk-only DOM pushes.
}

function renderPanels(): void {
  const profilesView = state.view === "profiles";
  const dashboardView = state.view === "dashboard";
  // Anything that is not the instance workbench hides the workbench panels.
  // Testing "not instances" rather than "is profiles" keeps this correct as
  // further views are added.
  const away = profilesView || dashboardView;
  el.profilePanel.classList.toggle("hidden", !profilesView);
  el.dashboardPanel.classList.toggle("hidden", !dashboardView);
  el.createPanel.classList.toggle("hidden", away || !state.creating);
  el.emptyPanel.classList.toggle("hidden", away || state.creating || state.instances.length > 0);
}

// mode is compared against instanceModes.globalChain here rather than through
// format.ts's instanceMode() helper because callers already have the raw
// mode string (an <select> value or a FleetInstance field) to hand.
function applyModeFields(prefix: "edit" | "create", mode: string): void {
  const chainMode = mode === instanceModes.globalChain;
  el[`${prefix}ChainFields`].classList.toggle("hidden", !chainMode);
}

function clearActiveDetailCache(): void {
  state.editInstanceId = "";
  state.editDirty = false;
  state.editVersion = 0;
  state.proxyGroups = [];
  state.proxyApply = false;
  state.latencyBatchRunning = false;
  state.latencyBatchToken += 1;
}

function selectInstance(id: string): boolean {
  if (state.activeId !== id || state.view === "profiles") {
    if (!confirmDiscardChanges("切换实例")) {
      return false;
    }
    if (state.view === "profiles") {
      // Mirrors the old resetConfigEditor()'s dirty-flag reset. The editor
      // itself (CodeMirror instance + its `dirty`/`ownerId` DOM datasets) is
      // now owned entirely by ProfileManagerView.vue, which re-syncs its
      // display the next time its own navigation functions run -- this file
      // no longer has a handle to reach into that editor directly, so it
      // only clears the reactive flags hasUnsavedChanges()/beforeunload read.
      state.profileFormDirty = false;
      state.profileConfigDirty = false;
    }
    clearLatencyStateForInstance(state, state.activeId);
    clearActiveDetailCache();
  }
  state.activeId = id;
  state.view = "instances";
  state.creating = false;
  localStorage.setItem("activeInstance", id);
  render();
  refreshActiveDetails();
  return true;
}

function showCreate(): boolean {
  if (!state.profiles.length) {
    // openProfileManager is owned by ProfileManagerView.vue (see bridge.ts's
    // ownership rule), so this file reaches it through the shared action
    // table rather than a local function -- app.ts no longer defines one.
    actions.openProfileManager();
    showMessage("请先创建配置档，再创建引用它的实例。", "error");
    return false;
  }
  if (!confirmDiscardChanges("新建实例")) return false;
  if (hasUnsavedChanges()) clearActiveDetailCache();
  state.view = "instances";
  state.creating = true;
  showMessage("");
  render();
  return true;
}

// The dashboard is read-only, so leaving the workbench for it cannot lose
// edits and needs no discard prompt. Coming back does, because the profile
// editor may still be mid-operation.
function openDashboard(): boolean {
  if (profileOperationRunning()) return false;
  if (state.view === "profiles" && !confirmDiscardChanges("打开总览")) return false;
  if (state.view === "profiles") {
    state.profileCreating = false;
    state.profileFormDirty = false;
    state.profileConfigDirty = false;
  }
  state.view = "dashboard";
  render();
  sampleFleetTraffic();
  return true;
}

function closeDashboard(): boolean {
  state.view = "instances";
  render();
  refreshActiveDetails();
  return true;
}

/** Options accepted by refreshActiveDetails(); see call sites in app.ts. */
interface RefreshActiveDetailsOptions {
  skipFast?: boolean;
}

// Now a no-op: every tab that used to poll through here (overview/proxies/
// logs) owns its own fetch-on-visible/fetch-on-interval loop (see
// views/detail/useTabPolling.ts), driven off store.activeTab/store.activeId
// directly rather than being told to refresh by this module. Kept (rather
// than deleted) because refresh()/selectInstance()/closeDashboard()/
// runBulkAction() all still call it, and turning every one of those into a
// conditional call site is more churn than one empty function.
async function refreshActiveDetails(options: RefreshActiveDetailsOptions = {}): Promise<void> {}

// See refreshActiveDetails() above -- same reasoning, this is the half that
// used to dispatch to refreshLogs()/refreshProxies() by active tab.
async function pollActiveTab(): Promise<void> {}

/** Request body POST/PUT /api/profiles(/:id) accepts; see saveProfile(). */
interface SaveProfileBody {
  name: string;
  subscriptionUrl?: string;
  autoUpdate?: boolean;
  updateIntervalMinutes?: number;
  config?: string;
}

// Profiles: thin actions. ProfileManagerView.vue owns the editor, the
// dirty/version bookkeeping, and the operation-context guard that used to
// live in this file (activeProfileContextId/captureProfileOperationContext/
// profileOperationContextMatches/advanceProfileContext, all deleted); this
// file keeps only what genuinely can't move across the bridge -- the
// mutual-exclusion gates that also drive chrome.profileBusy -- plus the
// actual network call and the state.profiles upsert.
async function saveProfile(payload: SaveProfilePayload): Promise<FleetProfile> {
  if (profileOperationRunning() || !saveProfileGate.begin()) {
    throw new Error("配置档操作正在进行，请稍候。");
  }
  render();
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
    state.profiles = payload.creating
      ? [...state.profiles, saved]
      : state.profiles.map((item) => (item.id === saved.id ? saved : item));
    await refresh();
    return saved;
  } finally {
    saveProfileGate.end();
    render();
  }
}

async function deleteProfile(profileId: string): Promise<void> {
  if (profileOperationRunning() || !deleteProfileGate.begin()) {
    throw new Error("配置档操作正在进行，请稍候。");
  }
  render();
  try {
    await api(`/api/profiles/${profileId}`, { method: "DELETE" });
    state.profiles = state.profiles.filter((item) => item.id !== profileId);
    await refresh({ forceInstances: true });
  } finally {
    deleteProfileGate.end();
    render();
  }
}

async function refreshSubscriptionProfile(profileId: string): Promise<FleetProfile> {
  if (profileOperationRunning() || !refreshSubscriptionGate.begin()) {
    throw new Error("配置档操作正在进行，请稍候。");
  }
  render();
  try {
    const refreshed = await api<FleetProfile>(`/api/profiles/${profileId}/refresh`, { method: "POST" });
    state.profiles = state.profiles.map((item) => (item.id === refreshed.id ? refreshed : item));
    await refresh();
    return refreshed;
  } finally {
    refreshSubscriptionGate.end();
    render();
  }
}

// Not gated by the three ActionGate objects above: it is a plain read with no
// mutual-exclusion concern, and ProfileManagerView.vue already sequences its
// own calls (profileConfigLoadSeq) the same way loadProfileConfig() used to.
async function fetchProfileConfig(profileId: string): Promise<string> {
  const payload = await api<{ config?: string }>(`/api/profiles/${profileId}/config`);
  return payload.config || "";
}

async function createInstance(payload: CreateInstancePayload): Promise<void> {
  if (!createGate.begin()) return;
  render();
  try {
    const created = await api<FleetInstance>("/api/instances", {
      method: "POST",
      body: JSON.stringify(payload),
    });
    state.activeId = created.id;
    localStorage.setItem("activeInstance", created.id);
    state.creating = false;
    clearActiveDetailCache();
    showMessage("实例已创建。");
    await refresh();
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    showMessage(message, "error");
  } finally {
    createGate.end();
    render();
  }
}

/** Shape of the JSON body GET /api/ports/suggest returns. */
interface SuggestedPorts {
  mixedPort?: number;
  controllerPort?: number;
}

async function suggestPorts(): Promise<SuggestedPorts> {
  try {
    return await api<SuggestedPorts>("/api/ports/suggest");
  } catch (err) {
    console.warn("Unable to load suggested ports.", err);
    return {};
  }
}

function cancelCreate(): void {
  state.creating = false;
  render();
}

async function runBulkAction(action: string): Promise<void> {
  try {
    state.bulkRunning = true;
    updateBulkControls();
    const payload = await api<BatchActionPayload>(`/api/instances?action=${encodeURIComponent(action)}`, { method: "POST" });
    state.instances = payload.instances || state.instances;
    showMessage(formatBatchMessage(action, payload), payload.failed ? "error" : "info");
    render();
    await refreshActiveDetails();
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    showMessage(message, "error");
    await refresh({ forceInstances: true });
  } finally {
    state.bulkRunning = false;
    updateBulkControls();
  }
}

function bindEvents(): void {
  window.addEventListener("beforeunload", (event) => {
    if (!hasUnsavedChanges()) return;
    event.preventDefault();
    event.returnValue = "";
  });
}

function scheduleSlowPoll(delay: number = slowPollIntervalMs): void {
  clearTimeout(slowPollTimer || undefined);
  slowPollTimer = null;
  if (document.hidden) return;
  slowPollTimer = setTimeout(runSlowPoll, delay);
}

async function runSlowPoll(): Promise<void> {
  if (!document.hidden) await refresh({ periodic: true });
  scheduleSlowPoll();
}

function scheduleFastPoll(delay: number = fastPollIntervalMs): void {
  clearTimeout(fastPollTimer || undefined);
  fastPollTimer = null;
  if (document.hidden) return;
  fastPollTimer = setTimeout(runFastPoll, delay);
}

// Keep sampling while any view is open so opening the dashboard already has a
// filled window. Cost is one /connections call per running instance per tick.
async function sampleFleetTraffic(): Promise<void> {
  if (!state.instances?.length) return;
  await sampleFleet(
    state.instances,
    (id) => api<ConnectionsFetchPayload>(`/api/mihomo/${encodeURIComponent(id)}/connections`),
    Date.now(),
  );
  // dashboard.ts's sampler Map is a plain module-scope value outside Vue's
  // reactive graph (see bridge.ts's `chrome.trafficTick` comment); bumping
  // this after every sample is what gives DashboardView.vue's computeds a
  // real dependency to invalidate on, replacing the old direct
  // renderDashboard(el.dashboardPanel, state) call.
  chrome.trafficTick += 1;
}

async function runFastPoll(): Promise<void> {
  if (!document.hidden) {
    await sampleFleetTraffic();
    await pollActiveTab();
  }
  scheduleFastPoll();
}

document.addEventListener("visibilitychange", () => {
  if (document.hidden) return;
  runSlowPoll();
  runFastPoll();
});

// Country lookups run against the local database the controller already stages
// for mihomo, so no destination address ever leaves the machine.
setGeoResolver((ips) => api<GeoLookupResult>("/api/geoip", { method: "POST", body: JSON.stringify({ ips }) }));

bindEvents();

// Fills in bridge.ts's action table so the Vue chrome (TopBar/SideBar/etc.)
// can call back into this still-vanilla layer. Registered once, after
// bindEvents() so every implementation below is fully wired up first.
//
// openProfileManager/closeProfileManager are deliberately absent: bridge.ts's
// OWNERSHIP RULE reserves those two keys for ProfileManagerView.vue, which
// registers them itself. main.ts mounts that component before this module's
// dynamic import runs, so this call (which runs last) would otherwise
// silently clobber them back to no-ops.
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
  refreshSubscriptionProfile,
  fetchProfileConfig,
});

refresh();
scheduleSlowPoll();
scheduleFastPoll();
