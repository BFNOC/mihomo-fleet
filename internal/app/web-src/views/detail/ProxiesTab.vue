<script setup lang="ts">
// Vue replacement for #tab-proxies's markup (index.html:278-299) and the
// app.ts functions that fill/drive it: refreshProxies() (app.ts:926-968),
// renderProxyGroups() (app.ts:1019-1121), selectProxy() (app.ts:1123-1139),
// updateLatencyControls() (app.ts:285-310, the proxies-tab half), and the
// proxy tooltip (proxyTooltipButton()/showProxyTooltip()/hideProxyTooltip(),
// app.ts:312-340, plus the pointerover/pointerout/focusin/focusout listeners
// in bindEvents(), app.ts:1597-1619).
//
// LATENCY CONTROLLER: reused verbatim via createLatencyController()
// (latency.ts) -- testGroupLatency()/testAllLatency()/latencySettings()/
// persistLatencySettings() all run the exact same request/state logic as
// before. The two DOM-node-producing helpers on that controller
// (renderLatencyChip()/applyLatencyChipState()) are NOT reused: they build
// and mutate a raw <span> imperatively, which has no home in a Vue
// template. The chip markup below instead binds directly to the same pure
// formatters those helpers call internally (formatLatencyValue/latencyTone/
// latencyLabel/latencyTitle from format.ts), so the chip's actual
// presentation logic is identical, just re-hosted as a template computed
// instead of an imperative DOM write.
//
// TOOLTIP: uses <Teleport to="body"> instead of app.ts's module-scope
// `document.createElement("div")` appended directly to `document.body`
// (app.ts:102-107). That element has no counterpart in index.html and lived
// entirely outside the Vue-owned tree; Teleport gives the same "actually in
// <body>, not clipped by any ancestor's overflow" placement while keeping
// the node's lifecycle (and the show/hide state driving it) owned by this
// component. Positioning math (edge/gap clamping) is ported unchanged from
// showProxyTooltip() (app.ts:317-336). The hover/focus wiring is simplified:
// the original listened for the bubbling pointerover/pointerout on the
// whole list and filtered by `event.relatedTarget` to ignore moves within
// the same button; pointerenter/pointerleave do not bubble, so binding them
// per-button needs no such filtering.
import { computed, nextTick, onMounted, onUnmounted, ref } from "vue";
import { store } from "../../store.ts";
import { actions } from "../../bridge.ts";
import { api } from "../../api.ts";
import { createLatencyController } from "../../latency.ts";
import type { LatencyController } from "../../latency.ts";
import {
  activeInstance,
  isLatencyRunning,
  latencyResult,
  pruneLatencyResultsForGroups,
} from "../../state.ts";
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
  normalizeStoredLatencyTimeout,
  normalizeStoredLatencyUrl,
  proxyLabelSources,
  splitProxyLabel,
} from "../../format.ts";
import { useTabPolling } from "./useTabPolling.ts";

const selected = computed(() => activeInstance(store));
const isActiveTab = computed(() => store.activeTab === "proxies");

const filterText = ref("");
const proxySourceText = ref("运行时读取 mihomo，停止时读取缓存配置。");
const loadError = ref("");

function showMessage(text: string, kind?: string): void {
  actions.showMessage(text, kind === "error" ? "error" : "info");
}

const latencyUrlInput = ref<HTMLInputElement | null>(null);
const latencyTimeoutInput = ref<HTMLInputElement | null>(null);
// A ref (not a plain `let`) so the template's type-checking sees the
// post-onMounted() assignment -- a `let LatencyController | null` reassigned
// only inside a callback resolves to `never` in <script setup>'s generated
// template-render type, since the template macro snapshots each binding's
// type without following control flow across a closure boundary.
const latency = ref<LatencyController | null>(null);

onMounted(() => {
  latency.value = createLatencyController({
    state: store,
    el: { latencyUrl: latencyUrlInput.value!, latencyTimeout: latencyTimeoutInput.value! },
    getActive: () => activeInstance(store),
    showMessage,
  });
  const storedLatencyUrl = localStorage.getItem("fleetLatencyUrl");
  latencyUrlInput.value!.value = normalizeStoredLatencyUrl(storedLatencyUrl);
  latencyTimeoutInput.value!.value = normalizeStoredLatencyTimeout(localStorage.getItem("fleetLatencyTimeout"), storedLatencyUrl);
});

const hasLatencyTarget = computed(() => store.proxyGroups.some((group) => currentLatencyTarget(group, store.proxyGroups)));
const testAllDisabled = computed(() => {
  const instance = selected.value;
  return !instance || instance.status !== "running" || !store.proxyApply || !hasLatencyTarget.value || store.latencyBatchRunning;
});

interface ChipView {
  kind: LatencyKind;
  className: string;
  text: string;
  title: string;
}

interface ProxyEntry {
  name: string;
  label: string;
  source: string;
}

interface DisplayGroup {
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

// Mirrors renderProxyGroups()'s per-group/per-proxy assembly (app.ts:1019-1116).
// Deliberately does NOT port proxyGroupsRenderSnapshot()/lastProxyGroupsSnapshot
// or the focus-capture/restore helpers around it (capturedProxyFocusKey()/
// restoreProxyListFocus()/proxyFocusKey()/latencyButtonFocusKey()/
// isLatencyFocusKey(), app.ts:988-1017) -- those existed only so a full
// `innerHTML = ""` repaint could fake unchanged-output skipping and
// focus/DOM-identity preservation. Vue's keyed v-for (`:key="group.name"` /
// `:key="entry.name"` below) does both natively.
const displayGroups = computed<DisplayGroup[]>(() => {
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
    const chips: ChipView[] = currentName
      ? (["url", "real"] as const).map((kind) => {
          const result = instance ? latencyResult(store, instance.id, group.name, currentName, kind) : null;
          const running = instance ? isLatencyRunning(store, instance.id, group.name, currentName, kind) : false;
          const value = formatLatencyValue(result, running);
          return {
            kind,
            className: `latency-chip ${latencyTone(result, running)}`,
            text: `${latencyLabel(kind)} ${value}`,
            title: result?.error || `${latencyTitle(kind)} ${value}`,
          };
        })
      : [];
    const urlRunning = Boolean(instance && currentName && isLatencyRunning(store, instance.id, group.name, currentName, "url"));
    const realRunning = Boolean(instance && currentName && isLatencyRunning(store, instance.id, group.name, currentName, "real"));
    list.push({
      group,
      proxies: names.map((name) => {
        const split = splitProxyLabel(name, labelSources);
        return { name, label: split.name, source: split.source };
      }),
      count: names.length,
      selectable: isSelectableProxyGroup(group),
      currentName,
      chips,
      urlDisabled: !store.proxyApply || !currentName || urlRunning,
      realDisabled: !store.proxyApply || !currentName || realRunning,
      actionTitle: !store.proxyApply ? "请先启动实例再测速" : !currentName ? "当前节点不可测速" : "",
    });
  }
  return list;
});

// Mirrors loadProfileProxyGroups()/loadProfileProxyGroupsForRuntime()
// (app.ts:909-924).
async function loadProfileProxyGroups(instance: FleetInstance | null): Promise<FleetProxyGroup[]> {
  if (!instance?.profileId) return [];
  const profileId = encodeURIComponent(instance.profileId);
  const instanceId = encodeURIComponent(instance.id);
  const payload = await api<{ groups?: FleetProxyGroup[] }>(`/api/profiles/${profileId}/proxies?instanceId=${instanceId}`);
  return payload.groups || [];
}

async function loadProfileProxyGroupsForRuntime(instance: FleetInstance | null): Promise<FleetProxyGroup[]> {
  try {
    return await loadProfileProxyGroups(instance);
  } catch (err) {
    console.warn("Unable to load profile proxy order; using mihomo runtime order.", err);
    return [];
  }
}

let requestSeq = 0;

// Mirrors refreshProxies() (app.ts:926-968), minus the innerHTML writes
// (replaced by `loadError`/`displayGroups` driving the template below).
async function refreshProxies(): Promise<void> {
  const instance = selected.value;
  if (!instance) return;
  const seq = ++requestSeq;
  try {
    let groups: FleetProxyGroup[] = [];
    let apply = false;
    if (instance.status === "running") {
      const [payload, profileGroups] = await Promise.all([
        api<{ proxies?: Record<string, FleetProxyGroup> }>(`/api/mihomo/${instance.id}/proxies`),
        loadProfileProxyGroupsForRuntime(instance),
      ]);
      if (seq !== requestSeq || store.activeId !== instance.id) return;
      const proxies = payload.proxies || {};
      groups = alignProxyGroupsToProfileOrder(
        Object.values(proxies).filter((item) => Array.isArray(item.all)),
        profileGroups,
      );
      groups = filterRuntimeProxyGroups(instance, groups);
      apply = true;
      proxySourceText.value = "当前读取运行中的 mihomo 节点，选择后立即应用并保存。";
    } else {
      groups = await loadProfileProxyGroups(instance);
      if (seq !== requestSeq || store.activeId !== instance.id) return;
      proxySourceText.value = "当前读取缓存配置，选择会保存到实例，下次启动后自动恢复。";
    }
    loadError.value = "";
    store.proxyGroups = groups;
    store.proxyApply = apply;
    pruneLatencyResultsForGroups(store, instance.id, groups);
  } catch (err) {
    if (seq !== requestSeq || store.activeId !== instance.id) return;
    const message = err instanceof Error ? err.message : String(err);
    loadError.value = localizedMessage(message);
  }
}

useTabPolling(isActiveTab, computed(() => selected.value?.id || ""), refreshProxies);

// Mirrors selectProxy() (app.ts:1123-1139).
async function selectProxy(groupName: string, proxyName: string): Promise<void> {
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

// Tooltip: see the file-level comment for why this is Teleport instead of
// the module-scope element app.ts used to create.
const tooltipEl = ref<HTMLElement | null>(null);
const tooltipVisible = ref(false);
const tooltipText = ref("");
const tooltipLeft = ref("0px");
const tooltipTop = ref("0px");
const hoverQuery = window.matchMedia("(hover: hover) and (pointer: fine)");

function positionTooltip(button: HTMLElement): void {
  const tooltip = tooltipEl.value;
  if (!tooltip) return;
  const edge = 8;
  const gap = 8;
  const buttonRect = button.getBoundingClientRect();
  const tooltipRect = tooltip.getBoundingClientRect();
  const maxLeft = Math.max(edge, window.innerWidth - tooltipRect.width - edge);
  const maxTop = Math.max(edge, window.innerHeight - tooltipRect.height - edge);
  const left = Math.min(Math.max(buttonRect.left, edge), maxLeft);
  let top = buttonRect.top - tooltipRect.height - gap;
  if (top < edge) top = buttonRect.bottom + gap;
  top = Math.min(Math.max(top, edge), maxTop);
  tooltipLeft.value = `${left}px`;
  tooltipTop.value = `${top}px`;
}

function showTooltipFromPointer(event: PointerEvent, text: string): void {
  if (!hoverQuery.matches) return;
  showTooltip(event.currentTarget as HTMLElement, text);
}

function showTooltip(button: HTMLElement, text: string): void {
  if (!text) return;
  tooltipText.value = text;
  tooltipVisible.value = true;
  void nextTick(() => positionTooltip(button));
}

function hideTooltip(): void {
  tooltipVisible.value = false;
}

function onWindowChange(): void {
  hideTooltip();
}

onMounted(() => {
  window.addEventListener("resize", onWindowChange);
  window.addEventListener("scroll", onWindowChange, true);
});
onUnmounted(() => {
  window.removeEventListener("resize", onWindowChange);
  window.removeEventListener("scroll", onWindowChange, true);
});
</script>

<template>
  <section class="panel">
    <div class="panel-title">
      <h3>Mihomo 节点组</h3>
      <p id="proxySource">{{ proxySourceText }}</p>
    </div>
    <div class="proxy-tools">
      <label>
        <span>测试 URL</span>
        <input id="latencyUrl" ref="latencyUrlInput" placeholder="http://cp.cloudflare.com/generate_204" @change="latency?.persistLatencySettings()">
      </label>
      <label class="latency-timeout">
        <span>超时 ms</span>
        <input id="latencyTimeout" ref="latencyTimeoutInput" type="number" min="500" max="15000" step="500" placeholder="10000" @change="latency?.persistLatencySettings()">
      </label>
      <button id="testAllLatency" type="button" :disabled="testAllDisabled" @click="latency?.testAllLatency('url')">测速各组当前</button>
      <button id="testAllRealLatency" type="button" :disabled="testAllDisabled" @click="latency?.testAllLatency('real')">真延迟各组当前</button>
    </div>
    <input id="proxyFilter" class="proxy-filter" placeholder="筛选节点" aria-label="筛选节点" v-model="filterText">
    <div id="proxiesList" class="proxy-list">
      <div v-if="loadError" class="message error">{{ loadError }}</div>
      <template v-else>
        <section v-for="entry in displayGroups" :key="entry.group.name" class="proxy-group">
          <div class="proxy-group-head">
            <strong>{{ entry.group.name }}</strong>
            <div class="proxy-group-meta">
              <span>{{ entry.group.now ? `当前 ${entry.group.now}` : `${entry.count} 个节点` }}</span>
              <span v-if="entry.chips.length" class="latency-chips current">
                <span v-for="chip in entry.chips" :key="chip.kind" :class="chip.className" :title="chip.title" :aria-label="chip.title">{{ chip.text }}</span>
              </span>
            </div>
            <div class="proxy-group-actions">
              <button type="button" :title="entry.actionTitle" :disabled="entry.urlDisabled" @click="latency?.testGroupLatency(entry.group, 'url')">测速</button>
              <button type="button" :title="entry.actionTitle" :disabled="entry.realDisabled" @click="latency?.testGroupLatency(entry.group, 'real')">真延迟</button>
            </div>
          </div>
          <div class="proxy-grid">
            <button
              v-for="proxy in entry.proxies"
              :key="proxy.name"
              type="button"
              class="proxy-choice"
              :class="{ selected: entry.group.now === proxy.name }"
              :disabled="!entry.selectable"
              :aria-label="proxy.name"
              :aria-pressed="entry.selectable ? (entry.group.now === proxy.name ? 'true' : 'false') : undefined"
              @click="entry.selectable && selectProxy(entry.group.name, proxy.name)"
              @pointerenter="showTooltipFromPointer($event, proxy.name)"
              @pointerleave="hideTooltip"
              @focus="showTooltip($event.currentTarget as HTMLElement, proxy.name)"
              @blur="hideTooltip"
            >
              <span class="proxy-name">{{ proxy.label }}</span>
              <span v-if="proxy.source" class="proxy-source">{{ proxy.source }}</span>
            </button>
          </div>
        </section>
        <div v-if="!displayGroups.length && store.proxyGroups.length" class="warning">没有匹配的节点。</div>
        <div v-if="!store.proxyGroups.length" class="warning">没有可显示的节点组。使用 proxy-providers 的订阅需要启动实例后读取 mihomo 运行态节点。</div>
      </template>
    </div>
  </section>
  <Teleport to="body">
    <div
      id="proxyTooltip"
      ref="tooltipEl"
      class="proxy-tooltip"
      :class="{ hidden: !tooltipVisible }"
      role="tooltip"
      :style="{ left: tooltipLeft, top: tooltipTop }"
    >{{ tooltipText }}</div>
  </Teleport>
</template>
