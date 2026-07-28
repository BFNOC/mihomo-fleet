import { formatBytes, shortMihomoVersion } from "./format.js";
import { escapeHTML, statusClass, statusText } from "./i18n.js";
import {
  aggregateSeries,
  connectionSnapshot,
  createSeries,
  deriveRate,
  diffConnections,
  filterConnections,
  countryFlag,
  formatDuration,
  formatRate,
  localAddressLabel,
  pushSample,
  seriesField,
  seriesLatest,
  seriesPeak,
  seriesSpan,
  sortConnections,
  sparklineGeometry,
  trafficWindowSeconds,
} from "./traffic.js";

// The fast poll runs at 1800ms, so a 60s window is ~33 samples.
const sampleCapacity = Math.ceil((trafficWindowSeconds * 1000) / 1800) + 2;
const sparkWidth = 320;
const sparkHeight = 56;
const trendHeight = 112;
const rowSparkWidth = 96;
const rowSparkHeight = 20;
// The dashboard is sized to the viewport and never scrolls, so both tables show
// exactly the number of rows their box can hold -- measured after each render
// (fitTables) rather than guessed. These are the starting guesses for the very
// first paint and the floor when a measurement is impossible (panel hidden,
// zero-height box). Connection rows are sorted busiest-first, so a tight budget
// still keeps the interesting ones.
const rowBudgets = { connections: 6, instances: 4 };
// A row that renders taller than this is a layout bug, not data -- clamping the
// divisor keeps a bad measurement from collapsing the table to a single row.
const maxRowHeight = 120;

// One sampler per instance: the rolling rate series plus the previous
// cumulative counter reading the next rate is derived from.
const samplers = new Map();
let connectionQuery = "";

function emptySampler() {
  return {
    series: createSeries(sampleCapacity),
    previous: null,
    connections: 0,
    reachable: false,
    connectionRows: [],
    connectionTotals: new Map(),
    sampledAt: 0,
  };
}

function sampler(instanceId) {
  let entry = samplers.get(instanceId);
  if (!entry) {
    entry = emptySampler();
    samplers.set(instanceId, entry);
  }
  return entry;
}

// A stopped or unreachable instance keeps no connection state: its rows are
// gone from the table and its counters must not seed the next rate it reports.
function resetConnectionState(entry) {
  entry.previous = null;
  entry.connections = 0;
  entry.reachable = false;
  entry.connectionRows = [];
  entry.connectionTotals = new Map();
  entry.sampledAt = 0;
}

export function setConnectionQuery(value) {
  connectionQuery = String(value ?? "");
}

export function forgetInstanceSamples(instanceId) {
  samplers.delete(instanceId);
}

// Deleted instances would otherwise keep contributing to the fleet total
// forever, since nothing else ever clears their sampler.
export function pruneSamplers(instances) {
  const live = new Set((instances || []).map((item) => item.id));
  for (const id of [...samplers.keys()]) {
    if (!live.has(id)) samplers.delete(id);
  }
}

export function instanceSeries(instanceId) {
  return samplers.get(instanceId)?.series || null;
}

export function instanceConnections(instanceId) {
  const entry = samplers.get(instanceId);
  return entry?.reachable ? entry.connections : 0;
}

export function fleetSeries(instances) {
  const running = (instances || []).map((item) => samplers.get(item.id)?.series).filter(Boolean);
  return aggregateSeries(running, sampleCapacity);
}

export function fleetConnections(instances) {
  return (instances || []).reduce((total, item) => total + instanceConnections(item.id), 0);
}

// Connections are stored per instance but read fleet-wide, so the owning
// instance's name has to travel with each row -- it is both a table column and
// a search term.
export function fleetConnectionRows(instances) {
  const rows = [];
  for (const item of instances || []) {
    const entry = samplers.get(item.id);
    if (!entry?.reachable) continue;
    for (const row of entry.connectionRows) {
      rows.push({ ...row, instanceId: item.id, instanceName: item.name || item.id });
    }
  }
  return rows;
}

// A stopped instance answers 409 from the proxy guard rather than returning
// empty data, so a rejection here is an expected state, not an error worth
// surfacing. Reset its counter baseline so the restart does not read as one
// giant delta.
export async function sampleInstance(instanceId, fetchConnections, now) {
  const entry = sampler(instanceId);
  let payload = null;
  try {
    payload = await fetchConnections(instanceId);
  } catch {
    resetConnectionState(entry);
    return;
  }
  const current = {
    at: now,
    uploadTotal: Number(payload?.uploadTotal) || 0,
    downloadTotal: Number(payload?.downloadTotal) || 0,
  };
  const rate = deriveRate(entry.previous, current);
  const rows = connectionSnapshot(payload);
  const { totals } = diffConnections(entry.connectionTotals, rows, entry.sampledAt ? now - entry.sampledAt : 0);
  entry.previous = current;
  entry.connectionRows = rows;
  entry.connectionTotals = totals;
  entry.sampledAt = now;
  entry.connections = rows.length;
  entry.reachable = true;
  pushSample(entry.series, { at: now, up: rate.up, down: rate.down });
}

export async function sampleFleet(instances, fetchConnections, now) {
  pruneSamplers(instances);
  const running = (instances || []).filter((item) => item.status === "running");
  for (const item of instances || []) {
    if (item.status !== "running") {
      const entry = samplers.get(item.id);
      if (entry) resetConnectionState(entry);
    }
  }
  await Promise.all(running.map((item) => sampleInstance(item.id, fetchConnections, now)));
}

function formatClock(ms) {
  const value = Number(ms) || 0;
  if (!value) return "--:--:--";
  const date = new Date(value);
  const pad = (n) => String(n).padStart(2, "0");
  return `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}

// The newest sample is the one number the chart is actually being watched for,
// so it gets a dot. Drawn as a zero-length round-capped stroke rather than a
// <circle>: the SVG scales non-uniformly (preserveAspectRatio="none"), which
// would squash a circle into an ellipse but leaves a round cap round.
function sparkHead(geometry, variant) {
  const point = geometry.points[geometry.points.length - 1];
  return `<path class="spark-head spark-${variant}-head" d="M${point.x} ${point.y}L${point.x} ${point.y}" vector-effect="non-scaling-stroke"/>`;
}

function dualSparkline(series, { width, height }) {
  const up = seriesField(series, "up");
  const down = seriesField(series, "down");
  const ceiling = Math.max(seriesPeak(series, "up"), seriesPeak(series, "down"));
  const open = `<svg class="spark spark-dual" viewBox="0 0 ${width} ${height}" preserveAspectRatio="none" aria-hidden="true">`;
  if (!up.length && !down.length) return `${open}</svg>`;
  const downGeo = sparklineGeometry(down.length ? down : [0], { width, height, max: ceiling });
  const upGeo = sparklineGeometry(up.length ? up : [0], { width, height, max: ceiling });
  const parts = [open];
  if (downGeo) {
    parts.push(`<path class="spark-area spark-down-fill" d="${downGeo.area}"/>`);
    parts.push(`<path class="spark-line spark-down-stroke" d="${downGeo.line}"/>`);
  }
  if (upGeo) {
    parts.push(`<path class="spark-area spark-up-fill" d="${upGeo.area}"/>`);
    parts.push(`<path class="spark-line spark-up-stroke" d="${upGeo.line}"/>`);
  }
  if (downGeo) parts.push(sparkHead(downGeo, "down"));
  if (upGeo) parts.push(sparkHead(upGeo, "up"));
  parts.push("</svg>");
  return parts.join("");
}

// One strip replaces what used to be four stacked cards (fleet, activity, and a
// card each for upload and download). Everything above the tables is fixed
// height, and every pixel spent here is a connection row the viewport-fit
// layout cannot show.
function metricsStrip(state, series, connections) {
  const instances = state.instances || [];
  const running = instances.filter((item) => item.status === "running");
  const reachable = running.filter((item) => samplers.get(item.id)?.reachable).length;
  const pending = instances.filter((item) => item.pendingRestart);
  const failed = instances.filter((item) => item.lastError || item.status === "error");
  const system = state.system || {};
  const tone = failed.length ? "is-danger" : pending.length ? "is-warn" : running.length ? "is-running" : "is-idle";
  const headline = !instances.length
    ? "尚无实例"
    : failed.length
      ? `${failed.length} 个异常`
      : pending.length
        ? `${pending.length} 个待重启`
        : running.length
          ? `${running.length} / ${instances.length} 运行中`
          : "全部已停止";
  const names = (list) => list.slice(0, 2).map((item) => escapeHTML(item.name)).join("、") + (list.length > 2 ? ` 等 ${list.length} 个` : "");
  const alert = failed.length ? `异常：${names(failed)}` : pending.length ? `待重启：${names(pending)}` : "";
  const chips = [
    { label: "mihomo", value: system.mihomoFound ? shortMihomoVersion(system.version) || "已就绪" : "未找到", tone: system.mihomoFound ? "is-ok" : "is-warn" },
    { label: "配置档", value: `${(state.profiles || []).length}`, tone: (state.profiles || []).length ? "is-ok" : "is-warn" },
    { label: "待重启", value: pending.length ? `${pending.length}` : "无", tone: pending.length ? "is-warn" : "is-ok" },
    { label: "异常", value: failed.length ? `${failed.length}` : "无", tone: failed.length ? "is-danger" : "is-ok" },
  ];
  const latest = seriesLatest(series);
  const up = formatRate(latest ? latest.up : 0);
  const down = formatRate(latest ? latest.down : 0);
  const peakUp = formatRate(seriesPeak(series, "up"));
  const peakDown = formatRate(seriesPeak(series, "down"));
  const unreachable = running.length - reachable;
  return `
    <article class="dash-card dash-strip">
      <div class="dash-strip-fleet">
        <span class="dash-orb ${tone}" aria-hidden="true"></span>
        <div class="dash-strip-fleet-copy">
          <h3>${escapeHTML(headline)}</h3>
          <p>${escapeHTML(alert || system.dataDir || "本地控制器")}</p>
        </div>
        <ul class="dash-chips" role="list">
          ${chips.map((chip) => `
            <li class="${chip.tone}">
              <span class="dash-check-dot ${chip.tone}" aria-hidden="true"></span>
              <span class="dash-chip-label">${escapeHTML(chip.label)}</span>
              <span class="dash-chip-value">${escapeHTML(chip.value)}</span>
            </li>`).join("")}
        </ul>
      </div>
      <div class="dash-strip-activity">
        <p class="eyebrow">ACTIVITY</p>
        <p class="dash-figure dash-figure-lg"><span class="dash-figure-value">${connections}</span></p>
        <p class="dash-figure-caption">活跃连接${unreachable > 0 ? ` · ${unreachable} 台未取到` : ""}</p>
      </div>
      <div class="dash-strip-rates">
        <div class="dash-strip-rate" data-direction="down">
          <span class="dash-rate-icon" aria-hidden="true">↓</span>
          <p class="dash-figure"><span class="dash-figure-value">${down.value}</span><span class="dash-figure-unit">${down.unit}</span></p>
          <small>峰值 ${peakDown.value} ${peakDown.unit}</small>
        </div>
        <div class="dash-strip-rate" data-direction="up">
          <span class="dash-rate-icon" aria-hidden="true">↑</span>
          <p class="dash-figure"><span class="dash-figure-value">${up.value}</span><span class="dash-figure-unit">${up.unit}</span></p>
          <small>峰值 ${peakUp.value} ${peakUp.unit}</small>
        </div>
        ${dualSparkline(series, { width: sparkWidth, height: sparkHeight })}
      </div>
    </article>`;
}

function trendBody(series, { width = sparkWidth, height = trendHeight } = {}) {
  const latest = seriesLatest(series);
  const span = seriesSpan(series);
  const currentUp = formatRate(latest ? latest.up : 0);
  const currentDown = formatRate(latest ? latest.down : 0);
  const sampleCount = series?.samples?.length || 0;
  return `
    <p class="dash-legend">
      <span class="dash-legend-item" data-direction="up">↑ ${currentUp.value} ${currentUp.unit}</span>
      <span class="dash-legend-item" data-direction="down">↓ ${currentDown.value} ${currentDown.unit}</span>
    </p>
    <div class="dash-trend-plot">
      ${dualSparkline(series, { width, height })}
      ${sampleCount < 2 ? `<p class="dash-trend-empty">等待采样填满近 ${trafficWindowSeconds} 秒窗口</p>` : ""}
    </div>
    <div class="dash-trend-axis">
      <span>${span ? formatClock(span.from) : `近 ${trafficWindowSeconds} 秒`}</span>
      <span>近 ${trafficWindowSeconds} 秒</span>
      <span>${span ? formatClock(span.to) : "现在"}</span>
    </div>`;
}

function trendCard(series, title, note) {
  const sampleCount = series?.samples?.length || 0;
  return `
    <article class="dash-card dash-trend">
      <div class="dash-trend-head">
        <div>
          <p class="eyebrow">LIVE TRAFFIC</p>
          <h3>${escapeHTML(title)}</h3>
          <p class="dash-trend-note">${escapeHTML(note)}</p>
        </div>
        <span class="dash-live">${sampleCount ? "实时" : "采样中"}</span>
      </div>
      ${trendBody(series)}
    </article>`;
}

function selectedDetail(state) {
  const instances = state.instances || [];
  const selected = instances.find((item) => item.id === state.activeId) || instances[0] || null;
  if (!selected) {
    return `
      <article class="dash-card dash-selected">
        <p class="eyebrow">INSTANCE</p>
        <h3>未选中实例</h3>
        <p class="dash-trend-note">在左侧列表点选实例，查看其近 ${trafficWindowSeconds} 秒流量。</p>
      </article>`;
  }
  const series = samplers.get(selected.id)?.series || createSeries(sampleCapacity);
  const latest = seriesLatest(series);
  const totals = samplers.get(selected.id)?.previous;
  const up = formatRate(latest ? latest.up : 0);
  const down = formatRate(latest ? latest.down : 0);
  const conns = instanceConnections(selected.id);
  const sampleCount = series?.samples?.length || 0;
  const meta = [
    statusText(selected.status),
    selected.mixedPort ? `混合 ${selected.mixedPort}` : "",
    selected.controllerPort ? `控制 ${selected.controllerPort}` : "",
    selected.pendingRestart ? "待重启" : "",
  ].filter(Boolean).join(" · ");
  const note = selected.status === "running"
    ? `单实例 · 近 ${trafficWindowSeconds} 秒内存采样`
    : "实例未运行，速率归零";
  return `
    <article class="dash-card dash-selected">
      <div class="dash-selected-head">
        <div>
          <p class="eyebrow">INSTANCE</p>
          <h3>${escapeHTML(selected.name)}</h3>
          <p class="dash-trend-note">${escapeHTML(meta)}</p>
        </div>
        <div class="dash-selected-actions">
          <span class="dash-live">${sampleCount ? "实时" : "采样中"}</span>
          <button type="button" class="dash-open-btn" data-open-instance="${escapeHTML(selected.id)}">打开工作台</button>
        </div>
      </div>
      <ul class="dash-selected-metrics" role="list">
        <li><strong>${conns}</strong><span>连接</span></li>
        <li><strong>${up.value} <small>${up.unit}</small></strong><span>↑ 当前</span></li>
        <li><strong>${down.value} <small>${down.unit}</small></strong><span>↓ 当前</span></li>
        <li><strong>${formatBytes(totals?.downloadTotal || 0)}</strong><span>↓ 累计</span></li>
      </ul>
      <p class="dash-trend-note dash-selected-note">${escapeHTML(note)}</p>
      <div class="dash-selected-spark">${dualSparkline(series, { width: sparkWidth, height: sparkHeight })}</div>
    </article>`;
}

// Trimming the list must never hide the row the user is looking at, so the
// selected instance takes the last visible slot when it falls past the cut.
function visibleInstances(instances, activeId) {
  if (instances.length <= rowBudgets.instances) return instances;
  const shown = instances.slice(0, rowBudgets.instances);
  if (shown.some((item) => item.id === activeId)) return shown;
  const active = instances.find((item) => item.id === activeId);
  if (!active) return shown;
  return [...shown.slice(0, -1), active];
}

function instanceRows(state) {
  const all = state.instances || [];
  if (!all.length) {
    return `<p class="dash-empty">还没有实例。先创建配置档，再新建实例。</p>`;
  }
  const activeId = state.activeId || all[0]?.id || "";
  const instances = visibleInstances(all, activeId);
  const rows = instances.map((item) => {
    const series = samplers.get(item.id)?.series || createSeries(sampleCapacity);
    const latest = seriesLatest(series);
    const up = formatRate(latest ? latest.up : 0);
    const down = formatRate(latest ? latest.down : 0);
    const totals = samplers.get(item.id)?.previous;
    const active = item.id === activeId;
    const bad = Boolean(item.lastError || item.status === "error");
    const warn = Boolean(item.pendingRestart);
    const dot = bad ? "is-danger" : item.status === "running" ? "is-ok" : warn ? "is-warn" : "is-idle";
    return `
      <tr class="${active ? "is-active" : ""}${bad ? " is-danger" : warn ? " is-warn" : ""}" data-instance-id="${escapeHTML(item.id)}" tabindex="0">
        <td class="dash-cell-name">
          <span class="dash-check-dot ${dot}" aria-hidden="true"></span>
          <span>
            <strong>${escapeHTML(item.name)}</strong>
            <small class="${statusClass(item.status)}">${escapeHTML(statusText(item.status))}${item.pendingRestart ? " · 待重启" : ""}${bad && item.lastError ? ` · ${escapeHTML(String(item.lastError).slice(0, 48))}` : ""}</small>
          </span>
        </td>
        <td class="num">${instanceConnections(item.id)}</td>
        <td class="num">${up.value} ${up.unit}<small>累计 ${formatBytes(totals?.uploadTotal || 0)}</small></td>
        <td class="num">${down.value} ${down.unit}<small>累计 ${formatBytes(totals?.downloadTotal || 0)}</small></td>
        <td class="dash-cell-spark">${dualSparkline(series, {
          width: rowSparkWidth,
          height: rowSparkHeight,
        })}</td>
      </tr>`;
  });
  return `
    <table class="dash-table dash-instance-table">
      <thead>
        <tr>
          <th scope="col">实例</th>
          <th scope="col">连接</th>
          <th scope="col">↑ 当前</th>
          <th scope="col">↓ 当前</th>
          <th scope="col">近 ${trafficWindowSeconds} 秒</th>
        </tr>
      </thead>
      <tbody>${rows.join("")}</tbody>
    </table>`;
}

// Country codes are resolved once per address and kept for the session: a
// connection's destination does not move between countries, and the table
// re-renders every 1.8s. A miss is cached as "" so a database that simply does
// not carry that address is not re-asked forever.
const geoCache = new Map();
const geoPending = new Set();
let geoAvailable = true;
let geoFetch = null;

export function setGeoResolver(fetchCountries) {
  geoFetch = fetchCountries;
}

function requestGeo(rows) {
  if (!geoAvailable || !geoFetch) return;
  const wanted = [];
  for (const row of rows) {
    const ip = row.ip;
    if (!ip || geoCache.has(ip) || geoPending.has(ip) || localAddressLabel(ip)) continue;
    geoPending.add(ip);
    wanted.push(ip);
  }
  if (!wanted.length) return;
  geoFetch(wanted)
    .then((result) => {
      if (result && result.available === false) geoAvailable = false;
      const countries = result?.countries || {};
      for (const ip of wanted) geoCache.set(ip, countries[ip] || "");
    })
    .catch(() => {
      // Leave the addresses uncached so the next paint retries; a failed
      // lookup should not permanently blank the column.
    })
    .finally(() => {
      for (const ip of wanted) geoPending.delete(ip);
    });
}

function geoCell(row) {
  const local = localAddressLabel(row.ip);
  if (local) return `<span class="dash-geo-local">${escapeHTML(local)}</span>`;
  const code = geoCache.get(row.ip);
  if (!code) return `<span class="dash-geo-unknown">—</span>`;
  return `<span class="dash-geo"><span class="dash-geo-flag" aria-hidden="true">${countryFlag(code)}</span>${escapeHTML(code)}</span>`;
}

function connectionTarget(row) {
  const address = [row.ip, row.port].filter(Boolean).join(":");
  if (row.host) return { primary: row.host, secondary: address };
  return { primary: address || "—", secondary: "" };
}

function connectionRow(row, now) {
  const target = connectionTarget(row);
  const up = formatRate(row.up);
  const down = formatRate(row.down);
  const origin = [row.process, row.sourceIP].filter(Boolean).join(" · ");
  const rule = [row.rule, row.rulePayload && `(${row.rulePayload})`].filter(Boolean).join(" ");
  // Reversed so the chain reads entry group first, matching how the config
  // declares it; chains[0] (the node that carried the request) stays the label.
  const chain = row.chains.length ? [...row.chains].reverse().join(" → ") : "";
  return `
    <tr>
      <td class="dash-conn-target">
        <strong>${escapeHTML(target.primary)}</strong>
        ${target.secondary || origin ? `<small>${escapeHTML([target.secondary, origin].filter(Boolean).join(" · "))}</small>` : ""}
      </td>
      <td>
        <span class="dash-conn-text">${escapeHTML(row.instanceName || "")}</span>
        <small>${escapeHTML([row.network, row.kind].filter(Boolean).join(" · "))}</small>
      </td>
      <td title="${escapeHTML(chain)}">
        <span class="dash-conn-text">${escapeHTML(row.node || "—")}</span>
        ${rule ? `<small>${escapeHTML(rule)}</small>` : ""}
      </td>
      <td class="dash-conn-geo">${geoCell(row)}</td>
      <td class="num">${up.value} ${up.unit}<small>${formatBytes(row.upload)}</small></td>
      <td class="num">${down.value} ${down.unit}<small>${formatBytes(row.download)}</small></td>
      <td class="num">${row.start ? escapeHTML(formatDuration(now - row.start)) : "—"}</td>
    </tr>`;
}

function connectionsCard(state, now) {
  const running = (state.instances || []).filter((item) => item.status === "running");
  const all = fleetConnectionRows(running);
  const matched = sortConnections(filterConnections(all, connectionQuery));
  const shown = matched.slice(0, rowBudgets.connections);
  requestGeo(shown);
  const note = !all.length
    ? "运行中的实例暂无活跃连接"
    : connectionQuery.trim()
      ? `匹配 ${matched.length} / ${all.length} 条${matched.length > shown.length ? ` · 显示前 ${shown.length}` : ""}`
      : `共 ${all.length} 条${matched.length > shown.length ? ` · 显示最忙的 ${shown.length}` : ""}`;
  const body = shown.length
    ? `
      <div class="dash-conn-body">
        <table class="dash-table dash-conn-table">
          <thead>
            <tr>
              <th scope="col">目标</th>
              <th scope="col">实例</th>
              <th scope="col">出口</th>
              <th scope="col">GEO</th>
              <th scope="col">↑ 当前</th>
              <th scope="col">↓ 当前</th>
              <th scope="col">时长</th>
            </tr>
          </thead>
          <tbody>${shown.map((row) => connectionRow(row, now)).join("")}</tbody>
        </table>
      </div>`
    : `<p class="dash-empty">${all.length ? "没有匹配的连接" : "暂无活跃连接"}</p>`;
  return `
    <article class="dash-card dash-conns">
      <div class="dash-conns-head">
        <div>
          <p class="eyebrow">CONNECTIONS</p>
          <h3>实时连接</h3>
          <p class="dash-trend-note">${escapeHTML(note)}</p>
        </div>
        <input
          class="dash-conn-search"
          type="search"
          autocomplete="off"
          spellcheck="false"
          placeholder="搜索域名 / IP / 进程 / 规则"
          aria-label="搜索连接"
          value="${escapeHTML(connectionQuery)}"
        >
      </div>
      ${body}
    </article>`;
}

// The whole dashboard is re-rendered from scratch on every 1.8s poll, which
// would otherwise drop the caret out of the search box mid-word.
function captureLiveState(container) {
  const search = container.querySelector(".dash-conn-search");
  const focused = search && document.activeElement === search;
  return { caret: focused ? [search.selectionStart, search.selectionEnd] : null };
}

function restoreLiveState(container, live) {
  if (!live.caret) return;
  const search = container.querySelector(".dash-conn-search");
  if (!search) return;
  search.focus();
  // A search input rejects setSelectionRange in some engines; losing the caret
  // position is not worth breaking the render over.
  try {
    search.setSelectionRange(live.caret[0], live.caret[1]);
  } catch {
    /* caret restore is best-effort */
  }
}

// An IME composition lives in the DOM node being typed into, so the 1.8s poll
// rebuilding the panel mid-word would drop the pinyin buffer on the floor.
// Compositions are short, so freezing the whole dashboard for one is cheaper
// than any scheme that keeps the input node alive across an innerHTML swap.
let composing = false;

function bindComposition(container) {
  if (container.dataset.dashComposition === "1") return;
  container.dataset.dashComposition = "1";
  container.addEventListener("compositionstart", (event) => {
    if (event.target.closest?.(".dash-conn-search")) composing = true;
  });
  container.addEventListener("compositionend", () => {
    composing = false;
  });
  // If the input loses focus mid-composition the browser may never fire
  // compositionend, and a stuck flag would freeze the dashboard for good.
  container.addEventListener("focusout", (event) => {
    if (event.target.closest?.(".dash-conn-search")) composing = false;
  });
}

function instancesNote(state) {
  const total = (state.instances || []).length;
  const hidden = total - Math.min(total, rowBudgets.instances);
  if (hidden > 0) return `显示 ${total - hidden} / ${total} 台 · 其余在左侧列表`;
  return "点选查看右侧趋势；双击或点「打开工作台」进入该实例。";
}

// Both tables fill a box the grid sized for them, so how many rows fit is a
// measurement, not a constant: read the box back after painting and let the
// next pass render exactly that many. The budgets cannot feed back into the
// layout (the boxes are `1fr` with overflow hidden), so this converges in one
// extra pass instead of oscillating.
function fitTables(container) {
  const specs = [
    { key: "connections", body: ".dash-conn-body", table: ".dash-conn-table" },
    { key: "instances", body: ".dash-inst-body", table: ".dash-instance-table" },
  ];
  let changed = false;
  for (const spec of specs) {
    const body = container.querySelector(spec.body);
    const table = body?.querySelector(spec.table);
    const firstRow = table?.tBodies?.[0]?.rows?.[0];
    // No box, no rows, or a collapsed panel: keep the current budget rather
    // than derive one from a zero-height measurement.
    if (!body || !table || !firstRow || !body.clientHeight) continue;
    const rowHeight = Math.min(firstRow.offsetHeight || 0, maxRowHeight);
    if (rowHeight <= 0) continue;
    const available = body.clientHeight - (table.tHead?.offsetHeight || 0);
    const fits = Math.max(1, Math.floor(available / rowHeight));
    if (fits === rowBudgets[spec.key]) continue;
    rowBudgets[spec.key] = fits;
    changed = true;
  }
  return changed;
}

// Viewport-fit is a CSS decision (a min-height media query), so the stylesheet
// announces it through --dash-fit rather than the script duplicating the
// breakpoint. Outside fit mode the table boxes grow with their content, and
// measuring them would feed row count back into box height -- a loop that adds
// rows forever.
function viewportFitActive(container) {
  if (typeof getComputedStyle !== "function") return false;
  return getComputedStyle(container).getPropertyValue("--dash-fit").trim() === "1";
}

export function renderDashboard(container, state) {
  if (!container) return;
  bindComposition(container);
  if (composing) return;
  const fit = viewportFitActive(container);
  if (!fit) {
    // Short window: the page scrolls anyway, so show a useful slice instead of
    // the handful that would fit a tall layout's leftover space.
    rowBudgets.connections = 24;
    rowBudgets.instances = Number.MAX_SAFE_INTEGER;
  }
  paintDashboard(container, state);
  // Re-measuring may reveal the box holds more (or fewer) rows than the last
  // paint assumed; one corrective pass is enough, and a second measurement can
  // only agree with it.
  if (fit && fitTables(container)) paintDashboard(container, state);
}

function paintDashboard(container, state) {
  const live = captureLiveState(container);
  const instances = state.instances || [];
  const running = instances.filter((item) => item.status === "running");
  const pending = instances.filter((item) => item.pendingRestart).length;
  const failed = instances.filter((item) => item.lastError || item.status === "error").length;
  const series = fleetSeries(running);
  const connections = fleetConnections(running);
  const summary = [
    `${instances.length} 个实例`,
    `${running.length} 运行中`,
    pending ? `${pending} 待重启` : "",
    failed ? `${failed} 异常` : "",
    `${connections} 连接`,
  ].filter(Boolean).join(" · ");
  container.innerHTML = `
    <div class="dashboard-head">
      <h2>舰队状态</h2>
      <p>${escapeHTML(summary || "尚无实例")}</p>
    </div>
    <div class="dashboard-grid dashboard-grid-strip">
      ${metricsStrip(state, series, connections)}
    </div>
    <div class="dashboard-grid dashboard-grid-mid">
      <article class="dash-card dash-instances">
        <div class="dash-instances-head">
          <div>
            <h3>实例</h3>
            <p>${escapeHTML(instancesNote(state))}</p>
          </div>
        </div>
        <div class="dash-inst-body">${instanceRows(state)}</div>
      </article>
      ${trendCard(series, "舰队流量", `全部运行中实例合计 · 近 ${trafficWindowSeconds} 秒内存采样`)}
      ${selectedDetail(state)}
    </div>
    <div class="dashboard-grid dashboard-grid-conns">
      ${connectionsCard(state, Date.now())}
    </div>`;
  restoreLiveState(container, live);
}
