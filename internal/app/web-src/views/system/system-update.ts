// Pure formatting/decision logic for SystemView.vue (feature #3,
// docs/feature-roadmap-post-1.3.md: mihomo core + geodata auto-update).
// Framework-free -- no vue, no store.ts -- so it can be unit-tested with
// plain node:test, matching rules-data.ts's rationale for the same split.
import type { FleetCoreUpdateStatus, FleetGeoFileStatus, FleetGeoUpdateStatus } from "../../state.ts";

/**
 * The core panel's one-line status text. Four distinct states, checked in
 * order: not installed at all, a check failure, up to date, and an update
 * available -- the last one names the target version so "更新" isn't a leap
 * of faith. checksumAvailable is deliberately NOT folded into this text on
 * its own; describeCoreChecksumNote below owns that, since "看起来有更新，但没有可校验的
 * 校验和" is a materially different message than plain "已是最新".
 */
export function describeCoreStatus(status: FleetCoreUpdateStatus): string {
  if (!status.installed) return "未检测到 mihomo，请先通过 -mihomo 参数指定路径或将其放入同目录/PATH。";
  if (status.checkError) return `检测失败：${status.checkError}`;
  if (!status.latestVersion) return "无法获取最新版本信息。";
  if (!status.updateAvailable) return `已是最新版本（${status.currentVersion || "未知"}）。`;
  return `发现新版本 ${status.latestVersion}（当前 ${status.currentVersion || "未知"}）。`;
}

/**
 * Separate from describeCoreStatus: only shown once an update is actually
 * available, since "该版本未发布校验和" is only relevant information at the
 * moment the operator might otherwise click 更新. In practice this should be
 * rare: GitHub's release API attaches a server-computed checksum
 * (asset.digest) to essentially every current asset, which
 * core_update.go's resolveChecksum treats as the primary source (see its
 * own doc comment) -- this note is reserved for the genuine edge case of an
 * asset predating that field with no sidecar/bundle fallback either.
 */
export function describeCoreChecksumNote(status: FleetCoreUpdateStatus): string {
  if (!status.installed || !status.updateAvailable || status.checksumAvailable) return "";
  return "该版本未发布可校验的 SHA-256 校验和，Mihomo Fleet 拒绝安装未经校验的文件，更新按钮已禁用。";
}

/** Whether the core 更新 button should be disabled. */
export function coreApplyDisabled(status: FleetCoreUpdateStatus, busy: boolean): boolean {
  return busy || !status.installed || !status.updateAvailable || !status.checksumAvailable;
}

const geoFileLabels: Record<string, string> = {
  "GeoIP.dat": "GeoIP 规则库",
  "GeoSite.dat": "GeoSite 规则库",
  "Country.mmdb": "国家 IP 数据库",
  "ASN.mmdb": "ASN 数据库",
};

/** Human label for one geodata file's canonical name, falling back to the raw name for anything this table doesn't recognize (forward-compatible with an upstream layout change). */
export function geoFileLabel(name: string): string {
  return geoFileLabels[name] || name;
}

/** One geodata file row's status text. */
export function describeGeoFile(file: FleetGeoFileStatus): string {
  if (!file.present) return file.checksumAvailable ? "本地缺失，可下载" : "本地缺失，且上游未发布校验和";
  if (!file.checksumAvailable) return "已安装（上游未发布校验和，无法确认是否为最新）";
  return file.updateAvailable ? "有更新" : "已是最新";
}

/** Source path display for a geodata file. */
export function geoSourcePath(file: FleetGeoFileStatus): string {
  return file.sourcePath || "";
}

/** Whether the geodata 更新 button should be disabled: nothing to do (every file either up to date or unverifiable), or a request is already in flight. */
export function geoApplyDisabled(status: FleetGeoUpdateStatus, busy: boolean): boolean {
  if (busy) return true;
  return !status.files.some((file) => file.checksumAvailable && (file.updateAvailable || !file.present));
}

/** Summary line above the geodata file list, e.g. "共 4 个文件，2 个可更新". */
export function geoSummaryText(status: FleetGeoUpdateStatus): string {
  if (status.checkError) return `检测失败：${status.checkError}`;
  if (!status.files.length) return "暂无地理数据文件信息。";
  const updatable = status.files.filter((file) => file.checksumAvailable && (file.updateAvailable || !file.present)).length;
  return updatable > 0 ? `共 ${status.files.length} 个文件，${updatable} 个可更新。` : `共 ${status.files.length} 个文件，均已是最新。`;
}

/** Result line after POST /api/system/geo-update: which files actually changed, and any per-file failures. */
export function describeGeoResult(updated: string[] | undefined, errors: string[] | undefined): string {
  const parts: string[] = [];
  if (updated && updated.length) parts.push(`已更新：${updated.map(geoFileLabel).join("、")}。`);
  if (errors && errors.length) parts.push(errors.join("；"));
  if (!parts.length) parts.push("没有文件需要更新。");
  return parts.join(" ");
}
