import { latencyKeySeparator } from "./constants.ts";
import type { InstanceMode, InstanceStatus, LatencyKind } from "./constants.ts";

// Domain object shapes. These mirror the JSON the Go controller sends (field
// names/optionality follow the `json:"..."` tags on the corresponding structs
// in internal/app/types.go) rather than being guessed from usage. Exported
// from here -- the module owning the state shape -- so every other module
// that reads state.instances/.profiles/.system/.proxyGroups imports the same
// definition instead of redeclaring it.

/** Mirrors internal/app/types.go's SubscriptionInfo. */
export interface FleetSubscriptionInfo {
  upload: number;
  download: number;
  total: number;
  expire: number;
}

/**
 * Mirrors internal/app/types.go's Profile (the `json:"...,omitempty"` tags
 * become optional properties here).
 */
export interface FleetProfile {
  id: string;
  name: string;
  configPath: string;
  subscriptionUrl?: string;
  autoUpdate: boolean;
  updateIntervalMinutes?: number;
  lastUpdatedAt?: string;
  lastUpdateError?: string;
  homeUrl?: string;
  subscriptionInfo?: FleetSubscriptionInfo;
  createdAt: string;
  updatedAt: string;
}

/**
 * Mirrors internal/app/types.go's InstanceView -- the read shape the
 * controller actually sends for GET /api/instances (not the stored Instance,
 * which has slightly different optionality). `mode` and `status` reuse the
 * literal unions constants.ts already derives from the Go-side enums
 * (viewFor/decorateStatus in manager.go only ever emit those values).
 */
export interface FleetInstance {
  id: string;
  name: string;
  profileId: string;
  profileName?: string;
  profileConfigPath?: string;
  mixedPort: number;
  proxyBind: string;
  controllerPort: number;
  userConfigPath: string;
  runtimeConfigPath: string;
  mode: InstanceMode;
  localProxies?: string;
  chain?: string[];
  selectedProxies?: Record<string, string>;
  selectedGroup?: string;
  selectedProxy?: string;
  createdAt: string;
  updatedAt: string;
  lastError?: string;
  status: InstanceStatus;
  pid?: number;
  pendingRestart?: boolean;
}

/**
 * Mirrors internal/app/types.go's ProfileProxyGroup, as returned by
 * GET /api/profiles/{id}/proxies. `all` can arrive as JSON `null` (a nil Go
 * slice with no `omitempty` still marshals to `null`, e.g. an empty
 * global-chain plan in parseGlobalChainProxyGroups) -- callers already guard
 * with `group.all || []` / `Array.isArray(group.all)`.
 */
export interface FleetProxyGroup {
  name: string;
  type?: string;
  all: string[] | null;
  now?: string;
}

/** Mirrors internal/app/types.go's SystemStatus (GET /api/system). */
export interface FleetSystemStatus {
  bind: string;
  port: number;
  dataDir: string;
  appVersion: string;
  mihomoPath: string;
  mihomoFound: boolean;
  mihomoSource: string;
  version?: string;
}

/** A single group/proxy/kind latency measurement, keyed by latencyKey(). */
export interface LatencyResult {
  delay: number;
  error: string;
  updatedAt: number;
}

/**
 * Patch applied by setLatencyResult(). Both fields are required (rather than
 * Partial<LatencyResult>) because every call site always supplies both
 * together; `updatedAt` is never patched in -- it is always recomputed by
 * setLatencyResult itself.
 */
export type LatencyResultPatch = Pick<LatencyResult, "delay" | "error">;

/** Top-level panel the chrome is showing. */
export type FleetView = "instances" | "profiles" | "dashboard";

/** Tab within the instance workbench (data-tab in index.html). */
export type FleetTab = "overview" | "proxies" | "logs";

/** Source of the profile currently being created in the create-profile form. */
export type ProfileCreateSource = "manual" | "subscription";

/**
 * Full shape of the single reactive state object (see store.ts). Exported so
 * every module that receives `state` as a parameter can annotate it with this
 * one definition instead of each redeclaring an ad-hoc shape.
 */
export interface FleetState {
  system: FleetSystemStatus | null;
  profiles: FleetProfile[];
  instances: FleetInstance[];
  view: FleetView;
  activeId: string;
  activeProfileId: string;
  activeTab: FleetTab;
  profileCreating: boolean;
  profileCreateSource: ProfileCreateSource;
  profileFormDirty: boolean;
  profileFormVersion: number;
  // These three replace DOM datasets that used to hold the same values
  // (el.profileEditor.dataset.profileId, el.configEditor.dataset.profileId,
  // el.configEditor.dataset.dirty). A dataset is invisible to Vue's reactivity,
  // so the chrome could not react to it -- the same problem that forced
  // chrome.profileBusy to exist for the ActionGate objects. They mirror the
  // editInstanceId/editDirty/editVersion triple below.
  //
  // profileConfigDirty is load-bearing beyond the profiles view:
  // hasUnsavedChanges() reads it, and every navigation action in the app routes
  // through confirmDiscardChanges().
  profileFormOwnerId: string;
  profileConfigOwnerId: string;
  profileConfigDirty: boolean;
  creating: boolean;
  proxyGroups: FleetProxyGroup[];
  proxyApply: boolean;
  latencyResults: Record<string, LatencyResult>;
  latencyRunning: Set<string>;
  latencyBatchRunning: boolean;
  latencyBatchToken: number;
  bulkRunning: boolean;
  cloneRunning: boolean;
  editInstanceId: string;
  editDirty: boolean;
  editVersion: number;
}

export function createState(): FleetState {
  return {
    system: null,
    profiles: [],
    instances: [],
    view: "instances",
    activeId: localStorage.getItem("activeInstance") || "",
    activeProfileId: "",
    activeTab: "overview",
    profileCreating: false,
    profileCreateSource: "manual",
    profileFormDirty: false,
    profileFormVersion: 0,
    profileFormOwnerId: "",
    profileConfigOwnerId: "",
    profileConfigDirty: false,
    creating: false,
    proxyGroups: [],
    proxyApply: false,
    latencyResults: {},
    latencyRunning: new Set<string>(),
    latencyBatchRunning: false,
    latencyBatchToken: 0,
    bulkRunning: false,
    cloneRunning: false,
    editInstanceId: "",
    editDirty: false,
    editVersion: 0,
  };
}

export function activeInstance(state: FleetState): FleetInstance | null {
  return state.instances.find((item) => item.id === state.activeId) || state.instances[0] || null;
}

export function profileById(state: FleetState, id: string): FleetProfile | null {
  return state.profiles.find((profile) => profile.id === id) || null;
}

export function profileReferenceCount(state: FleetState, profileId: string): number {
  return state.instances.filter((item) => item.profileId === profileId).length;
}

export function latencyKey(instanceId: string, group: string, proxy: string, kind: LatencyKind): string {
  return [instanceId, group, proxy, kind].join(latencyKeySeparator);
}

export function latencyResult(
  state: FleetState,
  instanceId: string,
  group: string,
  proxy: string,
  kind: LatencyKind,
): LatencyResult | null {
  return state.latencyResults[latencyKey(instanceId, group, proxy, kind)] || null;
}

export function isLatencyRunning(
  state: FleetState,
  instanceId: string,
  group: string,
  proxy: string,
  kind: LatencyKind,
): boolean {
  return state.latencyRunning.has(latencyKey(instanceId, group, proxy, kind));
}

export function setLatencyResult(
  state: FleetState,
  instanceId: string,
  group: string,
  proxy: string,
  kind: LatencyKind,
  patch: LatencyResultPatch,
): void {
  const key = latencyKey(instanceId, group, proxy, kind);
  state.latencyResults[key] = {
    ...(state.latencyResults[key] || {}),
    ...patch,
    updatedAt: Date.now(),
  };
}

export function setLatencyRunning(
  state: FleetState,
  instanceId: string,
  group: string,
  proxy: string,
  kind: LatencyKind,
  running: boolean,
): void {
  const key = latencyKey(instanceId, group, proxy, kind);
  if (running) state.latencyRunning.add(key);
  else state.latencyRunning.delete(key);
}

export function clearLatencyStateForInstance(state: FleetState, instanceId: string): void {
  const prefix = `${instanceId}${latencyKeySeparator}`;
  for (const key of Object.keys(state.latencyResults)) {
    if (key.startsWith(prefix)) delete state.latencyResults[key];
  }
  for (const key of [...state.latencyRunning]) {
    if (key.startsWith(prefix)) state.latencyRunning.delete(key);
  }
}

export function pruneLatencyResultsForGroups(
  state: FleetState,
  instanceId: string,
  groups: FleetProxyGroup[],
): void {
  if (!instanceId) return;
  const validGroupProxy = new Set<string>();
  for (const group of groups) {
    for (const name of group.all || []) {
      validGroupProxy.add(`${group.name}${latencyKeySeparator}${name}`);
    }
  }
  const prefix = `${instanceId}${latencyKeySeparator}`;
  for (const key of Object.keys(state.latencyResults)) {
    if (!key.startsWith(prefix)) continue;
    const [groupName, proxyName] = key.slice(prefix.length).split(latencyKeySeparator);
    if (!validGroupProxy.has(`${groupName}${latencyKeySeparator}${proxyName}`)) {
      delete state.latencyResults[key];
    }
  }
}
