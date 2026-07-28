import {
  defaultLatencyTimeout,
  defaultLatencyUrl,
  latencyBatchConcurrency,
  latencyKinds,
} from "./constants.ts";
import type { LatencyKind } from "./constants.ts";
import { api } from "./api.ts";
import {
  currentLatencyTarget,
  formatLatencyValue,
  latencyLabel,
  latencyTitle,
  latencyTone,
} from "./format.ts";
import { localizedMessage } from "./i18n.ts";
import {
  isLatencyRunning,
  latencyResult,
  setLatencyResult,
  setLatencyRunning,
} from "./state.ts";
import type { FleetInstance, FleetProxyGroup, FleetState } from "./state.ts";
import type { DomElements } from "./dom.ts";

export { currentLatencyTarget };

/** Return value of latencySettings()/the first half of persistLatencySettings(). */
export interface LatencySettings {
  url: string;
  timeoutMs: number;
}

/**
 * Shared `options` bag threaded through runGroupLatency() down into
 * testGroupURLLatency()/testGroupRealLatency(). `batchToken` identifies the
 * in-flight testAllLatency() run (absent for a single-chip test triggered
 * directly by the user); `notifyErrors` is false for batch runs (individual
 * failures are reflected in the chip, not popped up as a message) and true
 * for the single-target path.
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

/** Options accepted by createLatencyController(); see app.ts for the call site. */
export interface LatencyControllerOptions {
  state: FleetState;
  el: Pick<DomElements, "latencyUrl" | "latencyTimeout">;
  getActive: () => FleetInstance | null;
  showMessage: (text: string, kind?: string) => void;
  onControlsChange?: () => void;
  onChipChange?: (instanceId: string, groupName: string, proxyName: string, kind: LatencyKind) => void;
}

/** Public surface returned by createLatencyController(). */
export interface LatencyController {
  latencySettings: () => LatencySettings;
  persistLatencySettings: () => void;
  applyLatencyChipState: (
    chip: HTMLElement,
    selected: FleetInstance | null,
    groupName: string,
    proxyName: string,
    kind: LatencyKind,
  ) => void;
  updateLatencyChip: (instanceId: string, groupName: string, proxyName: string, kind: LatencyKind) => void;
  beginLatencyBatch: () => number;
  endLatencyBatch: (batchToken: number) => void;
  testGroupLatency: (group: FleetProxyGroup, kind: LatencyKind) => Promise<void>;
  testAllLatency: (kind: LatencyKind) => Promise<void>;
  renderLatencyChip: (
    selected: FleetInstance | null,
    groupName: string,
    proxyName: string,
    kind: LatencyKind,
  ) => HTMLSpanElement;
}

export function createLatencyController({
  state,
  el,
  getActive,
  showMessage,
  onControlsChange,
  onChipChange,
}: LatencyControllerOptions): LatencyController {
  function latencySettings(): LatencySettings {
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

  function applyLatencyChipState(
    chip: HTMLElement,
    selected: FleetInstance | null,
    groupName: string,
    proxyName: string,
    kind: LatencyKind,
  ): void {
    const running = selected ? isLatencyRunning(state, selected.id, groupName, proxyName, kind) : false;
    const result = selected ? latencyResult(state, selected.id, groupName, proxyName, kind) : null;
    const value = formatLatencyValue(result, running);
    const title = result?.error || `${latencyTitle(kind)} ${value}`;
    chip.className = `latency-chip ${latencyTone(result, running)}`;
    chip.textContent = `${latencyLabel(kind)} ${value}`;
    chip.title = title;
    chip.setAttribute("aria-label", title);
  }

  function updateLatencyChip(instanceId: string, groupName: string, proxyName: string, kind: LatencyKind): void {
    onChipChange?.(instanceId, groupName, proxyName, kind);
  }

  function updateLatencyControls(): void {
    onControlsChange?.();
  }

  function setRunning(instanceId: string, group: string, proxy: string, kind: LatencyKind, running: boolean): void {
    setLatencyRunning(state, instanceId, group, proxy, kind, running);
    updateLatencyControls();
  }

  function beginLatencyBatch(): number {
    state.latencyBatchToken += 1;
    state.latencyBatchRunning = true;
    updateLatencyControls();
    return state.latencyBatchToken;
  }

  function endLatencyBatch(batchToken: number): void {
    if (state.latencyBatchToken === batchToken) {
      state.latencyBatchRunning = false;
      updateLatencyControls();
    }
  }

  function shouldApplyLatencyResult(instanceId: string, batchToken: number | undefined): boolean {
    return state.activeId === instanceId && (batchToken === undefined || state.latencyBatchToken === batchToken);
  }

  async function testGroupURLLatency(
    selected: FleetInstance,
    groupName: string,
    proxyName: string,
    url: string,
    timeoutMs: number,
    batchToken: number | undefined,
    options: LatencyRunOptions = {},
  ): Promise<void> {
    setRunning(selected.id, groupName, proxyName, latencyKinds.url, true);
    updateLatencyChip(selected.id, groupName, proxyName, latencyKinds.url);
    try {
      const payload = await api<LatencyTestResponse>(`/api/instances/${selected.id}/latency`, {
        method: "POST",
        body: JSON.stringify({ group: groupName, proxy: proxyName, kind: latencyKinds.url, url, timeoutMs }),
      });
      if (shouldApplyLatencyResult(selected.id, batchToken)) {
        setLatencyResult(state, selected.id, groupName, proxyName, latencyKinds.url, {
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
      if (shouldApplyLatencyResult(selected.id, batchToken)) {
        setLatencyResult(state, selected.id, groupName, proxyName, latencyKinds.url, {
          delay: 0,
          error: localizedMessage(message),
        });
      }
      if (options.notifyErrors) showMessage(message, "error");
    } finally {
      setRunning(selected.id, groupName, proxyName, latencyKinds.url, false);
      updateLatencyChip(selected.id, groupName, proxyName, latencyKinds.url);
    }
  }

  async function testGroupRealLatency(
    selected: FleetInstance,
    groupName: string,
    proxyName: string,
    url: string,
    timeoutMs: number,
    batchToken: number | undefined,
    options: LatencyRunOptions = {},
  ): Promise<void> {
    setRunning(selected.id, groupName, proxyName, latencyKinds.real, true);
    updateLatencyChip(selected.id, groupName, proxyName, latencyKinds.real);
    try {
      const payload = await api<LatencyTestResponse>(`/api/instances/${selected.id}/latency`, {
        method: "POST",
        body: JSON.stringify({ group: groupName, proxy: proxyName, kind: latencyKinds.real, url, timeoutMs }),
      });
      if (shouldApplyLatencyResult(selected.id, batchToken)) {
        setLatencyResult(state, selected.id, groupName, proxyName, latencyKinds.real, {
          delay: Number(payload.delay) || 0,
          error: "",
        });
      }
    } catch (err) {
      // See testGroupURLLatency() above for why this narrowing is exhaustive
      // in practice.
      const message = err instanceof Error ? err.message : String(err);
      if (shouldApplyLatencyResult(selected.id, batchToken)) {
        setLatencyResult(state, selected.id, groupName, proxyName, latencyKinds.real, {
          delay: 0,
          error: localizedMessage(message),
        });
      }
      if (options.notifyErrors) showMessage(message, "error");
    } finally {
      setRunning(selected.id, groupName, proxyName, latencyKinds.real, false);
      updateLatencyChip(selected.id, groupName, proxyName, latencyKinds.real);
    }
  }

  async function runGroupLatency(group: FleetProxyGroup, kind: LatencyKind, options: LatencyRunOptions = {}): Promise<void> {
    const selected = getActive();
    if (!selected) return;
    if (selected.status !== "running") {
      showMessage("instance must be running to test latency", "error");
      return;
    }
    const name = currentLatencyTarget(group, state.proxyGroups);
    if (!name) return;
    if (isLatencyRunning(state, selected.id, group.name, name, kind)) return;
    const { url, timeoutMs } = latencySettings();
    const batchToken = options.batchToken;
    if (kind === latencyKinds.url) {
      await testGroupURLLatency(selected, group.name, name, url, timeoutMs, batchToken, options);
    } else {
      await testGroupRealLatency(selected, group.name, name, url, timeoutMs, batchToken, options);
    }
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
        const name = currentLatencyTarget(group, state.proxyGroups);
        if (name && isLatencyRunning(state, instanceId, group.name, name, kind)) continue;
        await runGroupLatency(group, kind, { batchToken, notifyErrors: false });
      }
    });
    await Promise.allSettled(workers);
  }

  async function testAllLatency(kind: LatencyKind): Promise<void> {
    const selected = getActive();
    if (!selected) return;
    if (selected.status !== "running") {
      showMessage("instance must be running to test latency", "error");
      return;
    }
    if (state.latencyBatchRunning) return;
    const batchToken = beginLatencyBatch();
    try {
      const groups = state.proxyGroups.filter((group) => currentLatencyTarget(group, state.proxyGroups));
      await runLatencyBatch(groups, kind, batchToken, selected.id);
    } finally {
      endLatencyBatch(batchToken);
    }
  }

  function renderLatencyChip(
    selected: FleetInstance | null,
    groupName: string,
    proxyName: string,
    kind: LatencyKind,
  ): HTMLSpanElement {
    const chip = document.createElement("span");
    chip.dataset.instanceId = selected?.id || "";
    chip.dataset.groupName = groupName;
    chip.dataset.proxyName = proxyName;
    chip.dataset.kind = kind;
    applyLatencyChipState(chip, selected, groupName, proxyName, kind);
    return chip;
  }

  return {
    latencySettings,
    persistLatencySettings,
    applyLatencyChipState,
    updateLatencyChip,
    beginLatencyBatch,
    endLatencyBatch,
    testGroupLatency,
    testAllLatency,
    renderLatencyChip,
  };
}
