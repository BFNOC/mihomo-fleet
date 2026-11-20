// Chinese display text for values that reach the browser in English: the Go
// backend's error strings and its status enum. Renamed from i18n.ts, which was
// a misnomer -- there is no internationalisation here and deliberately none
// planned (no vue-i18n, no locale files, no language switch; UI copy stays
// hardcoded Chinese in the templates). This is a one-way English-to-Chinese
// lookup and nothing else.
//
// localizedMessage() in particular is load-bearing, not cosmetic: drop it and
// raw Go error strings surface in the UI.
import { errorLabels, errorPatterns, statusLabels } from "./constants.ts";

// constants.ts currently exports these as bare object/array literals (no
// index signature yet, typed by whoever owns that file). The lookups here are
// keyed by arbitrary runtime strings (parsed error text / status values), so
// we view the imports through the shapes they actually have at runtime
// instead of leaking `any` into this file's call sites.
type ErrorLabelMap = Record<string, string>;
type StatusLabelMap = Record<string, string>;
type ErrorPatternEntry = [RegExp, (match: RegExpMatchArray) => string];

const errorLabelMap = errorLabels as ErrorLabelMap;
const errorPatternList = errorPatterns as ErrorPatternEntry[];
const statusLabelMap = statusLabels as StatusLabelMap;

export function localizedMessage(message: unknown): string {
  const text = String(message || "");
  const label = errorLabelMap[text];
  if (label) return label;
  // Backup validation prefixes the instance an inner error belongs to
  // (export.go validateBundle); translate the inner part like any other.
  const nested = text.match(/^instance "(.+?)": (.+)$/s);
  if (nested) return `实例 ${nested[1]}：${localizedMessage(nested[2])}`;
  // An import failure wraps the real reason; translate that part too.
  const created = text.match(/^create (instance|profile) "(.+?)": (.+)$/s);
  if (created) return `创建${created[1] === "instance" ? "实例" : "配置档"} ${created[2]} 失败：${localizedMessage(created[3])}`;
  // A shared-profile edit refused for another instance's port mapping.
  const breaks = text.match(/^config would break instance "(.+?)": (.+)$/s);
  if (breaks) return `这份配置会让实例 ${breaks[1]} 的端口映射失效，未保存：${localizedMessage(breaks[2])}`;
  for (const [pattern, render] of errorPatternList) {
    const match = text.match(pattern);
    if (match) return render(match);
  }
  return text;
}

export function statusText(status: string): string {
  return statusLabelMap[status] || status || "未知";
}

export function statusClass(status: string): string {
  return statusLabelMap[status] ? status : "unknown";
}
