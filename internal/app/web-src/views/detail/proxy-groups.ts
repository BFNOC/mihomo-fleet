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

// Deliberately does NOT port the pre-Vue render snapshot / focus capture-restore
// helpers. Those existed only so a full `innerHTML = ""` repaint could fake
// unchanged-output skipping and focus preservation; Vue's keyed v-for does both
// natively.
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
        return { name, label: split.name, source: split.source };
      }),
      count: names.length,
      selectable: isSelectableProxyGroup(group),
      currentName,
      chips: buildChips(instance, group.name, currentName),
      urlDisabled: !store.proxyApply || !currentName || running("url"),
      realDisabled: !store.proxyApply || !currentName || running("real"),
      actionTitle: !store.proxyApply ? "请先启动实例再测速" : !currentName ? "当前节点不可测速" : "",
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
  try {
    const running = instance.status === "running";
    const groups = running ? await loadRunningGroups(instance) : await loadProfileProxyGroups(instance);
    if (isStale()) return;
    proxySourceText.value = running
      ? "当前读取运行中的 mihomo 节点，选择后立即应用并保存。"
      : "当前读取缓存配置，选择会保存到实例，下次启动后自动恢复。";
    loadError.value = "";
    store.proxyGroups = groups;
    store.proxyApply = running;
    pruneLatencyResultsForGroups(store, instance.id, groups);
  } catch (err) {
    if (isStale()) return;
    const message = err instanceof Error ? err.message : String(err);
    loadError.value = localizedMessage(message);
  }
}

export async function selectProxy(groupName: string, proxyName: string): Promise<void> {
  const instance = selected.value;
  if (!instance) return;
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
  }
}
