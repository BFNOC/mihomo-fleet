import {
  defaultLatencyTimeout,
  defaultLatencyUrl,
  latencyBatchConcurrency,
  latencyKinds,
} from "./constants.js";
import { api } from "./api.js";
import {
  currentLatencyTarget,
  formatLatencyValue,
  latencyLabel,
  latencyTitle,
  latencyTone,
} from "./format.js";
import { localizedMessage } from "./i18n.js";
import {
  isLatencyRunning,
  latencyResult,
  setLatencyResult,
  setLatencyRunning,
} from "./state.js";

export { currentLatencyTarget };

export function createLatencyController({ state, el, getActive, showMessage, onControlsChange, onChipChange }) {
  function latencySettings() {
    const url = (el.latencyUrl.value || defaultLatencyUrl).trim();
    const timeoutMs = Math.min(15000, Math.max(500, Number(el.latencyTimeout.value) || defaultLatencyTimeout));
    return { url, timeoutMs };
  }

  function persistLatencySettings() {
    const { url, timeoutMs } = latencySettings();
    el.latencyTimeout.value = String(timeoutMs);
    localStorage.setItem("fleetLatencyUrl", url);
    localStorage.setItem("fleetLatencyTimeout", String(timeoutMs));
  }

  function applyLatencyChipState(chip, selected, groupName, proxyName, kind) {
    const running = selected ? isLatencyRunning(state, selected.id, groupName, proxyName, kind) : false;
    const result = selected ? latencyResult(state, selected.id, groupName, proxyName, kind) : null;
    const value = formatLatencyValue(result, running);
    const title = result?.error || `${latencyTitle(kind)} ${value}`;
    chip.className = `latency-chip ${latencyTone(result, running)}`;
    chip.textContent = `${latencyLabel(kind)} ${value}`;
    chip.title = title;
    chip.setAttribute("aria-label", title);
  }

  function updateLatencyChip(instanceId, groupName, proxyName, kind) {
    onChipChange?.(instanceId, groupName, proxyName, kind);
  }

  function updateLatencyControls() {
    onControlsChange?.();
  }

  function setRunning(instanceId, group, proxy, kind, running) {
    setLatencyRunning(state, instanceId, group, proxy, kind, running);
    updateLatencyControls();
  }

  function beginLatencyBatch() {
    state.latencyBatchToken += 1;
    state.latencyBatchRunning = true;
    updateLatencyControls();
    return state.latencyBatchToken;
  }

  function endLatencyBatch(batchToken) {
    if (state.latencyBatchToken === batchToken) {
      state.latencyBatchRunning = false;
      updateLatencyControls();
    }
  }

  function shouldApplyLatencyResult(instanceId, batchToken) {
    return state.activeId === instanceId && (batchToken === undefined || state.latencyBatchToken === batchToken);
  }

  async function testGroupURLLatency(selected, groupName, proxyName, url, timeoutMs, batchToken, options = {}) {
    setRunning(selected.id, groupName, proxyName, latencyKinds.url, true);
    updateLatencyChip(selected.id, groupName, proxyName, latencyKinds.url);
    try {
      const payload = await api(`/api/instances/${selected.id}/latency`, {
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
      if (shouldApplyLatencyResult(selected.id, batchToken)) {
        setLatencyResult(state, selected.id, groupName, proxyName, latencyKinds.url, {
          delay: 0,
          error: localizedMessage(err.message),
        });
      }
      if (options.notifyErrors) showMessage(err.message, "error");
    } finally {
      setRunning(selected.id, groupName, proxyName, latencyKinds.url, false);
      updateLatencyChip(selected.id, groupName, proxyName, latencyKinds.url);
    }
  }

  async function testGroupRealLatency(selected, groupName, proxyName, url, timeoutMs, batchToken, options = {}) {
    setRunning(selected.id, groupName, proxyName, latencyKinds.real, true);
    updateLatencyChip(selected.id, groupName, proxyName, latencyKinds.real);
    try {
      const payload = await api(`/api/instances/${selected.id}/latency`, {
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
      if (shouldApplyLatencyResult(selected.id, batchToken)) {
        setLatencyResult(state, selected.id, groupName, proxyName, latencyKinds.real, {
          delay: 0,
          error: localizedMessage(err.message),
        });
      }
      if (options.notifyErrors) showMessage(err.message, "error");
    } finally {
      setRunning(selected.id, groupName, proxyName, latencyKinds.real, false);
      updateLatencyChip(selected.id, groupName, proxyName, latencyKinds.real);
    }
  }

  async function runGroupLatency(group, kind, options = {}) {
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

  async function testGroupLatency(group, kind) {
    await runGroupLatency(group, kind, { notifyErrors: true });
  }

  async function runLatencyBatch(groups, kind, batchToken, instanceId) {
    let index = 0;
    const workerCount = Math.min(latencyBatchConcurrency, groups.length);
    const workers = Array.from({ length: workerCount }, async () => {
      while (index < groups.length) {
        if (state.activeId !== instanceId || state.latencyBatchToken !== batchToken) return;
        const group = groups[index];
        index += 1;
        const name = currentLatencyTarget(group, state.proxyGroups);
        if (name && isLatencyRunning(state, instanceId, group.name, name, kind)) continue;
        await runGroupLatency(group, kind, { batchToken, notifyErrors: false });
      }
    });
    await Promise.allSettled(workers);
  }

  async function testAllLatency(kind) {
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

  function renderLatencyChip(selected, groupName, proxyName, kind) {
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
