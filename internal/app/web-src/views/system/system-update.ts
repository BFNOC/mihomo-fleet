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

const geoFileDescriptions: Record<string, string> = {
  "GeoIP.dat": "mihomo 规则引擎 GEOIP 匹配",
  "GeoSite.dat": "mihomo 规则引擎 GEOSITE 匹配",
  "Country.mmdb": "连接列表国家代码解析",
  "ASN.mmdb": "mihomo 规则引擎 IP-ASN 匹配",
};

/**
 * Usage description for one geodata file's canonical name, shown as a small
 * note under its status line (docs/geo-update-enhancements.md #1/P3):
 * GeoIP.dat/GeoSite.dat/ASN.mmdb are staged for the mihomo *instances'* own
 * rule engine and mihomo-fleet itself never reads them, so without this
 * note their purpose is not obvious from a panel that otherwise only shows
 * present/updatable status. Empty string for anything this table doesn't
 * recognize.
 */
export function geoFileDescription(name: string): string {
  return geoFileDescriptions[name] || "";
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

// --- Download progress (docs/geo-update-enhancements.md P1) --------------

const byteUnits = ["B", "KB", "MB", "GB"];

/**
 * Human-readable byte count, e.g. formatBytes(1_234_567) -> "1.2 MB". Whole
 * bytes render with no decimal (matches how a byte count is normally read);
 * every larger unit keeps one decimal place. Caps at GB -- nothing this app
 * downloads (geodata files, the mihomo core binary) approaches that size,
 * but capping avoids an ever-growing unit list for a stray huge value
 * rather than silently mis-rendering one.
 */
export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return "0 B";
  let value = n;
  let unitIndex = 0;
  while (value >= 1024 && unitIndex < byteUnits.length - 1) {
    value /= 1024;
    unitIndex += 1;
  }
  return `${value.toFixed(unitIndex === 0 ? 0 : 1)} ${byteUnits[unitIndex]}`;
}

/** Human-readable transfer speed, e.g. formatSpeed(1_048_576) -> "1.0 MB/s". */
export function formatSpeed(bytesPerSec: number): string {
  if (!Number.isFinite(bytesPerSec) || bytesPerSec <= 0) return "0 B/s";
  return `${formatBytes(bytesPerSec)}/s`;
}

/**
 * 0-100 download percentage for the progress bar's width. totalSize <= 0
 * (Content-Length was not available) returns 0 rather than dividing by
 * zero -- SystemView.vue still shows the downloaded/speed text in that
 * case, just an empty bar instead of a guessed fill.
 */
export function geoProgressPercent(downloaded: number, totalSize: number): number {
  if (totalSize <= 0) return 0;
  return Math.min(100, Math.max(0, (downloaded / totalSize) * 100));
}

/** One-line summary of a geodata download's current progress, e.g. "GeoSite.dat（2/4） 4.2 MB / 6.3 MB  1.1 MB/s". Used for the SSE progress event's accessible text; SystemView.vue's visual layout renders the same fields across a compact multi-line block instead. */
export function formatGeoProgress(p: {
  file: string;
  downloaded: number;
  totalSize: number;
  speed: number;
  index: number;
  total: number;
}): string {
  const sizePart = p.totalSize > 0 ? `${formatBytes(p.downloaded)} / ${formatBytes(p.totalSize)}` : formatBytes(p.downloaded);
  return `${geoFileLabel(p.file)}（${p.index + 1}/${p.total}） ${sizePart}  ${formatSpeed(p.speed)}`;
}
