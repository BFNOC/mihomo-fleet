<script setup lang="ts">
// Vue replacement for #dashboardPanel's content (index.html:23) and
// dashboard.ts's DOM-rendering half: renderDashboard()/paintDashboard() and
// everything they called that built HTML strings (metricsStrip, trendCard/
// trendBody, selectedDetail, instanceRows/visibleInstances, connectionsCard/
// connectionRow/connectionTarget/geoCell, dualSparkline/sparkHead -- the
// sparkline half moved to DashboardSparkline.vue instead of being duplicated
// four times inline) plus the row-fit measurement (fitTables/
// viewportFitActive/rowBudgets) and the three delegated dashboard listeners
// app.ts's bindEvents() attached to el.dashboardPanel (click/dblclick/keydown
// for instance rows, input for the connection search box).
//
// Deliberately NOT ported: captureLiveState/restoreLiveState (the search
// box's caret save/restore) and bindComposition (the IME composition guard).
// Both exist only so a full `container.innerHTML = ...` repaint can fake
// preserving focus/caret/DOM identity across itself. Vue's keyed v-for keeps
// real DOM node identity for free, and the search box below is a plain
// v-model input, which handles IME composition correctly on its own -- there
// is nothing left for either helper to do.
//
// This view is mounted into #dashboardPanel by main.ts (mountShell pattern --
// see TopBar.vue/SideBar.vue), the same way they mount into .topbar/.sidebar.
// The host keeps its own wrapper element and class list (index.html's
// `class="dashboard hidden"`, toggled by app.ts's renderPanels()); this
// template supplies only the inner fragment. styles.css targets
// `.dashboard > *` directly (the viewport-fit media query, see styles.css
// ~2307), so this template's four top-level nodes below (.dashboard-head,
// .dashboard-grid-strip, .dashboard-grid-mid, .dashboard-grid-conns) must
// stay direct roots of the fragment -- no wrapping element of our own.
import { computed, nextTick, onMounted, onUnmounted, ref, watchEffect } from "vue";
import type { Ref } from "vue";
import { store } from "../../store.ts";
import { actions } from "../../bridge.ts";
import { activeInstance } from "../../state.ts";
import type { FleetInstance, FleetSystemStatus } from "../../state.ts";
import {
  fleetConnections,
  fleetConnectionRows,
  fleetSeries,
  instanceConnections,
  instanceSeries,
  requestGeo,
  resolveGeo,
} from "../../dashboard.ts";
import type { FleetConnectionRow } from "../../dashboard.ts";
import {
  countryFlag,
  createSeries,
  filterConnections,
  formatDuration,
  formatRate,
  localAddressLabel,
  seriesLatest,
  seriesPeak,
  seriesSpan,
  sortConnections,
  trafficWindowSeconds,
} from "../../traffic.ts";
import type { FormattedRate, TrafficSeries } from "../../traffic.ts";
import { formatBytes, shortMihomoVersion } from "../../format.ts";
import { statusClass, statusText } from "../../messages.ts";
import DashboardSparkline from "./DashboardSparkline.vue";

// ---------------------------------------------------------------------------
// Reactive invalidation for dashboard.ts's sampler state
// ---------------------------------------------------------------------------
// dashboard.ts's `samplers` Map (and the connection-rate/geo state derived
// from it) is a plain module-scope value, mutated outside Vue's reactive
// graph by app.ts's fast poll (sampleFleetTraffic -> sampleFleet, ~1.8s
// cadence, see dashboard.ts's own top-of-file comment). A computed() that
// calls instanceSeries()/fleetSeries()/fleetConnectionRows() would otherwise
// compute once and never invalidate -- the chart would freeze with no error.
//
// `heartbeat` is the explicit trigger every such computed reads first. It is
// a self-contained interval matching the fast-poll cadence, not tied to
// app.ts actually completing a sample -- deliberately so this view needs no
// wiring change to work at all. See this agent's report for the tighter
// alternative (a counter app.ts bumps after each real sample) that a bridge.ts
// change could swap in later; the only edit that alternative needs here is
// replacing `heartbeat.value` with that counter in the four computeds below.
const heartbeat = ref(0);
let heartbeatTimer: ReturnType<typeof setInterval> | null = null;
const heartbeatIntervalMs = 1800;

// How many rows each table may show. Real values are measured against the
// viewport after mount (see measureRows() far below); these are the pre-measure
// defaults. Declared up here, rather than next to that measurement code, because
// computeds AND the eager watchEffect that drives the GEO lookups read them --
// `const` is in its temporal dead zone until its own line runs, so a setup-time
// read of a ref declared later throws ReferenceError rather than seeing the
// default.
const rowBudgetConnections = ref(6);
const rowBudgetInstances = ref(4);

// ---------------------------------------------------------------------------
// Fleet/instance identity
// ---------------------------------------------------------------------------
// Reuses state.ts's activeInstance() (the same accessor TopBar.vue/
// SideBar.vue already use) rather than re-deriving "selected instance"
// locally -- dashboard.ts's own selectedDetail() already matched this exact
// fallback (activeId, else the first instance, else none); its sibling
// instanceRows() used a slightly looser one for row highlighting only
// (`state.activeId || all[0]?.id`, which does not re-check the id still
// exists). Using the shared accessor for both here is a strict fix, not a
// behavior change worth preserving.
const selectedInstance = computed(() => activeInstance(store));
const activeId = computed(() => selectedInstance.value?.id ?? "");
const runningInstances = computed(() => store.instances.filter((item) => item.status === "running"));
const pendingInstances = computed(() => store.instances.filter((item) => item.pendingRestart));
const failedInstances = computed(() => store.instances.filter((item) => item.lastError || item.status === "error"));

// ---------------------------------------------------------------------------
// Fleet-wide traffic (metrics strip + trend card)
// ---------------------------------------------------------------------------
const fleetTrafficSeries = computed<TrafficSeries>(() => {
  void heartbeat.value;
  return fleetSeries(runningInstances.value);
});
const fleetConnectionsCount = computed<number>(() => {
  void heartbeat.value;
  return fleetConnections(runningInstances.value);
});
const fleetLatest = computed(() => seriesLatest(fleetTrafficSeries.value));
const currentUp = computed<FormattedRate>(() => formatRate(fleetLatest.value ? fleetLatest.value.up : 0));
const currentDown = computed<FormattedRate>(() => formatRate(fleetLatest.value ? fleetLatest.value.down : 0));
const peakUp = computed<FormattedRate>(() => formatRate(seriesPeak(fleetTrafficSeries.value, "up")));
const peakDown = computed<FormattedRate>(() => formatRate(seriesPeak(fleetTrafficSeries.value, "down")));
const fleetSpan = computed(() => seriesSpan(fleetTrafficSeries.value));
const fleetSampleCount = computed(() => fleetTrafficSeries.value.samples.length);

// dashboard.ts's formatClock() was a private helper used only to label the
// trend axis; it touches no sampler state and is display-only, so it is
// reimplemented here rather than requested as a new export.
function formatClock(ms: number): string {
  const value = Number(ms) || 0;
  if (!value) return "--:--:--";
  const date = new Date(value);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}

// ---------------------------------------------------------------------------
// Metrics strip: headline / chips
// ---------------------------------------------------------------------------
interface DashboardChip {
  label: string;
  value: string;
  tone: string;
}

const systemStatus = computed<Partial<FleetSystemStatus>>(() => store.system || {});
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

// NOTE (gap): the original activity caption also appended
// " · N 台未取到" -- the count of running instances whose sampler read
// `reachable === false`. That flag lives only on dashboard.ts's private
// `samplers` entries; instanceConnections() folds it into "0 connections",
// which is indistinguishable from "reachable with zero connections". Restoring
// this needs a new dashboard.ts export, e.g.
// `export function instanceReachable(id: string): boolean` mirroring
// instanceConnections()'s own one-line body. See this agent's report.
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

// ---------------------------------------------------------------------------
// Instances table
// ---------------------------------------------------------------------------
const rowSparkWidth = 96;
const rowSparkHeight = 20;
const sparkWidth = 320;
const sparkHeight = 56;
const trendHeight = 112;
const maxRowHeight = 120;

interface InstanceSample {
  series: TrafficSeries;
  connections: number;
}
const emptyInstanceSample: InstanceSample = { series: createSeries(), connections: 0 };

// One Map built per heartbeat instead of calling instanceSeries()/
// instanceConnections() ad hoc from the template -- same sampler reads dashboard.ts's
// instanceRows() did per row, just gathered once per tick.
const perInstanceSamples = computed<Map<string, InstanceSample>>(() => {
  void heartbeat.value;
  const map = new Map<string, InstanceSample>();
  for (const item of store.instances) {
    map.set(item.id, {
      series: instanceSeries(item.id) ?? createSeries(),
      connections: instanceConnections(item.id),
    });
  }
  return map;
});
function sampleFor(id: string): InstanceSample {
  return perInstanceSamples.value.get(id) ?? emptyInstanceSample;
}
function instanceRates(id: string): { up: FormattedRate; down: FormattedRate } {
  const latest = seriesLatest(sampleFor(id).series);
  return { up: formatRate(latest ? latest.up : 0), down: formatRate(latest ? latest.down : 0) };
}

// NOTE (gap): instance rows and the selected-instance card also showed
// cumulative "累计 X" bytes next to the current rate, read from
// dashboard.ts's private `samplers.get(id).previous` (a TrafficCounterSample).
// That field is not derivable from any exported accessor (a rate series
// cannot be integrated back into a lifetime total), so those sub-captions are
// dropped here rather than shown as always-zero. Restoring them needs a new
// export, e.g. `export function instanceTotals(id: string): TrafficCounterSample | null`
// mirroring instanceConnections()'s pattern. See this agent's report.

function instanceDotClass(item: FleetInstance): string {
  const bad = Boolean(item.lastError || item.status === "error");
  if (bad) return "is-danger";
  if (item.status === "running") return "is-ok";
  return item.pendingRestart ? "is-warn" : "is-idle";
}
function instanceRowClass(item: FleetInstance): Record<string, boolean> {
  const bad = Boolean(item.lastError || item.status === "error");
  return {
    "is-active": item.id === activeId.value,
    "is-danger": bad,
    "is-warn": !bad && Boolean(item.pendingRestart),
  };
}
function instanceStatusSuffix(item: FleetInstance): string {
  return item.pendingRestart ? " · 待重启" : "";
}
function instanceErrorSuffix(item: FleetInstance): string {
  const bad = Boolean(item.lastError || item.status === "error");
  return bad && item.lastError ? ` · ${String(item.lastError).slice(0, 48)}` : "";
}

// Trimming the list must never hide the row the user is looking at, so the
// selected instance takes the last visible slot when it falls past the cut.
const visibleInstanceRows = computed<FleetInstance[]>(() => {
  const all = store.instances;
  const budget = rowBudgetInstances.value;
  if (all.length <= budget) return all;
  const shown = all.slice(0, budget);
  if (shown.some((item) => item.id === activeId.value)) return shown;
  const active = all.find((item) => item.id === activeId.value);
  return active ? [...shown.slice(0, -1), active] : shown;
});
const instancesNoteText = computed(() => {
  const total = store.instances.length;
  const shownCount = Math.min(total, rowBudgetInstances.value);
  const hidden = total - shownCount;
  if (hidden > 0) return `显示 ${shownCount} / ${total} 台 · 其余在左侧列表`;
  return "点选查看右侧趋势；双击或点「打开工作台」进入该实例。";
});

// ---------------------------------------------------------------------------
// Selected-instance card
// ---------------------------------------------------------------------------
const selectedSample = computed(() => (selectedInstance.value ? sampleFor(selectedInstance.value.id) : emptyInstanceSample));
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

// Single click previews the instance without leaving the dashboard (matches
// the "点选查看右侧趋势" hint above); this replicates app.ts's now-deleted
// focusDashboardInstance() body exactly (set activeId + persist), which never
// did anything heavier (no discard-changes prompt, no tab/proxy refetch) --
// unlike selectInstance() below, which does and is why it is not reused here.
// FleetActions has no equivalent of this narrower action; see this agent's
// report for the bridge addition that would let this move there instead of
// touching `store` directly.
function focusRow(id: string): void {
  store.activeId = id;
  localStorage.setItem("activeInstance", id);
}
// Double-click / Ctrl|Cmd+Enter / "打开工作台" jump into the full workbench.
// app.ts's openInstanceWorkbench() and selectInstance() both just set
// activeId + view "instances" + persist + render (openInstanceWorkbench's
// discard-prompt condition is a strict subset of selectInstance's), so the
// already-bridged selectInstance() covers this without needing a new action.
function openRow(id: string): void {
  actions.selectInstance(id);
}
function onRowKeydown(event: KeyboardEvent, id: string): void {
  if (event.key !== "Enter" && event.key !== " ") return;
  event.preventDefault();
  if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) openRow(id);
  else focusRow(id);
}

// ---------------------------------------------------------------------------
// Connections table
// ---------------------------------------------------------------------------
const searchQuery = ref("");
const allConnectionRows = computed<FleetConnectionRow[]>(() => {
  void heartbeat.value;
  return fleetConnectionRows(runningInstances.value);
});
const matchedConnectionRows = computed(() => sortConnections(filterConnections(allConnectionRows.value, searchQuery.value)));
const shownConnectionRows = computed(() => matchedConnectionRows.value.slice(0, rowBudgetConnections.value));

// Restores the GEO column, which connectionsCard()/geoCell() used to drive.
// Kicking the lookups off is a side effect, so it lives in a watchEffect rather
// than inside a computed; only what is actually on screen gets looked up, same
// as the original's requestGeo(shown).
watchEffect(() => {
  requestGeo(shownConnectionRows.value);
});

// dashboard.ts's geoCache is a plain Map outside Vue's reactive graph, so a
// resolved code cannot invalidate anything by itself. Reading `heartbeat` here
// republishes the cache once per tick -- the same cadence at which the old
// innerHTML repaint picked resolutions up.
const connectionGeo = computed<Record<string, string>>(() => {
  void heartbeat.value;
  const codes: Record<string, string> = {};
  for (const row of shownConnectionRows.value) codes[row.ip] = resolveGeo(row.ip);
  return codes;
});
const connectionsNote = computed(() => {
  const all = allConnectionRows.value;
  const matched = matchedConnectionRows.value;
  const shown = shownConnectionRows.value;
  if (!all.length) return "运行中的实例暂无活跃连接";
  if (searchQuery.value.trim()) {
    return `匹配 ${matched.length} / ${all.length} 条${matched.length > shown.length ? ` · 显示前 ${shown.length}` : ""}`;
  }
  return `共 ${all.length} 条${matched.length > shown.length ? ` · 显示最忙的 ${shown.length}` : ""}`;
});
// Duration column is stamped once per heartbeat rather than continuously, matching
// paintDashboard()'s original once-per-repaint `Date.now()` call.
const nowTick = computed(() => {
  void heartbeat.value;
  return Date.now();
});

function targetPrimary(row: FleetConnectionRow): string {
  const address = [row.ip, row.port].filter(Boolean).join(":");
  return row.host || address || "—";
}
function targetSecondary(row: FleetConnectionRow): string {
  if (!row.host) return "";
  return [row.ip, row.port].filter(Boolean).join(":");
}
function connectionOrigin(row: FleetConnectionRow): string {
  return [row.process, row.sourceIP].filter(Boolean).join(" · ");
}
function connectionRuleText(row: FleetConnectionRow): string {
  return [row.rule, row.rulePayload && `(${row.rulePayload})`].filter(Boolean).join(" ");
}
// Reversed so the chain reads entry group first, matching how the config
// declares it; chains[0] (the node that carried the request) is shown as the
// node column instead and left out of this title.
function connectionChainTitle(row: FleetConnectionRow): string {
  return row.chains.length ? [...row.chains].reverse().join(" → ") : "";
}

// ---------------------------------------------------------------------------
// Row-fit measurement (viewport-fit mode)
// ---------------------------------------------------------------------------
// Reimplemented here rather than reused from dashboard.ts: fitTables()/
// viewportFitActive() were never in the "pure logic, covered by unit tests"
// bucket the task called out (only the sampler/geo/traffic.ts functions were)
// -- they are DOM measurement glue that existed purely to serve the old
// innerHTML render loop, so they fall under "DOM rendering becomes your
// templates" like the rest of dashboard.ts's render functions.
// #dashboardPanel is the host element main.ts mounts this view into (see the
// file header); this view's own template has no single wrapping element to
// take a template ref on (it must not add one -- see the CSS note above), so
// the host is read back by the same stable id dom.ts's `el.dashboardPanel`
// already relies on.
let hostEl: HTMLElement | null = null;
const connBodyEl = ref<HTMLDivElement | null>(null);
const connTableEl = ref<HTMLTableElement | null>(null);
const instBodyEl = ref<HTMLDivElement | null>(null);
const instTableEl = ref<HTMLTableElement | null>(null);

function fitModeActive(): boolean {
  if (!hostEl || typeof getComputedStyle !== "function") return false;
  return getComputedStyle(hostEl).getPropertyValue("--dash-fit").trim() === "1";
}
function measureRows(body: HTMLElement | null, table: HTMLTableElement | null, budget: Ref<number>): void {
  const firstRow = table?.tBodies?.[0]?.rows?.[0];
  if (!body || !table || !firstRow || !body.clientHeight) return;
  const rowHeight = Math.min(firstRow.offsetHeight || 0, maxRowHeight);
  if (rowHeight <= 0) return;
  const available = body.clientHeight - (table.tHead?.offsetHeight || 0);
  const fits = Math.max(1, Math.floor(available / rowHeight));
  if (fits !== budget.value) budget.value = fits;
}
function applyFit(): void {
  if (!fitModeActive()) {
    // Short window: the page scrolls anyway, so show a useful slice instead
    // of the handful that would fit a tall layout's leftover space.
    rowBudgetConnections.value = 24;
    rowBudgetInstances.value = Number.MAX_SAFE_INTEGER;
    return;
  }
  measureRows(connBodyEl.value, connTableEl.value, rowBudgetConnections);
  measureRows(instBodyEl.value, instTableEl.value, rowBudgetInstances);
}

let resizeObserver: ResizeObserver | null = null;
let fitTimer: ReturnType<typeof setTimeout> | null = null;
function scheduleFit(): void {
  if (fitTimer !== null) clearTimeout(fitTimer);
  fitTimer = setTimeout(() => {
    fitTimer = null;
    applyFit();
  }, 150);
}

onMounted(() => {
  hostEl = document.getElementById("dashboardPanel");
  applyFit();
  // One corrective pass, mirroring dashboard.ts's original
  // `if (fit && fitTables(container)) paintDashboard(...)`: the first pass
  // measures against the default-budget render, and a budget change from
  // that pass can itself change row height once Vue repaints with it.
  void nextTick(() => applyFit());
  // A resized host also covers the show/hide toggle app.ts's renderPanels()
  // drives via the `.hidden` class: a `display: none` element reports no box
  // to ResizeObserver, and becoming visible again delivers a resize entry
  // with its real size, so no separate "on view change" hook is needed here.
  if (typeof ResizeObserver === "function" && hostEl) {
    resizeObserver = new ResizeObserver(() => scheduleFit());
    resizeObserver.observe(hostEl);
  }
  heartbeatTimer = setInterval(() => {
    heartbeat.value += 1;
  }, heartbeatIntervalMs);
});
onUnmounted(() => {
  resizeObserver?.disconnect();
  if (fitTimer !== null) clearTimeout(fitTimer);
  if (heartbeatTimer !== null) clearInterval(heartbeatTimer);
});
</script>

<template>
  <div class="dashboard-head">
    <h2>舰队状态</h2>
    <p>{{ summaryText }}</p>
  </div>

  <div class="dashboard-grid dashboard-grid-strip">
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
  </div>

  <div class="dashboard-grid dashboard-grid-mid">
    <article class="dash-card dash-instances">
      <div class="dash-instances-head">
        <div>
          <h3>实例</h3>
          <p>{{ instancesNoteText }}</p>
        </div>
      </div>
      <div ref="instBodyEl" class="dash-inst-body">
        <p v-if="!store.instances.length" class="dash-empty">还没有实例。先创建配置档，再新建实例。</p>
        <table v-else ref="instTableEl" class="dash-table dash-instance-table">
          <thead>
            <tr>
              <th scope="col">实例</th>
              <th scope="col">连接</th>
              <th scope="col">↑ 当前</th>
              <th scope="col">↓ 当前</th>
              <th scope="col">近 {{ trafficWindowSeconds }} 秒</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="item in visibleInstanceRows"
              :key="item.id"
              :class="instanceRowClass(item)"
              tabindex="0"
              @click="focusRow(item.id)"
              @dblclick="openRow(item.id)"
              @keydown="onRowKeydown($event, item.id)"
            >
              <td class="dash-cell-name">
                <span class="dash-check-dot" :class="instanceDotClass(item)" aria-hidden="true"></span>
                <span>
                  <strong>{{ item.name }}</strong>
                  <small :class="statusClass(item.status)">{{ statusText(item.status) }}{{ instanceStatusSuffix(item) }}{{ instanceErrorSuffix(item) }}</small>
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
        <span class="dash-legend-item" data-direction="up">↑ {{ currentUp.value }} {{ currentUp.unit }}</span>
        <span class="dash-legend-item" data-direction="down">↓ {{ currentDown.value }} {{ currentDown.unit }}</span>
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
            <button type="button" class="dash-open-btn" @click="openRow(selectedInstance.id)">打开工作台</button>
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
  </div>

  <div class="dashboard-grid dashboard-grid-conns">
    <article class="dash-card dash-conns">
      <div class="dash-conns-head">
        <div>
          <p class="eyebrow">CONNECTIONS</p>
          <h3>实时连接</h3>
          <p class="dash-trend-note">{{ connectionsNote }}</p>
        </div>
        <input
          v-model="searchQuery"
          class="dash-conn-search"
          type="search"
          autocomplete="off"
          spellcheck="false"
          placeholder="搜索域名 / IP / 进程 / 规则"
          aria-label="搜索连接"
        >
      </div>
      <div v-if="shownConnectionRows.length" ref="connBodyEl" class="dash-conn-body">
        <table ref="connTableEl" class="dash-table dash-conn-table">
          <thead>
            <tr>
              <th scope="col">目标</th>
              <th scope="col">实例</th>
              <th scope="col">出口</th>
              <th scope="col">GEO</th>
              <th scope="col">↑ 当前</th>
              <th scope="col">↓ 当前</th>
              <th scope="col">时长</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in shownConnectionRows" :key="row.id">
              <td class="dash-conn-target">
                <strong>{{ targetPrimary(row) }}</strong>
                <small v-if="targetSecondary(row) || connectionOrigin(row)">{{ [targetSecondary(row), connectionOrigin(row)].filter(Boolean).join(" · ") }}</small>
              </td>
              <td>
                <span class="dash-conn-text">{{ row.instanceName || "" }}</span>
                <small>{{ [row.network, row.kind].filter(Boolean).join(" · ") }}</small>
              </td>
              <td :title="connectionChainTitle(row)">
                <span class="dash-conn-text">{{ row.node || "—" }}</span>
                <small v-if="connectionRuleText(row)">{{ connectionRuleText(row) }}</small>
              </td>
              <td class="dash-conn-geo">
                <span v-if="localAddressLabel(row.ip)" class="dash-geo-local">{{ localAddressLabel(row.ip) }}</span>
                <span v-else-if="connectionGeo[row.ip]" class="dash-geo">
                  <span class="dash-geo-flag" aria-hidden="true">{{ countryFlag(connectionGeo[row.ip]) }}</span>{{ connectionGeo[row.ip] }}
                </span>
                <span v-else class="dash-geo-unknown">—</span>
              </td>
              <td class="num">{{ formatRate(row.up).value }} {{ formatRate(row.up).unit }}<small>{{ formatBytes(row.upload) }}</small></td>
              <td class="num">{{ formatRate(row.down).value }} {{ formatRate(row.down).unit }}<small>{{ formatBytes(row.download) }}</small></td>
              <td class="num">{{ row.start ? formatDuration(nowTick - row.start) : "—" }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-else class="dash-empty">{{ allConnectionRows.length ? "没有匹配的连接" : "暂无活跃连接" }}</p>
    </article>
  </div>
</template>
