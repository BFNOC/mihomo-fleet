<script setup lang="ts">
// Metrics strip: fleet health orb, status chips, connection count, both traffic
// directions. Replaces the pre-Vue metricsStrip().
import { computed } from "vue";
import { store } from "../../store.ts";
import type { FleetInstance, FleetSystemStatus } from "../../state.ts";
import { formatRate, seriesPeak } from "../../traffic.ts";
import type { FormattedRate } from "../../traffic.ts";
import { shortMihomoVersion } from "../../format.ts";
import DashboardSparkline from "./DashboardSparkline.vue";
import {
  currentDown,
  currentUp,
  failedInstances,
  fleetConnectionsCount,
  fleetTrafficSeries,
  pendingInstances,
  runningInstances,
  sparkHeight,
  sparkWidth,
} from "./dashboard-data.ts";

interface DashboardChip {
  label: string;
  value: string;
  tone: string;
}

const systemStatus = computed<Partial<FleetSystemStatus>>(() => store.system || {});
const peakUp = computed<FormattedRate>(() => formatRate(seriesPeak(fleetTrafficSeries.value, "up")));
const peakDown = computed<FormattedRate>(() => formatRate(seriesPeak(fleetTrafficSeries.value, "down")));

const fleetTone = computed(() =>
  failedInstances.value.length ? "is-danger" : pendingInstances.value.length ? "is-warn" : runningInstances.value.length ? "is-running" : "is-idle",
);
const headlineText = computed(() => {
  if (!store.instances.length) return "尚无实例";
  if (failedInstances.value.length) return `${failedInstances.value.length} 个异常`;
  if (pendingInstances.value.length) return `${pendingInstances.value.length} 个待重启`;
  if (runningInstances.value.length) return `${runningInstances.value.length} / ${store.instances.length} 运行中`;
  return "全部已停止";
});

function namesList(list: FleetInstance[]): string {
  const shown = list.slice(0, 2).map((item) => item.name).join("、");
  return list.length > 2 ? `${shown} 等 ${list.length} 个` : shown;
}

const alertText = computed(() => {
  if (failedInstances.value.length) return `异常：${namesList(failedInstances.value)}`;
  if (pendingInstances.value.length) return `待重启：${namesList(pendingInstances.value)}`;
  return "";
});
const fleetSubtitle = computed(() => alertText.value || systemStatus.value.dataDir || "本地控制器");

const chips = computed<DashboardChip[]>(() => [
  {
    label: "mihomo",
    value: systemStatus.value.mihomoFound ? shortMihomoVersion(systemStatus.value.version) || "已就绪" : "未找到",
    tone: systemStatus.value.mihomoFound ? "is-ok" : "is-warn",
  },
  { label: "配置档", value: `${store.profiles.length}`, tone: store.profiles.length ? "is-ok" : "is-warn" },
  { label: "待重启", value: pendingInstances.value.length ? `${pendingInstances.value.length}` : "无", tone: pendingInstances.value.length ? "is-warn" : "is-ok" },
  { label: "异常", value: failedInstances.value.length ? `${failedInstances.value.length}` : "无", tone: failedInstances.value.length ? "is-danger" : "is-ok" },
]);
</script>

<template>
  <article class="dash-card dash-strip">
    <div class="dash-strip-fleet">
      <span class="dash-orb" :class="fleetTone" aria-hidden="true"></span>
      <div class="dash-strip-fleet-copy">
        <h3>{{ headlineText }}</h3>
        <p>{{ fleetSubtitle }}</p>
      </div>
      <ul class="dash-chips" role="list">
        <li v-for="chip in chips" :key="chip.label" :class="chip.tone">
          <span class="dash-check-dot" :class="chip.tone" aria-hidden="true"></span>
          <span class="dash-chip-label">{{ chip.label }}</span>
          <span class="dash-chip-value">{{ chip.value }}</span>
        </li>
      </ul>
    </div>
    <div class="dash-strip-activity">
      <p class="eyebrow">ACTIVITY</p>
      <p class="dash-figure dash-figure-lg"><span class="dash-figure-value">{{ fleetConnectionsCount }}</span></p>
      <p class="dash-figure-caption">活跃连接</p>
    </div>
    <div class="dash-strip-rates">
      <div class="dash-strip-rate" data-direction="down">
        <span class="dash-rate-icon" aria-hidden="true">↓</span>
        <p class="dash-figure"><span class="dash-figure-value">{{ currentDown.value }}</span><span class="dash-figure-unit">{{ currentDown.unit }}</span></p>
        <small>峰值 {{ peakDown.value }} {{ peakDown.unit }}</small>
      </div>
      <div class="dash-strip-rate" data-direction="up">
        <span class="dash-rate-icon" aria-hidden="true">↑</span>
        <p class="dash-figure"><span class="dash-figure-value">{{ currentUp.value }}</span><span class="dash-figure-unit">{{ currentUp.unit }}</span></p>
        <small>峰值 {{ peakUp.value }} {{ peakUp.unit }}</small>
      </div>
      <DashboardSparkline :series="fleetTrafficSeries" :width="sparkWidth" :height="sparkHeight" />
    </div>
  </article>
</template>
