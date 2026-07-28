<script setup lang="ts">
// Vue replacement for the inner content of <section id="emptyPanel">
// (index.html:171-176), plus two small pieces of app.ts that only ever
// touch this panel's one button:
//   - renderPanels()'s #emptyCreate text (app.ts:425): "创建第一个实例" once
//     at least one profile exists, "先创建配置档" while there are none. The
//     button opens the same create flow either way -- showCreate() itself
//     redirects to the profile manager with an error message when there are
//     no profiles yet (app.ts:846-851), so this label is purely a hint, not
//     a different action.
//   - updateBulkControls()'s #emptyCreate disabled state (app.ts:405).
//     SideBar.vue's top comment notes this is intentionally not reproduced
//     there, since #emptyCreate belongs to this panel, not the shell.
//
// Unlike CreatePanel.vue, there is no form-reset concern here: this panel
// has no editable field state that could go stale between openings, so it
// stays a single always-mounted component, same as TopBar/SideBar/
// MessageBanner (and, now, CreatePanel.vue too).
import { computed } from "vue";
import { store } from "../../store.ts";
import { actions } from "../../bridge.ts";

const label = computed(() => (store.profiles.length ? "创建第一个实例" : "先创建配置档"));
const disabled = computed(() => store.bulkRunning);
</script>

<template>
  <div class="empty-mark" aria-hidden="true">MF</div>
  <h2>还没有实例</h2>
  <p>创建一个本地 mihomo 实例。Fleet 会分配端口、写入运行配置，并让控制器绑定到本机回环地址。</p>
  <button id="emptyCreate" class="primary" type="button" :disabled="disabled" @click="actions.showCreate()">{{ label }}</button>
</template>
