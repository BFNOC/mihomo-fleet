import "./styles.css";
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
  slowPollIntervalMs,
} from "./constants.ts";
import type { LatencyKind } from "./constants.ts";
import { api, writeClipboard } from "./api.ts";
import { renderDashboard, sampleFleet, setConnectionQuery, setGeoResolver } from "./dashboard.ts";
import type { ConnectionsFetchPayload, GeoLookupResult } from "./dashboard.ts";
import { bindElements } from "./dom.ts";
import type { DomElements } from "./dom.ts";
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
  proxyEndpointText,
  proxyLabelSources,
  proxyPortLabel,
  selectionSummary,
  splitProxyLabel,
} from "./format.ts";
import type { BatchActionPayload } from "./format.ts";
import { escapeHTML, localizedMessage, statusText } from "./i18n.ts";
import { createLatencyController } from "./latency.ts";
import {
  activeInstance,
  clearLatencyStateForInstance,
  isLatencyRunning,
  latencyResult,
  profileById,
  profileReferenceCount,
  pruneLatencyResultsForGroups,
} from "./state.ts";
import type { FleetInstance, FleetProfile, FleetProxyGroup, FleetState, FleetSystemStatus, FleetTab } from "./state.ts";
import { banner, chrome, registerActions } from "./bridge.ts";
import { store } from "./store.ts";
import {
  canClearSavedProfileConfig,
  createActionGate,
  createYamlEditor,
  profileOptionLabel,
  shouldApplyProfileConfigLoad,
  shouldApplyProfileOperation,
} from "./yaml-editor.ts";
import type { ActionGate } from "./yaml-editor.ts";

// Aliased (not reassigned -- `state` stays `const`) to the same reactive
// object store.ts wraps in reactive(createState()). Vue's chrome components
// read that object directly, so mutating fields on `state` here is what
// makes their re-render happen with no explicit render() call needed on
// their side. See store.ts for the contract.
const state = store;
const el = bindElements();

const createGate = createActionGate();
const saveBasicsGate = createActionGate();
const saveProfileGate = createActionGate();
const deleteProfileGate = createActionGate();
const refreshSubscriptionGate = createActionGate();
let profileConfigLoadSeq = 0;
let profileContextSeq = 0;
let refreshSeq = 0;
let proxiesRequestSeq = 0;
let lastProfileListSnapshot = "";
let lastProxyGroupsSnapshot = "";
let slowPollTimer: ReturnType<typeof setTimeout> | null = null;
let fastPollTimer: ReturnType<typeof setTimeout> | null = null;

const configEditor = createYamlEditor(el.configEditor, {
  ariaLabel: "配置档 YAML 编辑器",
  onChange() {
    el.configEditor.dataset.dirty = "1";
    setConfigEditorError("");
    renderConfigEditorState();
  },
  onSave() {
    saveProfile();
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
    for (const chip of el.proxiesList.querySelectorAll<HTMLElement>(".latency-chip")) {
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

/** Argument/return shape of captureProfileOperationContext()/profileOperationContextMatches(). */
interface ProfileOperationContext {
  contextSeq: number;
  profileId: string;
}

function active(): FleetInstance | null {
  return activeInstance(state);
}

function activeProfile(): FleetProfile | null {
  return profileById(state, state.activeProfileId);
}

function configEditorDirty(): boolean {
  return el.configEditor.dataset.dirty === "1";
}

function profileOperationRunning(): boolean {
  return saveProfileGate.isRunning()
    || deleteProfileGate.isRunning()
    || refreshSubscriptionGate.isRunning();
}

function beginProfileOperation(gate: ActionGate): boolean {
  if (profileOperationRunning() || !gate.begin()) return false;
  render();
  return true;
}

function activeProfileContextId(): string {
  return state.profileCreating ? "__new__" : state.activeProfileId;
}

function captureProfileOperationContext(profileId: string = activeProfileContextId()): ProfileOperationContext {
  return { contextSeq: profileContextSeq, profileId };
}

function profileOperationContextMatches(context: ProfileOperationContext): boolean {
  const activeProfileId = activeProfileContextId();
  return el.profileEditor.dataset.profileId === activeProfileId
    && shouldApplyProfileOperation({
      requestContextSeq: context.contextSeq,
      currentContextSeq: profileContextSeq,
      requestedProfileId: context.profileId,
      activeProfileId,
      view: state.view,
    });
}

function advanceProfileContext(): void {
  profileContextSeq += 1;
}

function hasUnsavedChanges(): boolean {
  return state.editDirty || state.profileFormDirty || configEditorDirty();
}

function confirmDiscardChanges(action: string): boolean {
  if (!hasUnsavedChanges()) return true;
  return window.confirm(`有未保存的修改。确定放弃并${action}吗？`);
}

function setConfigEditorError(message: string): void {
  const text = message ? localizedMessage(message) : "";
  el.configEditorError.textContent = text;
  el.configEditorError.classList.toggle("hidden", !text);
}

function renderConfigEditorState(profile: FleetProfile | null = activeProfile()): void {
  const isSubscription = state.profileCreating
    ? state.profileCreateSource === "subscription"
    : Boolean(profile?.subscriptionUrl);
  const dirty = configEditorDirty();
  const contextMatches = Boolean(
    state.profileCreating
      ? el.configEditor.dataset.profileId === "__new__"
      : profile && el.configEditor.dataset.profileId === profile.id,
  );
  let text = "正在加载";
  let status = "loading";
  if (!profile && !state.profileCreating) {
    text = "未选择配置档";
    status = "idle";
  } else if (saveProfileGate.isRunning()) {
    text = "正在保存";
    status = "saving";
  } else if (deleteProfileGate.isRunning()) {
    text = "正在删除配置档";
    status = "saving";
  } else if (refreshSubscriptionGate.isRunning()) {
    text = "正在更新订阅";
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
  const profileBusy = profileOperationRunning();
  el.saveProfile.disabled = (!profile && !state.profileCreating)
    || (!isSubscription && !contextMatches)
    || profileBusy;
  el.discardConfig.disabled = !dirty || profileBusy;
  el.findConfig.disabled = (!profile && !state.profileCreating) || isSubscription || profileBusy;
}

function resetConfigEditor(): void {
  profileConfigLoadSeq += 1;
  configEditor.setValue("");
  configEditor.setReadOnly(true);
  el.configEditor.dataset.profileId = "";
  el.configEditor.dataset.dirty = "";
  setConfigEditorError("");
  renderConfigEditorState();
}

// Writes the raw text into the reactive banner; MessageBanner.vue owns both
// the localizedMessage() translation and the 6s auto-dismiss timer now, so
// neither happens here (see bridge.ts's `banner` and the component for why).
function showMessage(text: string, kind: string = "info"): void {
  banner.text = text;
  banner.tone = kind === "error" ? "error" : "info";
}

function renderSubscriptionInfo(profile: FleetProfile): void {
  el.subscriptionInfo.textContent = "";
  const summary = document.createElement("span");
  summary.textContent = formatSubscriptionInfo(profile);
  el.subscriptionInfo.append(summary);
  // isHttpUrl() itself treats a missing homeUrl as "" internally, so reading it
  // through this local (rather than `profile.homeUrl!` after the check) matches
  // that behavior exactly while keeping the value a plain `string` below.
  const homeUrl = profile.homeUrl || "";
  if (isHttpUrl(homeUrl)) {
    el.subscriptionInfo.append(document.createTextNode(" · 主页 "));
    const link = document.createElement("a");
    link.href = homeUrl.trim();
    link.target = "_blank";
    link.rel = "noopener noreferrer";
    link.textContent = homeUrl.trim();
    el.subscriptionInfo.append(link);
  }
}

function updateLatencyControls(): void {
  const selected = active();
  const disabled = !selected || selected.status !== "running" || !state.proxyApply;
  const hasLatencyTarget = state.proxyGroups.some((group) => currentLatencyTarget(group, state.proxyGroups));
  el.testAllLatency.disabled = disabled || !hasLatencyTarget || state.latencyBatchRunning;
  el.testAllRealLatency.disabled = disabled || !hasLatencyTarget || state.latencyBatchRunning;
  for (const button of el.proxiesList.querySelectorAll<HTMLButtonElement>(".proxy-group-actions button")) {
    // These datasets are only ever populated by renderProxyGroups() below with
    // group/proxy names and a genuine LatencyKind, so the fallbacks/cast here
    // just satisfy the compiler about values this same module guarantees.
    const running = Boolean(
      selected &&
      button.dataset.testable !== "false" &&
      isLatencyRunning(
        state,
        selected.id,
        button.dataset.groupName || "",
        button.dataset.proxyName || "",
        button.dataset.kind as LatencyKind,
      ),
    );
    button.disabled = disabled || button.dataset.testable === "false" || running;
    if (running) button.title = "测速中";
    else if (button.dataset.disabledReason !== undefined) button.title = button.dataset.disabledReason;
  }
}

function proxyTooltipButton(target: EventTarget | null): HTMLElement | null {
  if (!(target instanceof Element)) return null;
  return target.closest<HTMLElement>(".proxy-choice");
}

function showProxyTooltip(button: HTMLElement): void {
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

function hideProxyTooltip(): void {
  proxyTooltip.classList.add("hidden");
}

/** Options accepted by refresh(); see call sites below and in bindEvents(). */
interface RefreshOptions {
  forceInstances?: boolean;
  periodic?: boolean;
}

async function refresh(options: RefreshOptions = {}): Promise<void> {
  const seq = ++refreshSeq;
  try {
    const [system, profiles, list] = await Promise.all([
      api<FleetSystemStatus>("/api/system"),
      api<{ profiles?: FleetProfile[] }>("/api/profiles"),
      api<{ instances?: FleetInstance[] }>("/api/instances"),
    ]);
    if (seq !== refreshSeq) return;
    state.system = system;
    state.profiles = profiles.profiles || [];
    if (!state.profileCreating && state.activeProfileId && !state.profiles.some((profile) => profile.id === state.activeProfileId)) {
      state.activeProfileId = state.profiles[0]?.id || "";
    }
    if (!state.bulkRunning || options.forceInstances) {
      state.instances = list.instances || [];
      // `state.instances.length` just guarded a non-empty array, so index 0 is
      // always present; the assertion only documents that to noUncheckedIndexedAccess.
      if (!state.activeId && state.instances.length) state.activeId = state.instances[0]!.id;
      if (state.activeId && !state.instances.some((item) => item.id === state.activeId)) {
        state.activeId = state.instances[0]?.id || "";
      }
    }
    localStorage.setItem("activeInstance", state.activeId);
    render();
    await refreshActiveDetails({ skipFast: options.periodic });
  } catch (err) {
    if (seq !== refreshSeq) return;
    const message = err instanceof Error ? err.message : String(err);
    showMessage(message, "error");
  }
}

function render(): void {
  const selected = active();
  // ActionGate objects backing profileOperationRunning() live outside the
  // reactive graph (plain closures in this module), so a component reading
  // them directly would never re-render; render() is the sync point since it
  // already runs on every state change (see bridge.ts's `chrome`).
  chrome.profileBusy = profileOperationRunning();
  updateBulkControls();
  renderPanels(selected);
  renderProfileManager();
  updateCreateProfileControls();
}

async function copyProxyValue(value: string, success: string | undefined): Promise<void> {
  try {
    await writeClipboard(value);
    showMessage(success || "");
  } catch (err) {
    console.warn("Unable to copy proxy value.", err);
    showMessage("复制失败，请检查浏览器剪贴板权限。", "error");
  }
}

function updateBulkControls(): void {
  el.emptyCreate.disabled = state.bulkRunning;
}

function editFormContainsFocus(): boolean {
  return Boolean(el.editForm && el.editForm.contains(document.activeElement));
}

function renderPanels(selected: FleetInstance | null): void {
  const profilesView = state.view === "profiles";
  const dashboardView = state.view === "dashboard";
  // Anything that is not the instance workbench hides the workbench panels.
  // Testing "not instances" rather than "is profiles" keeps this correct as
  // further views are added.
  const away = profilesView || dashboardView;
  el.profilePanel.classList.toggle("hidden", !profilesView);
  el.dashboardPanel.classList.toggle("hidden", !dashboardView);
  el.createPanel.classList.toggle("hidden", away || !state.creating);
  el.emptyPanel.classList.toggle("hidden", away || state.creating || state.instances.length > 0);
  el.detailPanel.classList.toggle("hidden", away || state.creating || !selected);
  el.createSubmit.disabled = createGate.isRunning();
  el.emptyCreate.textContent = state.profiles.length ? "创建第一个实例" : "先创建配置档";
  el.saveBasics.disabled = !selected || saveBasicsGate.isRunning();
  if (dashboardView) renderDashboard(el.dashboardPanel, state);
  if (away || !selected) return;

  el.detailName.textContent = selected.name;
  el.detailMeta.textContent = selected.lastError
    ? localizedMessage(selected.lastError)
    : `${statusText(selected.status)} · ${selected.id}`;
  el.metricStatus.textContent = statusText(selected.status);
  el.metricPid.textContent = String(selected.pid || "无");
  el.metricMixed.textContent = String(proxyPortLabel(selected.mixedPort));
  el.metricController.textContent = String(selected.controllerPort);
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
    el.editMixedPort.value = String(selected.mixedPort);
    el.editProxyBind.value = selected.proxyBind || defaultProxyBind;
    el.editControllerPort.value = String(selected.controllerPort);
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
}

function applyModeFields(prefix: "edit" | "create", mode: string): void {
  const chainMode = mode === instanceModes.globalChain;
  el[`${prefix}ChainFields`].classList.toggle("hidden", !chainMode);
}

// `allowNew` is accepted (matching every call site) but unused in the body,
// same as before this file was typed -- not this pass's job to clean up.
function renderProfileOptions(select: HTMLSelectElement, selectedId: string, allowNew: boolean): void {
  const current = selectedId || select.value;
  select.innerHTML = "";
  for (const profile of state.profiles) {
    const opt = document.createElement("option");
    opt.value = profile.id;
    opt.textContent = profileOptionLabel(profile, profileReferenceCount(state, profile.id));
    select.append(opt);
  }
  if (current && [...select.options].some((opt) => opt.value === current)) {
    select.value = current;
  } else if (select.options.length > 0) {
    // Just guarded by the length check above, so index 0 is always present.
    select.value = select.options[0]!.value;
  }
}

function profileListSnapshot(): string {
  return JSON.stringify({
    activeProfileId: state.activeProfileId,
    creating: state.profileCreating,
    busy: profileOperationRunning(),
    profiles: state.profiles.map((profile) => [
      profile.id,
      profile.name,
      profile.subscriptionUrl || "",
      profileReferenceCount(state, profile.id),
    ]),
  });
}

function renderProfileList(): void {
  const snapshot = profileListSnapshot();
  if (snapshot === lastProfileListSnapshot) return;
  lastProfileListSnapshot = snapshot;
  const activeElement = document.activeElement;
  const focusedId = activeElement instanceof HTMLElement && el.profileList.contains(activeElement)
    ? activeElement.dataset.profileId || ""
    : "";
  el.profileList.innerHTML = "";
  for (const profile of state.profiles) {
    const references = profileReferenceCount(state, profile.id);
    const button = document.createElement("button");
    button.type = "button";
    button.className = `profile-row ${!state.profileCreating && state.activeProfileId === profile.id ? "active" : ""}`;
    button.disabled = profileOperationRunning();
    button.dataset.profileId = profile.id;
    button.setAttribute("aria-current", !state.profileCreating && state.activeProfileId === profile.id ? "true" : "false");
    button.innerHTML = `
      <span class="profile-row-main"></span>
      <span class="profile-row-meta"></span>
      <code class="profile-row-id"></code>
    `;
    // These selectors always match the innerHTML template set just above.
    button.querySelector(".profile-row-main")!.textContent = profile.name || "未命名配置档";
    button.querySelector(".profile-row-meta")!.textContent = `${profile.subscriptionUrl ? "订阅配置" : "手写配置"} · ${references > 0 ? `${references} 个实例` : "未使用"}`;
    button.querySelector(".profile-row-id")!.textContent = profile.id;
    button.addEventListener("click", () => selectProfile(profile.id));
    el.profileList.append(button);
  }
  if (!state.profiles.length) {
    const empty = document.createElement("p");
    empty.className = "profile-list-empty";
    empty.textContent = "还没有配置档。";
    el.profileList.append(empty);
  }
  if (focusedId) el.profileList.querySelector<HTMLElement>(`[data-profile-id="${CSS.escape(focusedId)}"]`)?.focus({ preventScroll: true });
}

function renderProfileManager(): void {
  el.profileCount.textContent = `${state.profiles.length} 个`;
  renderProfileList();
  const profile = activeProfile();
  const hasEditor = state.profileCreating || Boolean(profile);
  el.profileEditorEmpty.classList.toggle("hidden", hasEditor);
  el.profileEditor.classList.toggle("hidden", !hasEditor);
  if (!hasEditor) {
    renderConfigEditorState(null);
    return;
  }

  // hasEditor (just checked above) is `state.profileCreating || Boolean(profile)`;
  // every `profile.foo` access below only runs on the `!state.profileCreating`
  // branch, which -- given hasEditor is true -- proves `profile` was the
  // `Boolean(profile)` disjunct, i.e. non-null.
  const isSubscription = state.profileCreating
    ? state.profileCreateSource === "subscription"
    : Boolean(profile!.subscriptionUrl);
  const references = profile ? profileReferenceCount(state, profile.id) : 0;
  el.profileEditorTitle.textContent = state.profileCreating ? "新建配置档" : profile!.name;
  el.profileSourceTabs.classList.toggle("hidden", !state.profileCreating);
  el.profileManualMode.classList.toggle("active", state.profileCreateSource === "manual");
  el.profileSubscriptionMode.classList.toggle("active", state.profileCreateSource === "subscription");
  el.subscriptionSettings.classList.toggle("hidden", !isSubscription);
  el.profileConfigSection.classList.toggle("hidden", state.profileCreating && isSubscription);
  el.profileReferenceBadge.textContent = state.profileCreating
    ? "尚未创建"
    : references > 0 ? `${references} 个实例引用` : "未使用";
  el.profileReferenceBadge.classList.toggle("in-use", references > 0);
  el.profileMeta.textContent = state.profileCreating
    ? (isSubscription ? "创建后会下载并缓存订阅 YAML。" : "手写配置可以直接编辑 YAML。")
    : isSubscription ? `订阅缓存：${formatProfileUpdate(profile!)}` : "手写配置：修改会作用于所有引用实例。";
  el.profileDeleteHint.textContent = state.profileCreating
    ? ""
    : references > 0 ? `该配置档仍被 ${references} 个实例引用，需先将这些实例改绑到其他配置档。` : "删除后无法恢复。";
  el.deleteProfile.classList.toggle("hidden", state.profileCreating);
  const profileBusy = profileOperationRunning();
  el.deleteProfile.disabled = state.profileCreating || references > 0 || profileBusy;
  el.refreshSubscription.disabled = state.profileCreating || state.profileFormDirty || profileBusy;
  el.newProfileBtn.disabled = profileBusy;
  el.profileName.disabled = profileBusy;
  el.profileManualMode.disabled = profileBusy;
  el.profileSubscriptionMode.disabled = profileBusy;
  el.subscriptionUrl.disabled = profileBusy;
  el.subscriptionAutoUpdate.disabled = profileBusy;
  el.subscriptionInterval.disabled = profileBusy;
  if (isSubscription && profile) renderSubscriptionInfo(profile);
  renderConfigEditorState(profile);
}

function markProfileFormDirty(): void {
  state.profileFormDirty = true;
  state.profileFormVersion += 1;
}

function clearProfileFormDirty(): void {
  state.profileFormDirty = false;
  el.configEditor.dataset.dirty = "";
}

function populateProfileForm(profile: FleetProfile | null): void {
  el.profileEditor.dataset.profileId = profile?.id || "__new__";
  el.profileName.value = profile?.name || "";
  el.profileId.value = profile?.id || "创建后自动生成";
  el.subscriptionUrl.value = profile?.subscriptionUrl || "";
  el.subscriptionAutoUpdate.checked = profile ? Boolean(profile.autoUpdate) : true;
  el.subscriptionInterval.value = String(profile?.updateIntervalMinutes || "360");
  if (!profile) el.subscriptionInfo.textContent = "";
}

async function loadProfileConfig(profileId: string): Promise<void> {
  const profile = profileById(state, profileId);
  if (!profile) return;
  resetConfigEditor();
  const requestSeq = ++profileConfigLoadSeq;
  renderConfigEditorState(profile);
  try {
    const payload = await api<{ config?: string }>(`/api/profiles/${profile.id}/config`);
    if (!shouldApplyProfileConfigLoad({
      requestSeq,
      currentSeq: profileConfigLoadSeq,
      requestedProfileId: profile.id,
      activeProfileId: state.activeProfileId,
      dirty: configEditorDirty(),
    })) return;
    configEditor.setValue(payload.config || "");
    configEditor.setReadOnly(Boolean(profile.subscriptionUrl));
    el.configEditor.dataset.profileId = profile.id;
    el.configEditor.dataset.dirty = "";
    setConfigEditorError("");
    renderConfigEditorState(profile);
  } catch (err) {
    if (requestSeq !== profileConfigLoadSeq || state.activeProfileId !== profile.id) return;
    const message = err instanceof Error ? err.message : String(err);
    setConfigEditorError(message);
    renderConfigEditorState(profile);
    showMessage(message, "error");
  }
}

function startNewProfile(): boolean {
  if (profileOperationRunning()) return false;
  if (!confirmDiscardChanges("新建配置档")) return false;
  advanceProfileContext();
  state.editDirty = false;
  state.view = "profiles";
  state.profileCreating = true;
  state.activeProfileId = "";
  state.profileCreateSource = "manual";
  state.profileFormVersion = 0;
  resetConfigEditor();
  populateProfileForm(null);
  configEditor.setValue(defaultConfig);
  configEditor.setReadOnly(false);
  el.configEditor.dataset.profileId = "__new__";
  clearProfileFormDirty();
  render();
  el.profileName.focus();
  return true;
}

/** Options accepted by selectProfile(); see call sites in app.ts. */
interface SelectProfileOptions {
  force?: boolean;
  allowBusy?: boolean;
}

function selectProfile(profileId: string, options: SelectProfileOptions = {}): boolean {
  if (profileOperationRunning() && !options.allowBusy) return false;
  if (!options.force && !state.profileCreating && state.activeProfileId === profileId) {
    state.view = "profiles";
    render();
    if (el.configEditor.dataset.profileId !== profileId) loadProfileConfig(profileId);
    return true;
  }
  if (!options.force && state.activeProfileId !== profileId && !confirmDiscardChanges("切换配置档")) return false;
  const profile = profileById(state, profileId);
  if (!profile) return false;
  advanceProfileContext();
  state.view = "profiles";
  state.profileCreating = false;
  state.activeProfileId = profile.id;
  state.profileFormVersion = 0;
  populateProfileForm(profile);
  clearProfileFormDirty();
  render();
  loadProfileConfig(profile.id);
  return true;
}

function openProfileManager(profileId: string = ""): boolean {
  if (profileOperationRunning()) return false;
  if (state.view !== "profiles" && !confirmDiscardChanges("打开配置档管理")) return false;
  state.editDirty = false;
  state.view = "profiles";
  state.creating = false;
  const targetId = profileId || state.activeProfileId || active()?.profileId || state.profiles[0]?.id || "";
  if (targetId) return selectProfile(targetId, { force: true });
  return startNewProfile();
}

// The dashboard is read-only, so leaving the workbench for it cannot lose
// edits and needs no discard prompt. Coming back does, because the profile
// editor may still be mid-operation.
function openDashboard(): boolean {
  if (profileOperationRunning()) return false;
  if (state.view === "profiles" && !confirmDiscardChanges("打开总览")) return false;
  if (state.view === "profiles") {
    advanceProfileContext();
    state.profileCreating = false;
    state.profileFormDirty = false;
    resetConfigEditor();
  }
  state.view = "dashboard";
  render();
  sampleFleetTraffic();
  return true;
}

function closeDashboard(): boolean {
  state.view = "instances";
  render();
  refreshActiveDetails();
  return true;
}

// Keep the user on the dashboard when they pick a row; only "打开工作台"
// jumps into the dense instance detail pane.
function focusDashboardInstance(id: string): boolean {
  if (!id || !state.instances.some((item) => item.id === id)) return false;
  state.activeId = id;
  localStorage.setItem("activeInstance", id);
  if (state.view === "dashboard") renderDashboard(el.dashboardPanel, state);
  else render();
  return true;
}

function openInstanceWorkbench(id: string): boolean {
  if (!id) return false;
  if (state.view === "profiles" && !confirmDiscardChanges("打开工作台")) return false;
  if (state.view === "profiles") {
    state.profileFormDirty = false;
    resetConfigEditor();
  }
  clearLatencyStateForInstance(state, state.activeId);
  clearActiveDetailCache();
  state.activeId = id;
  state.view = "instances";
  state.creating = false;
  localStorage.setItem("activeInstance", id);
  render();
  refreshActiveDetails();
  return true;
}

function closeProfileManager(): boolean {
  if (profileOperationRunning()) return false;
  if (!confirmDiscardChanges("返回实例")) return false;
  advanceProfileContext();
  state.view = "instances";
  state.profileCreating = false;
  state.profileFormDirty = false;
  resetConfigEditor();
  render();
  refreshActiveDetails();
  return true;
}

function updateCreateProfileControls(): void {
  renderProfileOptions(el.createProfile, el.createProfile.value || state.profiles[0]?.id || "", false);
  const hasProfiles = state.profiles.length > 0;
  const chainMode = el.createMode.value === instanceModes.globalChain;
  el.createProfile.disabled = !hasProfiles;
  el.createProfileRequired.classList.toggle("hidden", hasProfiles);
  el.createSubmit.disabled = createGate.isRunning() || !hasProfiles;
  applyModeFields("create", el.createMode.value);
}

/** Shape of the JSON body GET /api/ports/suggest returns. */
interface SuggestedPorts {
  mixedPort?: number;
  controllerPort?: number;
}

async function fillSuggestedPorts(): Promise<void> {
  if (el.createMixedPort.value && el.createControllerPort.value) return;
  try {
    const ports = await api<SuggestedPorts>("/api/ports/suggest");
    el.createMixedPort.placeholder = ports.mixedPort ? `建议 ${ports.mixedPort}` : "自动";
    el.createControllerPort.placeholder = ports.controllerPort ? `建议 ${ports.controllerPort}` : "自动";
  } catch (err) {
    console.warn("Unable to load suggested ports.", err);
  }
}

function clearActiveDetailCache(): void {
  state.editInstanceId = "";
  state.editDirty = false;
  state.editVersion = 0;
  el.logs.dataset.instanceId = "";
  state.proxyGroups = [];
  state.proxyApply = false;
  state.latencyBatchRunning = false;
  state.latencyBatchToken += 1;
  lastProxyGroupsSnapshot = "";
  el.proxiesList.innerHTML = "";
}

function markEditFormDirty(): void {
  const selected = active();
  state.editInstanceId = selected?.id || state.editInstanceId;
  state.editDirty = true;
  state.editVersion += 1;
}

function selectInstance(id: string): boolean {
  if (state.activeId !== id || state.view === "profiles") {
    if (!confirmDiscardChanges("切换实例")) {
      return false;
    }
    if (state.view === "profiles") {
      state.profileFormDirty = false;
      resetConfigEditor();
    }
    clearLatencyStateForInstance(state, state.activeId);
    clearActiveDetailCache();
  }
  state.activeId = id;
  state.view = "instances";
  state.creating = false;
  localStorage.setItem("activeInstance", id);
  render();
  refreshActiveDetails();
  return true;
}

function showCreate(): boolean {
  if (!state.profiles.length) {
    openProfileManager();
    showMessage("请先创建配置档，再创建引用它的实例。", "error");
    return false;
  }
  if (!confirmDiscardChanges("新建实例")) return false;
  if (hasUnsavedChanges()) clearActiveDetailCache();
  state.view = "instances";
  state.creating = true;
  el.createName.value = "";
  el.createMode.value = instanceModes.rule;
  el.createMixedPort.value = "";
  el.createProxyBind.value = defaultProxyBind;
  el.createControllerPort.value = "";
  renderProfileOptions(el.createProfile, state.profiles[0]?.id || "", false);
  el.createLocalProxies.value = "";
  el.createChain.value = "";
  showMessage("");
  render();
  fillSuggestedPorts();
  return true;
}

/** Options accepted by refreshActiveDetails(); see call sites in app.ts. */
interface RefreshActiveDetailsOptions {
  skipFast?: boolean;
}

async function refreshActiveDetails(options: RefreshActiveDetailsOptions = {}): Promise<void> {
  const selected = active();
  if (!selected || state.creating || state.view !== "instances") return;
  if (options.skipFast) return;
  await pollActiveTab();
}

async function pollActiveTab(): Promise<void> {
  if (state.view !== "instances") return;
  if (state.activeTab === "logs") await refreshLogs();
  if (state.activeTab === "proxies") await refreshProxies();
}

async function refreshLogs(): Promise<void> {
  const selected = active();
  if (!selected) return;
  try {
    const payload = await api<{ lines?: string[] }>(`/api/instances/${selected.id}/logs`);
    const shouldStick = el.logs.dataset.instanceId !== selected.id || isLogScrolledToBottom();
    const text = (payload.lines || []).join("\n") || "还没有进程日志。";
    if (el.logs.textContent !== text) el.logs.textContent = text;
    el.logs.dataset.instanceId = selected.id;
    if (shouldStick) el.logs.scrollTop = el.logs.scrollHeight;
  } catch (err) {
    el.logs.dataset.instanceId = "";
    const message = err instanceof Error ? err.message : String(err);
    el.logs.textContent = localizedMessage(message);
  }
}

function isLogScrolledToBottom(): boolean {
  return el.logs.scrollHeight - el.logs.scrollTop - el.logs.clientHeight <= logStickThreshold;
}

async function loadProfileProxyGroups(selected: FleetInstance | null | undefined): Promise<FleetProxyGroup[]> {
  if (!selected?.profileId) return [];
  const profileId = encodeURIComponent(selected.profileId);
  const instanceId = encodeURIComponent(selected.id);
  const payload = await api<{ groups?: FleetProxyGroup[] }>(`/api/profiles/${profileId}/proxies?instanceId=${instanceId}`);
  return payload.groups || [];
}

async function loadProfileProxyGroupsForRuntime(selected: FleetInstance | null | undefined): Promise<FleetProxyGroup[]> {
  try {
    return await loadProfileProxyGroups(selected);
  } catch (err) {
    console.warn("Unable to load profile proxy order; using mihomo runtime order.", err);
    return [];
  }
}

async function refreshProxies(): Promise<void> {
  const selected = active();
  if (!selected) return;
  const seq = ++proxiesRequestSeq;
  try {
    let groups: FleetProxyGroup[] = [];
    let apply = false;
    if (selected.status === "running") {
      const [payload, profileGroups] = await Promise.all([
        api<{ proxies?: Record<string, FleetProxyGroup> }>(`/api/mihomo/${selected.id}/proxies`),
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
    const message = err instanceof Error ? err.message : String(err);
    el.proxiesList.innerHTML = `<div class="message error">${escapeHTML(localizedMessage(message))}</div>`;
    lastProxyGroupsSnapshot = "";
  }
}

function proxyGroupsRenderSnapshot(groups: FleetProxyGroup[], apply: boolean, filter: string) {
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

function proxyFocusKey(groupName: string, proxyName: string): string {
  return `${groupName}${latencyKeySeparator}${proxyName}`;
}

function latencyButtonFocusKey(groupName: string, kind: LatencyKind): string {
  return `${groupName}${latencyKeySeparator}latency-btn${latencyKeySeparator}${kind}`;
}

function capturedProxyFocusKey(): string {
  const activeElement = document.activeElement;
  if (!(activeElement instanceof HTMLElement) || !el.proxiesList.contains(activeElement)) return "";
  return activeElement.dataset.proxyFocus || "";
}

function isLatencyFocusKey(key: string | undefined): boolean {
  return String(key || "").includes(`${latencyKeySeparator}latency-btn${latencyKeySeparator}`);
}

function restoreProxyListFocus(focusedKey: string): void {
  if (!focusedKey) return;
  const controls = [...el.proxiesList.querySelectorAll<HTMLButtonElement>("[data-proxy-focus]")].filter((node) => !node.disabled);
  if (!controls.length) return;
  const groupPrefix = `${focusedKey.split(latencyKeySeparator)[0] || ""}${latencyKeySeparator}`;
  const sameKindControls = controls.filter((node) => isLatencyFocusKey(node.dataset.proxyFocus) === isLatencyFocusKey(focusedKey));
  const pool = sameKindControls.length ? sameKindControls : controls;
  const target = pool.find((node) => node.dataset.proxyFocus === focusedKey)
    || pool.find((node) => node.dataset.proxyFocus?.startsWith(groupPrefix))
    || pool[0];
  target?.focus({ preventScroll: true });
}

function renderProxyGroups(groups: FleetProxyGroup[], apply: boolean): void {
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
    latencyButton.disabled = Boolean(!apply || !currentName || (selected && isLatencyRunning(state, selected.id, group.name, currentName, latencyKinds.url)));
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
    realLatencyButton.disabled = Boolean(!apply || !currentName || (selected && isLatencyRunning(state, selected.id, group.name, currentName, latencyKinds.real)));
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

async function selectProxy(group: string, proxy: string, apply: boolean): Promise<void> {
  const selected = active();
  if (!selected) return;
  try {
    const updated = await api<FleetInstance>(`/api/instances/${selected.id}/selection`, {
      method: "POST",
      body: JSON.stringify({ group, proxy, apply }),
    });
    state.instances = state.instances.map((item) => (item.id === updated.id ? updated : item));
    showMessage(apply ? `已应用并保存 ${group} -> ${proxy}。` : `已保存 ${group} -> ${proxy}。`);
    render();
    await refreshProxies();
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    showMessage(message, "error");
  }
}

async function runAction(path: string, success: string): Promise<void> {
  try {
    await api(path, { method: "POST" });
    showMessage(success);
    await refresh();
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    showMessage(message, "error");
    await refresh();
  }
}

async function runBulkAction(action: string): Promise<void> {
  try {
    state.bulkRunning = true;
    updateBulkControls();
    const payload = await api<BatchActionPayload>(`/api/instances?action=${encodeURIComponent(action)}`, { method: "POST" });
    state.instances = payload.instances || state.instances;
    showMessage(formatBatchMessage(action, payload), payload.failed ? "error" : "info");
    render();
    await refreshActiveDetails();
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    showMessage(message, "error");
    await refresh({ forceInstances: true });
  } finally {
    state.bulkRunning = false;
    updateBulkControls();
  }
}

function setActiveTab(button: HTMLButtonElement): void {
  // Every `.tab` button in index.html carries a `data-tab` matching one of
  // FleetTab's literal values (that's the markup's whole purpose), so this
  // cast documents a guarantee the DOM shell -- not this function -- owns.
  state.activeTab = button.dataset.tab as FleetTab;
  for (const tab of el.tabButtons) {
    const isActive = tab === button;
    tab.classList.toggle("active", isActive);
    tab.setAttribute("aria-selected", isActive ? "true" : "false");
    tab.tabIndex = isActive ? 0 : -1;
  }
  document.querySelectorAll(".tab-panel").forEach((panel) => panel.classList.add("hidden"));
  // The active tab always has a matching #tab-<name> panel in index.html.
  document.querySelector(`#tab-${state.activeTab}`)!.classList.remove("hidden");
}

async function createInstanceFromForm(): Promise<void> {
  if (!state.profiles.length || !el.createProfile.value) {
    showMessage("请先创建并选择配置档。", "error");
    return;
  }
  if (!createGate.begin()) return;
  render();
  try {
    const payload = {
      name: el.createName.value.trim(),
      profileId: el.createProfile.value,
      mixedPort: Number(el.createMixedPort.value) || 0,
      proxyBind: el.createProxyBind.value.trim(),
      controllerPort: Number(el.createControllerPort.value) || 0,
      mode: el.createMode.value,
      localProxies: el.createMode.value === instanceModes.globalChain ? el.createLocalProxies.value : "",
      chain: el.createMode.value === instanceModes.globalChain ? chainFromText(el.createChain.value) : [],
    };
    const created = await api<FleetInstance>("/api/instances", {
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
    const message = err instanceof Error ? err.message : String(err);
    showMessage(message, "error");
  } finally {
    createGate.end();
    render();
  }
}

async function saveActiveBasics(): Promise<void> {
  const selected = active();
  if (!selected || !saveBasicsGate.begin()) return;
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
    showMessage("基础信息已保存。");
    await refresh();
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    showMessage(message, "error");
  } finally {
    saveBasicsGate.end();
    render();
  }
}

/** Request body POST/PUT /api/profiles(/:id) accepts; see saveProfile(). */
interface SaveProfileBody {
  name: string;
  subscriptionUrl?: string;
  autoUpdate?: boolean;
  updateIntervalMinutes?: number;
  config?: string;
}

async function saveProfile(): Promise<void> {
  const profile = activeProfile();
  if ((!profile && !state.profileCreating) || !beginProfileOperation(saveProfileGate)) return;
  const creating = state.profileCreating;
  // The guard above only lets execution continue when `profile ||
  // state.profileCreating`; every `profile!` below is only read on the
  // `!creating` branch, which -- given that guard -- proves `profile` is
  // non-null there.
  const source = creating ? state.profileCreateSource : (profile!.subscriptionUrl ? "subscription" : "manual");
  const savedProfileId = creating ? "__new__" : profile!.id;
  const operationContext = captureProfileOperationContext(savedProfileId);
  const savedConfigVersion = configEditor.getVersion();
  const savedFormVersion = state.profileFormVersion;
  const configMayChange = source === "manual"
    ? configEditorDirty()
    : !creating && el.subscriptionUrl.value.trim() !== profile!.subscriptionUrl;
  const body: SaveProfileBody = {
    name: el.profileName.value.trim(),
  };
  if (source === "subscription") {
    body.subscriptionUrl = el.subscriptionUrl.value.trim();
    body.autoUpdate = el.subscriptionAutoUpdate.checked;
    body.updateIntervalMinutes = Number(el.subscriptionInterval.value) || 0;
  } else {
    body.config = configEditor.getValue();
  }
  try {
    if (el.profileEditor.dataset.profileId !== savedProfileId
      || (source === "manual" && el.configEditor.dataset.profileId !== savedProfileId)) {
      setConfigEditorError("配置档已变化，请重新选择后再保存。");
      return;
    }
    setConfigEditorError("");
    renderConfigEditorState(profile);
    const saved = await api<FleetProfile>(creating ? "/api/profiles" : `/api/profiles/${profile!.id}`, {
      method: creating ? "POST" : "PUT",
      body: JSON.stringify(body),
    });
    if (!profileOperationContextMatches(operationContext)) return;
    if (creating) advanceProfileContext();
    state.profileCreating = false;
    state.activeProfileId = saved.id;
    el.profileEditor.dataset.profileId = saved.id;
    el.configEditor.dataset.profileId = saved.id;
    const sameFormVersion = state.profileFormVersion === savedFormVersion;
    const sameConfigVersion = creating
      ? savedConfigVersion === configEditor.getVersion()
      : canClearSavedProfileConfig({
        savedProfileId,
        savedVersion: savedConfigVersion,
        activeProfileId: state.activeProfileId,
        currentVersion: configEditor.getVersion(),
      });
    if (sameFormVersion && sameConfigVersion) {
      clearProfileFormDirty();
      setConfigEditorError("");
      populateProfileForm(saved);
    }
    showMessage(creating
      ? "配置档已创建。"
      : configMayChange ? "配置档已保存，引用它的运行中实例需要重启后生效。" : "配置档已保存。");
    await refresh();
    if (source === "subscription"
      && sameFormVersion
      && state.view === "profiles"
      && !state.profileCreating
      && state.activeProfileId === saved.id) {
      await loadProfileConfig(saved.id);
    }
  } catch (err) {
    if (profileOperationContextMatches(operationContext)) {
      const message = err instanceof Error ? err.message : String(err);
      setConfigEditorError(message);
      showMessage(message, "error");
    }
  } finally {
    saveProfileGate.end();
    renderConfigEditorState();
    render();
  }
}

function bindEvents(): void {
  el.tabButtons.forEach((button) => {
    button.addEventListener("click", async () => {
      setActiveTab(button);
      await refreshActiveDetails();
    });
  });

  el.tabList.addEventListener("keydown", (event) => {
    if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
    // `document.activeElement` is only ever one of el.tabButtons' own buttons
    // while focus is inside the tab list; indexOf just returns -1 otherwise,
    // matching the runtime semantics of the original untyped comparison.
    const currentIndex = el.tabButtons.indexOf(document.activeElement as HTMLButtonElement);
    if (currentIndex === -1) return;
    event.preventDefault();
    const delta = event.key === "ArrowRight" ? 1 : -1;
    // Modulo-wrapped into [0, el.tabButtons.length), and el.tabButtons is the
    // static (non-empty) .tab button list, so this index always exists.
    const nextButton = el.tabButtons[(currentIndex + delta + el.tabButtons.length) % el.tabButtons.length]!;
    nextButton.focus();
    setActiveTab(nextButton);
    refreshActiveDetails();
  });

  el.dashboardPanel.addEventListener("click", (event) => {
    // Matches the `event.target as Element | null` cast dashboard.ts's own
    // bindComposition() uses for the same reason: DOM lib's generic event
    // maps type every target as bare EventTarget.
    const target = event.target as Element | null;
    const openBtn = target?.closest<HTMLElement>("[data-open-instance]");
    if (openBtn) {
      openInstanceWorkbench(openBtn.dataset.openInstance || "");
      return;
    }
    const row = target?.closest<HTMLElement>("tr[data-instance-id]");
    if (row) focusDashboardInstance(row.dataset.instanceId || "");
  });
  // Re-rendering on every keystroke is affordable (the poll already redraws
  // the panel every 1.8s) and renderDashboard restores the caret afterwards.
  el.dashboardPanel.addEventListener("input", (event) => {
    // The DOM lib's generic "input" event map types this as the base Event,
    // but the browser always dispatches a real InputEvent for text input.
    if ((event as InputEvent).isComposing) return;
    const target = event.target as Element | null;
    const search = target?.closest<HTMLInputElement>(".dash-conn-search");
    if (!search) return;
    setConnectionQuery(search.value);
    renderDashboard(el.dashboardPanel, state);
  });
  el.dashboardPanel.addEventListener("dblclick", (event) => {
    const target = event.target as Element | null;
    const row = target?.closest<HTMLElement>("tr[data-instance-id]");
    if (row) openInstanceWorkbench(row.dataset.instanceId || "");
  });
  el.dashboardPanel.addEventListener("keydown", (event) => {
    if (event.key !== "Enter" && event.key !== " ") return;
    const target = event.target as Element | null;
    const row = target?.closest<HTMLElement>("tr[data-instance-id]");
    if (!row) return;
    event.preventDefault();
    if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) openInstanceWorkbench(row.dataset.instanceId || "");
    else focusDashboardInstance(row.dataset.instanceId || "");
  });
  el.emptyCreate.addEventListener("click", showCreate);
  el.createManageProfiles.addEventListener("click", () => openProfileManager());
  el.createProfile.addEventListener("change", updateCreateProfileControls);
  el.createMode.addEventListener("change", updateCreateProfileControls);
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
      const created = await api<FleetInstance>(`/api/instances/${selected.id}/clone`, {
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
      const message = err instanceof Error ? err.message : String(err);
      showMessage(message, "error");
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
      const message = err instanceof Error ? err.message : String(err);
      showMessage(message, "error");
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
  el.newProfileBtn.addEventListener("click", startNewProfile);
  el.profileManualMode.addEventListener("click", () => {
    if (!state.profileCreating || state.profileCreateSource === "manual") return;
    state.profileCreateSource = "manual";
    markProfileFormDirty();
    configEditor.setReadOnly(false);
    render();
  });
  el.profileSubscriptionMode.addEventListener("click", () => {
    if (!state.profileCreating || state.profileCreateSource === "subscription") return;
    state.profileCreateSource = "subscription";
    markProfileFormDirty();
    configEditor.setReadOnly(true);
    render();
  });
  el.profileName.addEventListener("input", markProfileFormDirty);
  for (const input of [el.subscriptionUrl, el.subscriptionAutoUpdate, el.subscriptionInterval]) {
    input.addEventListener("input", markProfileFormDirty);
    input.addEventListener("change", markProfileFormDirty);
  }
  el.saveProfile.addEventListener("click", saveProfile);
  el.findConfig.addEventListener("click", () => configEditor.focusSearch());
  el.discardConfig.addEventListener("click", async () => {
    if (!configEditorDirty() || !window.confirm("确定放弃当前 YAML 修改并重新加载吗？")) return;
    if (state.profileCreating) {
      configEditor.setValue(defaultConfig);
      el.configEditor.dataset.dirty = "";
      renderConfigEditorState();
      return;
    }
    await loadProfileConfig(state.activeProfileId);
  });

  el.deleteProfile.addEventListener("click", async () => {
    const profile = activeProfile();
    if (!profile) return;
    const references = profileReferenceCount(state, profile.id);
    if (references > 0) {
      showMessage(`该配置档仍被 ${references} 个实例引用，无法删除。`, "error");
      return;
    }
    if (!confirm(`确定删除配置档 ${profile.name}？此操作不可撤销。`)) return;
    if (!beginProfileOperation(deleteProfileGate)) return;
    const operationContext = captureProfileOperationContext(profile.id);
    try {
      await api(`/api/profiles/${profile.id}`, { method: "DELETE" });
      if (!profileOperationContextMatches(operationContext)) return;
      advanceProfileContext();
      state.profiles = state.profiles.filter((item) => item.id !== profile.id);
      state.activeProfileId = state.profiles[0]?.id || "";
      state.profileFormDirty = false;
      resetConfigEditor();
      showMessage("配置档已删除。");
      await refresh({ forceInstances: true });
      if (state.view === "profiles" && state.activeProfileId) {
        selectProfile(state.activeProfileId, { force: true, allowBusy: true });
      }
    } catch (err) {
      if (profileOperationContextMatches(operationContext)) {
        const message = err instanceof Error ? err.message : String(err);
        showMessage(message, "error");
        await refresh({ forceInstances: true });
      }
    } finally {
      deleteProfileGate.end();
      render();
    }
  });

  el.refreshSubscription.addEventListener("click", async () => {
    const profile = activeProfile();
    if (!profile || state.profileFormDirty) {
      showMessage("请先保存订阅设置，再立即更新。", "error");
      return;
    }
    if (!beginProfileOperation(refreshSubscriptionGate)) return;
    const operationContext = captureProfileOperationContext(profile.id);
    try {
      const refreshed = await api<FleetProfile>(`/api/profiles/${profile.id}/refresh`, { method: "POST" });
      if (!profileOperationContextMatches(operationContext)) return;
      state.profiles = state.profiles.map((item) => (item.id === refreshed.id ? refreshed : item));
      showMessage("订阅已更新。运行中的实例需要重启后使用新的缓存配置。");
      populateProfileForm(refreshed);
      render();
      await loadProfileConfig(refreshed.id);
    } catch (err) {
      if (profileOperationContextMatches(operationContext)) {
        const message = err instanceof Error ? err.message : String(err);
        showMessage(message, "error");
        await refresh();
        if (profileOperationContextMatches(operationContext)) await loadProfileConfig(profile.id);
      }
    } finally {
      refreshSubscriptionGate.end();
      render();
    }
  });

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

function scheduleSlowPoll(delay: number = slowPollIntervalMs): void {
  clearTimeout(slowPollTimer || undefined);
  slowPollTimer = null;
  if (document.hidden) return;
  slowPollTimer = setTimeout(runSlowPoll, delay);
}

async function runSlowPoll(): Promise<void> {
  if (!document.hidden) await refresh({ periodic: true });
  scheduleSlowPoll();
}

function scheduleFastPoll(delay: number = fastPollIntervalMs): void {
  clearTimeout(fastPollTimer || undefined);
  fastPollTimer = null;
  if (document.hidden) return;
  fastPollTimer = setTimeout(runFastPoll, delay);
}

// Keep sampling while any view is open so opening the dashboard already has a
// filled window. Cost is one /connections call per running instance per tick.
async function sampleFleetTraffic(): Promise<void> {
  if (!state.instances?.length) return;
  await sampleFleet(
    state.instances,
    (id) => api<ConnectionsFetchPayload>(`/api/mihomo/${encodeURIComponent(id)}/connections`),
    Date.now(),
  );
  if (state.view === "dashboard") renderDashboard(el.dashboardPanel, state);
}

async function runFastPoll(): Promise<void> {
  if (!document.hidden) {
    await sampleFleetTraffic();
    await pollActiveTab();
  }
  scheduleFastPoll();
}

document.addEventListener("visibilitychange", () => {
  if (document.hidden) return;
  runSlowPoll();
  runFastPoll();
});

// Row budgets come from measured box heights, so a resized window has to
// re-measure. Debounced because a window drag fires this continuously.
let dashboardResizeTimer: ReturnType<typeof setTimeout> | null = null;
window.addEventListener("resize", () => {
  if (state.view !== "dashboard") return;
  clearTimeout(dashboardResizeTimer || undefined);
  dashboardResizeTimer = setTimeout(() => renderDashboard(el.dashboardPanel, state), 150);
});

// Country lookups run against the local database the controller already stages
// for mihomo, so no destination address ever leaves the machine.
setGeoResolver((ips) => api<GeoLookupResult>("/api/geoip", { method: "POST", body: JSON.stringify({ ips }) }));

bindEvents();

// Fills in bridge.ts's action table so the Vue chrome (TopBar/SideBar/etc.)
// can call back into this still-vanilla layer. Registered once, after
// bindEvents() so every implementation below is fully wired up first.
registerActions({
  selectInstance,
  showCreate,
  openDashboard,
  closeDashboard,
  openProfileManager,
  closeProfileManager,
  startAll: () => runBulkAction("start-all"),
  stopAll: () => runBulkAction("stop-all"),
  copyProxyValue,
  showMessage,
  dismissMessage: () => showMessage(""),
});

refresh();
scheduleSlowPoll();
scheduleFastPoll();
