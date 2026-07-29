<script setup lang="ts">
// Live connection table. Replaces the pre-Vue connectionsCard()/connectionRow()/
// connectionTarget()/geoCell() plus the delegated `input` listener on the search
// box.
//
// Deliberately NOT ported: captureLiveState/restoreLiveState (the search box's
// caret save/restore) and bindComposition (the IME composition guard). Both
// existed only so a full `container.innerHTML = ...` repaint could fake
// preserving focus/caret across itself. Vue's keyed v-for keeps real DOM node
// identity for free, and the search box below is a plain v-model input, which
// handles IME composition correctly on its own.
import { computed, ref, watchEffect } from "vue";
import { requestGeo, resolveGeo } from "../../dashboard.ts";
import type { FleetConnectionRow } from "../../dashboard.ts";
import { countryFlag, filterConnections, formatDuration, formatRate, localAddressLabel, sortConnections } from "../../traffic.ts";
import { formatBytes } from "../../format.ts";
import { allConnectionRows, nowTick } from "./dashboard-data.ts";

// This table scrolls inside its own card rather than being clipped to a measured
// row budget the way the instance table still is (use-row-budget.ts) -- "what is
// the fleet doing right now" is not answerable from the six rows that happen to
// fit. The page itself still does not scroll in viewport-fit mode; the overflow
// lives on .dash-conn-body (dashboard-tables.css).
//
// The cap is not about layout, it is about per-tick cost: every visible row's
// rate and age recompute and repaint on each heartbeat, and requestGeo() fires a
// lookup for every address it has not cached. Rows are sorted busiest-first, so
// what a cap drops is the idle tail.
const maxConnectionRows = 500;

const searchQuery = ref("");
const matchedConnectionRows = computed(() => sortConnections(filterConnections(allConnectionRows.value, searchQuery.value)));
const shownConnectionRows = computed(() => matchedConnectionRows.value.slice(0, maxConnectionRows));

// Kicking the lookups off is a side effect, so it lives in a watchEffect rather
// than inside a computed; only what is actually on screen gets looked up.
watchEffect(() => {
  requestGeo(shownConnectionRows.value);
});

// dashboard.ts's geoCache is a plain Map outside Vue's reactive graph, so a
// resolved code cannot invalidate anything by itself. Reading nowTick (which
// reads the heartbeat) republishes the cache once per tick -- the same cadence
// at which the pre-Vue innerHTML repaint picked resolutions up.
const connectionGeo = computed<Record<string, string>>(() => {
  void nowTick.value;
  const codes: Record<string, string> = {};
  for (const row of shownConnectionRows.value) codes[row.ip] = resolveGeo(row.ip);
  return codes;
});

const connectionsNote = computed(() => {
  const all = allConnectionRows.value;
  const matched = matchedConnectionRows.value;
  const shown = shownConnectionRows.value;
  if (!all.length) return "运行中的实例暂无活跃连接";
  if (searchQuery.value.trim()) {
    return `匹配 ${matched.length} / ${all.length} 条${matched.length > shown.length ? ` · 显示前 ${shown.length}` : ""}`;
  }
  return `共 ${all.length} 条${matched.length > shown.length ? ` · 显示最忙的 ${shown.length}` : ""}`;
});

function targetPrimary(row: FleetConnectionRow): string {
  const address = [row.ip, row.port].filter(Boolean).join(":");
  return row.host || address || "—";
}

function targetSecondary(row: FleetConnectionRow): string {
  if (!row.host) return "";
  return [row.ip, row.port].filter(Boolean).join(":");
}

function connectionOrigin(row: FleetConnectionRow): string {
  return [row.process, row.sourceIP].filter(Boolean).join(" · ");
}

function connectionSubtitle(row: FleetConnectionRow): string {
  return [targetSecondary(row), connectionOrigin(row)].filter(Boolean).join(" · ");
}

function connectionRuleText(row: FleetConnectionRow): string {
  return [row.rule, row.rulePayload && `(${row.rulePayload})`].filter(Boolean).join(" ");
}

// Reversed so the chain reads entry group first, matching how the config
// declares it; chains[0] (the node that carried the request) is shown as the
// node column instead and left out of this title.
function connectionChainTitle(row: FleetConnectionRow): string {
  return row.chains.length ? [...row.chains].reverse().join(" → ") : "";
}
</script>

<template>
  <article class="dash-card dash-conns">
    <div class="dash-conns-head">
      <div>
        <p class="eyebrow">CONNECTIONS</p>
        <h3>实时连接</h3>
        <p class="dash-trend-note">{{ connectionsNote }}</p>
      </div>
      <input
        v-model="searchQuery"
        class="dash-conn-search"
        type="search"
        autocomplete="off"
        spellcheck="false"
        placeholder="搜索域名 / IP / 进程 / 规则"
        aria-label="搜索连接"
      >
    </div>
    <div v-if="shownConnectionRows.length" class="dash-conn-body">
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
        <tbody>
          <!-- row.id (mihomo's own connection id) is only unique within one
               instance's process, not fleet-wide -- two instances can and do
               issue the same id independently. Keying on row.id alone breaks
               Vue's keyed-diff invariant (duplicate keys in one v-for), which
               does not just mispatch props: on every re-render it leaves the
               previous patch's now-orphaned nodes in the DOM instead of
               reusing or removing them, so the table's real node count grows
               without bound every heartbeat tick even while the panel is
               display:none. Confirmed by measurement: a single running
               instance (unique ids) converges and stays flat; three instances
               (colliding ids) grow tr/td/small counts every tick with no
               plateau. instanceId scopes the key back to what mihomo actually
               guarantees unique. -->
          <tr v-for="row in shownConnectionRows" :key="`${row.instanceId}:${row.id}`">
            <td class="dash-conn-target">
              <strong>{{ targetPrimary(row) }}</strong>
              <small v-if="connectionSubtitle(row)">{{ connectionSubtitle(row) }}</small>
            </td>
            <td>
              <span class="dash-conn-text">{{ row.instanceName || "" }}</span>
              <small>{{ [row.network, row.kind].filter(Boolean).join(" · ") }}</small>
            </td>
            <td :title="connectionChainTitle(row)">
              <span class="dash-conn-text">{{ row.node || "—" }}</span>
              <small v-if="connectionRuleText(row)">{{ connectionRuleText(row) }}</small>
            </td>
            <td class="dash-conn-geo">
              <span v-if="localAddressLabel(row.ip)" class="dash-geo-local">{{ localAddressLabel(row.ip) }}</span>
              <span v-else-if="connectionGeo[row.ip]" class="dash-geo">
                <span class="dash-geo-flag" aria-hidden="true">{{ countryFlag(connectionGeo[row.ip]) }}</span>{{ connectionGeo[row.ip] }}
              </span>
              <span v-else class="dash-geo-unknown">—</span>
            </td>
            <td class="num">{{ formatRate(row.up).value }} {{ formatRate(row.up).unit }}<small>{{ formatBytes(row.upload) }}</small></td>
            <td class="num">{{ formatRate(row.down).value }} {{ formatRate(row.down).unit }}<small>{{ formatBytes(row.download) }}</small></td>
            <td class="num">{{ row.start ? formatDuration(nowTick - row.start) : "—" }}</td>
          </tr>
        </tbody>
      </table>
    </div>
    <p v-else class="dash-empty">{{ allConnectionRows.length ? "没有匹配的连接" : "暂无活跃连接" }}</p>
  </article>
</template>
