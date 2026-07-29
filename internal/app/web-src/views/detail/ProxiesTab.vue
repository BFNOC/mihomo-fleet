<script setup lang="ts">
// #tab-proxies. The group loading and view-model assembly live in
// proxy-groups.ts, the hover tooltip in use-proxy-tooltip.ts; this file wires
// the latency controller and renders.
//
// LATENCY CONTROLLER: reused verbatim via createLatencyController()
// (latency.ts). Its chip markup is NOT reused -- that helper built and mutated a
// raw <span> imperatively, which has no home in a Vue template. The chips below
// bind the same format.ts formatters it called internally, so the presentation
// logic is identical, just re-hosted as a computed.
import { computed, onMounted, ref } from "vue";
import { store } from "../../store.ts";
import { actions } from "../../bridge.ts";
import { createLatencyController } from "../../latency.ts";
import type { LatencyController } from "../../latency.ts";
import { activeInstance } from "../../state.ts";
import { currentLatencyTarget, normalizeStoredLatencyTimeout, normalizeStoredLatencyUrl } from "../../format.ts";
import { useTabPolling } from "./useTabPolling.ts";
import { useProxyTooltip } from "./use-proxy-tooltip.ts";
import {
  displayGroups,
  filterText,
  loadError,
  proxiesLoading,
  proxySourceText,
  refreshProxies,
  selectProxy,
} from "./proxy-groups.ts";

const selected = computed(() => activeInstance(store));
const isActiveTab = computed(() => store.activeTab === "proxies");

const { tooltipEl, tooltipVisible, tooltipText, tooltipLeft, tooltipTop, showTooltip, showTooltipFromPointer, hideTooltip } = useProxyTooltip();

const latencyUrlInput = ref<HTMLInputElement | null>(null);
const latencyTimeoutInput = ref<HTMLInputElement | null>(null);
// A ref (not a plain `let`) so the template's type-checking sees the
// post-onMounted() assignment -- a `let LatencyController | null` reassigned
// only inside a callback resolves to `never` in <script setup>'s generated
// template-render type, since the template macro snapshots each binding's type
// without following control flow across a closure boundary.
const latency = ref<LatencyController | null>(null);

onMounted(() => {
  latency.value = createLatencyController({
    state: store,
    el: { latencyUrl: latencyUrlInput.value!, latencyTimeout: latencyTimeoutInput.value! },
    getActive: () => activeInstance(store),
    showMessage: (text, kind) => actions.showMessage(text, kind === "error" ? "error" : "info"),
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

useTabPolling(isActiveTab, computed(() => selected.value?.id || ""), refreshProxies);
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
    <input id="proxyFilter" v-model="filterText" class="proxy-filter" placeholder="筛选节点" aria-label="筛选节点">
    <div id="proxiesList" class="proxy-list">
      <div v-if="loadError" class="message error">{{ loadError }}</div>
      <template v-else>
        <section v-for="entry in displayGroups" :key="entry.group.name" class="proxy-group">
          <div class="proxy-group-head">
            <strong>{{ entry.group.name }}</strong>
            <div class="proxy-group-meta">
              <span>{{ entry.group.now ? `当前 ${entry.group.now}` : `${entry.count} 个节点` }}</span>
              <span v-if="entry.pending" class="latency-chip running">切换中</span>
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
              :disabled="!entry.selectable || entry.pending"
              :aria-label="proxy.name"
              :aria-pressed="entry.selectable ? (entry.group.now === proxy.name ? 'true' : 'false') : undefined"
              @click="entry.selectable && !entry.pending && selectProxy(entry.group.name, proxy.name)"
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
        <div v-if="!store.proxyGroups.length && proxiesLoading" class="warning">正在加载节点组。</div>
        <div v-else-if="!store.proxyGroups.length" class="warning">没有可显示的节点组。使用 proxy-providers 的订阅需要启动实例后读取 mihomo 运行态节点。</div>
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
