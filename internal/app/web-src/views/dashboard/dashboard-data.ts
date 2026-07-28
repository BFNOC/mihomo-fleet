import { computed, onMounted, onUnmounted, ref } from "vue";
import { store } from "../../store.ts";
import { actions } from "../../bridge.ts";
import { activeInstance } from "../../state.ts";
import { fleetConnections, fleetConnectionRows, fleetSeries, instanceConnections, instanceSeries } from "../../dashboard.ts";
import type { FleetConnectionRow } from "../../dashboard.ts";
import { createSeries, formatRate, seriesLatest } from "../../traffic.ts";
import type { FormattedRate, TrafficSeries } from "../../traffic.ts";

// Everything the five dashboard cards read in common. It lives at module scope,
// not inside a component, because each of these derivations is shared by two or
// three cards and recomputing them per card would triple the sampler reads.
//
// ---------------------------------------------------------------------------
// Reactive invalidation for dashboard.ts's sampler state
// ---------------------------------------------------------------------------
// dashboard.ts's `samplers` Map (and the connection-rate/geo state derived from
// it) is a plain module-scope value, mutated outside Vue's reactive graph by the
// fast poll (services/polling.ts -> sampleFleet, ~1.8s cadence). A computed()
// that calls instanceSeries()/fleetSeries()/fleetConnectionRows() would
// otherwise compute once and never invalidate -- the charts would freeze with no
// error.
//
// `heartbeat` is the explicit trigger every such computed reads first. It is a
// self-contained interval matching the fast-poll cadence, not tied to the poll
// actually completing a sample -- deliberately, so the dashboard needs no wiring
// into that loop to work at all. bridge.ts's chrome.trafficTick is the tighter
// alternative (it bumps only after a real sample lands); swapping to it is a
// one-line change in each `void heartbeat.value` below.
//
// MUST be declared before every computed that reads it: `const` is in its
// temporal dead zone until its own line runs, and the eager watchEffect driving
// the GEO lookups reads these during setup, so a later declaration throws
// ReferenceError rather than yielding a default.
const heartbeat = ref(0);
const heartbeatIntervalMs = 1800;

/**
 * Drives `heartbeat`. Called once, by DashboardView.vue -- the cards mount and
 * unmount with it (visibility is a CSS class, never v-if), so one timer owned by
 * the parent needs no refcounting across the children.
 */
export function useDashboardHeartbeat(): void {
  let timer: ReturnType<typeof setInterval> | null = null;
  onMounted(() => {
    timer = setInterval(() => {
      heartbeat.value += 1;
    }, heartbeatIntervalMs);
  });
  onUnmounted(() => {
    if (timer !== null) clearInterval(timer);
  });
}

// Chart geometry, shared so the row sparkline and the two large plots stay
// visually consistent.
export const rowSparkWidth = 96;
export const rowSparkHeight = 20;
export const sparkWidth = 320;
export const sparkHeight = 56;
export const trendHeight = 112;

// ---------------------------------------------------------------------------
// Fleet/instance identity
// ---------------------------------------------------------------------------
// Reuses state.ts's activeInstance() (the same accessor TopBar.vue/SideBar.vue
// use) rather than re-deriving "selected instance" locally.
export const selectedInstance = computed(() => activeInstance(store));
export const activeId = computed(() => selectedInstance.value?.id ?? "");
export const runningInstances = computed(() => store.instances.filter((item) => item.status === "running"));
export const pendingInstances = computed(() => store.instances.filter((item) => item.pendingRestart));
export const failedInstances = computed(() => store.instances.filter((item) => item.lastError || item.status === "error"));

// ---------------------------------------------------------------------------
// Fleet-wide traffic
// ---------------------------------------------------------------------------
export const fleetTrafficSeries = computed<TrafficSeries>(() => {
  void heartbeat.value;
  return fleetSeries(runningInstances.value);
});
export const fleetConnectionsCount = computed<number>(() => {
  void heartbeat.value;
  return fleetConnections(runningInstances.value);
});
const fleetLatest = computed(() => seriesLatest(fleetTrafficSeries.value));
export const currentUp = computed<FormattedRate>(() => formatRate(fleetLatest.value ? fleetLatest.value.up : 0));
export const currentDown = computed<FormattedRate>(() => formatRate(fleetLatest.value ? fleetLatest.value.down : 0));

// ---------------------------------------------------------------------------
// Per-instance samples
// ---------------------------------------------------------------------------
export interface InstanceSample {
  series: TrafficSeries;
  connections: number;
}
const emptyInstanceSample: InstanceSample = { series: createSeries(), connections: 0 };

// One Map built per heartbeat instead of calling instanceSeries()/
// instanceConnections() ad hoc per row -- the same sampler reads, gathered once
// per tick and shared by the instances table and the selected-instance card.
export const perInstanceSamples = computed<Map<string, InstanceSample>>(() => {
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

export function sampleFor(id: string): InstanceSample {
  return perInstanceSamples.value.get(id) ?? emptyInstanceSample;
}

// ---------------------------------------------------------------------------
// Connections
// ---------------------------------------------------------------------------
export const allConnectionRows = computed<FleetConnectionRow[]>(() => {
  void heartbeat.value;
  return fleetConnectionRows(runningInstances.value);
});

// Stamped once per heartbeat rather than continuously, matching the pre-Vue
// repaint's once-per-render Date.now() call.
export const nowTick = computed(() => {
  void heartbeat.value;
  return Date.now();
});

// ---------------------------------------------------------------------------
// Row actions -- shared by the instances table and the selected-instance card
// ---------------------------------------------------------------------------

/**
 * Single click previews an instance without leaving the dashboard (matching the
 * table's "点选查看右侧趋势" hint). This is the pre-Vue focusDashboardInstance()
 * body exactly: set activeId + persist, nothing heavier -- no discard-changes
 * prompt, no tab/proxy refetch. That is why it does not reuse selectInstance(),
 * which does both.
 */
export function focusInstance(id: string): void {
  store.activeId = id;
  localStorage.setItem("activeInstance", id);
}

/**
 * Double-click / Ctrl|Cmd+Enter / "打开工作台" jump into the full workbench. The
 * pre-Vue openInstanceWorkbench() and selectInstance() differ only in that the
 * former's discard-prompt condition is a strict subset of the latter's, so the
 * already-bridged action covers both.
 */
export function openInstanceWorkbench(id: string): void {
  actions.selectInstance(id);
}
