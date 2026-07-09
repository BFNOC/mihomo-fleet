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
const legacyDefaultLatencyUrl = "https://www.gstatic.com/generate_204";
const legacyDefaultLatencyTimeout = "5000";
const defaultLatencyUrl = "http://cp.cloudflare.com/generate_204";
const defaultLatencyTimeout = 10000;
const latencyBatchConcurrency = 4;
const latencyKeySeparator = "\u001f";
const logStickThreshold = 24;
const defaultProxyBind = "127.0.0.1";
const latencyKinds = {
  url: "url",
  real: "real",
};
const instanceModes = {
  rule: "rule",
  globalChain: "global-chain",
};
const proxyCopyDefs = [
  { id: "addr", label: "地址", title: "复制主机和端口" },
  { id: "http", label: "HTTP", title: "复制 HTTP 代理地址" },
  { id: "socks", label: "SOCKS", title: "复制 SOCKS5 代理地址" },
  { id: "env", label: "ENV", title: "复制 bash/zsh export 环境变量" },
];

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
  cloneRunning: false,
  editInstanceId: "",
  editDirty: false,
  editVersion: 0,
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
  "mixed and controller ports must differ": "混合端口与控制器端口不能相同。",
  "stop the instance before changing proxy bind": "修改代理绑定地址前请先停止该实例。",
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
  "global-chain mode requires proxies, proxy-providers, or local proxies": "全局链式模式需要订阅节点、provider 或本地节点。",
  "instance is starting; retry once it finishes starting": "实例正在启动，请稍后重试。",
  "missing or invalid API token": "API 令牌缺失或无效。",
};

const errorPatterns = [
  [/^profile "(.+)" not found$/, (match) => `配置档 ${match[1]} 不存在。`],
  [/^profile "(.+)" is not a subscription profile$/, (match) => `配置档 ${match[1]} 不是订阅配置档。`],
  [/^profile "(.+)" subscription update is already running$/, (match) => `配置档 ${match[1]} 正在更新订阅。`],
  [/^instance "(.+)" not found$/, (match) => `实例 ${match[1]} 不存在。`],
  [/^instance "(.+)" is being deleted$/, (match) => `实例 ${match[1]} 正在删除中，请稍后重试。`],
  [/^stop instance before delete: (.+)$/, (match) => `删除前请先停止实例：${match[1]}`],
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
  [/^instance mode "(.+)" is invalid$/, (match) => `实例模式 ${match[1]} 无效。`],
  [/^parse local proxies: (.+)$/, (match) => `解析本地节点失败：${match[1]}`],
  [/^local proxy (.+) is missing name$/, (match) => `本地节点 ${match[1]} 缺少 name。`],
  [/^local proxy name "(.+)" is duplicated$/, (match) => `本地节点 ${match[1]} 重名。`],
  [/^local proxy name "(.+)" conflicts with generated global-chain group$/, (match) => `本地节点 ${match[1]} 与内置链路组重名。`],
  [/^local proxy name "(.+)" conflicts with profile proxy$/, (match) => `本地节点 ${match[1]} 与配置档节点重名。`],
  [/^chain references unknown proxy or group "(.+)"$/, (match) => `链路顺序引用了不存在的节点或组：${match[1]}。`],
  [/^chain cannot reference generated relay group "(.+)"$/, (match) => `链路顺序不能引用 ${match[1]} 自身。`],
  [/^chain contains duplicate member "(.+)"$/, (match) => `链路顺序重复引用了 ${match[1]}。`],
  [/^global-chain mode has no selectable proxy after chain members$/, () => "链路节点移除后没有可选择的节点。请补充订阅/出口节点，或调整链路顺序。"],
  [/^proxy bind address "(.+)" must not include a port; use the mixed port field instead$/, (match) => `代理绑定地址 ${match[1]} 不要写端口，请使用混合端口字段。`],
  [/^proxy bind address "(.+)" must be an IP address, localhost, all, or \*$/, (match) => `代理绑定地址 ${match[1]} 无效，请填写 IP、localhost、all 或 *。`],
  [/^proxy bind address "(.+)" has invalid IPv6 brackets$/, (match) => `代理绑定地址 ${match[1]} 的 IPv6 方括号不完整。`],
];

const el = {
  systemLine: document.querySelector("#systemLine"),
  systemWarning: document.querySelector("#systemWarning"),
  instanceSelect: document.querySelector("#instanceSelect"),
  instanceList: document.querySelector("#instanceList"),
  portMatrixCount: document.querySelector("#portMatrixCount"),
  portMatrixList: document.querySelector("#portMatrixList"),
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
  createMode: document.querySelector("#createMode"),
  createSubscriptionFields: document.querySelector("#createSubscriptionFields"),
  createSubscriptionUrl: document.querySelector("#createSubscriptionUrl"),
  createAutoUpdate: document.querySelector("#createAutoUpdate"),
  createUpdateInterval: document.querySelector("#createUpdateInterval"),
  createMixedPort: document.querySelector("#createMixedPort"),
  createProxyBind: document.querySelector("#createProxyBind"),
  createControllerPort: document.querySelector("#createControllerPort"),
  createConfigWrap: document.querySelector("#createConfigWrap"),
  createConfig: document.querySelector("#createConfig"),
  createChainFields: document.querySelector("#createChainFields"),
  createLocalProxies: document.querySelector("#createLocalProxies"),
  createChain: document.querySelector("#createChain"),
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
  cloneBtn: document.querySelector("#cloneBtn"),
  deleteBtn: document.querySelector("#deleteBtn"),
  overviewMixed: document.querySelector("#overviewMixed"),
  overviewProxyBind: document.querySelector("#overviewProxyBind"),
  overviewController: document.querySelector("#overviewController"),
  overviewMode: document.querySelector("#overviewMode"),
  overviewChain: document.querySelector("#overviewChain"),
  overviewProfile: document.querySelector("#overviewProfile"),
  overviewUserConfig: document.querySelector("#overviewUserConfig"),
  overviewRuntimeConfig: document.querySelector("#overviewRuntimeConfig"),
  overviewSelection: document.querySelector("#overviewSelection"),
  editName: document.querySelector("#editName"),
  editProfile: document.querySelector("#editProfile"),
  editMode: document.querySelector("#editMode"),
  editMixedPort: document.querySelector("#editMixedPort"),
  editProxyBind: document.querySelector("#editProxyBind"),
  editControllerPort: document.querySelector("#editControllerPort"),
  editChainFields: document.querySelector("#editChainFields"),
  editLocalProxies: document.querySelector("#editLocalProxies"),
  editChain: document.querySelector("#editChain"),
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

const proxyTooltip = document.createElement("div");
proxyTooltip.id = "proxyTooltip";
proxyTooltip.className = "proxy-tooltip hidden";
proxyTooltip.setAttribute("role", "tooltip");
document.body.append(proxyTooltip);

const proxyTooltipHoverQuery = window.matchMedia("(hover: hover) and (pointer: fine)");

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

function instanceMode(item) {
  return item?.mode === instanceModes.globalChain ? instanceModes.globalChain : instanceModes.rule;
}

function modeLabel(mode) {
  return mode === instanceModes.globalChain ? "全局链式" : "规则分流";
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

function normalizeStoredLatencyUrl(value) {
  const url = String(value || "").trim();
  return !url || url === legacyDefaultLatencyUrl ? defaultLatencyUrl : url;
}

function normalizeStoredLatencyTimeout(value, storedUrl) {
  const timeout = String(value || "").trim();
  const url = String(storedUrl || "").trim();
  if (!timeout || (timeout === legacyDefaultLatencyTimeout && (!url || url === legacyDefaultLatencyUrl))) {
    return String(defaultLatencyTimeout);
  }
  return timeout;
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

function proxyLabelSources() {
  const sources = new Set();
  for (const profile of state.profiles) {
    const name = String(profile.name || "").trim();
    if (name) sources.add(name);
  }
  for (const instance of state.instances) {
    const name = String(instance.profileName || "").trim();
    if (name) sources.add(name);
  }
  return [...sources].sort((left, right) => right.length - left.length);
}

function splitProxyLabel(name, sources) {
  const full = String(name || "");
  for (const source of sources) {
    for (const separator of [" - ", "-"]) {
      const prefix = `${source}${separator}`;
      if (full.startsWith(prefix) && full.length > prefix.length) {
        return { source, name: full.slice(prefix.length).trimStart() };
      }
    }
  }
  return { source: "", name: full };
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
  updateLatencyControls();
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
  const title = result?.error || `${latencyTitle(kind)} ${value}`;
  chip.className = `latency-chip ${latencyTone(result, running)}`;
  chip.textContent = `${latencyLabel(kind)} ${value}`;
  chip.title = title;
  chip.setAttribute("aria-label", title);
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
  const disabled = !selected || selected.status !== "running" || !state.proxyApply;
  const hasLatencyTarget = state.proxyGroups.some((group) => currentLatencyTarget(group));
  el.testAllLatency.disabled = disabled || !hasLatencyTarget || state.latencyBatchRunning;
  el.testAllRealLatency.disabled = disabled || !hasLatencyTarget || state.latencyBatchRunning;
  for (const button of el.proxiesList.querySelectorAll(".proxy-group-actions button")) {
    const running =
      selected &&
      button.dataset.testable !== "false" &&
      isLatencyRunning(selected.id, button.dataset.groupName, button.dataset.proxyName, button.dataset.kind);
    button.disabled = disabled || button.dataset.testable === "false" || running;
    if (running) {
      button.title = "测速中";
    } else if (button.dataset.disabledReason !== undefined) {
      button.title = button.dataset.disabledReason;
    }
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

const API_SECRET_STORAGE_KEY = "fleetApiSecret";

async function buildApiError(res) {
  let message = `${res.status} ${res.statusText}`;
  try {
    const body = await res.json();
    if (body.error) message = body.error;
  } catch {
    // 保留状态消息
  }
  return new Error(message);
}

// 令牌弹窗每个页面加载周期最多出现一次：apiTokenPromptShown 在弹窗首次得到结果
// （无论用户输入了什么、取消，还是重试后仍然 401）后置位，此后 api() 不再弹窗，
// 只把错误正常抛给调用方展示。apiTokenPromptInFlight 让并发触发的多个 401
// （例如首屏 refresh() 的三个并行请求）共享同一次弹窗，而不是各弹一次。
let apiTokenPromptShown = false;
let apiTokenPromptInFlight = null;

function requestApiToken() {
  if (apiTokenPromptShown) return Promise.resolve(null);
  if (apiTokenPromptInFlight) return apiTokenPromptInFlight;
  apiTokenPromptInFlight = Promise.resolve()
    .then(() => window.prompt("此面板需要 API 令牌才能访问，请输入 -api-secret 配置的令牌："))
    .then((entered) => {
      apiTokenPromptShown = true;
      if (!entered) return null;
      localStorage.setItem(API_SECRET_STORAGE_KEY, entered);
      return entered;
    })
    .finally(() => {
      apiTokenPromptInFlight = null;
    });
  return apiTokenPromptInFlight;
}

async function api(path, options = {}) {
  const token = localStorage.getItem(API_SECRET_STORAGE_KEY) || "";
  const headers = { "Content-Type": "application/json", "X-Mihomo-Fleet": "1", ...(options.headers || {}) };
  if (token) headers.Authorization = `Bearer ${token}`;

  let res = await fetch(path, { ...options, headers });
  if (res.status === 401) {
    // 服务端配置了 -api-secret 但本地未保存令牌，或令牌已失效：提示用户输入一次并重试。
    const entered = await requestApiToken();
    if (entered) {
      headers.Authorization = `Bearer ${entered}`;
      res = await fetch(path, { ...options, headers });
    }
  }
  if (!res.ok) {
    throw await buildApiError(res);
  }
  if (res.status === 204) return null;
  return res.json();
}

let messageClearTimer = null;

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

let refreshSeq = 0;

async function refresh(options = {}) {
  const seq = ++refreshSeq;
  try {
    const [system, profiles, list] = await Promise.all([
      api("/api/system"),
      api("/api/profiles"),
      api("/api/instances"),
    ]);
    // 乱序返回校验：只有仍是最新一轮请求时才写入状态并渲染。
    if (seq !== refreshSeq) return;
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
    // periodic：4s 轮询通道跳过 logs/proxies，交给独立的 1.8s 通道处理，避免双重请求。
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
    button.querySelector(".row-meta").textContent = `混合端口 ${proxyPortLabel(item.mixedPort)} · ${profile}${choice}`;
    button.addEventListener("click", () => selectInstance(item.id));
    el.instanceList.append(button);
  }
}

function renderPortMatrix(selected) {
  const focusedKey = el.portMatrixList.contains(document.activeElement)
    ? document.activeElement.dataset.portFocus
    : "";
  const countText = `${state.instances.length} 个出口`;
  // 避免 aria-live 区域在每次矩阵重绘时重复播报相同数量。
  if (el.portMatrixCount.textContent !== countText) el.portMatrixCount.textContent = countText;
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

  // 自动刷新会重建列表；恢复端口矩阵内的焦点，避免键盘用户每 4 秒被打断。
  if (focusedKey) {
    restorePortMatrixFocus(focusedKey);
  }
}

function restorePortMatrixFocus(focusedKey) {
  const controls = [...el.portMatrixList.querySelectorAll("[data-port-focus]")]
    .filter((node) => !node.disabled);
  const instanceId = portFocusInstanceId(focusedKey);
  // 复制按钮可能因端口变化被禁用；先退回同实例，再退回第一个出口。
  const target = controls.find((node) => node.dataset.portFocus === focusedKey)
    || controls.find((node) => node.dataset.portFocus === `select:${instanceId}`)
    || controls.find((node) => node.dataset.portFocus?.startsWith("select:"));
  target?.focus({ preventScroll: true });
}

function portFocusInstanceId(focusedKey) {
  return focusedKey.split(":")[1] || "";
}

function proxyBindAddresses(item) {
  const values = String(item?.proxyBind || defaultProxyBind)
    .split(",")
    .map((value) => value.trim())
    .filter(Boolean);
  return values.length ? values : [defaultProxyBind];
}

function proxyEndpoint(port, host = defaultProxyBind) {
  const value = proxyPort(port);
  if (!value) return "";
  return `${formatProxyHost(host)}:${value}`;
}

function proxyEndpoints(item) {
  const port = proxyPort(item?.mixedPort);
  if (!port) return [];
  return proxyBindAddresses(item).map((host) => proxyEndpoint(port, host));
}

function proxyEndpointText(item) {
  const endpoints = proxyEndpoints(item);
  return endpoints.length ? endpoints.join("，") : "端口未分配";
}

function formatProxyHost(host) {
  const value = String(host || defaultProxyBind).trim();
  return value.includes(":") && !value.startsWith("[") ? `[${value}]` : value;
}

function proxyPortLabel(port) {
  return proxyPort(port) || "未分配";
}

function proxyPort(port) {
  if (typeof port !== "number" && typeof port !== "string") return 0;
  if (typeof port === "string" && !/^\d+$/.test(port.trim())) return 0;
  const value = Number(port);
  return Number.isInteger(value) && value >= 1 && value <= 65535 ? value : 0;
}

function proxyCopyPlaceholders() {
  return proxyCopyDefs.map((action) => ({ ...action, value: "" }));
}

function proxyCopyActions(item) {
  const endpoints = proxyEndpoints(item);
  if (!endpoints.length) return proxyCopyPlaceholders();
  const httpValues = endpoints.map((endpoint) => `http://${endpoint}`);
  const socksValues = endpoints.map((endpoint) => `socks5://${endpoint}`);
  const http = httpValues[0];
  const socks = socksValues[0];
  const values = {
    addr: endpoints.join("\n"),
    http: httpValues.join("\n"),
    socks: socksValues.join("\n"),
    env: proxyEnvExports(http, socks),
  };
  const messages = {
    addr: `已复制 ${item.name} 地址。`,
    http: `已复制 ${item.name} HTTP。`,
    socks: `已复制 ${item.name} SOCKS。`,
    env: `已复制 ${item.name} 环境变量。`,
  };
  return proxyCopyDefs.map((action) => ({
    ...action,
    value: values[action.id],
    message: messages[action.id],
  }));
}

function proxyEnvExports(http, socks) {
  return [
    `export HTTP_PROXY='${http}'`,
    `export HTTPS_PROXY='${http}'`,
    `export ALL_PROXY='${socks}'`,
    `export http_proxy='${http}'`,
    `export https_proxy='${http}'`,
    `export all_proxy='${socks}'`,
  ].join("\n");
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

async function writeClipboard(value) {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(value);
    return;
  }
  // 兼容非安全上下文或旧浏览器，localhost 以外也能复制端口文本。
  const textarea = document.createElement("textarea");
  const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  textarea.value = value;
  textarea.setAttribute("readonly", "");
  // 复制缓冲区只服务 execCommand，不进入键盘顺序或读屏树。
  textarea.tabIndex = -1;
  textarea.setAttribute("aria-hidden", "true");
  textarea.className = "clipboard-buffer";
  document.body.append(textarea);
  textarea.select();
  try {
    if (!document.execCommand("copy")) {
      throw new Error("复制失败。");
    }
  } finally {
    textarea.remove();
    if (previousFocus && document.contains(previousFocus)) previousFocus.focus({ preventScroll: true });
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
  if (!state.editDirty || state.editInstanceId !== selected.id) {
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
}

function selectionSummary(item) {
  const entries = Object.entries(item.selectedProxies || {});
  if (entries.length) {
    return entries.map(([group, proxy]) => `${group} -> ${proxy}`).join("；");
  }
  return item.selectedProxy ? `${item.selectedGroup} -> ${item.selectedProxy}` : "无";
}

function chainSummary(item) {
  if (instanceMode(item) !== instanceModes.globalChain) return "不适用";
  const chain = Array.isArray(item.chain) ? item.chain.filter(Boolean) : [];
  if (!chain.length) return "默认";
  return chain.map((name) => chainLabel(item, name)).join(" -> ");
}

function chainLabel(item, name) {
  if (name !== "节点选择") return name;
  const selected = item.selectedProxies?.[name]
    || (item.selectedGroup === name ? item.selectedProxy : "");
  return selected ? `${name}（${selected}）` : name;
}

function chainFromText(value) {
  return String(value || "")
    .split(/\r?\n/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function chainToText(values) {
  return Array.isArray(values) ? values.join("\n") : "";
}

function applyModeFields(prefix, mode) {
  const chainMode = mode === instanceModes.globalChain;
  el[`${prefix}ChainFields`].classList.toggle("hidden", !chainMode);
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
  el.configEditor.value = "";
  el.configEditor.dataset.id = "";
  el.configEditor.dataset.profileId = "";
  el.configEditor.dataset.dirty = "";
  el.subscriptionSettings.dataset.profileId = "";
  el.subscriptionSettings.dataset.dirty = "";
  el.logs.dataset.instanceId = "";
  state.proxyGroups = [];
  state.proxyApply = false;
  state.latencyBatchRunning = false;
  state.latencyBatchToken += 1;
  lastProxyGroupsSnapshot = "";
  // 同步清空节点列表 DOM：否则上一个实例的按钮在切换后到新实例首次 fetch 返回前
  // 仍然可点，selectProxy 会把旧实例的组名 POST 给新的 activeId。
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
    clearLatencyStateForInstance(state.activeId);
    clearActiveDetailCache();
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
}

async function refreshActiveDetails(options = {}) {
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
      // await 之后重新校验，避免慢响应覆盖用户正在编辑的内容或写错实例。
      if (state.activeId !== selected.id || el.configEditor.dataset.dirty === "1") return;
      el.configEditor.value = cfg.config;
      el.configEditor.dataset.id = selected.id;
      el.configEditor.dataset.profileId = selected.profileId;
      el.configEditor.dataset.dirty = "";
    } catch (err) {
      showMessage(err.message, "error");
    }
  }
  // 4s 轮询通道跳过 logs/proxies，避免与 1.8s 通道重复请求；其余触发点（切换实例/标签页等）仍立即刷新。
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
    if (el.logs.textContent !== text) {
      el.logs.textContent = text;
    }
    el.logs.dataset.instanceId = selected.id;
    if (shouldStick) {
      el.logs.scrollTop = el.logs.scrollHeight;
    }
  } catch (err) {
    el.logs.dataset.instanceId = "";
    el.logs.textContent = localizedMessage(err.message);
  }
}

function isLogScrolledToBottom() {
  return el.logs.scrollHeight - el.logs.scrollTop - el.logs.clientHeight <= logStickThreshold;
}

let proxiesRequestSeq = 0;

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
      // await 后校验：乱序返回或实例已切换时丢弃这次结果，避免展示/提交错误实例的节点组。
      if (seq !== proxiesRequestSeq || state.activeId !== selected.id) return;
      const proxies = payload.proxies || {};
      groups = alignProxyGroupsToProfileOrder(Object.values(proxies).filter((item) => Array.isArray(item.all)), profileGroups);
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

function filterRuntimeProxyGroups(selected, groups) {
  if (instanceMode(selected) !== instanceModes.globalChain) return groups;
  return groups.filter((group) => String(group.name || "").toUpperCase() !== "GLOBAL");
}

let lastProxyGroupsSnapshot = "";

// 快照包含分组结构、过滤词、节点标签拆分依据（labelSources），以及每组"当前节点"
// 实际渲染出的延迟数据，避免数据未变时无谓重建 DOM（丢焦点/闪断 tooltip），同时
// 不会因为漏掉某个渲染输入（延迟字段、labelSources）而在它变化后卡死不更新——例如
// 重命名 profile 后，节点组数据本身没变，但 splitProxyLabel 的拆分结果会变化。
function proxyGroupsRenderSnapshot(groups, apply, filter) {
  const selected = active();
  const labelSources = proxyLabelSources();
  const latencyBits = groups.map((group) => {
    const currentName = currentLatencyTarget(group);
    if (!currentName) return null;
    return [
      group.name,
      currentName,
      selected ? latencyResult(selected.id, group.name, currentName, latencyKinds.url) : null,
      selected ? isLatencyRunning(selected.id, group.name, currentName, latencyKinds.url) : false,
      selected ? latencyResult(selected.id, group.name, currentName, latencyKinds.real) : null,
      selected ? isLatencyRunning(selected.id, group.name, currentName, latencyKinds.real) : false,
    ];
  });
  return JSON.stringify({ instanceId: selected?.id || "", groups, apply, filter, labelSources, latencyBits });
}

function proxyFocusKey(groupName, proxyName) {
  return `${groupName}${latencyKeySeparator}${proxyName}`;
}

// 测速/真延迟按钮的焦点键与 proxyFocusKey 共享"组名在前"的前缀结构（同组回退可以
// 复用同一套 startsWith 逻辑），但用专门的 kind 标记加前缀，与节点选择按钮的键
// 空间区分开，避免重建后把节点选择按钮的焦点错误地"恢复"到测速按钮上（反之亦然）。
function latencyButtonFocusKey(groupName, kind) {
  return `${groupName}${latencyKeySeparator}latency-btn${latencyKeySeparator}${kind}`;
}

function capturedProxyFocusKey() {
  if (!el.proxiesList.contains(document.activeElement)) return "";
  return document.activeElement.dataset.proxyFocus || "";
}

// 参照 restorePortMatrixFocus：精确匹配 -> 同组第一个可用按钮 -> 列表第一个可用按钮。
// 按钮类型（节点选择 vs 测速/真延迟）通过各自键的前缀天然区分：同组回退只在
// startsWith(groupPrefix) 的候选里找，而 latencyButtonFocusKey 与 proxyFocusKey
// 除了共享的组名前缀外互不相同，因此不会跨类型误恢复焦点。
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

function isLatencyFocusKey(key) {
  return String(key || "").includes(`${latencyKeySeparator}latency-btn${latencyKeySeparator}`);
}

function renderProxyGroups(groups, apply) {
  const selected = active();
  const filter = el.proxyFilter.value.trim().toLowerCase();
  const snapshot = proxyGroupsRenderSnapshot(groups, apply, filter);
  if (snapshot === lastProxyGroupsSnapshot) return;
  lastProxyGroupsSnapshot = snapshot;
  const labelSources = proxyLabelSources();
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
    latencyButton.dataset.disabledReason = disabledReason;
    latencyButton.dataset.groupName = group.name;
    latencyButton.dataset.proxyName = currentName;
    latencyButton.dataset.kind = latencyKinds.url;
    latencyButton.dataset.testable = currentName ? "true" : "false";
    latencyButton.dataset.proxyFocus = latencyButtonFocusKey(group.name, latencyKinds.url);
    latencyButton.disabled = !apply || !currentName || (selected && isLatencyRunning(selected.id, group.name, currentName, latencyKinds.url));
    latencyButton.addEventListener("click", () => testGroupLatency(group, latencyKinds.url));
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
    realLatencyButton.disabled = !apply || !currentName || (selected && isLatencyRunning(selected.id, group.name, currentName, latencyKinds.real));
    realLatencyButton.addEventListener("click", () => testGroupLatency(group, latencyKinds.real));
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
  // 自动刷新会重建列表；恢复节点列表内的焦点，避免键盘用户每 1.8 秒被打断。
  restoreProxyListFocus(focusedKey);
}

function isSelectableProxyGroup(group) {
  const type = String(group?.type || "select").toLowerCase();
  return type !== "relay";
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
  await runGroupLatency(group, kind, { notifyErrors: true });
}

async function runGroupLatency(group, kind, options = {}) {
  const selected = active();
  if (!selected) return;
  if (selected.status !== "running") {
    showMessage("instance must be running to test latency", "error");
    return;
  }
  const name = currentLatencyTarget(group);
  if (!name) return;
  if (isLatencyRunning(selected.id, group.name, name, kind)) return;
  const { url, timeoutMs } = latencySettings();
  const batchToken = options.batchToken;
  if (kind === latencyKinds.url) {
    await testGroupURLLatency(selected, group.name, name, url, timeoutMs, batchToken, options);
  } else {
    await testGroupRealLatency(selected, group.name, name, url, timeoutMs, batchToken, options);
  }
}

async function testGroupURLLatency(selected, groupName, proxyName, url, timeoutMs, batchToken, options = {}) {
  setLatencyRunning(selected.id, groupName, proxyName, latencyKinds.url, true);
  updateLatencyChip(selected.id, groupName, proxyName, latencyKinds.url);
  try {
    const payload = await api(`/api/instances/${selected.id}/latency`, {
      method: "POST",
      body: JSON.stringify({ group: groupName, proxy: proxyName, kind: latencyKinds.url, url, timeoutMs }),
    });
    if (shouldApplyLatencyResult(selected.id, batchToken)) {
      setLatencyResult(selected.id, groupName, proxyName, latencyKinds.url, { delay: Number(payload.delay) || 0, error: "" });
    }
  } catch (err) {
    if (shouldApplyLatencyResult(selected.id, batchToken)) {
      setLatencyResult(selected.id, groupName, proxyName, latencyKinds.url, { delay: 0, error: localizedMessage(err.message) });
    }
    if (options.notifyErrors) showMessage(err.message, "error");
  } finally {
    setLatencyRunning(selected.id, groupName, proxyName, latencyKinds.url, false);
    updateLatencyChip(selected.id, groupName, proxyName, latencyKinds.url);
  }
}

async function testGroupRealLatency(selected, groupName, proxyName, url, timeoutMs, batchToken, options = {}) {
  setLatencyRunning(selected.id, groupName, proxyName, latencyKinds.real, true);
  updateLatencyChip(selected.id, groupName, proxyName, latencyKinds.real);
  try {
    const payload = await api(`/api/instances/${selected.id}/latency`, {
      method: "POST",
      body: JSON.stringify({ group: groupName, proxy: proxyName, kind: latencyKinds.real, url, timeoutMs }),
    });
    if (shouldApplyLatencyResult(selected.id, batchToken)) {
      setLatencyResult(selected.id, groupName, proxyName, latencyKinds.real, { delay: Number(payload.delay) || 0, error: "" });
    }
  } catch (err) {
    if (shouldApplyLatencyResult(selected.id, batchToken)) {
      setLatencyResult(selected.id, groupName, proxyName, latencyKinds.real, { delay: 0, error: localizedMessage(err.message) });
    }
    if (options.notifyErrors) showMessage(err.message, "error");
  } finally {
    setLatencyRunning(selected.id, groupName, proxyName, latencyKinds.real, false);
    updateLatencyChip(selected.id, groupName, proxyName, latencyKinds.real);
  }
}

function shouldApplyLatencyResult(instanceId, batchToken) {
  return state.activeId === instanceId && (batchToken === undefined || state.latencyBatchToken === batchToken);
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
    await runLatencyBatch(groups, kind, batchToken, selected.id);
  } finally {
    endLatencyBatch(batchToken);
  }
}

async function runLatencyBatch(groups, kind, batchToken, instanceId) {
  let index = 0;
  const workerCount = Math.min(latencyBatchConcurrency, groups.length);
  const workers = Array.from({ length: workerCount }, async () => {
    while (index < groups.length) {
      if (state.activeId !== instanceId || state.latencyBatchToken !== batchToken) return;
      const group = groups[index];
      index += 1;
      const name = currentLatencyTarget(group);
      if (name && isLatencyRunning(instanceId, group.name, name, kind)) continue;
      await runGroupLatency(group, kind, { batchToken, notifyErrors: false });
    }
  });
  await Promise.allSettled(workers);
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

el.cloneBtn.addEventListener("click", async () => {
  const selected = active();
  if (!selected || state.cloneRunning) return;
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
    clearLatencyStateForInstance(selected.id);
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
  if (!confirm(`确定删除 ${selected.name}？`)) return;
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

el.saveBasics.addEventListener("click", async () => {
  const selected = active();
  if (!selected) return;
  const editVersion = state.editVersion;
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
  // 过滤是纯本地字符串匹配，数据已在 state.proxyGroups 中，无需发请求。
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

const storedLatencyUrl = localStorage.getItem("fleetLatencyUrl");
el.latencyUrl.value = normalizeStoredLatencyUrl(storedLatencyUrl);
el.latencyTimeout.value = normalizeStoredLatencyTimeout(localStorage.getItem("fleetLatencyTimeout"), storedLatencyUrl);
el.latencyUrl.addEventListener("change", latencySettings);
el.latencyTimeout.addEventListener("change", latencySettings);
el.testAllLatency.addEventListener("click", () => testAllLatency(latencyKinds.url));
el.testAllRealLatency.addEventListener("click", () => testAllLatency(latencyKinds.real));

el.createConfig.value = defaultConfig;

const slowPollIntervalMs = 4000;
const fastPollIntervalMs = 1800;
let slowPollTimer = null;
let fastPollTimer = null;

// 自调度轮询：上一轮完成后才排下一轮，避免请求堆积；页面隐藏时暂停，恢复可见时立即补一轮。
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

refresh();
scheduleSlowPoll();
scheduleFastPoll();
