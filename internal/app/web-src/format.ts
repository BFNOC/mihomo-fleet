import {
  defaultProxyBind,
  instanceModes,
  legacyDefaultLatencyTimeout,
  legacyDefaultLatencyUrl,
  defaultLatencyTimeout,
  defaultLatencyUrl,
  proxyCopyDefs,
} from "./constants.ts";
import type { InstanceMode, LatencyKind, ProxyCopyActionDef, ProxyCopyActionId } from "./constants.ts";
import { localizedMessage } from "./messages.ts";
import { formatRate } from "./traffic.ts";
import type { FleetInstance, FleetProfile, FleetProxyGroup, FleetSubscriptionInfo, LatencyResult } from "./state.ts";

export function instanceMode(item: Pick<FleetInstance, "mode"> | null | undefined): InstanceMode {
  return item?.mode === instanceModes.globalChain ? instanceModes.globalChain : instanceModes.rule;
}

export function modeLabel(mode: InstanceMode): string {
  return mode === instanceModes.globalChain ? "全局链式" : "规则分流";
}

// `mihomo -v` prints a whole banner ("Mihomo Meta v1.19.29 darwin arm64 with
// go1.26.5 <build date> Use tags: with_gvisor"). The controller keeps that raw
// -- it is what a user would paste into a bug report -- but every place the UI
// shows it has a line to spare, and only the version number identifies the
// build to a human. The go version is skipped explicitly: it also matches the
// semver shape and would otherwise win on a banner without a leading "v".
export function shortMihomoVersion(raw: string | null | undefined): string {
  const text = String(raw || "").trim();
  if (!text) return "";
  const match = text.replace(/\bgo\d+(\.\d+)*/gi, " ").match(/v?(\d+\.\d+(?:\.\d+)?(?:[-+][0-9a-z.]+)?)/i);
  // The pattern's only capturing group is mandatory (not nested inside an
  // optional group), so a successful match always populates match[1].
  if (match) return match[1]!;
  // Not a shape we know: show something bounded rather than the whole banner.
  return text.length > 32 ? `${text.slice(0, 32)}…` : text;
}

export function formatBytes(value: number): string {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let size = Number(value) || 0;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit += 1;
  }
  // `unit` only ever advances while staying below units.length, so this
  // index is always in range even though noUncheckedIndexedAccess can't
  // prove the loop invariant on its own.
  return `${size.toFixed(unit === 0 ? 0 : 1)} ${units[unit]!}`;
}

export interface DocumentTitleInput {
  /** Fleet-wide upload rate, bytes per second. */
  up: number;
  /** Fleet-wide download rate, bytes per second. */
  down: number;
  /** How many instances are running right now. */
  running: number;
  /** store.system's appVersion. Empty until GET /api/system lands. */
  appVersion: string;
  /** document.visibilityState === "hidden". */
  hidden: boolean;
}

/**
 * The browser tab title. index.html ships a bare "MF" as the pre-boot
 * fallback; everything below only runs once the app is live.
 *
 * Rates show whenever anything is running, including at 0 B/s. Collapsing to
 * the plain name on an idle sample would flip the title's format every time
 * traffic paused for a single tick.
 *
 * The hidden branch exists because services/polling.ts stops both loops while
 * the tab is backgrounded, so the rates would freeze at whatever the last
 * sample happened to read. The instance count freezes with them -- the slow
 * poll stops on the same condition -- but it degrades to last-known rather
 * than to actively wrong: a count rarely changes between hide and return,
 * where a rate almost always has.
 */
export function documentTitle(input: DocumentTitleInput): string {
  const base = input.appVersion ? `MF v${input.appVersion}` : "MF";
  if (input.running <= 0) return base;
  if (input.hidden) return `${input.running} 个实例运行中 · ${base}`;
  const up = formatRate(input.up);
  const down = formatRate(input.down);
  return `↑${up.value} ${up.unit} ↓${down.value} ${down.unit} · ${base}`;
}

export function formatProfileUpdate(profile: Pick<FleetProfile, "lastUpdateError" | "lastUpdatedAt">): string {
  if (profile.lastUpdateError) return `上次更新失败：${localizedMessage(profile.lastUpdateError)}`;
  if (profile.lastUpdatedAt) return `上次更新 ${new Date(profile.lastUpdatedAt).toLocaleString()}`;
  return "尚未更新";
}

export function formatSubscriptionInfo(
  profile: Pick<FleetProfile, "subscriptionInfo" | "autoUpdate" | "updateIntervalMinutes">,
): string {
  const parts: string[] = [];
  const info: Partial<FleetSubscriptionInfo> = profile.subscriptionInfo || {};
  if (info.total) parts.push(`流量 ${formatBytes((info.upload || 0) + (info.download || 0))} / ${formatBytes(info.total)}`);
  if (info.expire) parts.push(`到期 ${new Date(info.expire * 1000).toLocaleDateString()}`);
  if (profile.autoUpdate && profile.updateIntervalMinutes) parts.push(`每 ${profile.updateIntervalMinutes} 分钟自动更新`);
  if (!profile.autoUpdate) parts.push("未启用自动更新");
  return parts.join(" · ") || "暂无订阅元数据";
}

/**
 * Crash-watchdog evidence: how many times auto-restart has relaunched this
 * instance, and (if any) what its most recent unexpected exit looked like.
 * Mirrors internal/app/manager.go's InstanceView.RestartCount/
 * LastExitReason/LastExitAt. Returns "" when there is nothing to report.
 * Gated on lastExitReason (a plain string, which collapses under Go's
 * `omitempty`) rather than lastExitAt (a time.Time, which does not -- see
 * FleetInstance's doc comment in state.ts) so a never-crashed instance never
 * renders a stray zero-value date.
 */
export function restartEvidenceText(
  item: Pick<FleetInstance, "restartCount" | "lastExitReason" | "lastExitAt">,
): string {
  if (!item.restartCount && !item.lastExitReason) return "";
  const parts: string[] = [];
  if (item.restartCount) parts.push(`已自动重启 ${item.restartCount} 次`);
  if (item.lastExitReason) {
    const when = item.lastExitAt ? new Date(item.lastExitAt).toLocaleString() : "";
    parts.push(`最近异常退出：${localizedMessage(item.lastExitReason)}${when ? `（${when}）` : ""}`);
  }
  return parts.join(" · ");
}

export function isHttpUrl(value: string | null | undefined): boolean {
  return /^https?:\/\//i.test(String(value || "").trim());
}

export function normalizeStoredLatencyUrl(value: string | null | undefined): string {
  const url = String(value || "").trim();
  return !url || url === legacyDefaultLatencyUrl ? defaultLatencyUrl : url;
}

export function normalizeStoredLatencyTimeout(
  value: string | null | undefined,
  storedUrl: string | null | undefined,
): string {
  const timeout = String(value || "").trim();
  const url = String(storedUrl || "").trim();
  if (!timeout || (timeout === legacyDefaultLatencyTimeout && (!url || url === legacyDefaultLatencyUrl))) {
    return String(defaultLatencyTimeout);
  }
  return timeout;
}

export function selectionSummary(
  item: Pick<FleetInstance, "selectedProxies" | "selectedProxy" | "selectedGroup">,
): string {
  const entries = Object.entries(item.selectedProxies || {});
  if (entries.length) {
    return entries.map(([group, proxy]) => `${group} -> ${proxy}`).join("；");
  }
  return item.selectedProxy ? `${item.selectedGroup} -> ${item.selectedProxy}` : "无";
}

export function chainSummary(
  item: Pick<FleetInstance, "mode" | "chain" | "selectedProxies" | "selectedGroup" | "selectedProxy">,
): string {
  if (instanceMode(item) !== instanceModes.globalChain) return "不适用";
  const chain = Array.isArray(item.chain) ? item.chain.filter(Boolean) : [];
  if (!chain.length) return "默认";
  return chain.map((name) => chainLabel(item, name)).join(" -> ");
}

function chainLabel(
  item: Pick<FleetInstance, "selectedProxies" | "selectedGroup" | "selectedProxy">,
  name: string,
): string {
  if (name !== "节点选择") return name;
  const selected = item.selectedProxies?.[name]
    || (item.selectedGroup === name ? item.selectedProxy : "");
  return selected ? `${name}（${selected}）` : name;
}

export function chainFromText(value: string): string[] {
  return String(value || "")
    .split(/\r?\n/)
    .map((item) => item.trim())
    .filter(Boolean);
}

export function chainToText(values: string[] | null | undefined): string {
  return Array.isArray(values) ? values.join("\n") : "";
}

function proxyBindAddresses(
  item: Partial<Pick<FleetInstance, "proxyBind">> | null | undefined,
): string[] {
  const values = String(item?.proxyBind || defaultProxyBind)
    .split(",")
    .map((value) => value.trim())
    .filter(Boolean);
  return values.length ? values : [defaultProxyBind];
}

// Accepts genuinely arbitrary input (JSON payload fields, form values):
// the typeof guards below are the actual validation, not just narrowing.
export function proxyPort(port: unknown): number {
  if (typeof port !== "number" && typeof port !== "string") return 0;
  if (typeof port === "string" && !/^\d+$/.test(port.trim())) return 0;
  const value = Number(port);
  return Number.isInteger(value) && value >= 1 && value <= 65535 ? value : 0;
}

export function proxyPortLabel(port: unknown): number | string {
  return proxyPort(port) || "未分配";
}

function formatProxyHost(host: string): string {
  const value = String(host || defaultProxyBind).trim();
  return value.includes(":") && !value.startsWith("[") ? `[${value}]` : value;
}

function proxyEndpoint(port: unknown, host: string = defaultProxyBind): string {
  const value = proxyPort(port);
  if (!value) return "";
  return `${formatProxyHost(host)}:${value}`;
}

function proxyEndpoints(
  item: Partial<Pick<FleetInstance, "mixedPort" | "proxyBind">> | null | undefined,
): string[] {
  const port = proxyPort(item?.mixedPort);
  if (!port) return [];
  return proxyBindAddresses(item).map((host) => proxyEndpoint(port, host));
}

export function proxyEndpointText(
  item: Partial<Pick<FleetInstance, "mixedPort" | "proxyBind">> | null | undefined,
): string {
  const endpoints = proxyEndpoints(item);
  return endpoints.length ? endpoints.join("，") : "端口未分配";
}

function proxyEnvExports(http: string, socks: string): string {
  return [
    `export HTTP_PROXY='${http}'`,
    `export HTTPS_PROXY='${http}'`,
    `export ALL_PROXY='${socks}'`,
    `export http_proxy='${http}'`,
    `export https_proxy='${http}'`,
    `export all_proxy='${socks}'`,
  ].join("\n");
}

/** A rendered proxy-copy button: proxyCopyDefs's static def plus the runtime value/message to copy. */
interface ProxyCopyAction extends ProxyCopyActionDef {
  value: string;
  message?: string;
}

export function proxyCopyPlaceholders(): ProxyCopyAction[] {
  return proxyCopyDefs.map((action) => ({ ...action, value: "" }));
}

/** One row of copy buttons: the endpoint's host (for the row label) plus its actions. */
export interface ProxyCopyGroup {
  host: string;
  actions: ProxyCopyAction[];
}

function proxyCopyActionsFor(name: string, endpoint: string, suffix: string): ProxyCopyAction[] {
  const http = `http://${endpoint}`;
  const socks = `socks5://${endpoint}`;
  const values: Record<ProxyCopyActionId, string> = {
    addr: endpoint,
    http,
    socks,
    env: proxyEnvExports(http, socks),
  };
  const messages: Record<ProxyCopyActionId, string> = {
    addr: `已复制 ${name} 地址${suffix}。`,
    http: `已复制 ${name} HTTP${suffix}。`,
    socks: `已复制 ${name} SOCKS${suffix}。`,
    env: `已复制 ${name} 环境变量${suffix}。`,
  };
  return proxyCopyDefs.map((action) => ({
    ...action,
    value: values[action.id],
    message: messages[action.id],
  }));
}

// One group per bind address, so each button copies exactly one endpoint. The
// old single row joined every endpoint with "\n", which pasted as one glued
// string into any single-line field and had to be split by hand.
export function proxyCopyActionGroups(
  item: Pick<FleetInstance, "name"> & Partial<Pick<FleetInstance, "mixedPort" | "proxyBind">>,
): ProxyCopyGroup[] {
  const hosts = proxyBindAddresses(item);
  const endpoints = proxyEndpoints(item);
  if (!endpoints.length) return [{ host: "", actions: proxyCopyPlaceholders() }];
  const labelled = endpoints.length > 1;
  return endpoints.map((endpoint, index) => {
    const host = hosts[index] ?? "";
    return {
      host: labelled ? host : "",
      actions: proxyCopyActionsFor(item.name, endpoint, labelled ? `（${host}）` : ""),
    };
  });
}

export function proxyLabelSources(
  profiles: Pick<FleetProfile, "name">[],
  instances: Pick<FleetInstance, "profileName">[],
): string[] {
  const sources = new Set<string>();
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

/** Result of peeling a known profile/instance name prefix off a raw proxy name. */
interface ProxyLabelSplit {
  source: string;
  name: string;
}

export function splitProxyLabel(name: string, sources: string[]): ProxyLabelSplit {
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

/** Mirrors internal/app/manager.go's InstanceBatchError. */
interface BatchActionError {
  id?: string;
  name?: string;
  error: string;
}

/**
 * Mirrors the JSON body handleInstancesBatch (internal/app/controller.go)
 * sends: InstanceBatchResult's fields plus the refreshed `instances` list.
 * `instances` is unused by formatBatchMessage itself but kept here so other
 * modules reading the same batch response reuse this type instead of
 * redeclaring it.
 */
export interface BatchActionPayload {
  total: number;
  success: number;
  failed: number;
  errors?: BatchActionError[];
  instances?: FleetInstance[];
}

export function formatBatchMessage(action: string, payload: BatchActionPayload): string {
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

function isBuiltInProxy(name: string): boolean {
  const text = String(name || "");
  return ["DIRECT", "REJECT", "REJECT-DROP", "PASS", "COMPATIBLE", "GLOBAL"].includes(text.toUpperCase());
}

export function currentLatencyTarget(
  group: Pick<FleetProxyGroup, "name" | "now"> | null | undefined,
  proxyGroups: Pick<FleetProxyGroup, "name">[] = [],
): string {
  const name = String(group?.now || "").trim();
  if (!name || isBuiltInProxy(name)) return "";
  if (proxyGroups.some((item) => item.name === name && item.name !== group?.name)) return "";
  return name;
}

export function isSelectableProxyGroup(group: Pick<FleetProxyGroup, "type"> | null | undefined): boolean {
  const type = String(group?.type || "select").toLowerCase();
  return type !== "relay";
}

function alignProxyNamesToProfileOrder(
  names: string[],
  profileGroup: Pick<FleetProxyGroup, "all"> | null | undefined,
): string[] {
  if (!profileGroup || !Array.isArray(profileGroup.all) || !profileGroup.all.length) return names;
  const order = new Map<string, number>();
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

export function alignProxyGroupsToProfileOrder(
  runtimeGroups: FleetProxyGroup[],
  profileGroups: FleetProxyGroup[] | null | undefined,
): FleetProxyGroup[] {
  if (!Array.isArray(profileGroups) || !profileGroups.length) return runtimeGroups;
  const profileByName = new Map(
    profileGroups.map((group, index): [string, FleetProxyGroup & { index: number }] => [group.name, { ...group, index }]),
  );
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

export function filterRuntimeProxyGroups(
  selected: Pick<FleetInstance, "mode"> | null | undefined,
  groups: FleetProxyGroup[],
): FleetProxyGroup[] {
  if (instanceMode(selected) !== instanceModes.globalChain) return groups;
  return groups.filter((group) => String(group.name || "").toUpperCase() !== "GLOBAL");
}

export function formatLatencyValue(result: LatencyResult | null | undefined, running: boolean): string {
  if (running) return "测速中";
  if (!result) return "—";
  if (result.error) return "失败";
  if (result.delay === 0) return "不可用";
  if (typeof result.delay === "number") return `${result.delay}ms`;
  return "—";
}

export function latencyTone(
  result: LatencyResult | null | undefined,
  running: boolean,
): "running" | "idle" | "bad" | "warn" | "good" {
  if (running) return "running";
  if (!result) return "idle";
  if (result.error || result.delay === 0) return "bad";
  if (result.delay >= 500) return "warn";
  return "good";
}

export function latencyLabel(kind: LatencyKind): string {
  return kind === "real" ? "真" : "测";
}

export function latencyTitle(kind: LatencyKind): string {
  return kind === "real" ? "真延迟" : "URL 延迟";
}
