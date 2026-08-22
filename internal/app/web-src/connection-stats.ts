// Aggregation over the live connection rows: "which rules actually fire" and
// "which exit carries the traffic", answered from the same snapshot the
// connection table already renders.
//
// Framework-free and unit-tested, like the other root-level logic modules.
// mihomo exposes no hit counters of its own -- this counts what is open right
// now, which is a sample, not a running total. Nothing here persists across a
// reload, and that is deliberate: see the note on `ConnectionStatRow.connections`.
import type { FleetConnectionRow } from "./dashboard.ts";

/** Which field the rows are grouped by. */
export type ConnectionStatDimension = "rule" | "node";

export interface ConnectionStatRow {
  /** Stable identity for `v-for` keying; unique within one result set. */
  key: string;
  /** Primary text: the rule name, or the node that carried the connection. */
  label: string;
  /** Secondary text: a rule's payload (the domain/CIDR it matched on). */
  detail: string;
  /**
   * Live connections in this group.
   *
   * A count of what is open at this instant, NOT how many times the rule has
   * ever matched. A rule that fires constantly but whose connections close
   * immediately scores lower here than one long-lived tunnel -- read this as
   * "what is using the link right now", which is the question the dashboard
   * asks everywhere else.
   */
  connections: number;
  /** Cumulative bytes over the lifetime of the connections still open. */
  upload: number;
  download: number;
  /** Current per-second rates, summed across the group. */
  up: number;
  down: number;
}

const unknownLabel = "—";

/**
 * Groups rows and sorts them by total live rate, then by cumulative bytes, then
 * by connection count.
 *
 * Rate leads because the table is read to answer "what is busy now"; two idle
 * groups then order by how much they have moved overall, and identical byte
 * counts (common right after a reload, when everything is zero) fall back to
 * the connection count so the order stays stable rather than arbitrary.
 */
export function aggregateConnections(
  rows: readonly FleetConnectionRow[],
  dimension: ConnectionStatDimension,
): ConnectionStatRow[] {
  const groups = new Map<string, ConnectionStatRow>();
  for (const row of rows) {
    const { key, label, detail } = groupOf(row, dimension);
    let group = groups.get(key);
    if (!group) {
      group = { key, label, detail, connections: 0, upload: 0, download: 0, up: 0, down: 0 };
      groups.set(key, group);
    }
    group.connections += 1;
    group.upload += Math.max(0, row.upload || 0);
    group.download += Math.max(0, row.download || 0);
    group.up += Math.max(0, row.up || 0);
    group.down += Math.max(0, row.down || 0);
  }
  return [...groups.values()].sort(compareStatRows);
}

function compareStatRows(a: ConnectionStatRow, b: ConnectionStatRow): number {
  const rate = b.up + b.down - (a.up + a.down);
  if (rate !== 0) return rate;
  const bytes = b.upload + b.download - (a.upload + a.download);
  if (bytes !== 0) return bytes;
  if (b.connections !== a.connections) return b.connections - a.connections;
  // Last resort so the order cannot flip between two identical groups on
  // consecutive ticks; the table repaints every heartbeat.
  return a.key < b.key ? -1 : a.key > b.key ? 1 : 0;
}

function groupOf(
  row: FleetConnectionRow,
  dimension: ConnectionStatDimension,
): { key: string; label: string; detail: string } {
  if (dimension === "node") {
    const node = (row.node || "").trim();
    return { key: node || unknownLabel, label: node || unknownLabel, detail: "" };
  }
  const rule = (row.rule || "").trim();
  const payload = (row.rulePayload || "").trim();
  // Keyed on rule AND payload, not rule alone: every RuleSet/DOMAIN-SUFFIX
  // entry shares the same rule name, so collapsing on it would answer
  // "RuleSet matched 400 connections" and hide which set did.
  // JSON rather than a delimiter string: a rule payload can contain very nearly
  // any character, so any separator picked by hand is a collision waiting to
  // happen -- and the first version of this line used a literal NUL, which made
  // git classify the whole module as binary and stop diffing it.
  const key = JSON.stringify([rule || unknownLabel, payload]);
  return { key, label: rule || unknownLabel, detail: payload };
}

/** Totals across every group, for the "共 N 条" summary line. */
export function totalConnectionStats(groups: readonly ConnectionStatRow[]): {
  groups: number;
  connections: number;
  up: number;
  down: number;
} {
  let connections = 0;
  let up = 0;
  let down = 0;
  for (const group of groups) {
    connections += group.connections;
    up += group.up;
    down += group.down;
  }
  return { groups: groups.length, connections, up, down };
}
