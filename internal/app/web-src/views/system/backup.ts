// Pure formatting/decision logic for BackupSection.vue (feature #7,
// docs/feature-roadmap-post-1.3.md #7: fleet backup / export-import).
// Framework-free -- no vue, no store.ts -- so it can be unit-tested with
// plain node:test, matching system-update.ts's rationale for the same split.
import type { FleetImportItemResult, FleetImportResult } from "../../state.ts";

function pad(value: number): string {
  return String(value).padStart(2, "0");
}

/**
 * Filename BackupSection.vue's export download suggests to the browser --
 * a local-time timestamp so exporting the same fleet twice in a day
 * doesn't collide (mirrors the Go side's own filename, GET /api/export's
 * Content-Disposition header in controller.go, though a JS-triggered Blob
 * download does not read that header back -- this is generated
 * independently on the frontend for exactly that reason).
 */
export function exportFilename(date: Date): string {
  const stamp =
    `${date.getFullYear()}${pad(date.getMonth() + 1)}${pad(date.getDate())}` +
    `-${pad(date.getHours())}${pad(date.getMinutes())}${pad(date.getSeconds())}`;
  return `mihomo-fleet-backup-${stamp}.json`;
}

/**
 * One profile/instance's outcome line, e.g. `HK (2)（原名“HK”重名，已重命名；端口冲突，
 * 已重新分配为 28002/29002）`. `kind` only changes whether port info is considered --
 * a profile entry never carries port fields.
 */
export function describeImportItem(kind: "profile" | "instance", item: FleetImportItemResult): string {
  const notes: string[] = [];
  if (item.renamed) notes.push(`原名“${item.originalName}”重名，已重命名`);
  if (kind === "instance" && item.portReallocated) {
    notes.push(`端口冲突，已重新分配为 ${item.mixedPort}/${item.controllerPort}`);
  }
  return notes.length ? `${item.name}（${notes.join("；")}）` : item.name;
}

/**
 * Summary lines for the result panel after a successful POST /api/import:
 * a leading count line (the roadmap explicitly wants "created/re-allocated"
 * reported, not left for the operator to infer from a raw list), then one
 * line per profile/instance via describeImportItem.
 */
export function summarizeImportResult(result: FleetImportResult): string[] {
  const lines: string[] = [`已导入 ${result.profiles.length} 个配置档、${result.instances.length} 个实例。`];
  for (const profile of result.profiles) lines.push(`配置档：${describeImportItem("profile", profile)}`);
  for (const instance of result.instances) lines.push(`实例：${describeImportItem("instance", instance)}`);
  return lines;
}
