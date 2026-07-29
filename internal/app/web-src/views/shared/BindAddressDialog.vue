<script setup lang="ts">
// The 代理绑定地址 picker as a native <dialog>. Modal because the field sits in
// a narrow form column where the old inline list forced IPv6 addresses to wrap
// mid-token; the top layer gives every address a full-width single line and
// leaves the form's geometry alone. <dialog> comes with backdrop, Esc handling
// and focus containment for free, so there is no overlay or trap code here.
//
// The parent owns the selection (commit/toggle); this component only renders
// and validates. Mounted-while-open pattern: the parent v-ifs this component,
// onMounted calls showModal(), and every close path (Esc, ×, backdrop, 完成)
// funnels through the native "close" event into the close emit.
import { computed, onMounted, reactive, ref } from "vue";

import { bindAddressState, loadBindAddresses } from "../../services/bind-addresses.ts";
import {
  bindKindLabel,
  bindListIncludes,
  groupBindAddresses,
  validateBindAddress,
  wildcardBindAddress,
} from "../../proxy-bind.ts";
import type { BindAddressGroup, BindAddressOption } from "../../proxy-bind.ts";

const props = defineProps<{ selected: string[]; inputId: string }>();
const emit = defineEmits<{ toggle: [address: string]; add: [address: string]; close: [] }>();

const dialogEl = ref<HTMLDialogElement | null>(null);
const manual = ref("");
const manualError = ref("");
const showLinkLocal = ref(false);
const expandedV6 = reactive(new Set<string>());

onMounted(() => {
  dialogEl.value?.showModal();
  void loadBindAddresses();
});

const groups = computed(() =>
  groupBindAddresses(bindAddressState.addresses, props.selected),
);
const wildcard = computed(() =>
  bindAddressState.addresses.find((option) => option.kind === "wildcard"),
);
const linkLocalCount = computed(() =>
  groups.value.reduce((sum, group) => sum + group.linkLocal.length, 0),
);
// A group that only carries link-local addresses has nothing to show while
// they are hidden; dropping it beats rendering a header over empty space.
const visibleGroups = computed(() =>
  groups.value.filter(
    (group) => group.primary.length || group.moreV6.length || (showLinkLocal.value && group.linkLocal.length),
  ),
);

function rows(group: BindAddressGroup): BindAddressOption[] {
  const list = [...group.primary];
  if (expandedV6.has(group.iface)) list.push(...group.moreV6);
  if (showLinkLocal.value) list.push(...group.linkLocal);
  return list;
}

function isSelected(address: string): boolean {
  return bindListIncludes(props.selected, address);
}

function addManual(): void {
  const value = manual.value.trim();
  const problem = validateBindAddress(value);
  if (problem) {
    manualError.value = problem;
    return;
  }
  if (bindListIncludes(props.selected, value)) {
    manualError.value = `${value} 已在列表中。`;
    return;
  }
  manualError.value = "";
  manual.value = "";
  emit("add", value);
}

function onDialogClick(event: MouseEvent): void {
  // Content fills the dialog box, so the dialog node itself is only hit when
  // the click lands on the backdrop.
  if (event.target === dialogEl.value) dialogEl.value?.close();
}
</script>

<template>
  <dialog
    ref="dialogEl"
    class="field-dialog"
    aria-label="选择代理绑定地址"
    @close="emit('close')"
    @click="onDialogClick"
  >
    <form method="dialog" class="field-dialog-frame">
      <header class="field-dialog-head">
        <span class="field-dialog-title">选择绑定地址</span>
        <button
          type="button"
          class="link-button"
          :disabled="bindAddressState.loading"
          @click="loadBindAddresses(true)"
        >{{ bindAddressState.loading ? "读取中" : "刷新" }}</button>
      </header>

      <div class="field-dialog-scroll">
        <label v-if="wildcard" class="bind-row bind-wildcard">
          <input
            type="checkbox"
            :checked="isSelected(wildcard.address)"
            @change="emit('toggle', wildcard.address)"
          >
          <span class="bind-address">{{ wildcard.address }}</span>
          <span class="bind-kind">{{ bindKindLabel(wildcard.kind) }}</span>
          <span class="bind-hint">同网段其他设备也能连接</span>
        </label>

        <section v-for="group in visibleGroups" :key="group.iface" class="bind-group">
          <div class="bind-group-head">
            <span class="bind-iface">{{ group.iface }}</span>
            <span class="bind-kind">{{ bindKindLabel(group.kind) }}</span>
          </div>
          <label v-for="option in rows(group)" :key="option.address" class="bind-row">
            <input
              type="checkbox"
              :checked="isSelected(option.address)"
              @change="emit('toggle', option.address)"
            >
            <span class="bind-address">{{ option.address }}</span>
            <span v-if="option.kind !== group.kind" class="bind-kind">{{ bindKindLabel(option.kind) }}</span>
          </label>
          <button
            v-if="group.moreV6.length && !expandedV6.has(group.iface)"
            type="button"
            class="link-button bind-more"
            @click="expandedV6.add(group.iface)"
          >还有 {{ group.moreV6.length }} 个 IPv6</button>
        </section>

        <p v-if="bindAddressState.error" class="field-note error">{{ bindAddressState.error }}</p>
        <p v-else-if="!bindAddressState.addresses.length && !bindAddressState.loading" class="field-note">
          没有读到本机网卡，请手动填写。
        </p>

        <button
          v-if="linkLocalCount"
          type="button"
          class="link-button bind-more"
          @click="showLinkLocal = !showLinkLocal"
        >{{ showLinkLocal ? "收起链路本地地址" : `显示链路本地地址（${linkLocalCount}）` }}</button>

        <div class="field-inline">
          <input
            :id="inputId"
            v-model="manual"
            placeholder="手动填写：IP、localhost、all 或 *"
            @keydown.enter.prevent="addManual"
          >
          <button type="button" @click="addManual">添加</button>
        </div>
        <p v-if="manualError" class="field-note error">{{ manualError }}</p>
      </div>

      <footer class="field-dialog-foot">
        <span class="field-note">已选 {{ selected.length }} 个地址</span>
        <button class="bind-done">完成</button>
      </footer>
    </form>
  </dialog>
</template>
