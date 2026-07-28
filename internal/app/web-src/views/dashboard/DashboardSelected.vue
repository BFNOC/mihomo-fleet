<script setup lang="ts">
// Selected-instance card. Replaces the pre-Vue selectedDetail().
import { computed } from "vue";
import { formatRate, seriesLatest, trafficWindowSeconds } from "../../traffic.ts";
import type { FormattedRate } from "../../traffic.ts";
import { statusText } from "../../messages.ts";
import DashboardSparkline from "./DashboardSparkline.vue";
import { openInstanceWorkbench, sampleFor, selectedInstance, sparkHeight, sparkWidth } from "./dashboard-data.ts";

// sampleFor() falls back to a shared empty sample for an unknown id, so the
// "nothing selected" case needs no separate branch here.
const selectedSample = computed(() => sampleFor(selectedInstance.value?.id ?? ""));
const selectedLatest = computed(() => seriesLatest(selectedSample.value.series));
const selectedUp = computed<FormattedRate>(() => formatRate(selectedLatest.value ? selectedLatest.value.up : 0));
const selectedDown = computed<FormattedRate>(() => formatRate(selectedLatest.value ? selectedLatest.value.down : 0));
const selectedSampleCount = computed(() => selectedSample.value.series.samples.length);

const selectedMeta = computed(() => {
  const sel = selectedInstance.value;
  if (!sel) return "";
  return [
    statusText(sel.status),
    sel.mixedPort ? `混合 ${sel.mixedPort}` : "",
    sel.controllerPort ? `控制 ${sel.controllerPort}` : "",
    sel.pendingRestart ? "待重启" : "",
  ].filter(Boolean).join(" · ");
});

const selectedNote = computed(() =>
  selectedInstance.value?.status === "running" ? `单实例 · 近 ${trafficWindowSeconds} 秒内存采样` : "实例未运行，速率归零",
);
</script>

<template>
  <article class="dash-card dash-selected">
    <template v-if="!selectedInstance">
      <p class="eyebrow">INSTANCE</p>
      <h3>未选中实例</h3>
      <p class="dash-trend-note">在左侧列表点选实例，查看其近 {{ trafficWindowSeconds }} 秒流量。</p>
    </template>
    <template v-else>
      <div class="dash-selected-head">
        <div>
          <p class="eyebrow">INSTANCE</p>
          <h3>{{ selectedInstance.name }}</h3>
          <p class="dash-trend-note">{{ selectedMeta }}</p>
        </div>
        <div class="dash-selected-actions">
          <span class="dash-live">{{ selectedSampleCount ? "实时" : "采样中" }}</span>
          <button type="button" class="dash-open-btn" @click="openInstanceWorkbench(selectedInstance.id)">打开工作台</button>
        </div>
      </div>
      <ul class="dash-selected-metrics" role="list">
        <li><strong>{{ selectedSample.connections }}</strong><span>连接</span></li>
        <li><strong>{{ selectedUp.value }} <small>{{ selectedUp.unit }}</small></strong><span>↑ 当前</span></li>
        <li><strong>{{ selectedDown.value }} <small>{{ selectedDown.unit }}</small></strong><span>↓ 当前</span></li>
      </ul>
      <p class="dash-trend-note dash-selected-note">{{ selectedNote }}</p>
      <div class="dash-selected-spark">
        <DashboardSparkline :series="selectedSample.series" :width="sparkWidth" :height="sparkHeight" />
      </div>
    </template>
  </article>
</template>
