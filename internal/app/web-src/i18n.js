import { errorLabels, errorPatterns, statusLabels } from "./constants.js";

export function localizedMessage(message) {
  const text = String(message || "");
  if (errorLabels[text]) return errorLabels[text];
  for (const [pattern, render] of errorPatterns) {
    const match = text.match(pattern);
    if (match) return render(match);
  }
  return text;
}

export function statusText(status) {
  return statusLabels[status] || status || "未知";
}

export function statusClass(status) {
  return statusLabels[status] ? status : "unknown";
}

export function escapeHTML(input) {
  return String(input).replace(/[&<>"']/g, (ch) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#039;",
  }[ch]));
}
