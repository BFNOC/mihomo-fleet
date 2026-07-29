<script setup lang="ts">
// 添加本地节点: a typed form that appends one proxy entry to the 本地节点 YAML box.
//
// It generates text and hands it up; it never becomes the field's source of truth.
// That keeps the editor authoritative (hand-written nodes, comments and anything
// this form has no field for all survive) and means the form only has to be right
// about the node it is adding.
import { computed, ref, watch } from "vue";

import {
  buildLocalProxyYaml,
  localProxyFormDefaults,
  localProxyTypeDef,
  localProxyTypes,
} from "../../local-proxy-yaml.ts";

const props = defineProps<{ existingNames: string[] }>();
const emit = defineEmits<{ insert: [yaml: string]; close: [] }>();

const type = ref(localProxyTypes[0]?.type || "ss");
const values = ref<Record<string, string>>(localProxyFormDefaults(type.value));
const error = ref("");

const fields = computed(() => localProxyTypeDef(type.value)?.fields || []);

// Switching protocol keeps what every protocol has in common -- retyping the
// server and port because trojan turned out to be the right guess is pure friction.
watch(type, (next) => {
  const carried = { ...values.value };
  const fresh = localProxyFormDefaults(next);
  for (const key of Object.keys(fresh)) {
    if (carried[key]) fresh[key] = carried[key] as string;
  }
  values.value = fresh;
  error.value = "";
});

function submit(): void {
  const result = buildLocalProxyYaml(type.value, values.value, props.existingNames);
  if (result.error) {
    error.value = result.error;
    return;
  }
  error.value = "";
  emit("insert", result.yaml);
  values.value = localProxyFormDefaults(type.value);
}
</script>

<template>
  <div class="node-form">
    <div class="node-form-grid">
      <label>
        <span>类型</span>
        <select v-model="type">
          <option v-for="def in localProxyTypes" :key="def.type" :value="def.type">{{ def.label }}</option>
        </select>
      </label>
      <label v-for="field in fields" :key="field.key">
        <span>{{ field.label }}</span>
        <select v-if="field.kind === 'select'" v-model="values[field.key]">
          <option v-for="option in field.options" :key="option" :value="option">{{ option || "（不设置）" }}</option>
        </select>
        <input
          v-else
          v-model="values[field.key]"
          :type="field.kind === 'password' ? 'password' : field.kind === 'number' ? 'number' : 'text'"
          :placeholder="field.placeholder || ''"
          spellcheck="false"
          @keydown.enter.prevent="submit"
        >
      </label>
    </div>
    <p v-if="error" class="field-note error">{{ error }}</p>
    <div class="node-form-actions">
      <button type="button" class="primary" @click="submit">插入 YAML</button>
      <button type="button" @click="emit('close')">收起</button>
    </div>
  </div>
</template>
