<script setup lang="ts">
// Vue replacement for the inner content of <header class="topbar"> (index.html:10-25)
// and three app.ts render functions:
//   - renderSystem() (app.ts:419-425 half) -- the #systemLine text only. The
//     other half of that function (#systemWarning, app.ts:427-431) belongs to
//     the sidebar and is being migrated separately.
//   - renderViewNavigation() (app.ts:407-417) -- only the #manageProfilesBtn
//     and #instanceSelectorWrap parts. #showDashboardBtn also lives in that
//     function but belongs to the sidebar, migrated separately.
//   - renderSelector() (app.ts:441-461) in full.
import { computed } from "vue";
import { store } from "../store.ts";
import { actions, chrome } from "../bridge.ts";
import { activeInstance } from "../state.ts";
import { shortMihomoVersion } from "../format.ts";
import { statusText } from "../i18n.ts";

// Mirrors renderSystem()'s #systemLine half (app.ts:419-425). Falls back to
// the static placeholder index.html previously hard-coded into <p id="systemLine">
// for the boot gap before the first GET /api/system response lands.
const systemLineText = computed(() => {
  const system = store.system;
  if (!system) return "正在加载本地控制器";
  const appVersion = `Mihomo Fleet v${system.appVersion || "dev"}`;
  return system.mihomoFound
    ? `${appVersion} · 控制器 127.0.0.1:${system.port} · mihomo ${shortMihomoVersion(system.version) || "已检测到"}`
    : `${appVersion} · 控制器 127.0.0.1:${system.port} · 未找到 mihomo`;
});

// Mirrors renderViewNavigation()'s #manageProfilesBtn/#instanceSelectorWrap
// half (app.ts:408-411, 416). #showDashboardBtn's onDashboard-only styling is
// the sidebar's concern, so it is intentionally not reproduced here.
const managingProfiles = computed(() => store.view === "profiles");
const selectorMuted = computed(() => managingProfiles.value || store.view === "dashboard");
const manageProfilesLabel = computed(() => (managingProfiles.value ? "返回实例" : "配置档管理"));

// Mirrors renderSelector()'s `selected` argument, which render() computes as
// active() = activeInstance(state) (app.ts:139-141, 395-398): the instance
// matching state.activeId, falling back to the first instance.
const selectedInstance = computed(() => activeInstance(store));

// Mirrors the click handler app.ts wires onto #manageProfilesBtn (app.ts:1612-1615).
function toggleProfileManager(): void {
  if (store.view === "profiles") actions.closeProfileManager();
  else actions.openProfileManager();
}

// Mirrors the change handler app.ts wires onto #instanceSelect (app.ts:1611),
// which calls selectInstance(id) -- a function that does far more than set a
// field (resets editors, refetches), so this goes through the bridge instead
// of a v-model on store.activeId directly.
//
// The cast is needed because bridge.ts types every `actions` entry from the
// shared zero-arg `noop`, so `actions.selectInstance` is statically
// `() => void` even though the real handler app.ts registers requires the
// new instance id. bridge.ts should give selectInstance its own
// `(id: string) => void` signature; see this component's migration report.
function onInstanceChange(event: Event): void {
  const value = (event.target as HTMLSelectElement).value;
  (actions.selectInstance as (id: string) => void)(value);
}
</script>

<template>
  <div class="brand">
    <div class="mark" aria-hidden="true">MF</div>
    <div class="brand-copy">
      <h1>Mihomo Fleet</h1>
      <p>{{ systemLineText }}</p>
    </div>
  </div>
  <div class="topbar-tools">
    <button
      id="manageProfilesBtn"
      type="button"
      :class="{ active: managingProfiles }"
      :disabled="chrome.profileBusy"
      @click="toggleProfileManager"
    >{{ manageProfilesLabel }}</button>
    <label class="selector" :class="{ 'muted-control': selectorMuted }">
      <span>当前实例</span>
      <select aria-label="当前实例" :value="selectedInstance?.id ?? ''" @change="onInstanceChange">
        <option v-if="!store.instances.length" value="">暂无实例</option>
        <option v-for="item in store.instances" :key="item.id" :value="item.id">{{ item.name }}（{{ statusText(item.status) }}）</option>
      </select>
    </label>
  </div>
</template>
