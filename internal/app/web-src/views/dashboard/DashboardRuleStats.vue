<script setup lang="ts">
// The aggregated half of the connections card: which rules are carrying live
// connections, and which exits they resolve to. Rendered in place of the
// connection table when that card's 统计 toggle is on, rather than as a sixth
// dashboard card -- the viewport-fit media query targets `.dashboard > *`, so
// the view's top-level node count is a structural contract (DESIGN.md).
//
// Owns no data of its own: the rows come from dashboard-data.ts, the same
// snapshot the table renders, and all the grouping lives in the framework-free
// connection-stats.ts.
import { computed, ref } from "vue";
import { aggregateConnections, totalConnectionStats } from "../../connection-stats.ts";
import type { ConnectionStatDimension } from "../../connection-stats.ts";
import { formatBytes } from "../../format.ts";
import { formatRate } from "../../traffic.ts";
import { allConnectionRows, nowTick } from "./dashboard-data.ts";

// Cap on rendered groups. Same reasoning as the connection table's own cap:
// this is a "what is busy" view, and the rows are sorted busiest-first, so what
// is dropped is the idle tail.
const maxStatRows = 40;

const dimension = ref<ConnectionStatDimension>("rule");

const groups = computed(() => {
  // allConnectionRows is heartbeat-backed (dashboard-data.ts); nowTick is read
  // for the same reason the table reads it -- see the geoCache note there.
  void nowTick.value;
  return aggregateConnections(allConnectionRows.value, dimension.value);
});

const shownGroups = computed(() => groups.value.slice(0, maxStatRows));
const totals = computed(() => totalConnectionStats(groups.value));

const note = computed(() => {
  const { groups: count, connections } = totals.value;
  if (!connections) return "运行中的实例暂无活跃连接";
  const noun = dimension.value === "rule" ? "条规则" : "个出口";
  const capped = count > shownGroups.value.length ? ` · 显示前 ${shownGroups.value.length}` : "";
  return `${count} ${noun} · ${connections} 条连接${capped}`;
});
</script>

<template>
  <div class="dash-stats">
    <div class="dash-stats-modes" role="group" aria-label="统计维度">
      <button
        type="button"
        class="dash-stats-mode"
        :class="{ 'is-active': dimension === 'rule' }"
        :aria-pressed="dimension === 'rule'"
        @click="dimension = 'rule'"
      >按规则</button>
      <button
        type="button"
        class="dash-stats-mode"
        :class="{ 'is-active': dimension === 'node' }"
        :aria-pressed="dimension === 'node'"
        @click="dimension = 'node'"
      >按出口</button>
      <p class="dash-trend-note">{{ note }}</p>
    </div>
    <div v-if="shownGroups.length" class="dash-conn-body">
      <table class="dash-table dash-stats-table">
        <thead>
          <tr>
            <th scope="col">{{ dimension === "rule" ? "规则" : "出口" }}</th>
            <th scope="col" class="num">连接</th>
            <th scope="col" class="num">上传</th>
            <th scope="col" class="num">下载</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="group in shownGroups" :key="group.key">
            <td class="dash-stats-label">
              <strong>{{ group.label }}</strong>
              <small v-if="group.detail">{{ group.detail }}</small>
            </td>
            <td class="num">{{ group.connections }}</td>
            <td class="num">
              {{ formatRate(group.up).value }} {{ formatRate(group.up).unit }}<small>{{ formatBytes(group.upload) }}</small>
            </td>
            <td class="num">
              {{ formatRate(group.down).value }} {{ formatRate(group.down).unit }}<small>{{ formatBytes(group.download) }}</small>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <p v-else class="dash-empty">暂无活跃连接</p>
  </div>
</template>
