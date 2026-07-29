<script setup lang="ts">
// #dashboardPanel's content. main.ts mounts this view into that host the same
// way TopBar.vue/SideBar.vue mount into .topbar/.sidebar.
//
// This file is layout only: the four top-level nodes below and the summary
// line. Each card is its own component, and everything they read in common
// (sampler-backed computeds, chart geometry, row actions) lives in
// dashboard-data.ts -- see that file for why the heartbeat exists.
//
// STRUCTURAL CONTRACT: the host keeps its own wrapper element and class list
// (index.html's `class="dashboard hidden"`, toggled by main.ts's watchEffect);
// this template supplies only the inner fragment. styles.css targets
// `.dashboard > *` directly (the viewport-fit media query), so the four
// top-level nodes below must stay direct roots of the fragment -- no wrapping
// element of our own. Each card component has a single root element and so
// renders exactly the <article> it replaces, adding no node of its own.
import { computed } from "vue";
import { store } from "../../store.ts";
import DashboardStrip from "./DashboardStrip.vue";
import DashboardInstances from "./DashboardInstances.vue";
import DashboardTrend from "./DashboardTrend.vue";
import DashboardSelected from "./DashboardSelected.vue";
import DashboardConnections from "./DashboardConnections.vue";
import {
  failedInstances,
  fleetConnectionsCount,
  pendingInstances,
  runningInstances,
  useDashboardHeartbeat,
} from "./dashboard-data.ts";

// Started once here rather than per card: the cards mount and unmount with this
// view, so one watcher owned by the parent needs no refcounting.
useDashboardHeartbeat();

// NOTE (gap): the pre-Vue caption also appended " · N 台未取到" -- the count of
// running instances whose sampler read `reachable === false`. That flag lives
// only on dashboard.ts's private `samplers` entries; instanceConnections() folds
// it into "0 connections", which is indistinguishable from "reachable with zero
// connections". Restoring it needs a new dashboard.ts export, e.g.
// `instanceReachable(id: string): boolean` mirroring instanceConnections() --
// there was one, but nothing ever called it, so it was removed as dead code;
// re-add it if this caption gets restored.
const summaryText = computed(() => {
  const parts = [
    `${store.instances.length} 个实例`,
    `${runningInstances.value.length} 运行中`,
    pendingInstances.value.length ? `${pendingInstances.value.length} 待重启` : "",
    failedInstances.value.length ? `${failedInstances.value.length} 异常` : "",
    `${fleetConnectionsCount.value} 连接`,
  ].filter(Boolean);
  return parts.join(" · ") || "尚无实例";
});
</script>

<template>
  <div class="dashboard-head">
    <h2>舰队状态</h2>
    <p>{{ summaryText }}</p>
  </div>

  <div class="dashboard-grid dashboard-grid-strip">
    <DashboardStrip />
  </div>

  <div class="dashboard-grid dashboard-grid-mid">
    <DashboardInstances />
    <DashboardTrend />
    <DashboardSelected />
  </div>

  <div class="dashboard-grid dashboard-grid-conns">
    <DashboardConnections />
  </div>
</template>
