import { API_SECRET_STORAGE_KEY } from "./constants.ts";

// Shape of the JSON error body the Go controller always sends alongside a
// non-2xx response (internal/app/controller.go: writeJSONStatus(w, status,
// map[string]string{"error": err.Error()})). The body is otherwise opaque
// JSON, so it is read as `unknown` and narrowed before use.
//
// The `typeof === "string"` narrowing below is load-bearing on that Go-side
// contract: every one of controller.go's error paths funnels through
// writeError, and map[string]string can only ever marshal `error` as a JSON
// string. If the backend ever switches to a structured error value, this
// narrowing stops matching and the UI silently degrades to the bare
// "500 Internal Server Error" status line instead of showing the real reason.
// Widen this alongside any such backend change.
interface ApiErrorBody {
  error?: string;
}

async function buildApiError(res: Response): Promise<Error> {
  let message = `${res.status} ${res.statusText}`;
  try {
    const body: unknown = await res.json();
    const detail = (body as ApiErrorBody | null | undefined)?.error;
    if (typeof detail === "string" && detail) message = detail;
  } catch {
    // keep status message
  }
  return new Error(message);
}

// fetch() itself only ever throws for a network-layer failure -- DNS, refused
// connection, the controller process being dead, a CORS preflight failure --
// never for a non-2xx HTTP status; that path returns a normal Response and is
// handled by buildApiError() above instead. The browser's own message for
// that thrown TypeError ("Failed to fetch", "NetworkError when attempting to
// fetch resource", ...) is English and is not one of constants.ts's
// errorLabels/errorPatterns entries -- those only match strings the Go
// backend itself sends -- so it would otherwise reach showMessage() and the
// banner completely untranslated. Converting it here, at the one place every
// call in this module funnels through, keeps that translation out of every
// caller's catch block.
async function fetchOrThrowNetworkError(path: string, init: RequestInit): Promise<Response> {
  try {
    return await fetch(path, init);
  } catch {
    throw new Error("无法连接本地控制器，请确认 Mihomo Fleet 仍在运行。");
  }
}

let apiTokenPromptShown = false;
let apiTokenPromptInFlight: Promise<string | null> | null = null;
// Whether the *last* resolved prompt ended with the user actually typing
// something (as opposed to cancelling). Read by api() below: a 401 on the
// immediate retry means that supplied token was itself wrong, and that
// specific case is what api() uses to un-latch apiTokenPromptShown -- see the
// comment at that call site for why a cancelled prompt does not.
let lastPromptedTokenWasSupplied = false;

function requestApiToken(): Promise<string | null> {
  if (apiTokenPromptShown) return Promise.resolve(null);
  if (apiTokenPromptInFlight) return apiTokenPromptInFlight;
  apiTokenPromptInFlight = Promise.resolve()
    .then(() => window.prompt("此面板需要 API 令牌才能访问，请输入 -api-secret 配置的令牌："))
    .then((entered) => {
      apiTokenPromptShown = true;
      lastPromptedTokenWasSupplied = Boolean(entered);
      if (!entered) return null;
      localStorage.setItem(API_SECRET_STORAGE_KEY, entered);
      return entered;
    })
    .finally(() => {
      apiTokenPromptInFlight = null;
    });
  return apiTokenPromptInFlight;
}

// `T` lets each call site declare the shape of the parsed JSON response
// (e.g. `api<InstanceView[]>("/api/instances")`). There is no way to verify
// that shape at runtime -- `res.json()` is inherently untyped -- so the
// return is cast from `unknown` to `T` at the single point where the body is
// parsed, rather than threading `any` through the rest of the function.
export async function api<T = unknown>(path: string, options: RequestInit = {}): Promise<T> {
  const token = localStorage.getItem(API_SECRET_STORAGE_KEY) || "";
  // `RequestInit["headers"]` also accepts a `Headers` instance or a
  // `[string, string][]` pair list, but every call site in this codebase
  // only ever passes a plain object (verified: no caller sets `headers`),
  // and the original untyped code already assumed a plain object when
  // spreading it here. The cast documents that assumption instead of
  // silently narrowing it away.
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    "X-Mihomo-Fleet": "1",
    ...((options.headers as Record<string, string> | undefined) || {}),
  };
  if (token) headers.Authorization = `Bearer ${token}`;

  let res = await fetchOrThrowNetworkError(path, { ...options, headers });
  if (res.status === 401) {
    const entered = await requestApiToken();
    if (entered) {
      headers.Authorization = `Bearer ${entered}`;
      res = await fetchOrThrowNetworkError(path, { ...options, headers });
      // The token the user just typed still came back 401 -- it was wrong, not
      // just stale. apiTokenPromptShown otherwise latches forever after the
      // first prompt (see its declaration), so without this every later poll
      // would keep repeating "API 令牌缺失或无效" with no way to fix the typo.
      // A *cancelled* prompt (lastPromptedTokenWasSupplied === false) does NOT
      // un-latch here: the user already declined once, so this deliberately
      // falls through to the same silent-401 behaviour as before rather than
      // re-prompting on every ~4s poll.
      if (res.status === 401 && lastPromptedTokenWasSupplied) {
        apiTokenPromptShown = false;
      }
    }
  }
  if (!res.ok) {
    throw await buildApiError(res);
  }
  if (res.status === 204) return null as T;
  const body: unknown = await res.json();
  return body as T;
}

export async function writeClipboard(value: string): Promise<void> {
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
