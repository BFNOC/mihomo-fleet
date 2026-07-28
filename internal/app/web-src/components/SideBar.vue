<script setup lang="ts">
// Vue replacement for the inner content of <aside class="sidebar"> (index.html:28-50)
// and parts of five app.ts render functions:
//   - renderSystem() (app.ts:419-432) -- only the #systemWarning half
//     (app.ts:427-431). The #systemLine half is the topbar, migrated separately.
//   - renderViewNavigation() (app.ts:407-417) -- only the #showDashboardBtn
//     part. #manageProfilesBtn/#instanceSelectorWrap are the topbar's concern.
//   - renderList() (app.ts:493-529) -- the instance list, in full.
//   - renderPortMatrix() (app.ts:538-607) -- the port matrix, in full,
//     including the per-row copy-action buttons.
//   - updateBulkControls() (app.ts:632-639) -- only the #newBtn/#startAllBtn/
//     #stopAllBtn disabled states. #emptyCreate belongs to a non-shell panel
//     and is intentionally not reproduced here.
//
// Deliberately NOT reproduced: instanceListSnapshot()/capturedInstanceListFocusKey()/
// restoreInstanceListFocus() (app.ts:463-491), portMatrixSnapshot()/the inline
// focus capture/restorePortMatrixFocus()/portFocusInstanceId() (app.ts:531-536,
// 544-547, 609-620). Those exist only so a full `innerHTML = ""` repaint can
// fake focus/DOM-identity preservation; Vue's keyed v-for does that natively,
// so none of that machinery has a job left to do here.
import { computed } from "vue";
import { store } from "../store.ts";
import { actions, chrome } from "../bridge.ts";
import { activeInstance } from "../state.ts";
import type { FleetInstance } from "../state.ts";
import { proxyCopyActions, proxyCopyPlaceholders, proxyEndpointText, proxyPort, proxyPortLabel, selectionSummary } from "../format.ts";
import { statusClass, statusText } from "../messages.ts";

// Mirrors render()'s `selected` argument, computed the same way app.ts does
// it (active() = activeInstance(state), app.ts:139-141): the instance
// matching state.activeId, falling back to the first instance. Used for the
// "active" row highlight in both the instance list and the port matrix.
const selectedId = computed(() => activeInstance(store)?.id ?? "");

// Mirrors renderViewNavigation()'s #showDashboardBtn half (app.ts:413-415).
const onDashboard = computed(() => store.view === "dashboard");

// Mirrors renderSystem()'s #systemWarning half (app.ts:426-431). Empty when
// there is nothing to warn about (system not loaded yet, or mihomo found),
// which the template turns into the `.hidden` toggle below.
const systemWarningText = computed(() => {
  const system = store.system;
  if (!system || system.mihomoFound) return "";
  return "未在 Mihomo Fleet 同目录或 PATH 中找到 mihomo。你仍然可以创建实例，但启动需要同目录二进制文件，或通过 -mihomo 参数指定路径。";
});

// Mirrors updateBulkControls()'s canStart/canStop (app.ts:633-634).
const canStart = computed(() => store.instances.some((item) => item.status !== "running" && item.status !== "starting"));
const canStop = computed(() => store.instances.some((item) => item.status === "running"));

// Mirrors renderPortMatrix()'s #portMatrixCount text (app.ts:539).
const portMatrixCountText = computed(() => `${store.instances.length} 个出口`);

// Mirrors renderList()'s inline profile/selection text (app.ts:504-506).
function profileLabel(item: FleetInstance): string {
  return item.profileName || item.profileId || "未选择配置档";
}

function selectionSuffix(item: FleetInstance): string {
  const text = selectionSummary(item);
  return text !== "无" ? ` · ${text}` : "";
}

// Mirrors renderPortMatrix()'s per-row copy-tool actions (app.ts:585).
function copyActionsFor(item: FleetInstance) {
  return proxyPort(item.mixedPort) ? proxyCopyActions(item) : proxyCopyPlaceholders();
}

// Mirrors renderPortMatrix()'s aria-label suffix for an unavailable copy
// action (app.ts:593-594).
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
    <button id="stopAllBtn" type="button" :disabled="store.bulkRunning || !canStop" @click="actions.stopAll()">全部关闭</button>
  </div>

  <!-- Stays mounted and toggles `.hidden` (styles.css: `display: none !important`)
       rather than v-if, matching the same pattern index.html already used for
       this element and MessageBanner.vue uses for #message. -->
  <div id="systemWarning" class="warning" :class="{ hidden: !systemWarningText }">{{ systemWarningText }}</div>

  <div id="instanceList" class="instance-list">
    <button
      v-for="item in store.instances"
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
          <button
            v-for="action in copyActionsFor(item)"
            :key="action.id"
            type="button"
            :disabled="!action.value"
            :title="action.title"
            :aria-label="`${action.title}：${item.name}${copyUnavailableSuffix(action.value)}`"
            @click="actions.copyProxyValue(action.value, action.message)"
          >{{ action.label }}</button>
        </div>
      </li>
    </ul>
  </section>
</template>
