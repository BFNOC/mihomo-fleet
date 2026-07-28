import { formatBytes } from "./format.js";
import { escapeHTML, statusClass, statusText } from "./i18n.js";
import {
  aggregateSeries,
  createSeries,
  deriveRate,
  formatRate,
  pushSample,
  seriesField,
  seriesLatest,
  seriesPeak,
  seriesSpan,
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

// One sampler per instance: the rolling rate series plus the previous
// cumulative counter reading the next rate is derived from.
const samplers = new Map();

function sampler(instanceId) {
  let entry = samplers.get(instanceId);
  if (!entry) {
    entry = { series: createSeries(sampleCapacity), previous: null, connections: 0, reachable: false };
    samplers.set(instanceId, entry);
  }
  return entry;
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
    entry.previous = null;
    entry.connections = 0;
    entry.reachable = false;
    return;
  }
  const current = {
    at: now,
    uploadTotal: Number(payload?.uploadTotal) || 0,
    downloadTotal: Number(payload?.downloadTotal) || 0,
  };
  const rate = deriveRate(entry.previous, current);
  entry.previous = current;
  entry.connections = Array.isArray(payload?.connections) ? payload.connections.length : 0;
  entry.reachable = true;
  pushSample(entry.series, { at: now, up: rate.up, down: rate.down });
}

export async function sampleFleet(instances, fetchConnections, now) {
  pruneSamplers(instances);
  const running = (instances || []).filter((item) => item.status === "running");
  for (const item of instances || []) {
    if (item.status !== "running") {
      const entry = samplers.get(item.id);
      if (entry) {
        entry.previous = null;
        entry.connections = 0;
        entry.reachable = false;
      }
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
  parts.push("</svg>");
  return parts.join("");
}

function singleSparkline(values, { width, height, max, variant }) {
  const geometry = sparklineGeometry(values, { width, height, max });
  const open = `<svg class="spark spark-${variant}" viewBox="0 0 ${width} ${height}" preserveAspectRatio="none" aria-hidden="true">`;
  if (!geometry) return `${open}</svg>`;
  return `${open}<path class="spark-area" d="${geometry.area}"/><path class="spark-line" d="${geometry.line}"/></svg>`;
}

function rateBlock(series, field, variant, label) {
  const values = seriesField(series, field);
  const latest = seriesLatest(series);
  const current = formatRate(latest ? latest[field] : 0);
  const peak = formatRate(seriesPeak(series, field));
  return `
    <article class="dash-card dash-rate" data-direction="${variant}">
      <div class="dash-rate-head">
        <span class="dash-rate-icon" aria-hidden="true">${variant === "up" ? "↑" : "↓"}</span>
        <div>
          <p class="eyebrow">${variant === "up" ? "UPLOAD" : "DOWNLOAD"}</p>
          <p class="dash-rate-label">${label}</p>
        </div>
        <span class="dash-live">实时</span>
      </div>
      <p class="dash-figure"><span class="dash-figure-value">${current.value}</span><span class="dash-figure-unit">${current.unit}</span></p>
      <div class="dash-rate-foot">
        <span>实时速率</span>
        <span>峰值 ${peak.value} ${peak.unit}</span>
      </div>
      ${singleSparkline(values, { width: sparkWidth, height: sparkHeight, variant })}
    </article>`;
}

function fleetCard(state) {
  const instances = state.instances || [];
  const running = instances.filter((item) => item.status === "running");
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
  const checks = [
    {
      label: "mihomo",
      value: system.mihomoFound ? `已就绪${system.version ? ` · ${system.version}` : ""}` : "未找到",
      tone: system.mihomoFound ? "is-ok" : "is-warn",
    },
    {
      label: "配置档",
      value: `${(state.profiles || []).length} 份`,
      tone: (state.profiles || []).length > 0 ? "is-ok" : "is-warn",
    },
    {
      label: "待重启",
      value: pending.length ? `${pending.length} 个` : "无",
      tone: pending.length ? "is-warn" : "is-ok",
    },
    {
      label: "异常",
      value: failed.length ? `${failed.length} 个` : "无",
      tone: failed.length ? "is-danger" : "is-ok",
    },
  ];
  const alert = failed.length
    ? failed.slice(0, 2).map((item) => escapeHTML(item.name)).join("、") + (failed.length > 2 ? ` 等 ${failed.length} 个` : "")
    : pending.length
      ? pending.slice(0, 2).map((item) => escapeHTML(item.name)).join("、") + (pending.length > 2 ? ` 等 ${pending.length} 个` : "")
      : "";
  return `
    <article class="dash-card dash-fleet">
      <p class="eyebrow">FLEET</p>
      <div class="dash-fleet-state">
        <span class="dash-orb ${tone}" aria-hidden="true"></span>
        <div>
          <h3>${escapeHTML(headline)}</h3>
          <p>${escapeHTML(system.dataDir || "本地控制器")}</p>
        </div>
      </div>
      ${alert ? `<p class="dash-alert ${failed.length ? "is-danger" : "is-warn"}">${failed.length ? "异常" : "待重启"}：${alert}</p>` : ""}
      <ul class="dash-checks" role="list">
        ${checks.map((check) => `
          <li>
            <span class="dash-check-dot ${check.tone}" aria-hidden="true"></span>
            <span class="dash-check-label">${escapeHTML(check.label)}</span>
            <span class="dash-check-value">${escapeHTML(check.value)}</span>
          </li>`).join("")}
      </ul>
    </article>`;
}

function activityCard(state, connections) {
  const instances = state.instances || [];
  const running = instances.filter((item) => item.status === "running");
  const reachable = running.filter((item) => samplers.get(item.id)?.reachable);
  const pending = instances.filter((item) => item.pendingRestart).length;
  const failed = instances.filter((item) => item.lastError || item.status === "error").length;
  return `
    <article class="dash-card dash-activity">
      <p class="eyebrow">ACTIVITY</p>
      <h3>当前活动</h3>
      <p class="dash-figure dash-figure-lg"><span class="dash-figure-value">${connections}</span></p>
      <p class="dash-figure-caption">活跃连接</p>
      <ul class="dash-split" role="list">
        <li><strong>${running.length}</strong><span>运行中</span></li>
        <li><strong>${pending}</strong><span>待重启</span></li>
        <li><strong>${failed || running.length - reachable.length}</strong><span>${failed ? "异常" : "未取到"}</span></li>
      </ul>
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
      <p class="dash-trend-note">${escapeHTML(note)}</p>
      ${trendBody(series, { height: trendHeight })}
    </article>`;
}

function instanceRows(state) {
  const instances = state.instances || [];
  if (!instances.length) {
    return `<p class="dash-empty">还没有实例。先创建配置档，再新建实例。</p>`;
  }
  const activeId = state.activeId || instances[0]?.id || "";
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
        <td class="mono">${item.mixedPort || "—"}</td>
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
    <table class="dash-table">
      <thead>
        <tr>
          <th scope="col">实例</th>
          <th scope="col">混合端口</th>
          <th scope="col">连接</th>
          <th scope="col">↑ 当前</th>
          <th scope="col">↓ 当前</th>
          <th scope="col">近 ${trafficWindowSeconds} 秒</th>
        </tr>
      </thead>
      <tbody>${rows.join("")}</tbody>
    </table>`;
}

export function renderDashboard(container, state) {
  if (!container) return;
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
      <p class="eyebrow">总览</p>
      <h2>舰队状态</h2>
      <p>${escapeHTML(summary || "尚无实例")}</p>
    </div>
    <div class="dashboard-grid">
      ${fleetCard(state)}
      ${rateBlock(series, "up", "up", "上传")}
      ${rateBlock(series, "down", "down", "下载")}
    </div>
    <div class="dashboard-grid dashboard-grid-lower">
      ${activityCard(state, connections)}
      ${trendCard(series, "舰队流量", `全部运行中实例合计 · 近 ${trafficWindowSeconds} 秒内存采样`)}
    </div>
    <div class="dashboard-grid dashboard-grid-instances">
      <article class="dash-card dash-instances">
        <div class="dash-instances-head">
          <div>
            <h3>实例</h3>
            <p>点选查看右侧趋势；双击或点「打开工作台」进入该实例。</p>
          </div>
        </div>
        ${instanceRows(state)}
      </article>
      ${selectedDetail(state)}
    </div>`;
}
