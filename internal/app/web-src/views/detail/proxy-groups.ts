import { computed, ref } from "vue";
import { store } from "../../store.ts";
import { actions } from "../../bridge.ts";
import { api } from "../../api.ts";
import { activeInstance, isLatencyRunning, latencyResult, pruneLatencyResultsForGroups } from "../../state.ts";
import type { FleetInstance, FleetProxyGroup } from "../../state.ts";
import type { LatencyKind } from "../../constants.ts";
import { localizedMessage } from "../../messages.ts";
import {
  alignProxyGroupsToProfileOrder,
  currentLatencyTarget,
  filterRuntimeProxyGroups,
  formatLatencyValue,
  isSelectableProxyGroup,
  latencyLabel,
  latencyTitle,
  latencyTone,
  proxyLabelSources,
  splitProxyLabel,
} from "../../format.ts";

// Loading proxy groups for the active instance and turning them into the view
// models ProxiesTab.vue renders. Module scope, matching that component's single
// mount inside InstanceDetail.vue.

export const proxySourceText = ref("运行时读取 mihomo，停止时读取缓存配置。");
export const loadError = ref("");
export const filterText = ref("");

// True while the first load for the currently active instance is still
// outstanding. `store.proxyGroups` gets reset to `[]` on every instance
// switch (navigation.ts's clearActiveDetailCache() / this view's own
// resetActiveDetailState()), so without this ProxiesTab.vue can't tell "no
// groups yet because the fetch hasn't returned" apart from "genuinely no
// groups" and renders the wrong empty state for the length of one poll.
export const proxiesLoading = ref(false);

// Per-group marker: true while a selectProxy() POST + refreshProxies() pair is
// in flight for that group. ProxiesTab.vue disables the group's node buttons
// while set; selectProxy() itself also refuses a second call for the same
// group so a fast double-click can't fire two POSTs before the first one's
// refreshProxies() lands.
export const pendingProxySelections = ref<Set<string>>(new Set());

const selected = computed(() => activeInstance(store));

export interface ChipView {
  kind: LatencyKind;
  className: string;
  text: string;
  title: string;
}

export interface ProxyEntry {
  name: string;
  label: string;
  source: string;
  latencyChip: ChipView | null;
}

export interface DisplayGroup {
  group: FleetProxyGroup;
  proxies: ProxyEntry[];
  count: number;
  selectable: boolean;
  currentName: string;
  chips: ChipView[];
  urlDisabled: boolean;
  realDisabled: boolean;
  actionTitle: string;
  pending: boolean;
}

function buildChips(instance: FleetInstance | null, groupName: string, currentName: string): ChipView[] {
  if (!currentName) return [];
  return (["url", "real"] as const).map((kind) => {
    const result = instance ? latencyResult(store, instance.id, groupName, currentName, kind) : null;
    const running = instance ? isLatencyRunning(store, instance.id, groupName, currentName, kind) : false;
    const value = formatLatencyValue(result, running);
    return {
      kind,
      className: `latency-chip ${latencyTone(result, running)}`,
      text: `${latencyLabel(kind)} ${value}`,
      title: result?.error || `${latencyTitle(kind)} ${value}`,
    };
  });
}

// One node's own latency, as opposed to buildChips()'s group-head chip which
// only ever shows the CURRENT node. Both read the same (instance, group,
// proxy, "url") key -- latency.ts's runGroupUrlDelayAll() now writes it for
// every member of the group from a single request, not just the selected
// one, so this needs no request-shaped state of its own.
//
// Returns null while idle (no test has ever touched this node), rather than
// a "--" placeholder, so a freshly loaded group renders with no chips at all
// instead of one per node. A running or resolved test always returns a chip,
// so "in flight", "tested with no delay", and "never tested" stay three
// visually distinct states instead of two.
function buildNodeLatencyChip(instance: FleetInstance | null, groupName: string, proxyName: string): ChipView | null {
  if (!instance) return null;
  const running = isLatencyRunning(store, instance.id, groupName, proxyName, "url");
  const result = latencyResult(store, instance.id, groupName, proxyName, "url");
  if (!running && !result) return null;
  const value = formatLatencyValue(result, running);
  return {
    kind: "url",
    className: `latency-chip ${latencyTone(result, running)}`,
    text: value,
    title: result?.error || `${latencyTitle("url")} ${value}`,
  };
}

// Deliberately does NOT port the pre-Vue render snapshot / focus capture-restore
// helpers -- those existed only so a full `innerHTML = ""` repaint could fake
// unchanged-output skipping and focus preservation, and Vue's keyed v-for
// handles focus/DOM-identity preservation natively. A stable key does NOT,
// however, skip recomputation: it only tells Vue which DOM node maps to which
// array entry, not whether that entry's value actually changed. Assigning a
// fresh-but-content-identical array to `store.proxyGroups` still invalidates
// this computed and reruns splitProxyLabel()/buildChips() for every proxy in
// every group. refreshProxies() below restores an equivalent to the old
// snapshot, scoped to the one array write that actually matters.
export const displayGroups = computed<DisplayGroup[]>(() => {
  const instance = selected.value;
  const filter = filterText.value.trim().toLowerCase();
  const labelSources = proxyLabelSources(store.profiles, store.instances);
  const list: DisplayGroup[] = [];
  for (const group of store.proxyGroups) {
    const names = (group.all || []).filter(
      (name) => !filter || name.toLowerCase().includes(filter) || group.name.toLowerCase().includes(filter),
    );
    if (!names.length) continue;
    const currentName = currentLatencyTarget(group, store.proxyGroups);
    const running = (kind: LatencyKind) =>
      Boolean(instance && currentName && isLatencyRunning(store, instance.id, group.name, currentName, kind));
    list.push({
      group,
      proxies: names.map((name) => {
        const split = splitProxyLabel(name, labelSources);
        return {
          name,
          label: split.name,
          source: split.source,
          latencyChip: buildNodeLatencyChip(instance, group.name, name),
        };
      }),
      count: names.length,
      selectable: isSelectableProxyGroup(group),
      currentName,
      chips: buildChips(instance, group.name, currentName),
      urlDisabled: !store.proxyApply || !currentName || running("url"),
      realDisabled: !store.proxyApply || !currentName || running("real"),
      actionTitle: !store.proxyApply ? "请先启动实例再测速" : !currentName ? "当前节点不可测速" : "",
      pending: pendingProxySelections.value.has(group.name),
    });
  }
  return list;
});

async function loadProfileProxyGroups(instance: FleetInstance | null): Promise<FleetProxyGroup[]> {
  if (!instance?.profileId) return [];
  const profileId = encodeURIComponent(instance.profileId);
  const instanceId = encodeURIComponent(instance.id);
  const payload = await api<{ groups?: FleetProxyGroup[] }>(`/api/profiles/${profileId}/proxies?instanceId=${instanceId}`);
  return payload.groups || [];
}

// The profile order is only a presentation preference, so failing to read it
// falls back to mihomo's runtime order rather than failing the whole refresh.
async function loadProfileProxyGroupsForRuntime(instance: FleetInstance | null): Promise<FleetProxyGroup[]> {
  try {
    return await loadProfileProxyGroups(instance);
  } catch (err) {
    console.warn("Unable to load profile proxy order; using mihomo runtime order.", err);
    return [];
  }
}

// Only the newest request may write, and only while its instance is still the
// active one.
let requestSeq = 0;

// Last payload actually written to store.proxyGroups, keyed by instance id so
// an instance switch (which resets store.proxyGroups to `[]` without touching
// these) can never be mistaken for "unchanged" just because a different
// instance happened to have produced the same JSON once before.
let lastProxyGroupsInstanceId = "";
let lastProxyGroupsSnapshot = "";

async function loadRunningGroups(instance: FleetInstance): Promise<FleetProxyGroup[]> {
  const [payload, profileGroups] = await Promise.all([
    api<{ proxies?: Record<string, FleetProxyGroup> }>(`/api/mihomo/${instance.id}/proxies`),
    loadProfileProxyGroupsForRuntime(instance),
  ]);
  const proxies = payload.proxies || {};
  const aligned = alignProxyGroupsToProfileOrder(
    Object.values(proxies).filter((item) => Array.isArray(item.all)),
    profileGroups,
  );
  return filterRuntimeProxyGroups(instance, aligned);
}

export async function refreshProxies(): Promise<void> {
  const instance = selected.value;
  if (!instance) return;
  const seq = ++requestSeq;
  const isStale = () => seq !== requestSeq || store.activeId !== instance.id;
  const isFirstLoad = store.proxyGroups.length === 0;
  if (isFirstLoad) proxiesLoading.value = true;
  try {
    const running = instance.status === "running";
    const groups = running ? await loadRunningGroups(instance) : await loadProfileProxyGroups(instance);
    if (isStale()) return;
    proxySourceText.value = running
      ? "当前读取运行中的 mihomo 节点，选择后立即应用并保存。"
      : "当前读取缓存配置，选择会保存到实例，下次启动后自动恢复。";
    loadError.value = "";
    // The API hands back a fresh array of fresh objects every poll even when
    // nothing changed; skip the store write (and the displayGroups recompute
    // it would force) when this instance's payload is byte-identical to what
    // is already showing.
    //
    // "is already showing" has to be read off store.proxyGroups itself, not off
    // the snapshot alone. Three call sites reset store.proxyGroups to `[]`
    // (navigation.ts's clearActiveDetailCache(), this view's
    // resetActiveDetailState(), and instances.ts), and showCreate() reaches the
    // first one WITHOUT changing store.activeId -- so an id+snapshot match can
    // hold while the store is empty, and skipping the write there would leave
    // the node list blank until the payload happened to change.
    const snapshot = JSON.stringify(groups);
    const showing = store.proxyGroups.length > 0;
    if (!showing || instance.id !== lastProxyGroupsInstanceId || snapshot !== lastProxyGroupsSnapshot) {
      lastProxyGroupsInstanceId = instance.id;
      lastProxyGroupsSnapshot = snapshot;
      store.proxyGroups = groups;
    }
    store.proxyApply = running;
    pruneLatencyResultsForGroups(store, instance.id, groups);
  } catch (err) {
    if (isStale()) return;
    const message = err instanceof Error ? err.message : String(err);
    loadError.value = localizedMessage(message);
  } finally {
    if (isFirstLoad && !isStale()) proxiesLoading.value = false;
  }
}

export async function selectProxy(groupName: string, proxyName: string): Promise<void> {
  const instance = selected.value;
  if (!instance) return;
  // The highlight only updates after POST + refreshProxies() land -- two
  // round trips -- so refuse a second select on a group that already has one
  // in flight rather than letting concurrent POSTs race for the same group.
  if (pendingProxySelections.value.has(groupName)) return;
  pendingProxySelections.value.add(groupName);
  const apply = store.proxyApply;
  try {
    const updated = await api<FleetInstance>(`/api/instances/${instance.id}/selection`, {
      method: "POST",
      body: JSON.stringify({ group: groupName, proxy: proxyName, apply }),
    });
    store.instances = store.instances.map((item) => (item.id === updated.id ? updated : item));
    actions.showMessage(apply ? `已应用并保存 ${groupName} -> ${proxyName}。` : `已保存 ${groupName} -> ${proxyName}。`);
    await refreshProxies();
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    actions.showMessage(message, "error");
  } finally {
    pendingProxySelections.value.delete(groupName);
  }
}
