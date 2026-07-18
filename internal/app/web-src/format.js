import {
  defaultProxyBind,
  instanceModes,
  legacyDefaultLatencyTimeout,
  legacyDefaultLatencyUrl,
  defaultLatencyTimeout,
  defaultLatencyUrl,
  proxyCopyDefs,
} from "./constants.js";
import { localizedMessage } from "./i18n.js";

export function instanceMode(item) {
  return item?.mode === instanceModes.globalChain ? instanceModes.globalChain : instanceModes.rule;
}

export function modeLabel(mode) {
  return mode === instanceModes.globalChain ? "全局链式" : "规则分流";
}

export function formatBytes(value) {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let size = Number(value) || 0;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit += 1;
  }
  return `${size.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

export function formatProfileUpdate(profile) {
  if (profile.lastUpdateError) return `上次更新失败：${localizedMessage(profile.lastUpdateError)}`;
  if (profile.lastUpdatedAt) return `上次更新 ${new Date(profile.lastUpdatedAt).toLocaleString()}`;
  return "尚未更新";
}

export function formatSubscriptionInfo(profile) {
  const parts = [];
  const info = profile.subscriptionInfo || {};
  if (info.total) parts.push(`流量 ${formatBytes((info.upload || 0) + (info.download || 0))} / ${formatBytes(info.total)}`);
  if (info.expire) parts.push(`到期 ${new Date(info.expire * 1000).toLocaleDateString()}`);
  if (profile.autoUpdate && profile.updateIntervalMinutes) parts.push(`每 ${profile.updateIntervalMinutes} 分钟自动更新`);
  if (!profile.autoUpdate) parts.push("未启用自动更新");
  return parts.join(" · ") || "暂无订阅元数据";
}

export function isHttpUrl(value) {
  return /^https?:\/\//i.test(String(value || "").trim());
}

export function normalizeStoredLatencyUrl(value) {
  const url = String(value || "").trim();
  return !url || url === legacyDefaultLatencyUrl ? defaultLatencyUrl : url;
}

export function normalizeStoredLatencyTimeout(value, storedUrl) {
  const timeout = String(value || "").trim();
  const url = String(storedUrl || "").trim();
  if (!timeout || (timeout === legacyDefaultLatencyTimeout && (!url || url === legacyDefaultLatencyUrl))) {
    return String(defaultLatencyTimeout);
  }
  return timeout;
}

export function selectionSummary(item) {
  const entries = Object.entries(item.selectedProxies || {});
  if (entries.length) {
    return entries.map(([group, proxy]) => `${group} -> ${proxy}`).join("；");
  }
  return item.selectedProxy ? `${item.selectedGroup} -> ${item.selectedProxy}` : "无";
}

export function chainSummary(item) {
  if (instanceMode(item) !== instanceModes.globalChain) return "不适用";
  const chain = Array.isArray(item.chain) ? item.chain.filter(Boolean) : [];
  if (!chain.length) return "默认";
  return chain.map((name) => chainLabel(item, name)).join(" -> ");
}

export function chainLabel(item, name) {
  if (name !== "节点选择") return name;
  const selected = item.selectedProxies?.[name]
    || (item.selectedGroup === name ? item.selectedProxy : "");
  return selected ? `${name}（${selected}）` : name;
}

export function chainFromText(value) {
  return String(value || "")
    .split(/\r?\n/)
    .map((item) => item.trim())
    .filter(Boolean);
}

export function chainToText(values) {
  return Array.isArray(values) ? values.join("\n") : "";
}

export function proxyBindAddresses(item) {
  const values = String(item?.proxyBind || defaultProxyBind)
    .split(",")
    .map((value) => value.trim())
    .filter(Boolean);
  return values.length ? values : [defaultProxyBind];
}

export function proxyPort(port) {
  if (typeof port !== "number" && typeof port !== "string") return 0;
  if (typeof port === "string" && !/^\d+$/.test(port.trim())) return 0;
  const value = Number(port);
  return Number.isInteger(value) && value >= 1 && value <= 65535 ? value : 0;
}

export function proxyPortLabel(port) {
  return proxyPort(port) || "未分配";
}

export function formatProxyHost(host) {
  const value = String(host || defaultProxyBind).trim();
  return value.includes(":") && !value.startsWith("[") ? `[${value}]` : value;
}

export function proxyEndpoint(port, host = defaultProxyBind) {
  const value = proxyPort(port);
  if (!value) return "";
  return `${formatProxyHost(host)}:${value}`;
}

export function proxyEndpoints(item) {
  const port = proxyPort(item?.mixedPort);
  if (!port) return [];
  return proxyBindAddresses(item).map((host) => proxyEndpoint(port, host));
}

export function proxyEndpointText(item) {
  const endpoints = proxyEndpoints(item);
  return endpoints.length ? endpoints.join("，") : "端口未分配";
}

export function proxyEnvExports(http, socks) {
  return [
    `export HTTP_PROXY='${http}'`,
    `export HTTPS_PROXY='${http}'`,
    `export ALL_PROXY='${socks}'`,
    `export http_proxy='${http}'`,
    `export https_proxy='${http}'`,
    `export all_proxy='${socks}'`,
  ].join("\n");
}

export function proxyCopyPlaceholders() {
  return proxyCopyDefs.map((action) => ({ ...action, value: "" }));
}

export function proxyCopyActions(item) {
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

export function proxyLabelSources(profiles, instances) {
  const sources = new Set();
  for (const profile of profiles) {
    const name = String(profile.name || "").trim();
    if (name) sources.add(name);
  }
  for (const instance of instances) {
    const name = String(instance.profileName || "").trim();
    if (name) sources.add(name);
  }
  return [...sources].sort((left, right) => right.length - left.length);
}

export function splitProxyLabel(name, sources) {
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

export function formatBatchMessage(action, payload) {
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

export function isBuiltInProxy(name) {
  const text = String(name || "");
  return ["DIRECT", "REJECT", "REJECT-DROP", "PASS", "COMPATIBLE", "GLOBAL"].includes(text.toUpperCase());
}

export function currentLatencyTarget(group, proxyGroups = []) {
  const name = String(group?.now || "").trim();
  if (!name || isBuiltInProxy(name)) return "";
  if (proxyGroups.some((item) => item.name === name && item.name !== group?.name)) return "";
  return name;
}

export function isSelectableProxyGroup(group) {
  const type = String(group?.type || "select").toLowerCase();
  return type !== "relay";
}

export function alignProxyNamesToProfileOrder(names, profileGroup) {
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

export function alignProxyGroupsToProfileOrder(runtimeGroups, profileGroups) {
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

export function filterRuntimeProxyGroups(selected, groups) {
  if (instanceMode(selected) !== instanceModes.globalChain) return groups;
  return groups.filter((group) => String(group.name || "").toUpperCase() !== "GLOBAL");
}

export function formatLatencyValue(result, running) {
  if (running) return "测速中";
  if (!result) return "—";
  if (result.error) return "失败";
  if (result.delay === 0) return "不可用";
  if (typeof result.delay === "number") return `${result.delay}ms`;
  return "—";
}

export function latencyTone(result, running) {
  if (running) return "running";
  if (!result) return "idle";
  if (result.error || result.delay === 0) return "bad";
  if (result.delay >= 500) return "warn";
  return "good";
}

export function latencyLabel(kind) {
  return kind === "real" ? "真" : "测";
}

export function latencyTitle(kind) {
  return kind === "real" ? "真延迟" : "URL 延迟";
}
