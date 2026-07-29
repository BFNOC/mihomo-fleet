/*
 * Client-side model for the instance `proxyBind` field: a comma-joined list of
 * listen addresses.
 *
 * The backend (internal/app/proxy_bind.go) is the authority -- it normalizes,
 * dedupes and coalesces the list on save, and rejects what it cannot parse.
 * Everything here exists so the picker can tell the user *before* a round trip,
 * and so a checkbox can decide whether an interface address is already in the
 * list. The rules below therefore mirror normalizeProxyBindAddress() rather than
 * inventing their own: same aliases (`all`, `*`, `localhost`), same "ports belong
 * in the mixed-port field" refusal, same bracket check.
 *
 * DELIBERATE GAP: bindKey() does not compress IPv6 zero runs, so `::1` and
 * `0:0:0:0:0:0:0:1` compare as different addresses here while Go's
 * canonicalProxyBindHost() treats them as one. The only consequence is a
 * checkbox that reads unchecked for an equivalent hand-typed spelling; the save
 * still normalizes correctly. Reimplementing net.ParseIP's canonical form in
 * TypeScript to close that gap would cost more than the gap does.
 */

/** One selectable host address, as returned by GET /api/system/bind-addresses. */
export interface BindAddressOption {
  address: string;
  kind: string;
  interface?: string;
}

export const wildcardBindAddress = "0.0.0.0";

const bindKindLabels: Record<string, string> = {
  wildcard: "所有网卡",
  loopback: "本机回环",
  private: "局域网",
  public: "公网",
  linkLocal: "链路本地",
};

export function bindKindLabel(kind: string): string {
  return bindKindLabels[kind] || "其他";
}

/** Splits the stored comma-joined field into individual addresses. */
export function splitBindList(value: string | null | undefined): string[] {
  return String(value || "")
    .split(",")
    .map((entry) => entry.trim())
    .filter(Boolean);
}

/** Rejoins addresses into the wire format the backend expects. */
export function joinBindList(values: readonly string[]): string {
  return values.join(",");
}

/**
 * Comparison key for "is this address already selected". Mirrors
 * canonicalProxyBindHost()'s alias folding; see the IPv6 gap noted above.
 */
export function bindKey(value: string): string {
  let host = String(value || "").trim().toLowerCase();
  host = host.replace(/\.$/, "");
  if (host === "all" || host === "*") return wildcardBindAddress;
  if (host === "localhost") return "127.0.0.1";
  if (host.startsWith("[") && host.endsWith("]")) host = host.slice(1, -1);
  return host;
}

export function bindListIncludes(values: readonly string[], address: string): boolean {
  const key = bindKey(address);
  return values.some((entry) => bindKey(entry) === key);
}

/**
 * Adds or removes `address`, preserving the order of everything else. Removal
 * matches on bindKey so unchecking works on a hand-typed alias (`localhost`)
 * for a listed address (`127.0.0.1`).
 */
export function toggleBindAddress(values: readonly string[], address: string): string[] {
  if (bindListIncludes(values, address)) {
    const key = bindKey(address);
    return values.filter((entry) => bindKey(entry) !== key);
  }
  return [...values, address.trim()];
}

function isIPv4(value: string): boolean {
  const parts = value.split(".");
  if (parts.length !== 4) return false;
  return parts.every((part) => {
    if (!/^\d{1,3}$/.test(part)) return false;
    // net.ParseIP rejects leading zeros (Go 1.17+), so "01.2.3.4" is not an IP.
    if (part.length > 1 && part.startsWith("0")) return false;
    return Number(part) <= 255;
  });
}

function isIPv6(value: string): boolean {
  if (!value.includes(":")) return false;
  const halves = value.split("::");
  if (halves.length > 2) return false;
  const compressed = halves.length === 2;
  const head = halves[0] ? halves[0].split(":") : [];
  const tail = compressed && halves[1] ? halves[1].split(":") : [];
  const groups = [...head, ...tail];
  if (!groups.length) return compressed; // "::" alone is the unspecified address
  // A trailing dotted quad (::ffff:1.2.3.4) fills two groups.
  const last = groups[groups.length - 1] as string;
  const dotted = last.includes(".");
  if (dotted && !isIPv4(last)) return false;
  const hexGroups = dotted ? groups.slice(0, -1) : groups;
  if (hexGroups.some((group) => !/^[0-9a-f]{1,4}$/.test(group))) return false;
  const count = hexGroups.length + (dotted ? 2 : 0);
  return compressed ? count <= 8 : count === 8;
}

function isIPAddress(value: string): boolean {
  const host = value.includes("%") ? value.slice(0, value.lastIndexOf("%")) : value;
  if (!host) return false;
  return host.includes(":") ? isIPv6(host) : isIPv4(host);
}

/**
 * Returns "" when the backend would accept this single address, otherwise the
 * Chinese reason. Mirrors normalizeProxyBindAddress(); the messages match the
 * localized backend strings in constants.ts so the user reads the same sentence
 * whether validation happened here or on save.
 */
export function validateBindAddress(value: string): string {
  const address = String(value || "").trim();
  if (!address) return "请输入地址。";
  const lower = address.toLowerCase();
  if (lower === "all" || address === "*" || lower === "localhost") return "";
  if (address.startsWith("[")) {
    if (!address.endsWith("]")) {
      return `代理绑定地址 ${address} 的 IPv6 方括号不完整。`;
    }
    const inner = address.slice(1, -1);
    if (inner.includes("]") || !isIPAddress(inner)) {
      return `代理绑定地址 ${address} 无效，请填写 IP、localhost、all 或 *。`;
    }
    return "";
  }
  if (/[/\s]/.test(address)) {
    return `代理绑定地址 ${address} 无效。`;
  }
  // A single colon on something that is not itself an IPv6 literal is the
  // host:port mistake the backend calls out by name.
  if (!isIPAddress(address) && address.split(":").length === 2) {
    const [host, port] = address.split(":");
    if (host && port) {
      return `代理绑定地址 ${address} 不要写端口，请使用混合端口字段。`;
    }
  }
  if (!isIPAddress(address)) {
    return `代理绑定地址 ${address} 无效，请填写 IP、localhost、all 或 *。`;
  }
  return "";
}

/**
 * One interface's addresses, arranged for the picker dialog: `primary` renders
 * as visible checkbox rows, `moreV6` collapses behind a per-group "还有 N 个
 * IPv6" toggle, `linkLocal` hides behind the dialog-wide 链路本地 toggle.
 */
export interface BindAddressGroup {
  iface: string;
  kind: string;
  primary: BindAddressOption[];
  moreV6: BindAddressOption[];
  linkLocal: BindAddressOption[];
}

/**
 * Groups the flat /api/system/bind-addresses list by interface for the picker
 * dialog. Group order is first appearance in the backend list, which is already
 * kind-sorted (loopback, private, public, linkLocal), so loopback interfaces
 * lead and link-local-only stragglers trail.
 *
 * IPv4 shows directly; IPv6 collapses into `moreV6` unless the interface has no
 * IPv4 at all, in which case its first IPv6 is promoted so the group is not an
 * empty header over a "还有 N 个" toggle. A currently selected address is always
 * promoted to `primary` regardless of family or kind -- a checked box must never
 * sit behind a collapsed toggle where unchecking it needs a hunt.
 *
 * The wildcard entry is excluded: it has no interface and the dialog renders it
 * as its own fixed row.
 */
export function groupBindAddresses(
  options: readonly BindAddressOption[],
  selected: readonly string[],
): BindAddressGroup[] {
  const byIface = new Map<string, BindAddressGroup>();
  for (const option of options) {
    if (option.kind === "wildcard") continue;
    const iface = option.interface || "";
    let group = byIface.get(iface);
    if (!group) {
      group = { iface, kind: option.kind, primary: [], moreV6: [], linkLocal: [] };
      byIface.set(iface, group);
    }
    if (bindListIncludes(selected, option.address)) group.primary.push(option);
    else if (option.kind === "linkLocal") group.linkLocal.push(option);
    else if (option.address.includes(":")) group.moreV6.push(option);
    else group.primary.push(option);
  }
  for (const group of byIface.values()) {
    if (!group.primary.length && group.moreV6.length) {
      group.primary.push(group.moreV6.shift() as BindAddressOption);
    }
  }
  return [...byIface.values()];
}

/**
 * What the instance will actually listen on, for the preview line under the
 * field. Mirrors coalesceProxyBindAddresses(): once 0.0.0.0 is in the list every
 * other IPv4 address is redundant, and showing them anyway makes the user think
 * they narrowed the exposure when they did not.
 */
export function bindListPreview(values: readonly string[]): string[] {
  const resolved = values.map((entry) => {
    const key = bindKey(entry);
    return key === wildcardBindAddress ? wildcardBindAddress : entry.trim();
  });
  const seen = new Set<string>();
  const unique = resolved.filter((entry) => {
    const key = bindKey(entry);
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
  if (!unique.some((entry) => bindKey(entry) === wildcardBindAddress)) return unique;
  return unique.filter((entry) => {
    const key = bindKey(entry);
    return key === wildcardBindAddress || key.includes(":");
  });
}
