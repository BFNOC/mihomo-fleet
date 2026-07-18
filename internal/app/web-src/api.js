import { API_SECRET_STORAGE_KEY } from "./constants.js";

async function buildApiError(res) {
  let message = `${res.status} ${res.statusText}`;
  try {
    const body = await res.json();
    if (body.error) message = body.error;
  } catch {
    // keep status message
  }
  return new Error(message);
}

let apiTokenPromptShown = false;
let apiTokenPromptInFlight = null;

function requestApiToken() {
  if (apiTokenPromptShown) return Promise.resolve(null);
  if (apiTokenPromptInFlight) return apiTokenPromptInFlight;
  apiTokenPromptInFlight = Promise.resolve()
    .then(() => window.prompt("此面板需要 API 令牌才能访问，请输入 -api-secret 配置的令牌："))
    .then((entered) => {
      apiTokenPromptShown = true;
      if (!entered) return null;
      localStorage.setItem(API_SECRET_STORAGE_KEY, entered);
      return entered;
    })
    .finally(() => {
      apiTokenPromptInFlight = null;
    });
  return apiTokenPromptInFlight;
}

export async function api(path, options = {}) {
  const token = localStorage.getItem(API_SECRET_STORAGE_KEY) || "";
  const headers = { "Content-Type": "application/json", "X-Mihomo-Fleet": "1", ...(options.headers || {}) };
  if (token) headers.Authorization = `Bearer ${token}`;

  let res = await fetch(path, { ...options, headers });
  if (res.status === 401) {
    const entered = await requestApiToken();
    if (entered) {
      headers.Authorization = `Bearer ${entered}`;
      res = await fetch(path, { ...options, headers });
    }
  }
  if (!res.ok) {
    throw await buildApiError(res);
  }
  if (res.status === 204) return null;
  return res.json();
}

export async function writeClipboard(value) {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(value);
    return;
  }
  const textarea = document.createElement("textarea");
  const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  textarea.value = value;
  textarea.setAttribute("readonly", "");
  textarea.tabIndex = -1;
  textarea.setAttribute("aria-hidden", "true");
  textarea.className = "clipboard-buffer";
  document.body.append(textarea);
  textarea.select();
  try {
    if (!document.execCommand("copy")) {
      throw new Error("复制失败。");
    }
  } finally {
    textarea.remove();
    if (previousFocus && document.contains(previousFocus)) previousFocus.focus({ preventScroll: true });
  }
}
