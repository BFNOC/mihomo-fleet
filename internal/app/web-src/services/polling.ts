import { fastPollBackgroundIntervalMs, fastPollIntervalMs, slowPollIntervalMs } from "../constants.ts";
import { api } from "../api.ts";
import { chrome } from "../bridge.ts";
import { sampleFleet } from "../dashboard.ts";
import type { ConnectionsFetchPayload } from "../dashboard.ts";
import { store } from "../store.ts";
import { refresh } from "./fleet-refresh.ts";

// Two independent loops. The slow one re-pulls the fleet lists; the fast one
// only samples traffic counters. Both self-reschedule and both stop dead while
// the tab is hidden, so a backgrounded tab costs nothing.
let slowPollTimer: ReturnType<typeof setTimeout> | null = null;
let fastPollTimer: ReturnType<typeof setTimeout> | null = null;

function scheduleSlowPoll(delay: number = slowPollIntervalMs): void {
  clearTimeout(slowPollTimer || undefined);
  slowPollTimer = null;
  if (document.hidden) return;
  slowPollTimer = setTimeout(runSlowPoll, delay);
}

async function runSlowPoll(): Promise<void> {
  // periodic: true -- see fleet-refresh.ts's RefreshOptions.periodic doc. This
  // is the only call site allowed to set it: it is the one that can race a
  // profile save/delete/refresh-subscription still holding chrome.profileBusy.
  if (!document.hidden) await refresh({ periodic: true });
  scheduleSlowPoll();
}

// Full 1.8s cadence only while the dashboard is the active view. Every other
// view still needs sampleFleetTraffic() ticking -- see its own comment on why
// the rolling 60s window must stay pre-warmed regardless of which view is
// open -- but nothing is reading per-tick connection rows while some other
// view is open, so there is no reason to pay for the full cadence in that
// state. See constants.ts for the two interval values.
function nextFastPollDelay(): number {
  return store.view === "dashboard" ? fastPollIntervalMs : fastPollBackgroundIntervalMs;
}

function scheduleFastPoll(delay: number = nextFastPollDelay()): void {
  clearTimeout(fastPollTimer || undefined);
  fastPollTimer = null;
  if (document.hidden) return;
  fastPollTimer = setTimeout(runFastPoll, delay);
}

/**
 * Keep sampling while any view is open so opening the dashboard already has a
 * filled window. Cost is one /connections call per running instance per tick,
 * at fastPollIntervalMs while the dashboard is open and the slower
 * fastPollBackgroundIntervalMs otherwise (see scheduleFastPoll()).
 *
 * Exported because openDashboard() takes an extra sample on the way in.
 */
export async function sampleFleetTraffic(): Promise<void> {
  if (!store.instances?.length) return;
  await sampleFleet(
    store.instances,
    (id) => api<ConnectionsFetchPayload>(`/api/mihomo/${encodeURIComponent(id)}/connections`),
    Date.now(),
  );
  // dashboard.ts's sampler Map is a plain module-scope value outside Vue's
  // reactive graph (see bridge.ts's `chrome.trafficTick` comment); bumping this
  // after every sample is what gives DashboardView.vue's computeds a real
  // dependency to invalidate on.
  chrome.trafficTick += 1;
}

// The per-tab half of this loop is gone: overview/proxies/logs each own their
// own fetch-on-visible/fetch-on-interval loop now (views/detail/useTabPolling.ts),
// driven off store.activeTab/store.activeId rather than being told to refresh
// from here.
async function runFastPoll(): Promise<void> {
  if (!document.hidden) await sampleFleetTraffic();
  scheduleFastPoll();
}

/** Starts both loops and resumes them whenever the tab becomes visible again. */
export function startPolling(): void {
  document.addEventListener("visibilitychange", () => {
    if (document.hidden) return;
    runSlowPoll();
    runFastPoll();
  });
  scheduleSlowPoll();
  scheduleFastPoll();
}
