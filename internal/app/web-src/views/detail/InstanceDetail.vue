<script setup lang="ts">
// Vue replacement for the whole #detailPanel region (index.html:178-310) and
// the "selected" half of app.ts's renderPanels() (app.ts:412-471, everything
// from `el.detailName.textContent = selected.name;` down through
// `updateLatencyControls();`), plus setActiveTab() (app.ts:1172-1186) and the
// tabList keydown handler from bindEvents() (app.ts:1357-1372), plus the
// start/stop/restart/clone/delete button handlers from bindEvents()
// (app.ts:1423-1478).
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
// state.creating || !selected)` (app.ts:423), where `away` is
// `state.view === "profiles" || state.view === "dashboard"` (app.ts:413-418).
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

const startDisabled = computed(() => {
  const instance = selected.value;
  return store.bulkRunning || !instance || instance.status === "running" || instance.status === "starting";
});
const stopDisabled = computed(() => store.bulkRunning || !selected.value || selected.value.status !== "running");
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

// Mirrors bindEvents()'s el.tabList keydown handler (app.ts:1357-1372):
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

// Mirrors runAction() (app.ts:1141-1151), reused by the start/stop/restart
// buttons below. The original called the module-wide refresh() (system +
// profiles + instances); this narrows to just the instances list -- see
// instance-refresh.ts for why that is enough here.
async function runInstanceAction(action: "start" | "stop" | "restart", success: string): Promise<void> {
  const instance = selected.value;
  if (!instance) return;
  try {
    await api(`/api/instances/${instance.id}/${action}`, { method: "POST" });
    actions.showMessage(success);
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    actions.showMessage(message, "error");
  } finally {
    await refreshInstancesList(store);
  }
}

// Mirrors clearActiveDetailCache() (app.ts:805-816), minus the DOM-cache
// fields (`el.logs.dataset.instanceId`, `el.proxiesList.innerHTML`,
// `lastProxyGroupsSnapshot`) that existed only to make the old innerHTML
// repaint behave -- ProxiesTab.vue/LogsTab.vue pick up an activeId change
// through their own reactive watchers instead, so there is nothing left to
// cache here.
function resetActiveDetailState(): void {
  store.editInstanceId = "";
  store.editDirty = false;
  store.editVersion = 0;
  store.proxyGroups = [];
  store.proxyApply = false;
  store.latencyBatchRunning = false;
  store.latencyBatchToken += 1;
}

// Mirrors the #cloneBtn click handler (app.ts:1436-1461). `confirmDiscardChanges`
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

// Mirrors the #deleteBtn click handler (app.ts:1463-1478). The original's
// warning also folded in profileFormDirty/configEditorDirty via
// hasUnsavedChanges(); same scoping note as cloneInstance() above applies.
async function deleteInstance(): Promise<void> {
  const instance = selected.value;
  if (!instance) return;
  const dirtyWarning = store.editDirty ? " 未保存的修改也会丢失。" : "";
  if (!window.confirm(`确定删除 ${instance.name}？${dirtyWarning}`)) return;
  try {
    await api(`/api/instances/${instance.id}`, { method: "DELETE" });
    store.activeId = "";
    resetActiveDetailState();
    actions.showMessage("实例已删除。");
    await refreshInstancesList(store);
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    actions.showMessage(message, "error");
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
          :disabled="store.bulkRunning"
          @click="runInstanceAction('restart', '已请求重启。')"
        >重启</button>
        <button id="deleteBtn" class="danger" type="button" :disabled="store.bulkRunning" @click="deleteInstance">删除</button>
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
