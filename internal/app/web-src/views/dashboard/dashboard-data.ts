import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { store } from "../../store.ts";
import { actions, chrome } from "../../bridge.ts";
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
// fast poll (services/polling.ts -> sampleFleet). A computed() that calls
// instanceSeries()/fleetSeries()/fleetConnectionRows() would otherwise compute
// once and never invalidate -- the charts would freeze with no error.
//
// `heartbeat` is the explicit trigger every such computed reads first. It is
// NOT a timer: DashboardView.vue's host is CSS-hidden, never v-if'd, so it
// stays mounted for the app's whole lifetime -- a plain setInterval here would
// re-run every dependent computed (aggregateSeries's bucket Map + sort,
// fleetConnectionRows's per-row copy, sortConnections's full sort, five cards'
// vdom diff and sparkline rebuilds) on a schedule with no relationship to
// whether anyone can see the result, including while the panel is hidden or
// the tab is backgrounded. Measured cost of that: with the dashboard hidden
// and the tab foregrounded, the old always-on interval alone was not the
// leak (see DashboardConnections.vue's :key fix for the actual unbounded-DOM
// bug), but it was still real, wasted work every 1.8s forever.
//
// heartbeat now only advances on a real sample landing (bridge.ts's
// chrome.trafficTick, bumped by services/polling.ts's sampleFleetTraffic()
// once the network round trip completes) AND only while dashboardIsVisible()
// is true. This removes the independent clock entirely -- heartbeat can never
// drift ahead of or behind an actual sample -- and makes the idle cost of a
// hidden/backgrounded dashboard exactly zero: chrome.trafficTick may still
// tick underneath (services/polling.ts keeps the sampler pre-warmed at a
// slower cadence while any other view is open), but the watch below no-ops
// instead of bumping heartbeat, so none of the six render effects above ever
// re-run.
//
// MUST be declared before every computed that reads it: `const` is in its
// temporal dead zone until its own line runs, and the eager watchEffect driving
// the GEO lookups reads these during setup, so a later declaration throws
// ReferenceError rather than yielding a default.
const heartbeat = ref(0);

// True only when the recompute this drives can actually be seen: the
// dashboard is the active view (not just mounted -- it always is) and the tab
// itself is foregrounded. Mirrors services/polling.ts's own `document.hidden`
// gate on the sample loop, so the two stay consistent instead of drifting.
function dashboardIsVisible(): boolean {
  return store.view === "dashboard" && document.visibilityState === "visible";
}

/**
 * Drives `heartbeat`. Called once, by DashboardView.vue -- the cards mount and
 * unmount with it (visibility is a CSS class, never v-if), so one watcher
 * owned by the parent needs no refcounting across the children.
 */
export function useDashboardHeartbeat(): void {
  let stopWatch: (() => void) | null = null;
  onMounted(() => {
    stopWatch = watch(
      () => chrome.trafficTick,
      () => {
        if (dashboardIsVisible()) heartbeat.value += 1;
      },
    );
  });
  onUnmounted(() => {
    stopWatch?.();
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
