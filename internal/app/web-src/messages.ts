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
