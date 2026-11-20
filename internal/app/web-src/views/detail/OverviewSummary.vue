<script setup lang="ts">
// The read-only half of the 概况 tab: what the selected instance listens on,
// how it routes, and its last exit-IP check. Split out of OverviewTab.vue so
// that file keeps only the edit form; it reads the same store and owns no
// state of its own.
import { computed } from "vue";
import { store } from "../../store.ts";
import { activeInstance } from "../../state.ts";
import { defaultProxyBind, ipCheckPresets } from "../../constants.ts";
import {
  chainSummary,
  instanceMode,
  modeLabel,
  proxyEndpointText,
  restartEvidenceText,
  selectionSummary,
} from "../../format.ts";
import { portMappingSummary } from "../../port-mappings.ts";
import { ipCheck, ipCheckPreset, ipCheckUrl, persistIpCheckUrl, pickIpCheckPreset } from "./ip-check.ts";

const selected = computed(() => activeInstance(store));

// FleetInstance's optional overview fields fall back the same way
// renderPanels() did (pre-Vue app.ts).
const overviewMixed = computed(() => (selected.value ? proxyEndpointText(selected.value) : ""));
const overviewProxyBind = computed(() => selected.value?.proxyBind || defaultProxyBind);
const overviewController = computed(() => (selected.value ? `127.0.0.1:${selected.value.controllerPort}` : ""));
const overviewMode = computed(() => (selected.value ? modeLabel(instanceMode(selected.value)) : ""));
const overviewChain = computed(() => (selected.value ? chainSummary(selected.value) : ""));
const overviewProfile = computed(() => selected.value?.profileName || selected.value?.profileId || "无");
const overviewUserConfig = computed(() => selected.value?.profileConfigPath || selected.value?.userConfigPath || "");
const overviewRuntimeConfig = computed(() => selected.value?.runtimeConfigPath || "");
const overviewSelection = computed(() => (selected.value ? selectionSummary(selected.value) : ""));
const overviewPortMappings = computed(() => portMappingSummary(selected.value?.portMappings));
const overviewConfigOverride = computed(() => (selected.value?.configOverride?.trim() ? "已设置" : "无"));
// Crash-watchdog status + evidence (#2): a short "开启/关闭" state plus --
// only when there is anything to report -- restart count and last crash
// reason, so the operator can tell "armed but never fired" apart from
// "armed and it already saved this instance once".
const overviewExitIp = computed(() => {
  const id = selected.value?.id;
  if (!id) return "";
  if (ipCheck.running.has(id)) return "测试中…";
  const result = ipCheck.results[id];
  if (!result) return "未测试";
  const at = new Date(result.at).toLocaleTimeString("zh-CN", { hour12: false });
  if (result.error) return `测试失败（${at}）：${result.error}`;
  return `${result.ip}（${at}，${result.elapsedMs} ms）`;
});
const overviewAutoRestart = computed(() => {
  if (!selected.value) return "";
  const base = selected.value.autoRestart ? "已开启" : "已关闭";
  const evidence = restartEvidenceText(selected.value);
  return evidence ? `${base} · ${evidence}` : base;
});
</script>

<template>
  <section class="panel">
    <h3>端口</h3>
    <dl class="kv">
      <dt>本地代理</dt>
      <dd id="overviewMixed">{{ overviewMixed }}</dd>
      <dt>代理绑定</dt>
      <dd id="overviewProxyBind">{{ overviewProxyBind }}</dd>
      <dt>端口映射</dt>
      <dd id="overviewPortMappings">{{ overviewPortMappings }}</dd>
      <dt>外部控制器</dt>
      <dd id="overviewController">{{ overviewController }}</dd>
      <dt>实例模式</dt>
      <dd id="overviewMode">{{ overviewMode }}</dd>
      <dt>生效链路</dt>
      <dd id="overviewChain">{{ overviewChain }}</dd>
      <dt>配置档</dt>
      <dd id="overviewProfile">{{ overviewProfile }}</dd>
      <dt>配置文件</dt>
      <dd id="overviewUserConfig">{{ overviewUserConfig }}</dd>
      <dt>运行配置</dt>
      <dd id="overviewRuntimeConfig">{{ overviewRuntimeConfig }}</dd>
      <dt>已保存节点</dt>
      <dd id="overviewSelection">{{ overviewSelection }}</dd>
      <dt>配置覆盖</dt>
      <dd id="overviewConfigOverride">{{ overviewConfigOverride }}</dd>
      <dt>崩溃自动重启</dt>
      <dd id="overviewAutoRestart">{{ overviewAutoRestart }}</dd>
      <dt>出口 IP</dt>
      <dd id="overviewExitIp">{{ overviewExitIp }}</dd>
    </dl>
    <div class="ip-check-url">
      <span>测试 IP 网址</span>
      <select id="ipCheckPreset" :value="ipCheckPreset" @change="pickIpCheckPreset">
        <option v-for="preset in ipCheckPresets" :key="preset" :value="preset">{{ preset }}</option>
        <option value="">自定义…</option>
      </select>
      <input id="ipCheckUrl" v-model="ipCheckUrl" :placeholder="ipCheckPresets[0]" @change="persistIpCheckUrl">
    </div>
    <p class="field-note">点击标签行的“测试 IP”后，经这个实例的混合端口请求该网址，网址需返回纯文本 IP 或带 <code>ip</code> 字段的 JSON。</p>
  </section>
</template>
