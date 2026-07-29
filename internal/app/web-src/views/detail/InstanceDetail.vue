<script setup lang="ts">
// Vue replacement for the whole #detailPanel region (index.html:178-310) plus
// four pieces of the pre-Vue app.ts: the "selected" half of renderPanels()
// (everything from `el.detailName.textContent = selected.name;` down through
// `updateLatencyControls();`), setActiveTab(), and bindEvents()'s tabList
// keydown handler and start/stop/restart/clone/delete button handlers.
//
// Self-toggles its own `.hidden` class from `store.view`/`store.creating`/
// the active instance, the same pattern MessageBanner.vue uses for #message
// -- rather than having app.ts's renderPanels() keep reaching in to toggle
// `el.detailPanel`'s class, which would leave a Vue-owned subtree half
// managed by vanilla code.
import { computed, nextTick, ref } from "vue";
import type { ComponentPublicInstance } from "vue";
import { store } from "../../store.ts";
import { actions } from "../../bridge.ts";
import { api } from "../../api.ts";
import { activeInstance, clearLatencyStateForInstance } from "../../state.ts";
import type { FleetInstance, FleetTab } from "../../state.ts";
import { proxyPortLabel } from "../../format.ts";
import { localizedMessage, statusText } from "../../messages.ts";
import { refreshInstancesList } from "./instance-refresh.ts";
import OverviewTab from "./OverviewTab.vue";
import ProxiesTab from "./ProxiesTab.vue";
import LogsTab from "./LogsTab.vue";

const selected = computed(() => activeInstance(store));

// Mirrors renderPanels()'s `el.detailPanel.classList.toggle("hidden", away ||
// state.creating || !selected)` (pre-Vue app.ts), where `away` is
// `state.view === "profiles" || state.view === "dashboard"` (pre-Vue app.ts).
const visible = computed(() => store.view === "instances" && !store.creating && selected.value !== null);

const metaText = computed(() => {
  const instance = selected.value;
  if (!instance) return "已停止";
  return instance.lastError
    ? localizedMessage(instance.lastError)
    : `${statusText(instance.status)} · ${instance.id}`;
});

const showPendingRestart = computed(
  () => Boolean(selected.value?.pendingRestart === true && selected.value?.status === "running"),
);

// Per-component in-flight tracking for the four action buttons below, so a
// double-click can't fire two POSTs/DELETEs for the same action before the
// first one's response (and the follow-up refreshInstancesList()) lands.
// This is local UI state, not FleetState -- nothing outside this component
// needs to know a button is mid-click.
const pendingActions = ref<Set<"start" | "stop" | "restart" | "delete">>(new Set());

const startDisabled = computed(() => {
  const instance = selected.value;
  return (
    store.bulkRunning ||
    !instance ||
    instance.status === "running" ||
    instance.status === "starting" ||
    pendingActions.value.has("start")
  );
});
const stopDisabled = computed(
  () => store.bulkRunning || !selected.value || selected.value.status !== "running" || pendingActions.value.has("stop"),
);
const restartDisabled = computed(() => store.bulkRunning || pendingActions.value.has("restart"));
const deleteDisabled = computed(() => store.bulkRunning || pendingActions.value.has("delete"));
const cloneDisabled = computed(() => store.bulkRunning || store.cloneRunning || !selected.value);

interface TabDef {
  id: FleetTab;
  label: string;
}

const tabs: TabDef[] = [
  { id: "overview", label: "概览" },
  { id: "proxies", label: "节点" },
  { id: "logs", label: "日志" },
];

const tabButtonRefs = ref<Partial<Record<FleetTab, HTMLButtonElement>>>({});

function setTabButtonRef(id: FleetTab, el: Element | ComponentPublicInstance | null): void {
  tabButtonRefs.value[id] = (el as HTMLButtonElement | null) ?? undefined;
}

function setActiveTab(id: FleetTab): void {
  store.activeTab = id;
}

// Mirrors bindEvents()'s el.tabList keydown handler (pre-Vue app.ts):
// ArrowLeft/ArrowRight cycles focus (and selection) between tabs.
function onTabListKeydown(event: KeyboardEvent): void {
  if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
  const currentIndex = tabs.findIndex((tab) => tab.id === store.activeTab);
  if (currentIndex === -1) return;
  event.preventDefault();
  const delta = event.key === "ArrowRight" ? 1 : -1;
  // Modulo-wrapped into [0, tabs.length), and `tabs` is the static
  // (non-empty) tab list above, so this index always exists.
  const next = tabs[(currentIndex + delta + tabs.length) % tabs.length]!;
  store.activeTab = next.id;
  void nextTick(() => tabButtonRefs.value[next.id]?.focus());
}

// Mirrors runAction() (pre-Vue app.ts), reused by the start/stop/restart
// buttons below. The original called the module-wide refresh() (system +
// profiles + instances); this narrows to just the instances list -- see
// instance-refresh.ts for why that is enough here.
async function runInstanceAction(action: "start" | "stop" | "restart", success: string): Promise<void> {
  const instance = selected.value;
  if (!instance || pendingActions.value.has(action)) return;
  pendingActions.value.add(action);
  try {
    await api(`/api/instances/${instance.id}/${action}`, { method: "POST" });
    actions.showMessage(success);
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    actions.showMessage(message, "error");
  } finally {
    pendingActions.value.delete(action);
    await refreshInstancesList(store);
  }
}

// Mirrors clearActiveDetailCache() in the pre-Vue app.ts, minus the DOM-cache
// fields (`el.logs.dataset.instanceId`, `el.proxiesList.innerHTML`) that
// existed only to make the old innerHTML repaint behave -- ProxiesTab.vue and
// LogsTab.vue pick up an activeId change through their own reactive watchers.
//
// proxy-groups.ts does keep a `lastProxyGroupsSnapshot` again, but it is not a
// DOM cache and is deliberately NOT reset from here: it self-invalidates by
// checking `store.proxyGroups.length` before short-circuiting, precisely so
// that every caller that blanks store.proxyGroups (this function included)
// stays correct without having to know about it.
function resetActiveDetailState(): void {
  store.editInstanceId = "";
  store.editDirty = false;
  store.editVersion = 0;
  store.proxyGroups = [];
  store.proxyApply = false;
  store.latencyBatchRunning = false;
  store.latencyBatchToken += 1;
}

// Mirrors the #cloneBtn click handler (pre-Vue app.ts). `confirmDiscardChanges`
// itself is not imported (it is app.ts-private and also weighs
// profileFormDirty/configEditorDirty, which cannot be true while this panel
// is visible -- that only happens in the profiles view); the equivalent
// check here is scoped to the one dirty flag that is actually reachable from
// here, `store.editDirty`.
async function cloneInstance(): Promise<void> {
  const instance = selected.value;
  if (!instance || store.cloneRunning) return;
  if (store.editDirty && !window.confirm("有未保存的修改。确定放弃并克隆并切换到新实例吗？")) return;
  try {
    store.cloneRunning = true;
    const created = await api<FleetInstance>(`/api/instances/${instance.id}/clone`, {
      method: "POST",
      body: JSON.stringify({}),
    });
    store.activeId = created.id;
    localStorage.setItem("activeInstance", created.id);
    store.creating = false;
    clearLatencyStateForInstance(store, instance.id);
    resetActiveDetailState();
    actions.showMessage(`已克隆 ${instance.name}。`);
    await refreshInstancesList(store);
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    actions.showMessage(message, "error");
  } finally {
    store.cloneRunning = false;
  }
}

// Mirrors the #deleteBtn click handler (pre-Vue app.ts). The original's
// warning also folded in profileFormDirty/configEditorDirty via
// hasUnsavedChanges(); same scoping note as cloneInstance() above applies.
async function deleteInstance(): Promise<void> {
  const instance = selected.value;
  if (!instance || pendingActions.value.has("delete")) return;
  const dirtyWarning = store.editDirty ? " 未保存的修改也会丢失。" : "";
  if (!window.confirm(`确定删除 ${instance.name}？${dirtyWarning}`)) return;
  // Captured before the await: if the user selects a different instance while
  // the DELETE is in flight, store.activeId no longer matches by the time we
  // come back, and clearing the selection / resetting the detail panel here
  // would wipe whatever the user just switched to.
  const deletedId = instance.id;
  pendingActions.value.add("delete");
  try {
    await api(`/api/instances/${deletedId}`, { method: "DELETE" });
    // The deleted instance is gone either way, so its latency results are
    // permanently stale -- prune them regardless of what's currently selected.
    clearLatencyStateForInstance(store, deletedId);
    if (store.activeId === deletedId) {
      store.activeId = "";
      resetActiveDetailState();
    }
    actions.showMessage("实例已删除。");
    await refreshInstancesList(store);
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    actions.showMessage(message, "error");
  } finally {
    pendingActions.value.delete("delete");
  }
}
</script>

<template>
  <section id="detailPanel" class="detail" :class="{ hidden: !visible }">
    <div class="detail-head">
      <div>
        <h2 id="detailName">{{ selected?.name || "实例" }}</h2>
        <p id="detailMeta">{{ metaText }}</p>
        <span v-if="showPendingRestart" id="pendingRestartHint" class="pending-restart-chip">配置已修改，重启后生效</span>
      </div>
      <div class="actions">
        <button
          id="startBtn"
          class="primary"
          type="button"
          :disabled="startDisabled"
          @click="runInstanceAction('start', '已请求启动。')"
        >启动</button>
        <button
          id="stopBtn"
          type="button"
          :disabled="stopDisabled"
          @click="runInstanceAction('stop', '已请求停止。')"
        >停止</button>
        <button id="cloneBtn" type="button" :disabled="cloneDisabled" @click="cloneInstance">克隆</button>
        <button
          id="restartBtn"
          type="button"
          :disabled="restartDisabled"
          @click="runInstanceAction('restart', '已请求重启。')"
        >重启</button>
        <button id="deleteBtn" class="danger" type="button" :disabled="deleteDisabled" @click="deleteInstance">删除</button>
      </div>
    </div>

    <div class="metrics">
      <div><span>状态</span><strong id="metricStatus">{{ selected ? statusText(selected.status) : "待加载" }}</strong></div>
      <div><span>PID</span><strong id="metricPid">{{ selected?.pid || "无" }}</strong></div>
      <div><span>混合端口</span><strong id="metricMixed">{{ selected ? proxyPortLabel(selected.mixedPort) : 0 }}</strong></div>
      <div><span>控制端口</span><strong id="metricController">{{ selected?.controllerPort ?? 0 }}</strong></div>
    </div>

    <div id="tabList" class="tabs" role="tablist" aria-label="实例详情" @keydown="onTabListKeydown">
      <button
        v-for="tab in tabs"
        :key="tab.id"
        :ref="(el) => setTabButtonRef(tab.id, el)"
        :id="`tab-btn-${tab.id}`"
        class="tab"
        :class="{ active: store.activeTab === tab.id }"
        :data-tab="tab.id"
        type="button"
        role="tab"
        :aria-selected="store.activeTab === tab.id ? 'true' : 'false'"
        :aria-controls="`tab-${tab.id}`"
        :tabindex="store.activeTab === tab.id ? 0 : -1"
        @click="setActiveTab(tab.id)"
      >{{ tab.label }}</button>
    </div>

    <div id="tab-overview" class="tab-panel" role="tabpanel" aria-labelledby="tab-btn-overview" tabindex="-1" v-show="store.activeTab === 'overview'">
      <OverviewTab />
    </div>
    <div id="tab-proxies" class="tab-panel" role="tabpanel" aria-labelledby="tab-btn-proxies" tabindex="-1" v-show="store.activeTab === 'proxies'">
      <ProxiesTab />
    </div>
    <div id="tab-logs" class="tab-panel" role="tabpanel" aria-labelledby="tab-btn-logs" tabindex="-1" v-show="store.activeTab === 'logs'">
      <LogsTab />
    </div>
  </section>
</template>
