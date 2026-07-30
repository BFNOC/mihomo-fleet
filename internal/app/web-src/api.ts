import { API_SECRET_STORAGE_KEY } from "./constants.ts";
import type {
  FleetCoreUpdateResult,
  FleetCoreUpdateStatus,
  FleetGeoDownloadEvent,
  FleetGeoUpdateStatus,
  FleetImportResult,
  FleetInstance,
  FleetProxyInstance,
} from "./state.ts";

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

// reloadInstance hot-reloads a running instance's config in place (feature
// #4, docs/feature-roadmap-post-1.3.md): POST /api/instances/{id}/reload
// regenerates the instance's runtime config from its current profile/fields
// and pushes it into the already-running mihomo process, without a restart.
// The backend refuses (409) when the pending change is a port/controller-
// port/proxy-bind edit -- those need a real restart to take effect -- so a
// caller catching this rejects into the same restart path the "重启" button
// already offers, rather than this ever silently no-oping the change.
export async function reloadInstance(id: string): Promise<FleetInstance> {
  return api<FleetInstance>(`/api/instances/${id}/reload`, { method: "POST" });
}

// System / components panel (feature #3, docs/feature-roadmap-post-1.3.md):
// mihomo core binary + geodata check/update. Both GETs are cheap status
// polls (no download happens server-side just from checking); both POSTs
// can legitimately run for a while (a multi-megabyte download+verify+swap),
// matching the Go side's 5-minute handler timeout -- api()'s fetch() itself
// carries no client-side timeout, so this is bounded only by the server.
export async function fetchCoreUpdateStatus(): Promise<FleetCoreUpdateStatus> {
  return api<FleetCoreUpdateStatus>("/api/system/core-update");
}

export async function applyCoreUpdate(): Promise<FleetCoreUpdateResult> {
  return api<FleetCoreUpdateResult>("/api/system/core-update", { method: "POST" });
}

export async function fetchGeoUpdateStatus(): Promise<FleetGeoUpdateStatus> {
  return api<FleetGeoUpdateStatus>("/api/system/geo-update");
}

// fetchProxyInstances lists the running instances eligible to proxy a
// core/geodata download through (docs/geo-update-enhancements.md P2) --
// backs the update panel's download-source dropdown.
export async function fetchProxyInstances(): Promise<FleetProxyInstance[]> {
  const res = await api<{ instances: FleetProxyInstance[] }>("/api/system/proxy-instances");
  return res.instances;
}

// applyGeoUpdateSSE streams POST /api/system/geo-update's download progress
// (docs/geo-update-enhancements.md P1). EventSource cannot be used here --
// it is GET-only, and this endpoint is a POST that also single-flights
// concurrent updates -- so the response's ReadableStream is read and SSE
// frames ("event: <type>\ndata: <json>\n\n") are parsed by hand instead. A
// non-2xx response (409 single-flight conflict, 401 missing/invalid token,
// 400 an unknown/stopped proxyInstanceId) never reaches the SSE body at all
// -- handleGeoUpdate/streamGeoUpdate (controller.go) only switch
// Content-Type to text/event-stream after resolving proxyInstanceId (if any)
// and acquiring the update lock -- so those cases are parsed as the same
// plain JSON error body every other api() call already expects, via
// buildApiError.
//
// Auth note: this bypasses api()'s 401 → requestApiToken() prompt/retry flow
// because the response switches to text/event-stream after the lock check --
// by the time a 401 could happen, the response format is already committed.
// In practice the status poll (fetchGeoUpdateStatus, which goes through
// api()) runs first on panel open and prompts for the token there.
//
// proxyInstanceId is P2's addition (docs/geo-update-enhancements.md section
// 3): when supplied, sent as the POST body so the backend routes the actual
// asset downloads through that managed instance's mixed-port instead of
// dialing GitHub/its CDN directly. Omitted (the historical, still-default
// behavior) means direct.
//
// SSE parser assumptions (coupled to controller.go's emitter, not the full
// SSE spec): frames use LF-only line endings (not CRLF), data is always a
// single-line JSON object (no multi-line data: fields), and every successful
// stream ends with a "complete" event.
export async function applyGeoUpdateSSE(
  onEvent: (event: FleetGeoDownloadEvent) => void,
  proxyInstanceId?: string,
): Promise<void> {
  const token = localStorage.getItem(API_SECRET_STORAGE_KEY) || "";
  const headers: Record<string, string> = { "X-Mihomo-Fleet": "1" };
  if (token) headers.Authorization = `Bearer ${token}`;
  let body: string | undefined;
  if (proxyInstanceId) {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify({ proxyInstanceId });
  }

  const res = await fetchOrThrowNetworkError("/api/system/geo-update", { method: "POST", headers, body });
  if (!res.ok) throw await buildApiError(res);
  if (!res.body) throw new Error("服务器未返回下载进度流。");

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  let sawComplete = false;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      const frames = buffer.split("\n\n");
      buffer = frames.pop() || "";
      for (const frame of frames) {
        const event = parseSSEFrame(frame);
        if (event) {
          if (event.event === "complete") sawComplete = true;
          onEvent(event);
        }
      }
    }
    buffer += decoder.decode();
    const trailing = parseSSEFrame(buffer);
    if (trailing) {
      if (trailing.event === "complete") sawComplete = true;
      onEvent(trailing);
    }
  } catch {
    throw new Error("下载进度流连接中断。");
  }
  if (!sawComplete) throw new Error("下载进度流意外中断。");
}

// parseSSEFrame reads one "event: <type>\ndata: <json>" block (already
// split on the blank-line frame separator by the caller). The `data:`
// line's JSON already carries its own `event` field (GeoDownloadEvent.Event
// on the Go side), so the `event:` line is only used as a fallback for the
// theoretical case that field came back empty -- both are read, per this
// module's SSE-parsing contract, rather than trusting just one.
function parseSSEFrame(frame: string): FleetGeoDownloadEvent | null {
  let eventLine = "";
  let data = "";
  for (const line of frame.split("\n")) {
    if (line.startsWith("event:")) eventLine = line.slice(6).trim();
    else if (line.startsWith("data:")) data += line.slice(5).trim();
  }
  if (!data) return null;
  try {
    const parsed = JSON.parse(data) as FleetGeoDownloadEvent;
    return parsed.event ? parsed : { ...parsed, event: eventLine as FleetGeoDownloadEvent["event"] };
  } catch {
    return null;
  }
}

// Fleet backup / migration (feature #7, docs/feature-roadmap-post-1.3.md #7):
// GET /api/export returns the whole fleet (every profile's config.yaml
// content, every instance minus its runtime secret) as one JSON document;
// POST /api/import validates and recreates it. The bundle itself is opaque
// to the frontend -- BackupSection.vue downloads/uploads it as a file and
// never inspects individual profile/instance fields -- so it is typed as
// `unknown` here rather than mirroring every nested field state.ts already
// declares server-side shapes for.
export async function fetchExportBundle(): Promise<unknown> {
  return api<unknown>("/api/export");
}

// bundleJson is sent verbatim as the request body (not re-serialized from a
// parsed object), so POST /api/import validates exactly the bytes the
// operator picked -- whether that file came straight from
// fetchExportBundle()'s own download or was edited by hand in between.
export async function importBundle(bundleJson: string): Promise<FleetImportResult> {
  return api<FleetImportResult>("/api/import", { method: "POST", body: bundleJson });
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
