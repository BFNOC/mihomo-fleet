<script setup lang="ts">
// Vue replacement for the inner content of <aside class="sidebar"> (index.html:28-50)
// and parts of five app.ts render functions:
//   - renderSystem() (pre-Vue app.ts) -- only the #systemWarning half
//     (pre-Vue app.ts). The #systemLine half is the topbar, migrated separately.
//   - renderViewNavigation() (pre-Vue app.ts) -- only the #showDashboardBtn
//     part. #manageProfilesBtn/#instanceSelectorWrap are the topbar's concern.
//   - renderList() (pre-Vue app.ts) -- the instance list, in full.
//   - renderPortMatrix() (pre-Vue app.ts) -- the port matrix, in full,
//     including the per-row copy-action buttons.
//   - updateBulkControls() (pre-Vue app.ts) -- only the #newBtn/#startAllBtn/
//     #stopAllBtn disabled states. #emptyCreate belongs to a non-shell panel
//     and is intentionally not reproduced here.
//
// Deliberately NOT reproduced: instanceListSnapshot()/capturedInstanceListFocusKey()/
// restoreInstanceListFocus() (pre-Vue app.ts), portMatrixSnapshot()/the inline
// focus capture/restorePortMatrixFocus()/portFocusInstanceId() (pre-Vue app.ts,
// 544-547, 609-620). Those exist only so a full `innerHTML = ""` repaint can
// fake focus/DOM-identity preservation; Vue's keyed v-for does that natively,
// so none of that machinery has a job left to do here.
import { computed, reactive, ref } from "vue";
import { store } from "../store.ts";
import { actions, chrome } from "../bridge.ts";
import { activeInstance } from "../state.ts";
import type { FleetInstance } from "../state.ts";
import { proxyCopyActionGroups, proxyEndpointText, proxyPort, proxyPortLabel, selectionSummary } from "../format.ts";
import { statusClass, statusText } from "../messages.ts";

// Mirrors render()'s `selected` argument, computed the same way app.ts does
// it (active() = activeInstance(state), pre-Vue app.ts): the instance
// matching state.activeId, falling back to the first instance. Used for the
// "active" row highlight in both the instance list and the port matrix.
const selectedId = computed(() => activeInstance(store)?.id ?? "");

// Mirrors renderViewNavigation()'s #showDashboardBtn half (pre-Vue app.ts).
const onDashboard = computed(() => store.view === "dashboard");

// Mirrors renderSystem()'s #systemWarning half (pre-Vue app.ts). Empty when
// there is nothing to warn about (system not loaded yet, or mihomo found),
// which the template turns into the `.hidden` toggle below.
const systemWarningText = computed(() => {
  const system = store.system;
  if (!system || system.mihomoFound) return "";
  return "未在 Mihomo Fleet 同目录或 PATH 中找到 mihomo。你仍然可以创建实例，但启动需要同目录二进制文件，或通过 -mihomo 参数指定路径。";
});

// Mirrors updateBulkControls()'s canStart/canStop (pre-Vue app.ts).
const canStart = computed(() => store.instances.some((item) => item.status !== "running" && item.status !== "starting"));
const canStop = computed(() => store.instances.some((item) => item.status === "running"));
const runningInstanceCount = computed(() => store.instances.filter((item) => item.status === "running").length);

// 全部关闭 stops every running instance in one request, with no per-instance
// confirmation of its own, sitting immediately beside 全部启动 -- a stray
// click takes down the whole fleet. Names the blast radius before acting,
// matching the confirm style already used for profile deletion
// (views/profiles/profile-operations.ts's deleteProfile: a plain
// window.confirm naming what is about to be lost).
function confirmStopAll(): void {
  if (!window.confirm(`确定停止全部 ${runningInstanceCount.value} 个运行中的实例？`)) return;
  actions.stopAll();
}

// Mirrors renderPortMatrix()'s #portMatrixCount text (pre-Vue app.ts).
const portMatrixCountText = computed(() => `${store.instances.length} 个出口`);

// Mirrors renderList()'s inline profile/selection text (pre-Vue app.ts).
function profileLabel(item: FleetInstance): string {
  return item.profileName || item.profileId || "未选择配置档";
}

function selectionSuffix(item: FleetInstance): string {
  const text = selectionSummary(item);
  return text !== "无" ? ` · ${text}` : "";
}

// Filters #instanceList only -- the port matrix below is a fleet-wide
// reference table (its own count header says so), not a second copy of the
// same list, so narrowing it along with the search box would hide ports the
// user may still want to see.
const instanceSearch = ref("");

// Everything a row actually prints (name, status, port, profile) is fair
// search game -- a fleet grows past "scan it visually" quickly, and a user
// who remembers "the stopped one" or "port 7890" shouldn't have to remember
// the name too.
function instanceHaystack(item: FleetInstance): string {
  return [item.name, profileLabel(item), statusText(item.status), proxyPortLabel(item.mixedPort)]
    .filter(Boolean)
    .join(" ")
    .toLowerCase();
}

const filteredInstances = computed(() => {
  const needle = instanceSearch.value.trim().toLowerCase();
  if (!needle) return store.instances;
  return store.instances.filter((item) => instanceHaystack(item).includes(needle));
});

// Which bind address each multi-bind instance's copy row targets, keyed by
// instance id. One row of buttons plus a <select> keeps the sidebar the same
// height however many addresses are bound; unset falls back to the first.
const copyHost = reactive<Record<string, string>>({});

function activeCopyGroup(item: FleetInstance) {
  const groups = proxyCopyActionGroups(item);
  return groups.find((group) => group.host === copyHost[item.id]) ?? groups[0]!;
}

// Mirrors renderPortMatrix()'s aria-label suffix for an unavailable copy
// action (pre-Vue app.ts).
function copyUnavailableSuffix(value: string): string {
  return value ? "" : "（端口未分配，无法复制）";
}
</script>

<template>
  <button
    id="showDashboardBtn"
    type="button"
    class="side-dashboard-btn"
    :class="{ active: onDashboard }"
    :aria-current="onDashboard ? 'page' : 'false'"
    :disabled="chrome.profileBusy"
    @click="onDashboard ? actions.closeDashboard() : actions.openDashboard()"
  >
    <span class="side-dashboard-label">总览</span>
    <span class="side-dashboard-hint">舰队状态与流量</span>
  </button>

  <div class="side-head">
    <h2>实例列表</h2>
    <button id="newBtn" class="primary" type="button" :disabled="store.bulkRunning" @click="actions.showCreate()">新建</button>
  </div>

  <div class="side-actions">
    <button id="startAllBtn" class="primary" type="button" :disabled="store.bulkRunning || !canStart" @click="actions.startAll()">全部启动</button>
    <button id="stopAllBtn" type="button" :disabled="store.bulkRunning || !canStop" @click="confirmStopAll()">全部关闭</button>
  </div>

  <!-- Stays mounted and toggles `.hidden` (styles.css: `display: none !important`)
       rather than v-if, matching the same pattern index.html already used for
       this element. -->
  <div id="systemWarning" class="warning" :class="{ hidden: !systemWarningText }">{{ systemWarningText }}</div>

  <!-- Bare <label>, no caption: matches DashboardConnections.vue's search box
       (aria-label + placeholder carry the meaning there too). The wrapper
       itself is not decorative -- label's block/grid display (the bare
       `label, .stacked` rule in workbench.css) is what makes a bare <input>,
       otherwise inline-block, span the sidebar's full width like every other
       control here, with no new CSS rule needed. -->
  <label v-if="store.instances.length" class="instance-search">
    <input
      id="instanceSearch"
      v-model="instanceSearch"
      type="search"
      autocomplete="off"
      spellcheck="false"
      placeholder="搜索实例名称、状态、端口或配置档"
      aria-label="搜索实例"
    >
  </label>

  <div id="instanceList" class="instance-list">
    <button
      v-for="item in filteredInstances"
      :key="item.id"
      type="button"
      class="instance-row"
      :class="{ active: item.id === selectedId }"
      @click="actions.selectInstance(item.id)"
    >
      <div class="row-main">
        <span class="row-name">{{ item.name }}</span>
        <span class="status" :class="statusClass(item.status)">{{ statusText(item.status) }}</span>
        <span v-if="item.pendingRestart === true && item.status === 'running'" class="pending-restart-chip">配置已修改，重启后生效</span>
      </div>
      <div class="row-meta">混合端口 {{ proxyPortLabel(item.mixedPort) }} · {{ profileLabel(item) }}{{ selectionSuffix(item) }}</div>
    </button>
    <!-- instanceSearch.trim() in the guard (not just !filteredInstances.length)
         keeps this from firing when the fleet itself is empty -- that case has
         its own empty state elsewhere (the non-shell #emptyCreate panel; see
         this file's header comment), and "没有匹配的实例" would misdescribe it
         as a search with no results. -->
    <p v-if="!filteredInstances.length && instanceSearch.trim()" class="port-empty">没有匹配的实例</p>
  </div>

  <section class="port-matrix" aria-labelledby="portMatrixTitle">
    <div class="port-matrix-head">
      <h3 id="portMatrixTitle">端口矩阵</h3>
      <span id="portMatrixCount" aria-live="polite" aria-atomic="true">{{ portMatrixCountText }}</span>
    </div>
    <ul id="portMatrixList" class="port-matrix-list" role="list">
      <li v-if="!store.instances.length" class="port-empty">暂无端口</li>
      <li
        v-for="item in store.instances"
        :key="item.id"
        class="port-row"
        :class="{ active: item.id === selectedId }"
      >
        <button
          type="button"
          class="port-row-select"
          :aria-label="`${item.name}，${statusText(item.status)}，${proxyEndpointText(item)}`"
          :aria-current="item.id === selectedId ? 'true' : undefined"
          @click="actions.selectInstance(item.id)"
        >
          <span class="port-row-top">
            <span class="port-row-name">{{ item.name }}</span>
            <span class="status" :class="statusClass(item.status)">{{ statusText(item.status) }}</span>
          </span>
          <span class="port-address">{{ proxyEndpointText(item) }}</span>
        </button>
        <div class="copy-tools">
          <select
            v-if="proxyCopyActionGroups(item).length > 1"
            v-model="copyHost[item.id]"
            class="copy-host"
            :aria-label="`选择要复制的 ${item.name} 绑定地址`"
          >
            <option v-for="group in proxyCopyActionGroups(item)" :key="group.host" :value="group.host">{{ group.host }}</option>
          </select>
          <button
            v-for="action in activeCopyGroup(item).actions"
            :key="action.id"
            type="button"
            :disabled="!action.value"
            :title="action.title"
            :aria-label="`${action.title}：${item.name}${activeCopyGroup(item).host ? `（${activeCopyGroup(item).host}）` : ''}${copyUnavailableSuffix(action.value)}`"
            @click="actions.copyProxyValue(action.value, action.message)"
          >{{ action.label }}</button>
        </div>
      </li>
    </ul>
  </section>
</template>
