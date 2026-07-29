<script setup lang="ts">
// 代理绑定地址: the comma-joined proxyBind field as a picker over the host's own
// interface addresses, plus manual entry for what enumeration cannot offer
// (an address on an interface that is currently down, an IPv6 zone spelling).
//
// The field's wire format does not change -- it is still one comma-joined string
// on the instance payload -- so the backend and the save path see exactly what
// the plain <input> used to send.
import { computed, ref } from "vue";

import { bindAddressState, loadBindAddresses } from "../../services/bind-addresses.ts";
import {
  bindKindLabel,
  bindListIncludes,
  bindListPreview,
  joinBindList,
  splitBindList,
  toggleBindAddress,
  validateBindAddress,
  wildcardBindAddress,
} from "../../proxy-bind.ts";
import { defaultProxyBind } from "../../constants.ts";

const props = defineProps<{ modelValue: string; inputId: string }>();
const emit = defineEmits<{ "update:modelValue": [value: string]; dirty: [] }>();

const manual = ref("");
const manualError = ref("");
const pickerOpen = ref(false);

const selected = computed(() => splitBindList(props.modelValue));
const preview = computed(() => bindListPreview(selected.value));
const exposesLan = computed(() => preview.value.some((entry) => entry === wildcardBindAddress));

function commit(values: string[]): void {
  // An empty list is the backend's own default (instanceProxyBind), but sending
  // "" would make the saved value depend on that fallback rather than on what the
  // field shows. Spell it out instead.
  emit("update:modelValue", values.length ? joinBindList(values) : defaultProxyBind);
  emit("dirty");
}

function togglePicker(): void {
  pickerOpen.value = !pickerOpen.value;
  if (pickerOpen.value) void loadBindAddresses();
}

function toggleAddress(address: string): void {
  commit(toggleBindAddress(selected.value, address));
}

function removeAt(index: number): void {
  commit(selected.value.filter((_, position) => position !== index));
}

function addManual(): void {
  const value = manual.value.trim();
  const problem = validateBindAddress(value);
  if (problem) {
    manualError.value = problem;
    return;
  }
  if (bindListIncludes(selected.value, value)) {
    manualError.value = `${value} 已在列表中。`;
    return;
  }
  manualError.value = "";
  manual.value = "";
  commit([...selected.value, value]);
}

function isSelected(address: string): boolean {
  return bindListIncludes(selected.value, address);
}
</script>

<template>
  <div class="picker-field">
    <div class="field-chips" role="list">
      <span v-for="(address, index) in selected" :key="`${address}-${index}`" class="field-chip" role="listitem">
        <span class="chip-text">{{ address }}</span>
        <button
          type="button"
          class="chip-remove"
          :aria-label="`移除 ${address}`"
          :disabled="selected.length < 2"
          :title="selected.length < 2 ? '至少保留一个绑定地址' : '移除'"
          @click="removeAt(index)"
        >×</button>
      </span>
      <button type="button" class="chip-add" :aria-expanded="pickerOpen" @click="togglePicker">
        {{ pickerOpen ? "收起网卡" : "选择网卡" }}
      </button>
    </div>

    <div v-if="pickerOpen" class="picker-list">
      <div class="picker-head">
        <span class="field-note">本机地址</span>
        <button type="button" class="link-button" :disabled="bindAddressState.loading" @click="loadBindAddresses(true)">
          {{ bindAddressState.loading ? "读取中" : "刷新" }}
        </button>
      </div>
      <label v-for="option in bindAddressState.addresses" :key="option.address" class="picker-row">
        <input type="checkbox" :checked="isSelected(option.address)" @change="toggleAddress(option.address)">
        <span class="picker-address">{{ option.address }}</span>
        <span class="picker-kind">{{ bindKindLabel(option.kind) }}</span>
        <span v-if="option.interface" class="picker-iface">{{ option.interface }}</span>
      </label>
      <p v-if="bindAddressState.error" class="field-note error">{{ bindAddressState.error }}</p>
      <p v-else-if="!bindAddressState.addresses.length && !bindAddressState.loading" class="field-note">
        没有读到本机网卡，请手动填写。
      </p>
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

    <p class="field-note">将监听：{{ preview.join("、") }}</p>
    <p v-if="exposesLan" class="field-note warn">所有网卡：同网段的其他设备也能使用这个代理。</p>
  </div>
</template>
