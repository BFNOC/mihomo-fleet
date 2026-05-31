const defaultConfig = `mixed-port: 7890
allow-lan: false
mode: rule
log-level: info
proxies: []
proxy-groups:
  - name: Proxy
    type: select
    proxies:
      - DIRECT
rules:
  - MATCH,DIRECT
`;

const newProfileValue = "__new__";
const defaultLatencyUrl = "https://www.gstatic.com/generate_204";
const defaultLatencyTimeout = 5000;
const latencyKeySeparator = "\u001f";
const latencyKinds = {
  url: "url",
  real: "real",
};

const state = {
  system: null,
  profiles: [],
  instances: [],
  activeId: localStorage.getItem("activeInstance") || "",
  activeTab: "overview",
  createSource: "manual",
  creating: false,
  proxyGroups: [],
  proxyApply: false,
  latencyResults: {},
  latencyRunning: new Set(),
  latencyBatchRunning: false,
  latencyBatchToken: 0,
  bulkRunning: false,
};

const statusLabels = {
  stopped: "已停止",
  starting: "启动中",
  running: "运行中",
  error: "异常",
};

const errorLabels = {
  "mihomo binary not found. Install mihomo or start with -mihomo /path/to/mihomo": "未找到 mihomo 可执行文件。请安装 mihomo，或使用 -mihomo /path/to/mihomo 指定路径。",
  "stop the instance before changing ports": "修改端口前请先停止该实例。",
  "stop the instance before changing profile": "修改配置档前请先停止该实例。",
  "profileId and config cannot be changed in the same request": "不能在同一次请求中同时修改配置档和配置内容。",
  "subscriptionUrl and config cannot both be set": "订阅链接和配置内容不能同时设置。",
  "subscription URL must start with http:// or https://": "订阅链接必须以 http:// 或 https:// 开头。",
  "subscription profile config is refreshed from its URL": "订阅配置档的内容由链接更新，请使用手写配置档编辑 YAML。",
  "group and proxy are required": "必须选择节点组和节点。",
  "method not allowed": "请求方法不允许。",
  "invalid host header": "Host 请求头无效。",
  "missing X-Mihomo-Fleet header": "缺少 X-Mihomo-Fleet 请求头。",
  "Content-Type must be application/json": "Content-Type 必须是 application/json。",
  "unable to allocate local ports": "无法自动分配本地端口。",
  "instance must be running to test latency": "请先启动实例再测速。",
  "proxy is required": "请选择要测速的节点。",
  "proxy is required for real latency": "真延迟需要指定单个节点。",
  "latency kind must be url or real": "测速类型无效。",
  "latency test URL must start with http:// or https://": "测试 URL 必须以 http:// 或 https:// 开头。",
};

const errorPatterns = [
  [/^profile "(.+)" not found$/, (match) => `配置档 ${match[1]} 不存在。`],
  [/^profile "(.+)" is not a subscription profile$/, (match) => `配置档 ${match[1]} 不是订阅配置档。`],
  [/^profile "(.+)" subscription update is already running$/, (match) => `配置档 ${match[1]} 正在更新订阅。`],
  [/^instance "(.+)" not found$/, (match) => `实例 ${match[1]} 不存在。`],
  [/^mixed proxy port (\d+) is unavailable$/, (match) => `混合端口 ${match[1]} 不可用。`],
  [/^controller port (\d+) is unavailable$/, (match) => `控制端口 ${match[1]} 不可用。`],
  [/^mixed proxy port (\d+) is already in use$/, (match) => `混合端口 ${match[1]} 已被占用。`],
  [/^controller port (\d+) is already in use$/, (match) => `控制端口 ${match[1]} 已被占用。`],
  [/^process "(.+)" did not exit after force kill$/, (match) => `进程 ${match[1]} 强制结束后仍未退出。`],
  [/^mihomo config test failed: (.+)$/, (match) => `mihomo 配置测试失败：${match[1]}`],
  [/^mihomo controller unreachable: (.+)$/, (match) => `无法连接 mihomo 控制器：${match[1]}`],
  [/^mihomo returned (.+)$/, (match) => `mihomo 返回错误：${match[1]}`],
  [/^parse user config: (.+)$/, (match) => `解析用户配置失败：${match[1]}`],
  [/^subscription server returned (.+)$/, (match) => `订阅服务器返回错误：${match[1]}`],
  [/^subscription host resolves to blocked address (.+)$/, () => "订阅链接解析到本机、内网或保留地址，已阻止。"],
  [/^remote profile data is invalid yaml: (.+)$/, (match) => `订阅内容不是有效 YAML：${match[1]}`],
  [/^remote profile must contain proxies or proxy-providers$/, () => "订阅内容缺少 proxies 或 proxy-providers。"],
];

const el = {
  systemLine: document.querySelector("#systemLine"),
  systemWarning: document.querySelector("#systemWarning"),
  instanceSelect: document.querySelector("#instanceSelect"),
  instanceList: document.querySelector("#instanceList"),
  message: document.querySelector("#message"),
  newBtn: document.querySelector("#newBtn"),
  startAllBtn: document.querySelector("#startAllBtn"),
  stopAllBtn: document.querySelector("#stopAllBtn"),
  emptyCreate: document.querySelector("#emptyCreate"),
  createPanel: document.querySelector("#createPanel"),
  emptyPanel: document.querySelector("#emptyPanel"),
  detailPanel: document.querySelector("#detailPanel"),
  createName: document.querySelector("#createName"),
  createProfile: document.querySelector("#createProfile"),
  createProfileName: document.querySelector("#createProfileName"),
  createSourceTabs: document.querySelector("#createSourceTabs"),
  createManualMode: document.querySelector("#createManualMode"),
  createSubscriptionMode: document.querySelector("#createSubscriptionMode"),
  createSubscriptionFields: document.querySelector("#createSubscriptionFields"),
  createSubscriptionUrl: document.querySelector("#createSubscriptionUrl"),
  createAutoUpdate: document.querySelector("#createAutoUpdate"),
  createUpdateInterval: document.querySelector("#createUpdateInterval"),
  createMixedPort: document.querySelector("#createMixedPort"),
  createControllerPort: document.querySelector("#createControllerPort"),
  createConfigWrap: document.querySelector("#createConfigWrap"),
  createConfig: document.querySelector("#createConfig"),
  createSubmit: document.querySelector("#createSubmit"),
  createCancel: document.querySelector("#createCancel"),
  detailName: document.querySelector("#detailName"),
  detailMeta: document.querySelector("#detailMeta"),
  metricStatus: document.querySelector("#metricStatus"),
  metricPid: document.querySelector("#metricPid"),
  metricMixed: document.querySelector("#metricMixed"),
  metricController: document.querySelector("#metricController"),
  startBtn: document.querySelector("#startBtn"),
  stopBtn: document.querySelector("#stopBtn"),
  restartBtn: document.querySelector("#restartBtn"),
  deleteBtn: document.querySelector("#deleteBtn"),
  overviewMixed: document.querySelector("#overviewMixed"),
  overviewController: document.querySelector("#overviewController"),
  overviewProfile: document.querySelector("#overviewProfile"),
  overviewUserConfig: document.querySelector("#overviewUserConfig"),
  overviewRuntimeConfig: document.querySelector("#overviewRuntimeConfig"),
  overviewSelection: document.querySelector("#overviewSelection"),
  editName: document.querySelector("#editName"),
  editProfile: document.querySelector("#editProfile"),
  editMixedPort: document.querySelector("#editMixedPort"),
  editControllerPort: document.querySelector("#editControllerPort"),
  saveBasics: document.querySelector("#saveBasics"),
  profileMeta: document.querySelector("#profileMeta"),
  subscriptionSettings: document.querySelector("#subscriptionSettings"),
  subscriptionUrl: document.querySelector("#subscriptionUrl"),
  subscriptionAutoUpdate: document.querySelector("#subscriptionAutoUpdate"),
  subscriptionInterval: document.querySelector("#subscriptionInterval"),
  subscriptionInfo: document.querySelector("#subscriptionInfo"),
  saveProfileSettings: document.querySelector("#saveProfileSettings"),
  refreshSubscription: document.querySelector("#refreshSubscription"),
  configEditor: document.querySelector("#configEditor"),
  saveConfig: document.querySelector("#saveConfig"),
  proxySource: document.querySelector("#proxySource"),
  latencyUrl: document.querySelector("#latencyUrl"),
  latencyTimeout: document.querySelector("#latencyTimeout"),
  testAllLatency: document.querySelector("#testAllLatency"),
  testAllRealLatency: document.querySelector("#testAllRealLatency"),
  proxyFilter: document.querySelector("#proxyFilter"),
  proxiesList: document.querySelector("#proxiesList"),
  logs: document.querySelector("#logs"),
};

function active() {
  return state.instances.find((item) => item.id === state.activeId) || state.instances[0] || null;
}

function profileById(id) {
  return state.profiles.find((profile) => profile.id === id) || null;
}

function statusText(status) {
  return statusLabels[status] || status || "未知";
}

function statusClass(status) {
  return statusLabels[status] ? status : "unknown";
}

function localizedMessage(message) {
  const text = String(message || "");
  if (errorLabels[text]) return errorLabels[text];
  for (const [pattern, render] of errorPatterns) {
    const match = text.match(pattern);
    if (match) return render(match);
  }
  return text;
}

function formatProfileUpdate(profile) {
  if (profile.lastUpdateError) return `上次更新失败：${localizedMessage(profile.lastUpdateError)}`;
  if (profile.lastUpdatedAt) return `上次更新 ${new Date(profile.lastUpdatedAt).toLocaleString()}`;
  return "尚未更新";
}

function formatSubscriptionInfo(profile) {
  const parts = [];
  const info = profile.subscriptionInfo || {};
  if (info.total) parts.push(`流量 ${formatBytes((info.upload || 0) + (info.download || 0))} / ${formatBytes(info.total)}`);
  if (info.expire) parts.push(`到期 ${new Date(info.expire * 1000).toLocaleDateString()}`);
  if (profile.homeUrl) parts.push(`主页 ${profile.homeUrl}`);
  if (profile.autoUpdate && profile.updateIntervalMinutes) parts.push(`每 ${profile.updateIntervalMinutes} 分钟自动更新`);
  if (!profile.autoUpdate) parts.push("未启用自动更新");
  return parts.join(" · ") || "暂无订阅元数据";
}

function formatBytes(value) {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let size = Number(value) || 0;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit += 1;
  }
  return `${size.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function latencySettings() {
  const url = (el.latencyUrl.value || defaultLatencyUrl).trim();
  const timeoutMs = Math.min(15000, Math.max(500, Number(el.latencyTimeout.value) || defaultLatencyTimeout));
  el.latencyTimeout.value = String(timeoutMs);
  localStorage.setItem("fleetLatencyUrl", url);
  localStorage.setItem("fleetLatencyTimeout", String(timeoutMs));
  return { url, timeoutMs };
}

function latencyKey(instanceId, group, proxy, kind) {
  return [instanceId, group, proxy, kind].join(latencyKeySeparator);
}

function setLatencyResult(instanceId, group, proxy, kind, patch) {
  const key = latencyKey(instanceId, group, proxy, kind);
  state.latencyResults[key] = {
    ...(state.latencyResults[key] || {}),
    ...patch,
    updatedAt: Date.now(),
  };
}

function latencyResult(instanceId, group, proxy, kind) {
  return state.latencyResults[latencyKey(instanceId, group, proxy, kind)] || null;
}

function setLatencyRunning(instanceId, group, proxy, kind, running) {
  const key = latencyKey(instanceId, group, proxy, kind);
  if (running) {
    state.latencyRunning.add(key);
  } else {
    state.latencyRunning.delete(key);
  }
}

function isLatencyRunning(instanceId, group, proxy, kind) {
  return state.latencyRunning.has(latencyKey(instanceId, group, proxy, kind));
}

function isBuiltInProxy(name) {
  const text = String(name || "");
  return ["DIRECT", "REJECT", "REJECT-DROP", "PASS", "COMPATIBLE", "GLOBAL"].includes(text.toUpperCase());
}

function currentLatencyTarget(group) {
  const name = String(group?.now || "").trim();
  if (!name || isBuiltInProxy(name)) return "";
  if (state.proxyGroups.some((item) => item.name === name && item.name !== group?.name)) return "";
  return name;
}

function alignProxyNamesToProfileOrder(names, profileGroup) {
  if (!profileGroup || !Array.isArray(profileGroup.all) || !profileGroup.all.length) return names;
  const order = new Map();
  profileGroup.all.forEach((name, index) => {
    if (!order.has(name)) order.set(name, index);
  });
  return names
    .map((name, index) => ({ name, index }))
    .sort((left, right) => {
      const leftOrder = order.get(left.name);
      const rightOrder = order.get(right.name);
      if (leftOrder !== undefined && rightOrder !== undefined) return leftOrder - rightOrder;
      if (leftOrder !== undefined) return -1;
      if (rightOrder !== undefined) return 1;
      return left.index - right.index;
    })
    .map((item) => item.name);
}

function alignProxyGroupsToProfileOrder(runtimeGroups, profileGroups) {
  if (!Array.isArray(profileGroups) || !profileGroups.length) return runtimeGroups;
  const profileByName = new Map(profileGroups.map((group, index) => [group.name, { ...group, index }]));
  return runtimeGroups
    .map((group, index) => {
      const profileGroup = profileByName.get(group.name);
      const all = Array.isArray(group.all) ? alignProxyNamesToProfileOrder(group.all, profileGroup) : group.all;
      return { ...group, all, _runtimeIndex: index };
    })
    .sort((left, right) => {
      const leftProfile = profileByName.get(left.name);
      const rightProfile = profileByName.get(right.name);
      if (leftProfile && rightProfile) return leftProfile.index - rightProfile.index;
      if (leftProfile) return -1;
      if (rightProfile) return 1;
      return left._runtimeIndex - right._runtimeIndex;
    })
    .map(({ _runtimeIndex, ...group }) => group);
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

function formatLatencyValue(result, running) {
  if (running) return "测速中";
  if (!result) return "—";
  if (result.error) return "失败";
  if (result.delay === 0) return "不可用";
  if (typeof result.delay === "number") return `${result.delay}ms`;
  return "—";
}

function latencyTone(result, running) {
  if (running) return "running";
  if (!result) return "idle";
  if (result.error || result.delay === 0) return "bad";
  if (result.delay >= 500) return "warn";
  return "good";
}

function latencyLabel(kind) {
  return kind === latencyKinds.real ? "真" : "测";
}

function latencyTitle(kind) {
  return kind === latencyKinds.real ? "真延迟" : "URL 延迟";
}

function applyLatencyChipState(chip, selected, groupName, proxyName, kind) {
  const running = selected ? isLatencyRunning(selected.id, groupName, proxyName, kind) : false;
  const result = selected ? latencyResult(selected.id, groupName, proxyName, kind) : null;
  const value = formatLatencyValue(result, running);
  chip.className = `latency-chip ${latencyTone(result, running)}`;
  chip.textContent = `${latencyLabel(kind)} ${value}`;
  chip.setAttribute("aria-label", `${latencyTitle(kind)} ${value}`);
}

function updateLatencyChip(instanceId, groupName, proxyName, kind) {
  if (state.activeId !== instanceId || state.activeTab !== "proxies") return;
  const selected = active();
  for (const chip of el.proxiesList.querySelectorAll(".latency-chip")) {
    if (
      chip.dataset.instanceId === instanceId &&
      chip.dataset.groupName === groupName &&
      chip.dataset.proxyName === proxyName &&
      chip.dataset.kind === kind
    ) {
      applyLatencyChipState(chip, selected, groupName, proxyName, kind);
    }
  }
}

function clearLatencyStateForInstance(instanceId) {
  const prefix = `${instanceId}${latencyKeySeparator}`;
  for (const key of Object.keys(state.latencyResults)) {
    if (key.startsWith(prefix)) delete state.latencyResults[key];
  }
  for (const key of [...state.latencyRunning]) {
    if (key.startsWith(prefix)) state.latencyRunning.delete(key);
  }
}

function setLatencyBatchRunning(running) {
  state.latencyBatchRunning = running;
  updateLatencyControls();
}

function beginLatencyBatch() {
  state.latencyBatchToken += 1;
  setLatencyBatchRunning(true);
  return state.latencyBatchToken;
}

function endLatencyBatch(batchToken) {
  if (state.latencyBatchToken === batchToken) {
    setLatencyBatchRunning(false);
  }
}

function updateLatencyControls() {
  const selected = active();
  const disabled = !selected || selected.status !== "running" || !state.proxyApply || state.latencyBatchRunning;
  const hasLatencyTarget = state.proxyGroups.some((group) => currentLatencyTarget(group));
  el.testAllLatency.disabled = disabled || !hasLatencyTarget;
  el.testAllRealLatency.disabled = disabled || !hasLatencyTarget;
  for (const button of el.proxiesList.querySelectorAll(".proxy-group-actions button")) {
    button.disabled = disabled || button.dataset.testable === "false";
  }
}

async function api(path, options = {}) {
  const res = await fetch(path, {
    headers: { "Content-Type": "application/json", "X-Mihomo-Fleet": "1", ...(options.headers || {}) },
    ...options,
  });
  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`;
    try {
      const body = await res.json();
      if (body.error) message = body.error;
    } catch {
      // 保留状态消息
    }
    throw new Error(message);
  }
  if (res.status === 204) return null;
  return res.json();
}

function showMessage(text, kind = "info") {
  if (!text) {
    el.message.classList.add("hidden");
    el.message.textContent = "";
    return;
  }
  el.message.textContent = localizedMessage(text);
  el.message.className = `message ${kind === "error" ? "error" : ""}`;
}

async function refresh(options = {}) {
  try {
    const [system, profiles, list] = await Promise.all([
      api("/api/system"),
      api("/api/profiles"),
      api("/api/instances"),
    ]);
    state.system = system;
    state.profiles = profiles.profiles || [];
    if (!state.bulkRunning || options.forceInstances) {
      state.instances = list.instances || [];
      if (!state.activeId && state.instances.length) {
        state.activeId = state.instances[0].id;
      }
      if (state.activeId && !state.instances.some((item) => item.id === state.activeId)) {
        state.activeId = state.instances[0]?.id || "";
      }
    }
    localStorage.setItem("activeInstance", state.activeId);
    render();
    await refreshActiveDetails();
  } catch (err) {
    showMessage(err.message, "error");
  }
}

function render() {
  const selected = active();
  renderSystem();
  renderSelector(selected);
  renderList(selected);
  updateBulkControls();
  renderPanels(selected);
  updateCreateProfileControls();
}

function renderSystem() {
  if (!state.system) return;
  const found = state.system.mihomoFound;
  const appVersion = `Mihomo Fleet v${state.system.appVersion || "dev"}`;
  el.systemLine.textContent = found
    ? `${appVersion}，控制器 127.0.0.1:${state.system.port}，mihomo ${state.system.version || "已检测到"}`
    : `${appVersion}，控制器 127.0.0.1:${state.system.port}，未找到 mihomo`;
  if (!found) {
    el.systemWarning.textContent = "未在 Mihomo Fleet 同目录或 PATH 中找到 mihomo。你仍然可以创建实例，但启动需要同目录二进制文件，或通过 -mihomo 参数指定路径。";
    el.systemWarning.classList.remove("hidden");
  } else {
    el.systemWarning.classList.add("hidden");
  }
}

function renderSelector(selected) {
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

function renderList(selected) {
  el.instanceList.innerHTML = "";
  for (const item of state.instances) {
    const button = document.createElement("button");
    button.className = `instance-row ${selected && selected.id === item.id ? "active" : ""}`;
    button.type = "button";
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
    button.querySelector(".row-meta").textContent = `混合端口 ${item.mixedPort} · ${profile}${choice}`;
    button.addEventListener("click", () => selectInstance(item.id));
    el.instanceList.append(button);
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

function renderPanels(selected) {
  el.createPanel.classList.toggle("hidden", !state.creating);
  el.emptyPanel.classList.toggle("hidden", state.creating || state.instances.length > 0);
  el.detailPanel.classList.toggle("hidden", state.creating || !selected);
  if (!selected) return;

  el.detailName.textContent = selected.name;
  el.detailMeta.textContent = selected.lastError ? localizedMessage(selected.lastError) : `${statusText(selected.status)} · ${selected.id}`;
  el.metricStatus.textContent = statusText(selected.status);
  el.metricPid.textContent = selected.pid || "无";
  el.metricMixed.textContent = selected.mixedPort;
  el.metricController.textContent = selected.controllerPort;
  el.overviewMixed.textContent = `127.0.0.1:${selected.mixedPort}`;
  el.overviewController.textContent = `127.0.0.1:${selected.controllerPort}`;
  el.overviewProfile.textContent = selected.profileName || selected.profileId || "无";
  el.overviewUserConfig.textContent = selected.profileConfigPath || selected.userConfigPath;
  el.overviewRuntimeConfig.textContent = selected.runtimeConfigPath;
  el.overviewSelection.textContent = selectionSummary(selected);
  el.editName.value = selected.name;
  renderProfileOptions(el.editProfile, selected.profileId, false);
  el.editMixedPort.value = selected.mixedPort;
  el.editControllerPort.value = selected.controllerPort;
  el.startBtn.disabled = state.bulkRunning || selected.status === "running" || selected.status === "starting";
  el.stopBtn.disabled = state.bulkRunning || selected.status !== "running";
  el.restartBtn.disabled = state.bulkRunning;
  el.deleteBtn.disabled = state.bulkRunning;
  updateLatencyControls();
  renderSubscriptionSettings(selected);
}

function selectionSummary(item) {
  const entries = Object.entries(item.selectedProxies || {});
  if (entries.length) {
    return entries.map(([group, proxy]) => `${group} -> ${proxy}`).join("；");
  }
  return item.selectedProxy ? `${item.selectedGroup} -> ${item.selectedProxy}` : "无";
}

function renderSubscriptionSettings(selected) {
  const profile = profileById(selected.profileId);
  if (!profile) {
    el.profileMeta.textContent = "配置档尚未加载。";
    el.subscriptionSettings.classList.add("hidden");
    return;
  }
  const isSubscription = Boolean(profile.subscriptionUrl);
  el.profileMeta.textContent = isSubscription
    ? `订阅缓存：${formatProfileUpdate(profile)}`
    : "手写配置：可以直接编辑 YAML。";
  el.subscriptionSettings.classList.toggle("hidden", !isSubscription);
  el.saveConfig.disabled = isSubscription;
  el.configEditor.readOnly = isSubscription;
  if (!isSubscription) return;
  const editing = el.subscriptionSettings.dataset.dirty === "1" && el.subscriptionSettings.dataset.profileId === profile.id;
  el.subscriptionSettings.dataset.profileId = profile.id;
  if (!editing) {
    el.subscriptionUrl.value = profile.subscriptionUrl || "";
    el.subscriptionAutoUpdate.checked = Boolean(profile.autoUpdate);
    el.subscriptionInterval.value = profile.updateIntervalMinutes || "";
  }
  el.subscriptionInfo.textContent = formatSubscriptionInfo(profile);
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
    opt.textContent = profile.name;
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

async function updateCreateProfileControls() {
  if (!el.createProfile.options.length) {
    renderProfileOptions(el.createProfile, el.createProfile.value, true);
  }
  const createNew = el.createProfile.value === newProfileValue || state.profiles.length === 0;
  const subscriptionMode = state.createSource === "subscription" && createNew;
  el.createSourceTabs.classList.toggle("hidden", !createNew);
  el.createManualMode.classList.toggle("active", state.createSource === "manual");
  el.createSubscriptionMode.classList.toggle("active", state.createSource === "subscription");
  el.createSubscriptionFields.classList.toggle("hidden", !subscriptionMode);
  el.createConfigWrap.classList.toggle("hidden", subscriptionMode);
  el.createProfileName.disabled = !createNew;
  el.createConfig.disabled = !createNew || subscriptionMode;
  el.createSubscriptionUrl.disabled = !subscriptionMode;
  el.createAutoUpdate.disabled = !subscriptionMode;
  el.createUpdateInterval.disabled = !subscriptionMode;
  if (subscriptionMode) {
    if (!el.createUpdateInterval.value) el.createUpdateInterval.value = "360";
    return;
  }
  if (createNew) {
    if (!el.createConfig.value) el.createConfig.value = defaultConfig;
    return;
  }
  const selectedProfile = profileById(el.createProfile.value);
  if (!selectedProfile || el.createConfig.dataset.profileId === selectedProfile.id) return;
  el.createConfig.dataset.profileId = selectedProfile.id;
  try {
    const payload = await api(`/api/profiles/${selectedProfile.id}/config`);
    el.createConfig.value = payload.config || "";
  } catch (err) {
    el.createConfig.value = localizedMessage(err.message);
  }
}

function selectInstance(id) {
  if (state.activeId !== id) {
    clearLatencyStateForInstance(state.activeId);
    el.configEditor.value = "";
    el.configEditor.dataset.id = "";
    el.configEditor.dataset.profileId = "";
    el.configEditor.dataset.dirty = "";
    el.subscriptionSettings.dataset.profileId = "";
    el.subscriptionSettings.dataset.dirty = "";
    state.proxyGroups = [];
    state.proxyApply = false;
    state.latencyBatchRunning = false;
    state.latencyBatchToken += 1;
  }
  state.activeId = id;
  state.creating = false;
  localStorage.setItem("activeInstance", id);
  render();
  refreshActiveDetails();
}

function showCreate() {
  state.creating = true;
  state.createSource = "manual";
  el.createName.value = "";
  el.createMixedPort.value = "";
  el.createControllerPort.value = "";
  renderProfileOptions(el.createProfile, state.profiles[0]?.id || newProfileValue, true);
  el.createProfileName.value = "";
  el.createSubscriptionUrl.value = "";
  el.createAutoUpdate.checked = true;
  el.createUpdateInterval.value = "360";
  el.createConfig.value = defaultConfig;
  el.createConfig.dataset.profileId = "";
  showMessage("");
  render();
}

async function refreshActiveDetails() {
  const selected = active();
  if (!selected || state.creating) return;
  if (
    el.configEditor.dataset.dirty !== "1" &&
    (!el.configEditor.value ||
      el.configEditor.dataset.id !== selected.id ||
      el.configEditor.dataset.profileId !== selected.profileId)
  ) {
    try {
      const cfg = await api(`/api/instances/${selected.id}/config`);
      el.configEditor.value = cfg.config;
      el.configEditor.dataset.id = selected.id;
      el.configEditor.dataset.profileId = selected.profileId;
      el.configEditor.dataset.dirty = "";
    } catch (err) {
      showMessage(err.message, "error");
    }
  }
  if (state.activeTab === "logs") await refreshLogs();
  if (state.activeTab === "proxies") await refreshProxies();
}

async function refreshLogs() {
  const selected = active();
  if (!selected) return;
  try {
    const payload = await api(`/api/instances/${selected.id}/logs`);
    el.logs.textContent = (payload.lines || []).join("\n") || "还没有进程日志。";
    el.logs.scrollTop = el.logs.scrollHeight;
  } catch (err) {
    el.logs.textContent = localizedMessage(err.message);
  }
}

async function refreshProxies() {
  const selected = active();
  if (!selected) return;
  try {
    let groups = [];
    let apply = false;
    if (selected.status === "running") {
      const [payload, profileGroups] = await Promise.all([
        api(`/api/mihomo/${selected.id}/proxies`),
        loadProfileProxyGroupsForRuntime(selected),
      ]);
      const proxies = payload.proxies || {};
      groups = alignProxyGroupsToProfileOrder(Object.values(proxies).filter((item) => Array.isArray(item.all)), profileGroups);
      apply = true;
      el.proxySource.textContent = "当前读取运行中的 mihomo 节点，选择后立即应用并保存。";
    } else {
      groups = await loadProfileProxyGroups(selected);
      el.proxySource.textContent = "当前读取缓存配置，选择会保存到实例，下次启动后自动恢复。";
    }
    state.proxyGroups = groups;
    state.proxyApply = apply;
    updateLatencyControls();
    if (!groups.length) {
      el.proxiesList.innerHTML = `<div class="warning">没有可显示的节点组。使用 proxy-providers 的订阅需要启动实例后读取 mihomo 运行态节点。</div>`;
      return;
    }
    renderProxyGroups(groups, apply);
  } catch (err) {
    el.proxiesList.innerHTML = `<div class="message error">${escapeHTML(localizedMessage(err.message))}</div>`;
  }
}

function renderProxyGroups(groups, apply) {
  const selected = active();
  const filter = el.proxyFilter.value.trim().toLowerCase();
  el.proxiesList.innerHTML = "";
  for (const group of groups) {
    const names = (group.all || []).filter((name) => !filter || name.toLowerCase().includes(filter) || group.name.toLowerCase().includes(filter));
    if (!names.length) continue;
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
    const currentName = currentLatencyTarget(group);
    if (currentName) {
      const currentChips = document.createElement("span");
      currentChips.className = "latency-chips current";
      currentChips.append(
        renderLatencyChip(selected, group.name, currentName, latencyKinds.url),
        renderLatencyChip(selected, group.name, currentName, latencyKinds.real),
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
    latencyButton.dataset.testable = currentName ? "true" : "false";
    latencyButton.disabled = !apply || !currentName || state.latencyBatchRunning;
    latencyButton.addEventListener("click", () => testGroupLatency(group, latencyKinds.url));
    const realLatencyButton = document.createElement("button");
    realLatencyButton.type = "button";
    realLatencyButton.textContent = "真延迟";
    realLatencyButton.title = disabledReason;
    realLatencyButton.dataset.testable = currentName ? "true" : "false";
    realLatencyButton.disabled = !apply || !currentName || state.latencyBatchRunning;
    realLatencyButton.addEventListener("click", () => testGroupLatency(group, latencyKinds.real));
    actions.append(latencyButton, realLatencyButton);
    head.append(title, metaWrap, actions);

    const grid = document.createElement("div");
    grid.className = "proxy-grid";
    for (const name of names) {
      const button = document.createElement("button");
      button.type = "button";
      button.className = `proxy-choice ${group.now === name ? "selected" : ""}`;
      button.title = name;
      button.setAttribute("aria-pressed", group.now === name ? "true" : "false");
      button.addEventListener("click", () => selectProxy(group.name, name, apply));
      const nameLabel = document.createElement("span");
      nameLabel.className = "proxy-name";
      nameLabel.textContent = name;
      button.append(nameLabel);
      grid.append(button);
    }
    section.append(head, grid);
    el.proxiesList.append(section);
  }
  if (!el.proxiesList.children.length) {
    el.proxiesList.innerHTML = `<div class="warning">没有匹配的节点。</div>`;
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

async function testGroupLatency(group, kind) {
  if (state.latencyBatchRunning) return;
  const batchToken = beginLatencyBatch();
  try {
    await runGroupLatency(group, kind, batchToken);
  } finally {
    endLatencyBatch(batchToken);
  }
}

async function runGroupLatency(group, kind, batchToken) {
  const selected = active();
  if (!selected) return;
  if (selected.status !== "running") {
    showMessage("instance must be running to test latency", "error");
    return;
  }
  const name = currentLatencyTarget(group);
  if (!name) return;
  const { url, timeoutMs } = latencySettings();
  if (kind === latencyKinds.url) {
    await testGroupURLLatency(selected, group.name, name, url, timeoutMs, batchToken);
  } else {
    await testGroupRealLatency(selected, group.name, name, url, timeoutMs, batchToken);
  }
}

async function testGroupURLLatency(selected, groupName, proxyName, url, timeoutMs, batchToken) {
  setLatencyRunning(selected.id, groupName, proxyName, latencyKinds.url, true);
  updateLatencyChip(selected.id, groupName, proxyName, latencyKinds.url);
  try {
    const payload = await api(`/api/instances/${selected.id}/latency`, {
      method: "POST",
      body: JSON.stringify({ group: groupName, proxy: proxyName, kind: latencyKinds.url, url, timeoutMs }),
    });
    if (state.activeId === selected.id && state.latencyBatchToken === batchToken) {
      setLatencyResult(selected.id, groupName, proxyName, latencyKinds.url, { delay: Number(payload.delay) || 0, error: "" });
    }
  } catch (err) {
    if (state.activeId === selected.id && state.latencyBatchToken === batchToken) {
      setLatencyResult(selected.id, groupName, proxyName, latencyKinds.url, { delay: 0, error: err.message });
    }
    showMessage(err.message, "error");
  } finally {
    setLatencyRunning(selected.id, groupName, proxyName, latencyKinds.url, false);
    updateLatencyChip(selected.id, groupName, proxyName, latencyKinds.url);
  }
}

async function testGroupRealLatency(selected, groupName, proxyName, url, timeoutMs, batchToken) {
  setLatencyRunning(selected.id, groupName, proxyName, latencyKinds.real, true);
  updateLatencyChip(selected.id, groupName, proxyName, latencyKinds.real);
  try {
    const payload = await api(`/api/instances/${selected.id}/latency`, {
      method: "POST",
      body: JSON.stringify({ group: groupName, proxy: proxyName, kind: latencyKinds.real, url, timeoutMs }),
    });
    if (state.activeId === selected.id && state.latencyBatchToken === batchToken) {
      setLatencyResult(selected.id, groupName, proxyName, latencyKinds.real, { delay: Number(payload.delay) || 0, error: "" });
    }
  } catch (err) {
    if (state.activeId === selected.id && state.latencyBatchToken === batchToken) {
      setLatencyResult(selected.id, groupName, proxyName, latencyKinds.real, { delay: 0, error: err.message });
    }
    showMessage(err.message, "error");
  } finally {
    setLatencyRunning(selected.id, groupName, proxyName, latencyKinds.real, false);
    updateLatencyChip(selected.id, groupName, proxyName, latencyKinds.real);
  }
}

async function testAllLatency(kind) {
  const selected = active();
  if (!selected) return;
  if (selected.status !== "running") {
    showMessage("instance must be running to test latency", "error");
    return;
  }
  if (state.latencyBatchRunning) return;
  const batchToken = beginLatencyBatch();
  try {
    const groups = state.proxyGroups.filter((group) => currentLatencyTarget(group));
    for (const group of groups) {
      if (state.activeId !== selected.id || state.latencyBatchToken !== batchToken) return;
      await runGroupLatency(group, kind, batchToken);
    }
  } finally {
    endLatencyBatch(batchToken);
  }
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

function escapeHTML(input) {
  return String(input).replace(/[&<>"']/g, (ch) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#039;",
  }[ch]));
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

function formatBatchMessage(action, payload) {
  const verb = action === "start-all" ? "启动" : "关闭";
  const total = Number(payload.total) || 0;
  const success = Number(payload.success) || 0;
  const failed = Number(payload.failed) || 0;
  if (total === 0) return `没有可${verb}的实例。`;

  let text = failed
    ? `批量${verb}完成：成功 ${success}/${total}，失败 ${failed}。`
    : `批量${verb}完成：成功 ${success}/${total}。`;
  const details = (payload.errors || [])
    .slice(0, 2)
    .map((item) => `${item.name || item.id}: ${localizedMessage(item.error)}`);
  if (details.length) text += ` ${details.join("；")}`;
  return text;
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

document.querySelectorAll(".tab").forEach((button) => {
  button.addEventListener("click", async () => {
    state.activeTab = button.dataset.tab;
    document.querySelectorAll(".tab").forEach((tab) => tab.classList.toggle("active", tab === button));
    document.querySelectorAll(".tab-panel").forEach((panel) => panel.classList.add("hidden"));
    document.querySelector(`#tab-${state.activeTab}`).classList.remove("hidden");
    await refreshActiveDetails();
  });
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

el.createSubmit.addEventListener("click", async () => {
  try {
    let profileId = el.createProfile.value;
    if (profileId === newProfileValue || state.profiles.length === 0) {
      const isSubscription = state.createSource === "subscription";
      const profile = await api("/api/profiles", {
        method: "POST",
        body: JSON.stringify({
          name: el.createProfileName.value.trim() || `${el.createName.value.trim() || "默认"}配置档`,
          config: isSubscription ? "" : el.createConfig.value,
          subscriptionUrl: isSubscription ? el.createSubscriptionUrl.value.trim() : "",
          autoUpdate: isSubscription ? el.createAutoUpdate.checked : false,
          updateIntervalMinutes: isSubscription ? Number(el.createUpdateInterval.value) || 0 : 0,
        }),
      });
      profileId = profile.id;
    }
    const payload = {
      name: el.createName.value.trim(),
      profileId,
      mixedPort: Number(el.createMixedPort.value) || 0,
      controllerPort: Number(el.createControllerPort.value) || 0,
    };
    const created = await api("/api/instances", {
      method: "POST",
      body: JSON.stringify(payload),
    });
    state.activeId = created.id;
    state.creating = false;
    el.configEditor.value = "";
    el.configEditor.dataset.id = "";
    el.configEditor.dataset.profileId = "";
    el.configEditor.dataset.dirty = "";
    showMessage("实例已创建。");
    await refresh();
  } catch (err) {
    showMessage(err.message, "error");
  }
});

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

el.deleteBtn.addEventListener("click", async () => {
  const selected = active();
  if (!selected) return;
  if (!confirm(`确定删除 ${selected.name}？`)) return;
  try {
    await api(`/api/instances/${selected.id}`, { method: "DELETE" });
    state.activeId = "";
    el.configEditor.value = "";
    el.configEditor.dataset.id = "";
    el.configEditor.dataset.profileId = "";
    el.configEditor.dataset.dirty = "";
    showMessage("实例已删除。");
    await refresh();
  } catch (err) {
    showMessage(err.message, "error");
  }
});

el.saveBasics.addEventListener("click", async () => {
  const selected = active();
  if (!selected) return;
  try {
    await api(`/api/instances/${selected.id}`, {
      method: "PUT",
      body: JSON.stringify({
        name: el.editName.value.trim(),
        profileId: el.editProfile.value,
        mixedPort: Number(el.editMixedPort.value),
        controllerPort: Number(el.editControllerPort.value),
      }),
    });
    showMessage("基础信息已保存。");
    await refresh();
  } catch (err) {
    showMessage(err.message, "error");
  }
});

el.saveConfig.addEventListener("click", async () => {
  const selected = active();
  if (!selected) return;
  try {
    await api(`/api/instances/${selected.id}`, {
      method: "PUT",
      body: JSON.stringify({ config: el.configEditor.value }),
    });
    showMessage("配置已保存。");
    el.configEditor.dataset.dirty = "";
    await refresh();
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
    el.configEditor.value = "";
    el.configEditor.dataset.id = "";
    el.configEditor.dataset.profileId = "";
    el.configEditor.dataset.dirty = "";
    render();
    await refreshActiveDetails();
  } catch (err) {
    showMessage(err.message, "error");
  } finally {
    el.refreshSubscription.disabled = false;
  }
});

el.configEditor.addEventListener("input", () => {
  el.configEditor.dataset.dirty = "1";
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
  if (state.activeTab === "proxies") refreshProxies();
});

el.latencyUrl.value = localStorage.getItem("fleetLatencyUrl") || defaultLatencyUrl;
el.latencyTimeout.value = localStorage.getItem("fleetLatencyTimeout") || String(defaultLatencyTimeout);
el.latencyUrl.addEventListener("change", latencySettings);
el.latencyTimeout.addEventListener("change", latencySettings);
el.testAllLatency.addEventListener("click", () => testAllLatency(latencyKinds.url));
el.testAllRealLatency.addEventListener("click", () => testAllLatency(latencyKinds.real));

el.createConfig.value = defaultConfig;
refresh();
setInterval(refresh, 4000);
setInterval(() => {
  if (state.activeTab === "logs") refreshLogs();
  if (state.activeTab === "proxies") refreshProxies();
}, 1800);
