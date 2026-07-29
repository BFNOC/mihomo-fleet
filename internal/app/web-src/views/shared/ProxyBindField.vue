<script setup lang="ts">
// 代理绑定地址: chips for the selected addresses plus a modal picker over the
// host's interface addresses (BindAddressDialog). Manual entry lives in the
// dialog too, for what enumeration cannot offer (an address on an interface
// that is currently down, an IPv6 zone spelling).
//
// The field's wire format does not change -- it is still one comma-joined string
// on the instance payload -- so the backend and the save path see exactly what
// the plain <input> used to send.
import { computed, ref } from "vue";

import BindAddressDialog from "./BindAddressDialog.vue";
import {
  bindListPreview,
  joinBindList,
  splitBindList,
  toggleBindAddress,
  wildcardBindAddress,
} from "../../proxy-bind.ts";
import { defaultProxyBind } from "../../constants.ts";

const props = defineProps<{ modelValue: string; inputId: string }>();
const emit = defineEmits<{ "update:modelValue": [value: string]; dirty: [] }>();

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

function toggleAddress(address: string): void {
  commit(toggleBindAddress(selected.value, address));
}

function addAddress(address: string): void {
  commit([...selected.value, address]);
}

function removeAt(index: number): void {
  commit(selected.value.filter((_, position) => position !== index));
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
      <button type="button" class="chip-add" :aria-expanded="pickerOpen" @click="pickerOpen = true">
        选择网卡
      </button>
    </div>

    <BindAddressDialog
      v-if="pickerOpen"
      :selected="selected"
      :input-id="inputId"
      @toggle="toggleAddress"
      @add="addAddress"
      @close="pickerOpen = false"
    />

    <p class="field-note">将监听：{{ preview.join("、") }}</p>
    <p v-if="exposesLan" class="field-note warn">所有网卡：同网段的其他设备也能使用这个代理。</p>
  </div>
</template>
