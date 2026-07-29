<script setup lang="ts">
// Fleet traffic trend card. Replaces the pre-Vue trendCard()/trendBody().
import { computed } from "vue";
import { seriesSpan, trafficWindowSeconds } from "../../traffic.ts";
import DashboardSparkline from "./DashboardSparkline.vue";
import { currentDown, currentUp, fleetTrafficSeries, sparkWidth, trendHeight } from "./dashboard-data.ts";

const fleetSpan = computed(() => seriesSpan(fleetTrafficSeries.value));
const fleetSampleCount = computed(() => fleetTrafficSeries.value.samples.length);

// The pre-Vue formatClock() was a private helper used only to label this axis;
// it touches no sampler state and is display-only, so it lives here rather than
// in traffic.ts.
function formatClock(ms: number): string {
  const value = Number(ms) || 0;
  if (!value) return "--:--:--";
  const date = new Date(value);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}
</script>

<template>
  <article class="dash-card dash-trend">
    <div class="dash-trend-head">
      <div>
        <p class="eyebrow">LIVE TRAFFIC</p>
        <h3>舰队流量</h3>
        <p class="dash-trend-note">全部运行中实例合计 · 近 {{ trafficWindowSeconds }} 秒内存采样</p>
      </div>
      <span class="dash-live">{{ fleetSampleCount ? "实时" : "采样中" }}</span>
    </div>
    <p class="dash-legend">
      <span class="dash-legend-item" data-direction="up">上传 {{ currentUp.value }} {{ currentUp.unit }}</span>
      <span class="dash-legend-item" data-direction="down">下载 {{ currentDown.value }} {{ currentDown.unit }}</span>
    </p>
    <div class="dash-trend-plot">
      <DashboardSparkline :series="fleetTrafficSeries" :width="sparkWidth" :height="trendHeight" />
      <p v-if="fleetSampleCount < 2" class="dash-trend-empty">等待采样填满近 {{ trafficWindowSeconds }} 秒窗口</p>
    </div>
    <div class="dash-trend-axis">
      <span>{{ fleetSpan ? formatClock(fleetSpan.from) : `近 ${trafficWindowSeconds} 秒` }}</span>
      <span>近 {{ trafficWindowSeconds }} 秒</span>
      <span>{{ fleetSpan ? formatClock(fleetSpan.to) : "现在" }}</span>
    </div>
  </article>
</template>
