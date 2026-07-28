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

export function escapeHTML(input: unknown): string {
  const escapes: Record<string, string> = {
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#039;",
  };
  // The regex only ever matches characters that are keys of `escapes`, so the
  // lookup is always defined; the assertion documents that invariant instead
  // of widening the return type with a fallback that can never trigger.
  return String(input).replace(/[&<>"']/g, (ch) => escapes[ch]!);
}
