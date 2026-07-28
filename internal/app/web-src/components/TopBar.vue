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
import { computed, nextTick } from "vue";
import { store } from "../store.ts";
import { actions, chrome } from "../bridge.ts";
import { activeInstance } from "../state.ts";
import { shortMihomoVersion } from "../format.ts";
import { statusText } from "../messages.ts";

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
// The re-sync after nextTick() is not redundant. selectInstance() can decline
// the switch -- it puts up a "discard changes?" confirm and bails if the user
// cancels -- leaving store.activeId untouched. The browser has already moved
// the native <select> to the clicked option by then, but from Vue's side
// :value never changed, so the vdom sees no diff and writes nothing: the
// control would keep displaying an instance that is not actually selected.
// Writing the element's value back from the store after the render settles
// covers the declined case, and is a no-op when the switch went through.
async function onInstanceChange(event: Event): Promise<void> {
  const select = event.target as HTMLSelectElement;
  actions.selectInstance(select.value);
  await nextTick();
  select.value = selectedInstance.value?.id ?? "";
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
