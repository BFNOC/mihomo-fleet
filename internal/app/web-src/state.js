import { latencyKeySeparator } from "./constants.js";

export function createState() {
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
    creating: false,
    proxyGroups: [],
    proxyApply: false,
    latencyResults: {},
    latencyRunning: new Set(),
    latencyBatchRunning: false,
    latencyBatchToken: 0,
    bulkRunning: false,
    cloneRunning: false,
    editInstanceId: "",
    editDirty: false,
    editVersion: 0,
  };
}

export function activeInstance(state) {
  return state.instances.find((item) => item.id === state.activeId) || state.instances[0] || null;
}

export function profileById(state, id) {
  return state.profiles.find((profile) => profile.id === id) || null;
}

export function profileReferenceCount(state, profileId) {
  return state.instances.filter((item) => item.profileId === profileId).length;
}

export function latencyKey(instanceId, group, proxy, kind) {
  return [instanceId, group, proxy, kind].join(latencyKeySeparator);
}

export function latencyResult(state, instanceId, group, proxy, kind) {
  return state.latencyResults[latencyKey(instanceId, group, proxy, kind)] || null;
}

export function isLatencyRunning(state, instanceId, group, proxy, kind) {
  return state.latencyRunning.has(latencyKey(instanceId, group, proxy, kind));
}

export function setLatencyResult(state, instanceId, group, proxy, kind, patch) {
  const key = latencyKey(instanceId, group, proxy, kind);
  state.latencyResults[key] = {
    ...(state.latencyResults[key] || {}),
    ...patch,
    updatedAt: Date.now(),
  };
}

export function setLatencyRunning(state, instanceId, group, proxy, kind, running) {
  const key = latencyKey(instanceId, group, proxy, kind);
  if (running) state.latencyRunning.add(key);
  else state.latencyRunning.delete(key);
}

export function clearLatencyStateForInstance(state, instanceId) {
  const prefix = `${instanceId}${latencyKeySeparator}`;
  for (const key of Object.keys(state.latencyResults)) {
    if (key.startsWith(prefix)) delete state.latencyResults[key];
  }
  for (const key of [...state.latencyRunning]) {
    if (key.startsWith(prefix)) state.latencyRunning.delete(key);
  }
}

export function pruneLatencyResultsForGroups(state, instanceId, groups) {
  if (!instanceId) return;
  const validGroupProxy = new Set();
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
