<script setup lang="ts">
// Instances table. Replaces the pre-Vue instanceRows()/visibleInstances() plus
// the three delegated listeners bindEvents() attached to the panel
// (click/dblclick/keydown for rows).
import { computed } from "vue";
import { store } from "../../store.ts";
import type { FleetInstance } from "../../state.ts";
import { formatRate, seriesLatest, trafficWindowSeconds } from "../../traffic.ts";
import type { FormattedRate } from "../../traffic.ts";
import { localizedMessage, statusClass, statusText } from "../../messages.ts";
import DashboardSparkline from "./DashboardSparkline.vue";
import { useRowBudget } from "./use-row-budget.ts";
import {
  activeId,
  focusInstance,
  openInstanceWorkbench,
  rowSparkHeight,
  rowSparkWidth,
  sampleFor,
} from "./dashboard-data.ts";

const { budget, bodyEl, tableEl } = useRowBudget({ initial: 4 });

function instanceRates(id: string): { up: FormattedRate; down: FormattedRate } {
  const latest = seriesLatest(sampleFor(id).series);
  return { up: formatRate(latest ? latest.up : 0), down: formatRate(latest ? latest.down : 0) };
}

function isBad(item: FleetInstance): boolean {
  return Boolean(item.lastError || item.status === "error");
}

function instanceDotClass(item: FleetInstance): string {
  if (isBad(item)) return "is-danger";
  if (item.status === "running") return "is-ok";
  return item.pendingRestart ? "is-warn" : "is-idle";
}

function instanceRowClass(item: FleetInstance): Record<string, boolean> {
  return {
    "is-active": item.id === activeId.value,
    "is-danger": isBad(item),
    "is-warn": !isBad(item) && Boolean(item.pendingRestart),
  };
}

function instanceStatusSuffix(item: FleetInstance): string {
  return item.pendingRestart ? " · 待重启" : "";
}

// Crash-watchdog evidence (#2), kept compact for this row: just the restart
// count. The full reason (format.ts's restartEvidenceText) already has two
// homes that fit a longer string better -- instanceErrorSuffix below (once
// isBad, LastError itself already carries the crash reason) and
// OverviewTab.vue's dedicated row -- so repeating it here would only widen
// the row without adding anything the operator can't already see there.
function instanceRestartSuffix(item: FleetInstance): string {
  return item.restartCount ? ` · 已重启 ${item.restartCount} 次` : "";
}

// Backend errors arrive as raw English (e.g. "signal: terminated"); route
// through localizedMessage the same way InstanceDetail.vue's metaText does so
// the row and the detail view never disagree on wording. 48 chars is a row
// budget, not a real limit, so a cut mid-sentence must read as cut -- an
// unmarked slice looked like the whole (localized) message.
const errorSuffixMaxLength = 48;

function instanceErrorSuffix(item: FleetInstance): string {
  if (!isBad(item) || !item.lastError) return "";
  const text = localizedMessage(item.lastError);
  const shown = text.length > errorSuffixMaxLength ? `${text.slice(0, errorSuffixMaxLength)}…` : text;
  return ` · ${shown}`;
}

// Trimming the list must never hide the row the user is looking at, so the
// selected instance takes the last visible slot when it falls past the cut.
const visibleInstanceRows = computed<FleetInstance[]>(() => {
  const all = store.instances;
  if (all.length <= budget.value) return all;
  const shown = all.slice(0, budget.value);
  if (shown.some((item) => item.id === activeId.value)) return shown;
  const active = all.find((item) => item.id === activeId.value);
  return active ? [...shown.slice(0, -1), active] : shown;
});

const instancesNoteText = computed(() => {
  const total = store.instances.length;
  const shownCount = Math.min(total, budget.value);
  const hidden = total - shownCount;
  if (hidden > 0) return `显示 ${shownCount} / ${total} 台 · 其余在左侧列表`;
  return "点选查看右侧趋势；双击或点「打开工作台」进入该实例。";
});

function onRowKeydown(event: KeyboardEvent, id: string): void {
  if (event.key !== "Enter" && event.key !== " ") return;
  event.preventDefault();
  if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) openInstanceWorkbench(id);
  else focusInstance(id);
}
</script>

<template>
  <article class="dash-card dash-instances">
    <div class="dash-instances-head">
      <div>
        <h3>实例</h3>
        <p>{{ instancesNoteText }}</p>
      </div>
    </div>
    <div ref="bodyEl" class="dash-inst-body">
      <p v-if="!store.instances.length" class="dash-empty">还没有实例。先创建配置档，再新建实例。</p>
      <table v-else ref="tableEl" class="dash-table dash-instance-table">
        <thead>
          <tr>
            <th scope="col">实例</th>
            <th scope="col">连接</th>
            <th scope="col">上传</th>
            <th scope="col">下载</th>
            <th scope="col">近 {{ trafficWindowSeconds }} 秒</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="item in visibleInstanceRows"
            :key="item.id"
            :class="instanceRowClass(item)"
            tabindex="0"
            @click="focusInstance(item.id)"
            @dblclick="openInstanceWorkbench(item.id)"
            @keydown="onRowKeydown($event, item.id)"
          >
            <td class="dash-cell-name">
              <span class="dash-check-dot" :class="instanceDotClass(item)" aria-hidden="true"></span>
              <span>
                <strong>{{ item.name }}</strong>
                <small :class="statusClass(item.status)">{{ statusText(item.status) }}{{ instanceStatusSuffix(item) }}{{ instanceRestartSuffix(item) }}{{ instanceErrorSuffix(item) }}</small>
              </span>
            </td>
            <td class="num">{{ sampleFor(item.id).connections }}</td>
            <td class="num">{{ instanceRates(item.id).up.value }} {{ instanceRates(item.id).up.unit }}</td>
            <td class="num">{{ instanceRates(item.id).down.value }} {{ instanceRates(item.id).down.unit }}</td>
            <td class="dash-cell-spark">
              <DashboardSparkline :series="sampleFor(item.id).series" :width="rowSparkWidth" :height="rowSparkHeight" />
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </article>
</template>
