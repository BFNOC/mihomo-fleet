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

// Shape of the JSON body the SAME endpoint returns when `group` is set and
// `proxy` is omitted with kind "url" -- controller.go routes that combination
// to mihomoGroupDelay() instead of the single-proxy path above, and echoes
// its map[string]int verbatim as `delays`. mihomoGroupDelay
// (internal/app/mihomo_api.go) collapses any whole-request failure to an
// empty map rather than an error, and a per-proxy failure inside mihomo
// itself is a missing key or a zero, never a negative number or an error
// string -- there is no richer per-node failure signal to plumb through.
interface GroupLatencyTestResponse {
  delays: Record<string, number>;
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

  // Writes one delay result per member of `group`, from a single request --
  // this is the reason to route through mihomo's /group/{name}/delay
  // (controller.go's `req.Group != "" && req.Proxy == "" && req.Kind ==
  // "url"` branch) instead of calling runGroupLatency() once per node. Doing
  // that instead of selecting each candidate in turn is the whole point: a
  // selection is applied and rerouts live traffic immediately on a running
  // instance (see selectProxy() in proxy-groups.ts), so comparing nodes by
  // selecting them one at a time was never a safe way to just look.
  //
  // Every name in `group.all` is marked running and later resolved together,
  // even ones mihomo's response omits or zeroes -- see
  // GroupLatencyTestResponse's comment. That keeps "in flight" (the running
  // flag), "tested, no delay" (a result with delay 0), and "never tested" (no
  // result at all, formatLatencyValue's "--") three distinct states instead
  // of collapsing the first two.
  async function runGroupUrlDelayAll(group: FleetProxyGroup): Promise<void> {
    const selected = runningInstance();
    if (!selected) return;
    const names = (group.all || []).filter(Boolean);
    if (!names.length) return;
    // All names toggle together below, so any one of them already running is
    // proof the whole group is mid-request; matches runGroupLatency's single-
    // flight guard just above, at group-request granularity instead of
    // per-node.
    if (isLatencyRunning(state, selected.id, group.name, names[0]!, "url")) return;
    const { url, timeoutMs } = latencySettings();
    for (const name of names) setLatencyRunning(state, selected.id, group.name, name, "url", true);
    try {
      const payload = await api<GroupLatencyTestResponse>(`/api/instances/${selected.id}/latency`, {
        method: "POST",
        body: JSON.stringify({ group: group.name, kind: "url", url, timeoutMs }),
      });
      if (shouldApplyResult(selected.id, undefined)) {
        const delays = payload.delays || {};
        for (const name of names) {
          setLatencyResult(state, selected.id, group.name, name, "url", { delay: Number(delays[name]) || 0, error: "" });
        }
      }
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      if (shouldApplyResult(selected.id, undefined)) {
        const localized = localizedMessage(message);
        for (const name of names) {
          setLatencyResult(state, selected.id, group.name, name, "url", { delay: 0, error: localized });
        }
      }
      showMessage(message, "error");
    } finally {
      for (const name of names) setLatencyRunning(state, selected.id, group.name, name, "url", false);
    }
  }

  async function testGroupLatency(group: FleetProxyGroup, kind: LatencyKind): Promise<void> {
    // "real" latency still needs one specific proxy -- controller.go rejects
    // a group-only request with kind "real" outright (mihomo has no
    // group-wide real-delay endpoint to call), so that path keeps testing
    // only the group's current node via runGroupLatency, same as before.
    if (kind === "url") {
      await runGroupUrlDelayAll(group);
      return;
    }
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

  // Deliberately still routes every group through runGroupLatency (one
  // request per group's CURRENT node), not runGroupUrlDelayAll, even for
  // "url". "测速各组当前" ("test each group's current [node]") answers a
  // different question than the per-group button now does -- "are the routes
  // already in use still fast" across the whole fleet, not "which candidate
  // in this one group is fastest" -- and changing its request shape here
  // would silently invalidate that Chinese label (see ProxiesTab.vue) without
  // this file's diff making that obvious.
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
