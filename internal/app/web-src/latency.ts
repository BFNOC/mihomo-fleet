import { defaultLatencyTimeout, defaultLatencyUrl, latencyBatchConcurrency } from "./constants.ts";
import type { LatencyKind } from "./constants.ts";
import { api } from "./api.ts";
import { currentLatencyTarget } from "./format.ts";
import { localizedMessage } from "./messages.ts";
import { isLatencyRunning, setLatencyResult, setLatencyRunning } from "./state.ts";
import type { FleetInstance, FleetProxyGroup, FleetState } from "./state.ts";

/**
 * The two settings inputs this controller reads its URL and timeout from, and
 * writes the clamped timeout back into. ProxiesTab.vue owns both as template
 * refs and passes them in.
 */
export interface LatencySettingsInputs {
  latencyUrl: HTMLInputElement;
  latencyTimeout: HTMLInputElement;
}

/**
 * Shared `options` bag threaded down into runLatencyTest(). `batchToken`
 * identifies the in-flight testAllLatency() run (absent for a single-chip test
 * triggered directly by the user); `notifyErrors` is false for batch runs
 * (individual failures are reflected in the chip, not popped up as a message)
 * and true for the single-target path.
 */
interface LatencyRunOptions {
  batchToken?: number;
  notifyErrors?: boolean;
}

// Shape of the JSON body POST /api/instances/{id}/latency returns (see
// internal/app/controller.go's latency handler:
// writeJSON(w, map[string]any{"delay": delay, "url": testURL, "timeoutMs": timeoutMS})).
// Only `delay` is read here; `url`/`timeoutMs` just echo the request.
interface LatencyTestResponse {
  delay: number;
  url: string;
  timeoutMs: number;
}

/** Options accepted by createLatencyController(); see ProxiesTab.vue for the call site. */
export interface LatencyControllerOptions {
  state: FleetState;
  el: LatencySettingsInputs;
  getActive: () => FleetInstance | null;
  showMessage: (text: string, kind?: string) => void;
}

/**
 * Public surface returned by createLatencyController() -- exactly what
 * ProxiesTab.vue calls, nothing more.
 *
 * Six further methods used to be exported here (latencySettings,
 * applyLatencyChipState, updateLatencyChip, beginLatencyBatch, endLatencyBatch,
 * renderLatencyChip) plus two callback options (onChipChange/onControlsChange).
 * None had a caller. The last two chip helpers built a <span> imperatively for
 * the pre-Vue renderer; ProxiesTab.vue binds the same format.ts formatters in
 * its template instead. The callbacks existed to poke that renderer after every
 * state write, which `reactive()` now does on its own -- state.latencyRunning is
 * a Set and state.latencyResults a plain object, both proxied by Vue, so writing
 * them re-renders whatever read them.
 */
export interface LatencyController {
  persistLatencySettings: () => void;
  testGroupLatency: (group: FleetProxyGroup, kind: LatencyKind) => Promise<void>;
  testAllLatency: (kind: LatencyKind) => Promise<void>;
}

export function createLatencyController({
  state,
  el,
  getActive,
  showMessage,
}: LatencyControllerOptions): LatencyController {
  function latencySettings(): { url: string; timeoutMs: number } {
    const url = (el.latencyUrl.value || defaultLatencyUrl).trim();
    const timeoutMs = Math.min(15000, Math.max(500, Number(el.latencyTimeout.value) || defaultLatencyTimeout));
    return { url, timeoutMs };
  }

  function persistLatencySettings(): void {
    const { url, timeoutMs } = latencySettings();
    el.latencyTimeout.value = String(timeoutMs);
    localStorage.setItem("fleetLatencyUrl", url);
    localStorage.setItem("fleetLatencyTimeout", String(timeoutMs));
  }

  // A result is dropped if the user switched instances mid-flight, or if a
  // newer batch superseded the one that asked for it.
  function shouldApplyResult(instanceId: string, batchToken: number | undefined): boolean {
    return state.activeId === instanceId && (batchToken === undefined || state.latencyBatchToken === batchToken);
  }

  // One request for either kind. `kind` reaches the backend as a field and is
  // otherwise opaque here, so the url/real split that used to be two 39-line
  // near-identical functions is a single parameter.
  async function runLatencyTest(
    selected: FleetInstance,
    groupName: string,
    proxyName: string,
    kind: LatencyKind,
    options: LatencyRunOptions,
  ): Promise<void> {
    const { url, timeoutMs } = latencySettings();
    const { batchToken, notifyErrors } = options;
    setLatencyRunning(state, selected.id, groupName, proxyName, kind, true);
    try {
      const payload = await api<LatencyTestResponse>(`/api/instances/${selected.id}/latency`, {
        method: "POST",
        body: JSON.stringify({ group: groupName, proxy: proxyName, kind, url, timeoutMs }),
      });
      if (shouldApplyResult(selected.id, batchToken)) {
        setLatencyResult(state, selected.id, groupName, proxyName, kind, {
          delay: Number(payload.delay) || 0,
          error: "",
        });
      }
    } catch (err) {
      // api() only ever rejects with an Error instance (buildApiError always
      // constructs one; a native fetch failure surfaces as TypeError, also an
      // Error subtype), so this narrowing covers every real call site here;
      // the fallback keeps `message` a plain string without resorting to `any`.
      const message = err instanceof Error ? err.message : String(err);
      if (shouldApplyResult(selected.id, batchToken)) {
        setLatencyResult(state, selected.id, groupName, proxyName, kind, {
          delay: 0,
          error: localizedMessage(message),
        });
      }
      if (notifyErrors) showMessage(message, "error");
    } finally {
      setLatencyRunning(state, selected.id, groupName, proxyName, kind, false);
    }
  }

  function runningInstance(): FleetInstance | null {
    const selected = getActive();
    if (!selected) return null;
    if (selected.status !== "running") {
      showMessage("instance must be running to test latency", "error");
      return null;
    }
    return selected;
  }

  // Re-checks "still running" per group rather than once per batch, so a batch
  // whose instance stops midway stops issuing requests. That check reports
  // through showMessage() regardless of `notifyErrors`, which is what the
  // pre-split code did; only per-request failures honour the flag.
  async function runGroupLatency(group: FleetProxyGroup, kind: LatencyKind, options: LatencyRunOptions): Promise<void> {
    const selected = runningInstance();
    if (!selected) return;
    const name = currentLatencyTarget(group, state.proxyGroups);
    if (!name) return;
    if (isLatencyRunning(state, selected.id, group.name, name, kind)) return;
    await runLatencyTest(selected, group.name, name, kind, options);
  }

  async function testGroupLatency(group: FleetProxyGroup, kind: LatencyKind): Promise<void> {
    await runGroupLatency(group, kind, { notifyErrors: true });
  }

  async function runLatencyBatch(
    groups: FleetProxyGroup[],
    kind: LatencyKind,
    batchToken: number,
    instanceId: string,
  ): Promise<void> {
    let index = 0;
    const workerCount = Math.min(latencyBatchConcurrency, groups.length);
    const workers = Array.from({ length: workerCount }, async () => {
      while (index < groups.length) {
        if (state.activeId !== instanceId || state.latencyBatchToken !== batchToken) return;
        const groupIndex = index;
        index += 1;
        // `groupIndex` was just bounds-checked by the `while` condition above
        // with no `await` in between, so no other worker's concurrent
        // `index += 1` can have invalidated it yet (JS has no preemption
        // between synchronous statements).
        const group = groups[groupIndex]!;
        await runGroupLatency(group, kind, { batchToken, notifyErrors: false });
      }
    });
    await Promise.allSettled(workers);
  }

  async function testAllLatency(kind: LatencyKind): Promise<void> {
    const selected = runningInstance();
    if (!selected || state.latencyBatchRunning) return;
    state.latencyBatchToken += 1;
    state.latencyBatchRunning = true;
    const batchToken = state.latencyBatchToken;
    try {
      const groups = state.proxyGroups.filter((group) => currentLatencyTarget(group, state.proxyGroups));
      await runLatencyBatch(groups, kind, batchToken, selected.id);
    } finally {
      if (state.latencyBatchToken === batchToken) state.latencyBatchRunning = false;
    }
  }

  return { persistLatencySettings, testGroupLatency, testAllLatency };
}
