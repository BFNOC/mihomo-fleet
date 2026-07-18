import {
  defaultConfig,
  defaultLatencyTimeout,
  defaultLatencyUrl,
  defaultProxyBind,
  fastPollIntervalMs,
  instanceModes,
  latencyKeySeparator,
  latencyKinds,
  logStickThreshold,
  newProfileValue,
  slowPollIntervalMs,
} from "./constants.js";
import { api, writeClipboard } from "./api.js";
import { bindElements } from "./dom.js";
import {
  alignProxyGroupsToProfileOrder,
  chainFromText,
  chainSummary,
  chainToText,
  currentLatencyTarget,
  filterRuntimeProxyGroups,
  formatBatchMessage,
  formatProfileUpdate,
  formatSubscriptionInfo,
  instanceMode,
  isHttpUrl,
  isSelectableProxyGroup,
  modeLabel,
  normalizeStoredLatencyTimeout,
  normalizeStoredLatencyUrl,
  proxyCopyActions,
  proxyCopyPlaceholders,
  proxyEndpointText,
  proxyLabelSources,
  proxyPort,
  proxyPortLabel,
  selectionSummary,
  splitProxyLabel,
} from "./format.js";
import { escapeHTML, localizedMessage, statusClass, statusText } from "./i18n.js";
import { createLatencyController } from "./latency.js";
import {
  activeInstance,
  clearLatencyStateForInstance,
  createState,
  isLatencyRunning,
  latencyResult,
  orphanProfiles,
  profileById,
  profileReferenceCount,
  pruneLatencyResultsForGroups,
} from "./state.js";
import {
  canClearSavedConfig,
  createActionGate,
  createYamlEditor,
  profileOptionLabel,
  shouldApplyConfigLoad,
  shouldApplyCreateProfileConfig,
} from "./yaml-editor.js";

const state = createState();
const el = bindElements();

const createGate = createActionGate();
const saveBasicsGate = createActionGate();
const saveConfigGate = createActionGate();
let createProfileConfigSeq = 0;
let configLoadSeq = 0;
let refreshSeq = 0;
let proxiesRequestSeq = 0;
let messageClearTimer = null;
let lastInstanceSelectorSnapshot = "";
let lastInstanceListSnapshot = "";
let lastPortMatrixSnapshot = "";
let lastProxyGroupsSnapshot = "";
let slowPollTimer = null;
let fastPollTimer = null;

const configEditor = createYamlEditor(el.configEditor, {
  ariaLabel: "配置 YAML 编辑器",
  onChange() {
    el.configEditor.dataset.dirty = "1";
    setConfigEditorError("");
    renderConfigEditorState();
  },
  onSave() {
    saveActiveConfig();
  },
});
configEditor.setReadOnly(true);

const proxyTooltip = document.createElement("div");
proxyTooltip.id = "proxyTooltip";
proxyTooltip.className = "proxy-tooltip hidden";
proxyTooltip.setAttribute("role", "tooltip");
document.body.append(proxyTooltip);
const proxyTooltipHoverQuery = window.matchMedia("(hover: hover) and (pointer: fine)");

const latency = createLatencyController({
  state,
  el,
  getActive: () => active(),
  showMessage,
  onControlsChange: updateLatencyControls,
  onChipChange(instanceId, groupName, proxyName, kind) {
    if (state.activeId !== instanceId || state.activeTab !== "proxies") return;
    const selected = active();
    for (const chip of el.proxiesList.querySelectorAll(".latency-chip")) {
      if (
        chip.dataset.instanceId === instanceId &&
        chip.dataset.groupName === groupName &&
        chip.dataset.proxyName === proxyName &&
        chip.dataset.kind === kind
      ) {
        latency.applyLatencyChipState(chip, selected, groupName, proxyName, kind);
      }
    }
  },
});

function active() {
  return activeInstance(state);
}

function configEditorDirty() {
  return el.configEditor.dataset.dirty === "1";
}

function hasUnsavedChanges() {
  return state.editDirty || configEditorDirty() || el.subscriptionSettings.dataset.dirty === "1";
}

function confirmDiscardChanges(action) {
  if (!hasUnsavedChanges()) return true;
  return window.confirm(`有未保存的修改。确定放弃并${action}吗？`);
}

function setConfigEditorError(message) {
  const text = message ? localizedMessage(message) : "";
  el.configEditorError.textContent = text;
  el.configEditorError.classList.toggle("hidden", !text);
}

function renderConfigEditorState(selected = active()) {
  const profile = selected ? profileById(state, selected.profileId) : null;
  const isSubscription = Boolean(profile?.subscriptionUrl);
  const dirty = configEditorDirty();
  const contextMatches = Boolean(
    selected
      && el.configEditor.dataset.id === selected.id
      && el.configEditor.dataset.profileId === selected.profileId,
  );
  let text = "正在加载";
  let status = "loading";
  if (!selected) {
    text = "未选择实例";
    status = "idle";
  } else if (saveConfigGate.isRunning()) {
    text = "正在保存";
    status = "saving";
  } else if (!el.configEditorError.classList.contains("hidden")) {
    text = "操作失败，修改未丢失";
    status = "error";
  } else if (isSubscription) {
    text = "订阅缓存，只读";
    status = "readonly";
  } else if (dirty) {
    text = "未保存修改";
    status = "dirty";
  } else if (contextMatches) {
    text = "已保存";
    status = "saved";
  }
  el.configEditorStatus.textContent = text;
  el.configEditorStatus.dataset.state = status;
  el.saveConfig.disabled = !selected || isSubscription || !contextMatches || !dirty || saveConfigGate.isRunning();
  el.discardConfig.disabled = !dirty || saveConfigGate.isRunning();
  el.findConfig.disabled = !selected;
}

function resetConfigEditor() {
  configLoadSeq += 1;
  configEditor.setValue("");
  configEditor.setReadOnly(true);
  el.configEditor.dataset.id = "";
  el.configEditor.dataset.profileId = "";
  el.configEditor.dataset.dirty = "";
  setConfigEditorError("");
  renderConfigEditorState();
}

function showMessage(text, kind = "info") {
  if (messageClearTimer) {
    clearTimeout(messageClearTimer);
    messageClearTimer = null;
  }
  if (!text) {
    el.message.classList.add("hidden");
    el.message.textContent = "";
    return;
  }
  const isError = kind === "error";
  el.message.textContent = localizedMessage(text);
  el.message.className = `message ${isError ? "error" : ""}`;
  el.message.setAttribute("role", isError ? "alert" : "status");
  if (!isError) {
    messageClearTimer = setTimeout(() => showMessage(""), 6000);
  }
}

function renderSubscriptionInfo(profile) {
  el.subscriptionInfo.textContent = "";
  const summary = document.createElement("span");
  summary.textContent = formatSubscriptionInfo(profile);
  el.subscriptionInfo.append(summary);
  if (isHttpUrl(profile.homeUrl)) {
    el.subscriptionInfo.append(document.createTextNode(" · 主页 "));
    const link = document.createElement("a");
    link.href = profile.homeUrl.trim();
    link.target = "_blank";
    link.rel = "noopener noreferrer";
    link.textContent = profile.homeUrl.trim();
    el.subscriptionInfo.append(link);
  }
}

function updateLatencyControls() {
  const selected = active();
  const disabled = !selected || selected.status !== "running" || !state.proxyApply;
  const hasLatencyTarget = state.proxyGroups.some((group) => currentLatencyTarget(group, state.proxyGroups));
  el.testAllLatency.disabled = disabled || !hasLatencyTarget || state.latencyBatchRunning;
  el.testAllRealLatency.disabled = disabled || !hasLatencyTarget || state.latencyBatchRunning;
  for (const button of el.proxiesList.querySelectorAll(".proxy-group-actions button")) {
    const running =
      selected &&
      button.dataset.testable !== "false" &&
      isLatencyRunning(state, selected.id, button.dataset.groupName, button.dataset.proxyName, button.dataset.kind);
    button.disabled = disabled || button.dataset.testable === "false" || running;
    if (running) button.title = "测速中";
    else if (button.dataset.disabledReason !== undefined) button.title = button.dataset.disabledReason;
  }
}

function proxyTooltipButton(target) {
  if (!(target instanceof Element)) return null;
  return target.closest(".proxy-choice");
}

function showProxyTooltip(button) {
  const text = button.dataset.tooltip || "";
  if (!text) return;
  proxyTooltip.textContent = text;
  proxyTooltip.classList.remove("hidden");
  proxyTooltip.style.left = "0px";
  proxyTooltip.style.top = "0px";
  const edge = 8;
  const gap = 8;
  const buttonRect = button.getBoundingClientRect();
  const tooltipRect = proxyTooltip.getBoundingClientRect();
  const maxLeft = Math.max(edge, window.innerWidth - tooltipRect.width - edge);
  const maxTop = Math.max(edge, window.innerHeight - tooltipRect.height - edge);
  let left = Math.min(Math.max(buttonRect.left, edge), maxLeft);
  let top = buttonRect.top - tooltipRect.height - gap;
  if (top < edge) top = buttonRect.bottom + gap;
  top = Math.min(Math.max(top, edge), maxTop);
  proxyTooltip.style.left = `${left}px`;
  proxyTooltip.style.top = `${top}px`;
}

function hideProxyTooltip() {
  proxyTooltip.classList.add("hidden");
}

async function refresh(options = {}) {
  const seq = ++refreshSeq;
  try {
    const [system, profiles, list] = await Promise.all([
      api("/api/system"),
      api("/api/profiles"),
      api("/api/instances"),
    ]);
    if (seq !== refreshSeq) return;
    state.system = system;
    state.profiles = profiles.profiles || [];
    if (!state.bulkRunning || options.forceInstances) {
      state.instances = list.instances || [];
      if (!state.activeId && state.instances.length) state.activeId = state.instances[0].id;
      if (state.activeId && !state.instances.some((item) => item.id === state.activeId)) {
        state.activeId = state.instances[0]?.id || "";
      }
    }
    localStorage.setItem("activeInstance", state.activeId);
    render();
    await refreshActiveDetails({ skipFast: options.periodic });
  } catch (err) {
    if (seq !== refreshSeq) return;
    showMessage(err.message, "error");
  }
}

function render() {
  const selected = active();
  renderSystem();
  renderSelector(selected);
  renderList(selected);
  renderPortMatrix(selected);
  updateBulkControls();
  renderPanels(selected);
  renderOrphanProfiles();
  updateCreateProfileControls();
}

function renderSystem() {
  if (!state.system) return;
  const found = state.system.mihomoFound;
  const appVersion = `Mihomo Fleet v${state.system.appVersion || "dev"}`;
  el.systemLine.textContent = found
    ? `${appVersion} · 控制器 127.0.0.1:${state.system.port} · mihomo ${state.system.version || "已检测到"}`
    : `${appVersion} · 控制器 127.0.0.1:${state.system.port} · 未找到 mihomo`;
  if (!found) {
    el.systemWarning.textContent = "未在 Mihomo Fleet 同目录或 PATH 中找到 mihomo。你仍然可以创建实例，但启动需要同目录二进制文件，或通过 -mihomo 参数指定路径。";
    el.systemWarning.classList.remove("hidden");
  } else {
    el.systemWarning.classList.add("hidden");
  }
}

function instanceSelectorSnapshot(selected) {
  return JSON.stringify({
    selectedId: selected?.id || "",
    items: state.instances.map((item) => [item.id, item.name, item.status]),
  });
}

function renderSelector(selected) {
  if (document.activeElement === el.instanceSelect) return;
  const snapshot = instanceSelectorSnapshot(selected);
  if (snapshot === lastInstanceSelectorSnapshot) return;
  lastInstanceSelectorSnapshot = snapshot;
  el.instanceSelect.innerHTML = "";
  if (!state.instances.length) {
    const opt = document.createElement("option");
    opt.textContent = "暂无实例";
    opt.value = "";
    el.instanceSelect.append(opt);
    return;
  }
  for (const item of state.instances) {
    const opt = document.createElement("option");
    opt.value = item.id;
    opt.textContent = `${item.name}（${statusText(item.status)}）`;
    opt.selected = selected && selected.id === item.id;
    el.instanceSelect.append(opt);
  }
}

function instanceListSnapshot(selected) {
  return JSON.stringify({
    selectedId: selected?.id || "",
    items: state.instances.map((item) => [
      item.id,
      item.name,
      item.status,
      item.mixedPort,
      item.profileName || item.profileId || "",
      selectionSummary(item),
      item.pendingRestart === true,
    ]),
  });
}

function capturedInstanceListFocusKey() {
  return el.instanceList.contains(document.activeElement) ? document.activeElement.dataset.instanceFocus || "" : "";
}

function restoreInstanceListFocus(focusedKey) {
  if (!focusedKey) return;
  const controls = [...el.instanceList.querySelectorAll("[data-instance-focus]")];
  if (!controls.length) return;
  const target = controls.find((node) => node.dataset.instanceFocus === focusedKey) || controls[0];
  target?.focus({ preventScroll: true });
}

function renderList(selected) {
  const snapshot = instanceListSnapshot(selected);
  if (snapshot === lastInstanceListSnapshot) return;
  lastInstanceListSnapshot = snapshot;
  const focusedKey = capturedInstanceListFocusKey();
  el.instanceList.innerHTML = "";
  for (const item of state.instances) {
    const button = document.createElement("button");
    button.className = `instance-row ${selected && selected.id === item.id ? "active" : ""}`;
    button.type = "button";
    button.dataset.instanceFocus = item.id;
    const profile = item.profileName || item.profileId || "未选择配置档";
    const selectedText = selectionSummary(item);
    const choice = selectedText !== "无" ? ` · ${selectedText}` : "";
    button.innerHTML = `
      <div class="row-main">
        <span class="row-name"></span>
        <span class="status ${statusClass(item.status)}"></span>
      </div>
      <div class="row-meta"></div>
    `;
    button.querySelector(".row-name").textContent = item.name;
    button.querySelector(".status").textContent = statusText(item.status);
    button.querySelector(".row-meta").textContent = `混合端口 ${proxyPortLabel(item.mixedPort)} · ${profile}${choice}`;
    if (item.pendingRestart === true && item.status === "running") {
      const hint = document.createElement("span");
      hint.className = "pending-restart-chip";
      hint.textContent = "配置已修改，重启后生效";
      button.querySelector(".row-main").append(hint);
    }
    button.addEventListener("click", () => selectInstance(item.id));
    el.instanceList.append(button);
  }
  restoreInstanceListFocus(focusedKey);
}

function portMatrixSnapshot(selected) {
  return JSON.stringify({
    selectedId: selected?.id || "",
    items: state.instances.map((item) => [item.id, item.name, item.status, item.mixedPort, item.proxyBind || ""]),
  });
}

function renderPortMatrix(selected) {
  const countText = `${state.instances.length} 个出口`;
  if (el.portMatrixCount.textContent !== countText) el.portMatrixCount.textContent = countText;
  const snapshot = portMatrixSnapshot(selected);
  if (snapshot === lastPortMatrixSnapshot) return;
  lastPortMatrixSnapshot = snapshot;
  const focusedKey = el.portMatrixList.contains(document.activeElement)
    ? document.activeElement.dataset.portFocus
    : "";
  el.portMatrixList.innerHTML = "";
  if (!state.instances.length) {
    const empty = document.createElement("li");
    empty.className = "port-empty";
    empty.textContent = "暂无端口";
    el.portMatrixList.append(empty);
    return;
  }
  for (const item of state.instances) {
    const row = document.createElement("li");
    row.className = `port-row ${selected && selected.id === item.id ? "active" : ""}`;

    const selectButton = document.createElement("button");
    selectButton.className = "port-row-select";
    selectButton.type = "button";
    selectButton.dataset.portFocus = `select:${item.id}`;
    selectButton.setAttribute("aria-label", `${item.name}，${statusText(item.status)}，${proxyEndpointText(item)}`);
    if (selected && selected.id === item.id) selectButton.setAttribute("aria-current", "true");
    selectButton.addEventListener("click", () => selectInstance(item.id));

    const top = document.createElement("span");
    top.className = "port-row-top";
    const name = document.createElement("span");
    name.className = "port-row-name";
    name.textContent = item.name;
    const status = document.createElement("span");
    status.className = `status ${statusClass(item.status)}`;
    status.textContent = statusText(item.status);
    top.append(name, status);

    const address = document.createElement("span");
    address.className = "port-address";
    address.textContent = proxyEndpointText(item);
    selectButton.append(top, address);

    const tools = document.createElement("div");
    tools.className = "copy-tools";
    const actions = proxyPort(item.mixedPort) ? proxyCopyActions(item) : proxyCopyPlaceholders();
    for (const action of actions) {
      const button = document.createElement("button");
      button.type = "button";
      button.textContent = action.label;
      button.title = action.title;
      button.disabled = !action.value;
      button.dataset.portFocus = `copy:${item.id}:${action.id}`;
      const unavailable = action.value ? "" : "（端口未分配，无法复制）";
      button.setAttribute("aria-label", `${action.title}：${item.name}${unavailable}`);
      if (action.value) {
        button.addEventListener("click", async () => {
          await copyProxyValue(action.value, action.message);
        });
      }
      tools.append(button);
    }

    row.append(selectButton, tools);
    el.portMatrixList.append(row);
  }
  if (focusedKey) restorePortMatrixFocus(focusedKey);
}

function restorePortMatrixFocus(focusedKey) {
  const controls = [...el.portMatrixList.querySelectorAll("[data-port-focus]")].filter((node) => !node.disabled);
  const instanceId = portFocusInstanceId(focusedKey);
  const target = controls.find((node) => node.dataset.portFocus === focusedKey)
    || controls.find((node) => node.dataset.portFocus === `select:${instanceId}`)
    || controls.find((node) => node.dataset.portFocus?.startsWith("select:"));
  target?.focus({ preventScroll: true });
}

function portFocusInstanceId(focusedKey) {
  return focusedKey.split(":")[1] || "";
}

async function copyProxyValue(value, success) {
  try {
    await writeClipboard(value);
    showMessage(success);
  } catch (err) {
    console.warn("Unable to copy proxy value.", err);
    showMessage("复制失败，请检查浏览器剪贴板权限。", "error");
  }
}

function updateBulkControls() {
  const canStart = state.instances.some((item) => item.status !== "running" && item.status !== "starting");
  const canStop = state.instances.some((item) => item.status === "running");
  el.newBtn.disabled = state.bulkRunning;
  el.emptyCreate.disabled = state.bulkRunning;
  el.startAllBtn.disabled = state.bulkRunning || !canStart;
  el.stopAllBtn.disabled = state.bulkRunning || !canStop;
}

function editFormContainsFocus() {
  return Boolean(el.editForm && el.editForm.contains(document.activeElement));
}

function renderPanels(selected) {
  el.createPanel.classList.toggle("hidden", !state.creating);
  el.emptyPanel.classList.toggle("hidden", state.creating || state.instances.length > 0);
  el.detailPanel.classList.toggle("hidden", state.creating || !selected);
  el.createSubmit.disabled = createGate.isRunning();
  el.saveBasics.disabled = !selected || saveBasicsGate.isRunning();
  if (!selected) {
    renderConfigEditorState(null);
    return;
  }

  el.detailName.textContent = selected.name;
  el.detailMeta.textContent = selected.lastError
    ? localizedMessage(selected.lastError)
    : `${statusText(selected.status)} · ${selected.id}`;
  el.metricStatus.textContent = statusText(selected.status);
  el.metricPid.textContent = selected.pid || "无";
  el.metricMixed.textContent = proxyPortLabel(selected.mixedPort);
  el.metricController.textContent = selected.controllerPort;
  el.overviewMixed.textContent = proxyEndpointText(selected);
  el.overviewProxyBind.textContent = selected.proxyBind || defaultProxyBind;
  el.overviewController.textContent = `127.0.0.1:${selected.controllerPort}`;
  el.overviewMode.textContent = modeLabel(instanceMode(selected));
  el.overviewChain.textContent = chainSummary(selected);
  el.overviewProfile.textContent = selected.profileName || selected.profileId || "无";
  el.overviewUserConfig.textContent = selected.profileConfigPath || selected.userConfigPath;
  el.overviewRuntimeConfig.textContent = selected.runtimeConfigPath;
  el.overviewSelection.textContent = selectionSummary(selected);
  el.pendingRestartHint.classList.toggle(
    "hidden",
    !(selected.pendingRestart === true && selected.status === "running"),
  );
  if ((!state.editDirty || state.editInstanceId !== selected.id) && !editFormContainsFocus()) {
    state.editInstanceId = selected.id;
    state.editDirty = false;
    state.editVersion = 0;
    el.editName.value = selected.name;
    renderProfileOptions(el.editProfile, selected.profileId, false);
    el.editMode.value = instanceMode(selected);
    el.editMixedPort.value = selected.mixedPort;
    el.editProxyBind.value = selected.proxyBind || defaultProxyBind;
    el.editControllerPort.value = selected.controllerPort;
    el.editLocalProxies.value = selected.localProxies || "";
    el.editChain.value = chainToText(selected.chain);
    applyModeFields("edit", el.editMode.value);
  }
  el.startBtn.disabled = state.bulkRunning || selected.status === "running" || selected.status === "starting";
  el.stopBtn.disabled = state.bulkRunning || selected.status !== "running";
  el.restartBtn.disabled = state.bulkRunning;
  el.cloneBtn.disabled = state.bulkRunning || state.cloneRunning;
  el.deleteBtn.disabled = state.bulkRunning;
  updateLatencyControls();
  renderSubscriptionSettings(selected);
  renderConfigEditorState(selected);
}

function applyModeFields(prefix, mode) {
  const chainMode = mode === instanceModes.globalChain;
  el[`${prefix}ChainFields`].classList.toggle("hidden", !chainMode);
}

function renderSubscriptionSettings(selected) {
  const profile = profileById(state, selected.profileId);
  if (!profile) {
    el.profileMeta.textContent = "配置档尚未加载。";
    el.subscriptionSettings.classList.add("hidden");
    configEditor.setReadOnly(true);
    return;
  }
  const isSubscription = Boolean(profile.subscriptionUrl);
  el.profileMeta.textContent = isSubscription
    ? `订阅缓存：${formatProfileUpdate(profile)}`
    : "手写配置：可以直接编辑 YAML。";
  el.subscriptionSettings.classList.toggle("hidden", !isSubscription);
  const editorReady = el.configEditor.dataset.id === selected.id
    && el.configEditor.dataset.profileId === selected.profileId;
  configEditor.setReadOnly(isSubscription || !editorReady);
  if (!isSubscription) return;
  const editing = el.subscriptionSettings.dataset.dirty === "1" && el.subscriptionSettings.dataset.profileId === profile.id;
  el.subscriptionSettings.dataset.profileId = profile.id;
  if (!editing) {
    el.subscriptionUrl.value = profile.subscriptionUrl || "";
    el.subscriptionAutoUpdate.checked = Boolean(profile.autoUpdate);
    el.subscriptionInterval.value = profile.updateIntervalMinutes || "";
  }
  renderSubscriptionInfo(profile);
}

function renderProfileOptions(select, selectedId, allowNew) {
  const current = selectedId || select.value;
  select.innerHTML = "";
  if (allowNew) {
    const opt = document.createElement("option");
    opt.value = newProfileValue;
    opt.textContent = "创建新配置档";
    select.append(opt);
  }
  for (const profile of state.profiles) {
    const opt = document.createElement("option");
    opt.value = profile.id;
    opt.textContent = profileOptionLabel(profile, profileReferenceCount(state, profile.id));
    select.append(opt);
  }
  if (current && [...select.options].some((opt) => opt.value === current)) {
    select.value = current;
  } else if (allowNew && state.profiles.length === 0) {
    select.value = newProfileValue;
  } else if (select.options.length > 0) {
    select.value = select.options[allowNew && state.profiles.length ? 1 : 0].value;
  }
}

function renderOrphanProfiles() {
  const orphans = orphanProfiles(state);
  const current = el.orphanProfileSelect.value;
  el.orphanProfileSelect.innerHTML = "";
  if (!orphans.length) {
    const opt = document.createElement("option");
    opt.value = "";
    opt.textContent = "没有可清理的配置档";
    el.orphanProfileSelect.append(opt);
    el.orphanProfileSelect.disabled = true;
    el.deleteOrphanProfileBtn.disabled = true;
    return;
  }
  el.orphanProfileSelect.disabled = false;
  el.deleteOrphanProfileBtn.disabled = false;
  for (const profile of orphans) {
    const opt = document.createElement("option");
    opt.value = profile.id;
    opt.textContent = profileOptionLabel(profile, 0);
    el.orphanProfileSelect.append(opt);
  }
  if (current && orphans.some((profile) => profile.id === current)) {
    el.orphanProfileSelect.value = current;
  }
}

async function updateCreateProfileControls() {
  if (!el.createProfile.options.length) {
    renderProfileOptions(el.createProfile, el.createProfile.value, true);
  }
  const createNew = el.createProfile.value === newProfileValue || state.profiles.length === 0;
  const subscriptionMode = state.createSource === "subscription" && createNew;
  const chainMode = el.createMode.value === instanceModes.globalChain;
  el.createSourceTabs.classList.toggle("hidden", !createNew);
  el.createManualMode.classList.toggle("active", state.createSource === "manual");
  el.createSubscriptionMode.classList.toggle("active", state.createSource === "subscription");
  el.createSubscriptionFields.classList.toggle("hidden", !subscriptionMode);
  el.createConfigWrap.classList.toggle("hidden", subscriptionMode || chainMode);
  applyModeFields("create", el.createMode.value);
  el.createProfileName.disabled = !createNew;
  el.createConfig.disabled = !createNew || subscriptionMode || chainMode;
  el.createSubscriptionUrl.disabled = !subscriptionMode;
  el.createAutoUpdate.disabled = !subscriptionMode;
  el.createUpdateInterval.disabled = !subscriptionMode;
  if (subscriptionMode) {
    createProfileConfigSeq += 1;
    if (!el.createUpdateInterval.value) el.createUpdateInterval.value = "360";
    return;
  }
  if (createNew) {
    createProfileConfigSeq += 1;
    if (!el.createConfig.value) el.createConfig.value = defaultConfig;
    return;
  }
  const selectedProfile = profileById(state, el.createProfile.value);
  if (!selectedProfile || el.createConfig.dataset.profileId === selectedProfile.id) return;
  const requestSeq = ++createProfileConfigSeq;
  el.createConfig.dataset.profileId = selectedProfile.id;
  try {
    const payload = await api(`/api/profiles/${selectedProfile.id}/config`);
    if (shouldApplyCreateProfileConfig({
      requestSeq,
      currentSeq: createProfileConfigSeq,
      requestedProfileId: selectedProfile.id,
      currentProfileId: el.createProfile.value,
    })) {
      el.createConfig.value = payload.config || "";
    }
  } catch (err) {
    if (requestSeq === createProfileConfigSeq && el.createProfile.value === selectedProfile.id) {
      el.createConfig.dataset.profileId = "";
      showMessage(err.message, "error");
    }
  }
}

async function fillSuggestedPorts() {
  if (el.createMixedPort.value && el.createControllerPort.value) return;
  try {
    const ports = await api("/api/ports/suggest");
    el.createMixedPort.placeholder = ports.mixedPort ? `建议 ${ports.mixedPort}` : "自动";
    el.createControllerPort.placeholder = ports.controllerPort ? `建议 ${ports.controllerPort}` : "自动";
  } catch (err) {
    console.warn("Unable to load suggested ports.", err);
  }
}

function clearActiveDetailCache() {
  state.editInstanceId = "";
  state.editDirty = false;
  state.editVersion = 0;
  resetConfigEditor();
  el.subscriptionSettings.dataset.profileId = "";
  el.subscriptionSettings.dataset.dirty = "";
  el.logs.dataset.instanceId = "";
  state.proxyGroups = [];
  state.proxyApply = false;
  state.latencyBatchRunning = false;
  state.latencyBatchToken += 1;
  lastProxyGroupsSnapshot = "";
  el.proxiesList.innerHTML = "";
}

function markEditFormDirty() {
  const selected = active();
  state.editInstanceId = selected?.id || state.editInstanceId;
  state.editDirty = true;
  state.editVersion += 1;
}

function selectInstance(id) {
  if (state.activeId !== id) {
    if (!confirmDiscardChanges("切换实例")) {
      el.instanceSelect.value = state.activeId;
      return false;
    }
    clearLatencyStateForInstance(state, state.activeId);
    clearActiveDetailCache();
  }
  state.activeId = id;
  state.creating = false;
  localStorage.setItem("activeInstance", id);
  render();
  refreshActiveDetails();
  return true;
}

function showCreate() {
  if (!confirmDiscardChanges("新建实例")) return false;
  if (hasUnsavedChanges()) clearActiveDetailCache();
  state.creating = true;
  state.createSource = "manual";
  el.createName.value = "";
  el.createMode.value = instanceModes.rule;
  el.createMixedPort.value = "";
  el.createProxyBind.value = defaultProxyBind;
  el.createControllerPort.value = "";
  renderProfileOptions(el.createProfile, state.profiles[0]?.id || newProfileValue, true);
  el.createProfileName.value = "";
  el.createSubscriptionUrl.value = "";
  el.createAutoUpdate.checked = true;
  el.createUpdateInterval.value = "360";
  el.createConfig.value = defaultConfig;
  el.createConfig.dataset.profileId = "";
  el.createLocalProxies.value = "";
  el.createChain.value = "";
  showMessage("");
  render();
  fillSuggestedPorts();
  return true;
}

async function refreshActiveDetails(options = {}) {
  const selected = active();
  if (!selected || state.creating) return;
  if (
    !configEditorDirty() &&
    (!configEditor.getValue() ||
      el.configEditor.dataset.id !== selected.id ||
      el.configEditor.dataset.profileId !== selected.profileId)
  ) {
    const requestSeq = ++configLoadSeq;
    renderConfigEditorState(selected);
    try {
      const cfg = await api(`/api/instances/${selected.id}/config`);
      const current = active();
      if (requestSeq !== configLoadSeq || !shouldApplyConfigLoad({
        requestedInstanceId: selected.id,
        requestedProfileId: selected.profileId,
        activeInstanceId: current?.id || "",
        activeProfileId: current?.profileId || "",
        dirty: configEditorDirty(),
      })) return;
      configEditor.setValue(cfg.config);
      el.configEditor.dataset.id = selected.id;
      el.configEditor.dataset.profileId = selected.profileId;
      el.configEditor.dataset.dirty = "";
      configEditor.setReadOnly(Boolean(profileById(state, selected.profileId)?.subscriptionUrl));
      setConfigEditorError("");
      renderConfigEditorState(selected);
    } catch (err) {
      if (requestSeq === configLoadSeq) {
        setConfigEditorError(err.message);
        renderConfigEditorState(selected);
        showMessage(err.message, "error");
      }
    }
  }
  if (options.skipFast) return;
  await pollActiveTab();
}

async function pollActiveTab() {
  if (state.activeTab === "logs") await refreshLogs();
  if (state.activeTab === "proxies") await refreshProxies();
}

async function refreshLogs() {
  const selected = active();
  if (!selected) return;
  try {
    const payload = await api(`/api/instances/${selected.id}/logs`);
    const shouldStick = el.logs.dataset.instanceId !== selected.id || isLogScrolledToBottom();
    const text = (payload.lines || []).join("\n") || "还没有进程日志。";
    if (el.logs.textContent !== text) el.logs.textContent = text;
    el.logs.dataset.instanceId = selected.id;
    if (shouldStick) el.logs.scrollTop = el.logs.scrollHeight;
  } catch (err) {
    el.logs.dataset.instanceId = "";
    el.logs.textContent = localizedMessage(err.message);
  }
}

function isLogScrolledToBottom() {
  return el.logs.scrollHeight - el.logs.scrollTop - el.logs.clientHeight <= logStickThreshold;
}

async function loadProfileProxyGroups(selected) {
  if (!selected?.profileId) return [];
  const profileId = encodeURIComponent(selected.profileId);
  const instanceId = encodeURIComponent(selected.id);
  const payload = await api(`/api/profiles/${profileId}/proxies?instanceId=${instanceId}`);
  return payload.groups || [];
}

async function loadProfileProxyGroupsForRuntime(selected) {
  try {
    return await loadProfileProxyGroups(selected);
  } catch (err) {
    console.warn("Unable to load profile proxy order; using mihomo runtime order.", err);
    return [];
  }
}

async function refreshProxies() {
  const selected = active();
  if (!selected) return;
  const seq = ++proxiesRequestSeq;
  try {
    let groups = [];
    let apply = false;
    if (selected.status === "running") {
      const [payload, profileGroups] = await Promise.all([
        api(`/api/mihomo/${selected.id}/proxies`),
        loadProfileProxyGroupsForRuntime(selected),
      ]);
      if (seq !== proxiesRequestSeq || state.activeId !== selected.id) return;
      const proxies = payload.proxies || {};
      groups = alignProxyGroupsToProfileOrder(
        Object.values(proxies).filter((item) => Array.isArray(item.all)),
        profileGroups,
      );
      groups = filterRuntimeProxyGroups(selected, groups);
      apply = true;
      el.proxySource.textContent = "当前读取运行中的 mihomo 节点，选择后立即应用并保存。";
    } else {
      groups = await loadProfileProxyGroups(selected);
      if (seq !== proxiesRequestSeq || state.activeId !== selected.id) return;
      el.proxySource.textContent = "当前读取缓存配置，选择会保存到实例，下次启动后自动恢复。";
    }
    state.proxyGroups = groups;
    state.proxyApply = apply;
    pruneLatencyResultsForGroups(state, selected.id, groups);
    updateLatencyControls();
    if (!groups.length) {
      el.proxiesList.innerHTML = `<div class="warning">没有可显示的节点组。使用 proxy-providers 的订阅需要启动实例后读取 mihomo 运行态节点。</div>`;
      lastProxyGroupsSnapshot = "";
      return;
    }
    renderProxyGroups(groups, apply);
  } catch (err) {
    if (seq !== proxiesRequestSeq || state.activeId !== selected.id) return;
    el.proxiesList.innerHTML = `<div class="message error">${escapeHTML(localizedMessage(err.message))}</div>`;
    lastProxyGroupsSnapshot = "";
  }
}

function proxyGroupsRenderSnapshot(groups, apply, filter) {
  const selected = active();
  const labelSources = proxyLabelSources(state.profiles, state.instances);
  const latencyBits = groups.map((group) => {
    const currentName = currentLatencyTarget(group, state.proxyGroups);
    if (!currentName) return null;
    return [
      group.name,
      currentName,
      selected ? latencyResult(state, selected.id, group.name, currentName, latencyKinds.url) : null,
      selected ? isLatencyRunning(state, selected.id, group.name, currentName, latencyKinds.url) : false,
      selected ? latencyResult(state, selected.id, group.name, currentName, latencyKinds.real) : null,
      selected ? isLatencyRunning(state, selected.id, group.name, currentName, latencyKinds.real) : false,
    ];
  });
  return JSON.stringify({ instanceId: selected?.id || "", groups, apply, filter, labelSources, latencyBits });
}

function proxyFocusKey(groupName, proxyName) {
  return `${groupName}${latencyKeySeparator}${proxyName}`;
}

function latencyButtonFocusKey(groupName, kind) {
  return `${groupName}${latencyKeySeparator}latency-btn${latencyKeySeparator}${kind}`;
}

function capturedProxyFocusKey() {
  if (!el.proxiesList.contains(document.activeElement)) return "";
  return document.activeElement.dataset.proxyFocus || "";
}

function isLatencyFocusKey(key) {
  return String(key || "").includes(`${latencyKeySeparator}latency-btn${latencyKeySeparator}`);
}

function restoreProxyListFocus(focusedKey) {
  if (!focusedKey) return;
  const controls = [...el.proxiesList.querySelectorAll("[data-proxy-focus]")].filter((node) => !node.disabled);
  if (!controls.length) return;
  const groupPrefix = `${focusedKey.split(latencyKeySeparator)[0] || ""}${latencyKeySeparator}`;
  const sameKindControls = controls.filter((node) => isLatencyFocusKey(node.dataset.proxyFocus) === isLatencyFocusKey(focusedKey));
  const pool = sameKindControls.length ? sameKindControls : controls;
  const target = pool.find((node) => node.dataset.proxyFocus === focusedKey)
    || pool.find((node) => node.dataset.proxyFocus?.startsWith(groupPrefix))
    || pool[0];
  target?.focus({ preventScroll: true });
}

function renderProxyGroups(groups, apply) {
  const selected = active();
  const filter = el.proxyFilter.value.trim().toLowerCase();
  const snapshot = proxyGroupsRenderSnapshot(groups, apply, filter);
  if (snapshot === lastProxyGroupsSnapshot) return;
  lastProxyGroupsSnapshot = snapshot;
  const labelSources = proxyLabelSources(state.profiles, state.instances);
  const focusedKey = capturedProxyFocusKey();
  hideProxyTooltip();
  el.proxiesList.innerHTML = "";
  for (const group of groups) {
    const names = (group.all || []).filter((name) => !filter || name.toLowerCase().includes(filter) || group.name.toLowerCase().includes(filter));
    if (!names.length) continue;
    const selectableGroup = isSelectableProxyGroup(group);
    const section = document.createElement("section");
    section.className = "proxy-group";

    const head = document.createElement("div");
    head.className = "proxy-group-head";
    const title = document.createElement("strong");
    title.textContent = group.name;

    const metaWrap = document.createElement("div");
    metaWrap.className = "proxy-group-meta";
    const meta = document.createElement("span");
    meta.textContent = group.now ? `当前 ${group.now}` : `${names.length} 个节点`;
    metaWrap.append(meta);
    const currentName = currentLatencyTarget(group, state.proxyGroups);
    if (currentName) {
      const currentChips = document.createElement("span");
      currentChips.className = "latency-chips current";
      currentChips.append(
        latency.renderLatencyChip(selected, group.name, currentName, latencyKinds.url),
        latency.renderLatencyChip(selected, group.name, currentName, latencyKinds.real),
      );
      metaWrap.append(currentChips);
    }

    const actions = document.createElement("div");
    actions.className = "proxy-group-actions";
    const disabledReason = !apply ? "请先启动实例再测速" : !currentName ? "当前节点不可测速" : "";
    const latencyButton = document.createElement("button");
    latencyButton.type = "button";
    latencyButton.textContent = "测速";
    latencyButton.title = disabledReason;
    latencyButton.dataset.disabledReason = disabledReason;
    latencyButton.dataset.groupName = group.name;
    latencyButton.dataset.proxyName = currentName;
    latencyButton.dataset.kind = latencyKinds.url;
    latencyButton.dataset.testable = currentName ? "true" : "false";
    latencyButton.dataset.proxyFocus = latencyButtonFocusKey(group.name, latencyKinds.url);
    latencyButton.disabled = !apply || !currentName || (selected && isLatencyRunning(state, selected.id, group.name, currentName, latencyKinds.url));
    latencyButton.addEventListener("click", () => latency.testGroupLatency(group, latencyKinds.url));
    const realLatencyButton = document.createElement("button");
    realLatencyButton.type = "button";
    realLatencyButton.textContent = "真延迟";
    realLatencyButton.title = disabledReason;
    realLatencyButton.dataset.disabledReason = disabledReason;
    realLatencyButton.dataset.groupName = group.name;
    realLatencyButton.dataset.proxyName = currentName;
    realLatencyButton.dataset.kind = latencyKinds.real;
    realLatencyButton.dataset.testable = currentName ? "true" : "false";
    realLatencyButton.dataset.proxyFocus = latencyButtonFocusKey(group.name, latencyKinds.real);
    realLatencyButton.disabled = !apply || !currentName || (selected && isLatencyRunning(state, selected.id, group.name, currentName, latencyKinds.real));
    realLatencyButton.addEventListener("click", () => latency.testGroupLatency(group, latencyKinds.real));
    actions.append(latencyButton, realLatencyButton);
    head.append(title, metaWrap, actions);

    const grid = document.createElement("div");
    grid.className = "proxy-grid";
    for (const name of names) {
      const label = splitProxyLabel(name, labelSources);
      const button = document.createElement("button");
      button.type = "button";
      button.className = `proxy-choice ${group.now === name ? "selected" : ""}`;
      button.dataset.tooltip = name;
      button.dataset.proxyFocus = proxyFocusKey(group.name, name);
      button.setAttribute("aria-label", name);
      button.disabled = !selectableGroup;
      if (selectableGroup) {
        button.setAttribute("aria-pressed", group.now === name ? "true" : "false");
        button.addEventListener("click", () => selectProxy(group.name, name, apply));
      }
      const nameLabel = document.createElement("span");
      nameLabel.className = "proxy-name";
      nameLabel.textContent = label.name;
      button.append(nameLabel);
      if (label.source) {
        const sourceLabel = document.createElement("span");
        sourceLabel.className = "proxy-source";
        sourceLabel.textContent = label.source;
        button.append(sourceLabel);
      }
      grid.append(button);
    }
    section.append(head, grid);
    el.proxiesList.append(section);
  }
  if (!el.proxiesList.children.length) {
    el.proxiesList.innerHTML = `<div class="warning">没有匹配的节点。</div>`;
  }
  restoreProxyListFocus(focusedKey);
}

async function selectProxy(group, proxy, apply) {
  const selected = active();
  if (!selected) return;
  try {
    const updated = await api(`/api/instances/${selected.id}/selection`, {
      method: "POST",
      body: JSON.stringify({ group, proxy, apply }),
    });
    state.instances = state.instances.map((item) => (item.id === updated.id ? updated : item));
    showMessage(apply ? `已应用并保存 ${group} -> ${proxy}。` : `已保存 ${group} -> ${proxy}。`);
    render();
    await refreshProxies();
  } catch (err) {
    showMessage(err.message, "error");
  }
}

async function runAction(path, success) {
  try {
    await api(path, { method: "POST" });
    showMessage(success);
    await refresh();
  } catch (err) {
    showMessage(err.message, "error");
    await refresh();
  }
}

async function runBulkAction(action) {
  try {
    state.bulkRunning = true;
    updateBulkControls();
    const payload = await api(`/api/instances?action=${encodeURIComponent(action)}`, { method: "POST" });
    state.instances = payload.instances || state.instances;
    showMessage(formatBatchMessage(action, payload), payload.failed ? "error" : "info");
    render();
    await refreshActiveDetails();
  } catch (err) {
    showMessage(err.message, "error");
    await refresh({ forceInstances: true });
  } finally {
    state.bulkRunning = false;
    updateBulkControls();
  }
}

function setActiveTab(button) {
  state.activeTab = button.dataset.tab;
  for (const tab of el.tabButtons) {
    const isActive = tab === button;
    tab.classList.toggle("active", isActive);
    tab.setAttribute("aria-selected", isActive ? "true" : "false");
    tab.tabIndex = isActive ? 0 : -1;
  }
  document.querySelectorAll(".tab-panel").forEach((panel) => panel.classList.add("hidden"));
  document.querySelector(`#tab-${state.activeTab}`).classList.remove("hidden");
}

async function createInstanceFromForm() {
  if (!createGate.begin()) return;
  render();
  try {
    const createNewProfile = el.createProfile.value === newProfileValue || state.profiles.length === 0;
    const isSubscription = createNewProfile && state.createSource === "subscription";
    const payload = {
      name: el.createName.value.trim(),
      profileId: createNewProfile ? "" : el.createProfile.value,
      profileName: createNewProfile
        ? el.createProfileName.value.trim() || `${el.createName.value.trim() || "默认"}配置档`
        : "",
      config: createNewProfile && !isSubscription ? el.createConfig.value : "",
      subscriptionUrl: isSubscription ? el.createSubscriptionUrl.value.trim() : "",
      autoUpdate: isSubscription ? el.createAutoUpdate.checked : false,
      updateIntervalMinutes: isSubscription ? Number(el.createUpdateInterval.value) || 0 : 0,
      mixedPort: Number(el.createMixedPort.value) || 0,
      proxyBind: el.createProxyBind.value.trim(),
      controllerPort: Number(el.createControllerPort.value) || 0,
      mode: el.createMode.value,
      localProxies: el.createMode.value === instanceModes.globalChain ? el.createLocalProxies.value : "",
      chain: el.createMode.value === instanceModes.globalChain ? chainFromText(el.createChain.value) : [],
    };
    const created = await api("/api/instances", {
      method: "POST",
      body: JSON.stringify(payload),
    });
    state.activeId = created.id;
    localStorage.setItem("activeInstance", created.id);
    state.creating = false;
    clearActiveDetailCache();
    showMessage("实例已创建。");
    await refresh();
  } catch (err) {
    showMessage(err.message, "error");
  } finally {
    createGate.end();
    render();
  }
}

async function saveActiveBasics() {
  const selected = active();
  if (!selected || !saveBasicsGate.begin()) return;
  const profileChanged = selected.profileId !== el.editProfile.value;
  if (profileChanged && configEditorDirty() && !window.confirm("当前 YAML 尚未保存。改绑配置档后将丢弃这些修改，确定继续吗？")) {
    saveBasicsGate.end();
    render();
    return;
  }
  const editVersion = state.editVersion;
  render();
  try {
    await api(`/api/instances/${selected.id}`, {
      method: "PUT",
      body: JSON.stringify({
        name: el.editName.value.trim(),
        profileId: el.editProfile.value,
        mixedPort: Number(el.editMixedPort.value),
        proxyBind: el.editProxyBind.value.trim(),
        controllerPort: Number(el.editControllerPort.value),
        mode: el.editMode.value,
        localProxies: el.editMode.value === instanceModes.globalChain ? el.editLocalProxies.value : "",
        chain: el.editMode.value === instanceModes.globalChain ? chainFromText(el.editChain.value) : [],
      }),
    });
    if (state.editInstanceId === selected.id && state.editVersion === editVersion) {
      state.editDirty = false;
    }
    if (profileChanged) resetConfigEditor();
    showMessage("基础信息已保存。");
    await refresh();
  } catch (err) {
    showMessage(err.message, "error");
  } finally {
    saveBasicsGate.end();
    render();
  }
}

async function saveActiveConfig() {
  const selected = active();
  if (!selected || !configEditorDirty() || !saveConfigGate.begin()) return;
  const savedInstanceId = selected.id;
  const savedProfileId = el.configEditor.dataset.profileId;
  if (
    profileById(state, selected.profileId)?.subscriptionUrl
    || el.configEditor.dataset.id !== savedInstanceId
    || savedProfileId !== selected.profileId
  ) {
    saveConfigGate.end();
    setConfigEditorError("配置档已变化，请放弃修改并重新加载。");
    renderConfigEditorState(selected);
    return;
  }
  const savedVersion = configEditor.getVersion();
  const config = configEditor.getValue();
  setConfigEditorError("");
  renderConfigEditorState(selected);
  try {
    await api(`/api/instances/${savedInstanceId}`, {
      method: "PUT",
      body: JSON.stringify({ config, expectedProfileId: savedProfileId }),
    });
    showMessage("配置已保存。");
    const current = active();
    if (canClearSavedConfig({
      savedInstanceId,
      savedProfileId,
      savedVersion,
      activeInstanceId: current?.id || "",
      activeProfileId: current?.profileId || "",
      currentVersion: configEditor.getVersion(),
    })) {
      el.configEditor.dataset.dirty = "";
      setConfigEditorError("");
    }
    await refresh();
  } catch (err) {
    setConfigEditorError(err.message);
    showMessage(err.message, "error");
  } finally {
    saveConfigGate.end();
    renderConfigEditorState();
    render();
  }
}

function bindEvents() {
  el.tabButtons.forEach((button) => {
    button.addEventListener("click", async () => {
      setActiveTab(button);
      await refreshActiveDetails();
    });
  });

  el.tabList.addEventListener("keydown", (event) => {
    if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
    const currentIndex = el.tabButtons.indexOf(document.activeElement);
    if (currentIndex === -1) return;
    event.preventDefault();
    const delta = event.key === "ArrowRight" ? 1 : -1;
    const nextButton = el.tabButtons[(currentIndex + delta + el.tabButtons.length) % el.tabButtons.length];
    nextButton.focus();
    setActiveTab(nextButton);
    refreshActiveDetails();
  });

  el.instanceSelect.addEventListener("change", (event) => selectInstance(event.target.value));
  el.newBtn.addEventListener("click", showCreate);
  el.startAllBtn.addEventListener("click", () => runBulkAction("start-all"));
  el.stopAllBtn.addEventListener("click", () => runBulkAction("stop-all"));
  el.emptyCreate.addEventListener("click", showCreate);
  el.createProfile.addEventListener("change", () => {
    el.createConfig.dataset.profileId = "";
    updateCreateProfileControls();
  });
  el.createMode.addEventListener("change", updateCreateProfileControls);
  el.createManualMode.addEventListener("click", () => {
    state.createSource = "manual";
    updateCreateProfileControls();
  });
  el.createSubscriptionMode.addEventListener("click", () => {
    state.createSource = "subscription";
    updateCreateProfileControls();
  });
  el.createCancel.addEventListener("click", () => {
    state.creating = false;
    render();
  });
  el.createSubmit.addEventListener("click", createInstanceFromForm);

  el.startBtn.addEventListener("click", () => {
    const selected = active();
    if (selected) runAction(`/api/instances/${selected.id}/start`, "已请求启动。");
  });
  el.stopBtn.addEventListener("click", () => {
    const selected = active();
    if (selected) runAction(`/api/instances/${selected.id}/stop`, "已请求停止。");
  });
  el.restartBtn.addEventListener("click", () => {
    const selected = active();
    if (selected) runAction(`/api/instances/${selected.id}/restart`, "已请求重启。");
  });

  el.cloneBtn.addEventListener("click", async () => {
    const selected = active();
    if (!selected || state.cloneRunning) return;
    if (!confirmDiscardChanges("克隆并切换到新实例")) return;
    try {
      state.cloneRunning = true;
      el.cloneBtn.disabled = true;
      const created = await api(`/api/instances/${selected.id}/clone`, {
        method: "POST",
        body: JSON.stringify({}),
      });
      state.activeId = created.id;
      localStorage.setItem("activeInstance", created.id);
      state.creating = false;
      clearLatencyStateForInstance(state, selected.id);
      clearActiveDetailCache();
      showMessage(`已克隆 ${selected.name}。`);
      await refresh();
    } catch (err) {
      showMessage(err.message, "error");
    } finally {
      state.cloneRunning = false;
      render();
    }
  });

  el.deleteBtn.addEventListener("click", async () => {
    const selected = active();
    if (!selected) return;
    const dirtyWarning = hasUnsavedChanges() ? " 未保存的修改也会丢失。" : "";
    if (!confirm(`确定删除 ${selected.name}？${dirtyWarning}`)) return;
    try {
      await api(`/api/instances/${selected.id}`, { method: "DELETE" });
      state.activeId = "";
      clearActiveDetailCache();
      showMessage("实例已删除。");
      await refresh();
    } catch (err) {
      showMessage(err.message, "error");
    }
  });

  [
    el.editName,
    el.editMixedPort,
    el.editProxyBind,
    el.editControllerPort,
    el.editLocalProxies,
    el.editChain,
  ].forEach((input) => input.addEventListener("input", markEditFormDirty));
  el.editProfile.addEventListener("change", markEditFormDirty);
  el.editMode.addEventListener("change", () => {
    markEditFormDirty();
    applyModeFields("edit", el.editMode.value);
  });

  el.saveBasics.addEventListener("click", saveActiveBasics);
  el.saveConfig.addEventListener("click", saveActiveConfig);
  el.findConfig.addEventListener("click", () => configEditor.focusSearch());
  el.discardConfig.addEventListener("click", async () => {
    if (!configEditorDirty() || !window.confirm("确定放弃当前 YAML 修改并重新加载吗？")) return;
    resetConfigEditor();
    await refreshActiveDetails();
  });

  el.deleteOrphanProfileBtn.addEventListener("click", async () => {
    const profileId = el.orphanProfileSelect.value;
    const profile = profileId ? profileById(state, profileId) : null;
    if (!profile) return;
    if (!confirm(`确定删除配置档 ${profile.name}？此操作不可撤销。`)) return;
    try {
      await api(`/api/profiles/${profile.id}`, { method: "DELETE" });
      if (el.createConfig.dataset.profileId === profile.id) el.createConfig.dataset.profileId = "";
      showMessage("配置档已删除。");
      await refresh({ forceInstances: true });
    } catch (err) {
      showMessage(err.message, "error");
    }
  });

  el.saveProfileSettings.addEventListener("click", async () => {
    const selected = active();
    if (!selected) return;
    try {
      const profile = await api(`/api/profiles/${selected.profileId}`, {
        method: "PUT",
        body: JSON.stringify({
          subscriptionUrl: el.subscriptionUrl.value.trim(),
          autoUpdate: el.subscriptionAutoUpdate.checked,
          updateIntervalMinutes: Number(el.subscriptionInterval.value) || 0,
        }),
      });
      state.profiles = state.profiles.map((item) => (item.id === profile.id ? profile : item));
      el.subscriptionSettings.dataset.dirty = "";
      showMessage("订阅设置已保存。");
      render();
    } catch (err) {
      showMessage(err.message, "error");
    }
  });

  el.refreshSubscription.addEventListener("click", async () => {
    const selected = active();
    if (!selected) return;
    try {
      el.refreshSubscription.disabled = true;
      const profile = await api(`/api/profiles/${selected.profileId}/refresh`, { method: "POST" });
      state.profiles = state.profiles.map((item) => (item.id === profile.id ? profile : item));
      el.subscriptionSettings.dataset.dirty = "";
      showMessage("订阅已更新。运行中的实例需要重启后使用新的缓存配置。");
      resetConfigEditor();
      render();
      await refreshActiveDetails();
    } catch (err) {
      showMessage(err.message, "error");
    } finally {
      el.refreshSubscription.disabled = false;
    }
  });

  for (const input of [el.subscriptionUrl, el.subscriptionAutoUpdate, el.subscriptionInterval]) {
    input.addEventListener("input", () => {
      el.subscriptionSettings.dataset.dirty = "1";
    });
    input.addEventListener("change", () => {
      el.subscriptionSettings.dataset.dirty = "1";
    });
  }

  el.proxyFilter.addEventListener("input", () => {
    if (state.activeTab === "proxies") renderProxyGroups(state.proxyGroups, state.proxyApply);
  });

  el.proxiesList.addEventListener("pointerover", (event) => {
    if (!proxyTooltipHoverQuery.matches) return;
    const button = proxyTooltipButton(event.target);
    if (!button || !el.proxiesList.contains(button)) return;
    if (event.relatedTarget instanceof Node && button.contains(event.relatedTarget)) return;
    showProxyTooltip(button);
  });
  el.proxiesList.addEventListener("pointerout", (event) => {
    const button = proxyTooltipButton(event.target);
    if (!button) return;
    if (event.relatedTarget instanceof Node && button.contains(event.relatedTarget)) return;
    hideProxyTooltip();
  });
  el.proxiesList.addEventListener("focusin", (event) => {
    const button = proxyTooltipButton(event.target);
    if (button && el.proxiesList.contains(button)) showProxyTooltip(button);
  });
  el.proxiesList.addEventListener("focusout", (event) => {
    if (proxyTooltipButton(event.target)) hideProxyTooltip();
  });

  window.addEventListener("resize", hideProxyTooltip);
  window.addEventListener("scroll", hideProxyTooltip, true);
  window.addEventListener("beforeunload", (event) => {
    if (!hasUnsavedChanges()) return;
    event.preventDefault();
    event.returnValue = "";
  });

  const storedLatencyUrl = localStorage.getItem("fleetLatencyUrl");
  el.latencyUrl.value = normalizeStoredLatencyUrl(storedLatencyUrl);
  el.latencyTimeout.value = normalizeStoredLatencyTimeout(localStorage.getItem("fleetLatencyTimeout"), storedLatencyUrl);
  el.latencyUrl.addEventListener("change", latency.persistLatencySettings);
  el.latencyTimeout.addEventListener("change", latency.persistLatencySettings);
  el.testAllLatency.addEventListener("click", () => latency.testAllLatency(latencyKinds.url));
  el.testAllRealLatency.addEventListener("click", () => latency.testAllLatency(latencyKinds.real));
}

function scheduleSlowPoll(delay = slowPollIntervalMs) {
  clearTimeout(slowPollTimer);
  slowPollTimer = null;
  if (document.hidden) return;
  slowPollTimer = setTimeout(runSlowPoll, delay);
}

async function runSlowPoll() {
  if (!document.hidden) await refresh({ periodic: true });
  scheduleSlowPoll();
}

function scheduleFastPoll(delay = fastPollIntervalMs) {
  clearTimeout(fastPollTimer);
  fastPollTimer = null;
  if (document.hidden) return;
  fastPollTimer = setTimeout(runFastPoll, delay);
}

async function runFastPoll() {
  if (!document.hidden) await pollActiveTab();
  scheduleFastPoll();
}

document.addEventListener("visibilitychange", () => {
  if (document.hidden) return;
  runSlowPoll();
  runFastPoll();
});

el.createConfig.value = defaultConfig;
bindEvents();
refresh();
scheduleSlowPoll();
scheduleFastPoll();
