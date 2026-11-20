/*
 * Instance port mappings (internal/app/port_mappings.go): extra inbound ports,
 * each pinned to one proxy or group. Framework-free so the form logic is
 * unit-testable with node --test.
 *
 * The backend stays the authority: portMappingProblem() only catches what can
 * be judged without the profile or the host (range, blanks, duplicates, the
 * instance's own two ports), so the user hears about those before a round trip.
 */
import { instanceModes } from "./constants.ts";

export interface PortMapping {
  port: number;
  proxy: string;
}

/** A form row: the port stays text so a half-typed number is not coerced. */
export interface PortMappingRow {
  port: string;
  proxy: string;
}

export function rowsFromMappings(mappings: readonly PortMapping[] | undefined): PortMappingRow[] {
  return (mappings ?? []).map((m) => ({ port: String(m.port), proxy: m.proxy }));
}

export function mappingsFromRows(rows: readonly PortMappingRow[]): PortMapping[] {
  // The target is sent as typed: mihomo matches proxy names exactly, padding
  // included, so trimming here could turn a valid name into an unknown one.
  return rows.map((row) => ({ port: Number(row.port.trim()), proxy: row.proxy }));
}

/** First problem in the rows, in Chinese, or "" when the backend may judge the rest. */
export function portMappingProblem(rows: readonly PortMappingRow[], mixedPort: number, controllerPort: number): string {
  const seen = new Map<number, number>();
  for (const [index, row] of rows.entries()) {
    const label = `第 ${index + 1} 条端口映射`;
    const text = row.port.trim();
    const port = Number(text);
    if (!/^\d+$/.test(text) || port < 1 || port > 65535) return `${label}的端口需为 1–65535 的整数。`;
    if (!row.proxy.trim()) return `${label}（端口 ${port}）未选择目标节点。`;
    const previous = seen.get(port);
    if (previous) return `端口 ${port} 在第 ${previous} 条和第 ${index + 1} 条映射里重复。`;
    if (port === mixedPort) return `${label}的端口 ${port} 与本实例混合端口相同。`;
    if (port === controllerPort) return `${label}的端口 ${port} 与本实例控制端口相同。`;
    seen.set(port, index + 1);
  }
  return "";
}

export interface TargetCandidate {
  name: string;
  kind: string;
}

/**
 * Names a mapping may target, matching checkPortMappingTargets(): the final
 * config's proxies and groups. Global-chain mode replaces the profile's groups
 * with 节点选择 and adds the local proxies, which the chain candidates already
 * describe; rule mode keeps the profile's own proxies and groups.
 */
export function portMappingTargetOptions(
  mode: string,
  candidates: readonly TargetCandidate[],
  groupNames: readonly string[],
  proxyNames: readonly string[],
): string[] {
  // Profile proxies by their raw names (proxyNames); the chain candidates trim
  // them, and a mapping must match mihomo's exact name.
  const names = mode === instanceModes.globalChain
    ? [...candidates.filter((c) => c.kind !== "profile").map((c) => c.name), ...proxyNames]
    : [...proxyNames, ...groupNames];
  return [...new Set(names)];
}

export function portMappingSummary(mappings: readonly PortMapping[] | undefined): string {
  if (!mappings?.length) return "无";
  return mappings.map((m) => `${m.port} → ${m.proxy}`).join("，");
}
