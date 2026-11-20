<script setup lang="ts">
// 端口映射: extra inbound ports, each pinned to one proxy or group. Rows are
// typed by hand; the backend re-checks everything when the form is saved.
import { computed } from "vue";

import { portMappingProblem } from "../../port-mappings.ts";
import type { PortMappingRow } from "../../port-mappings.ts";

const props = defineProps<{
  modelValue: PortMappingRow[];
  options: string[];
  mixedPort: number;
  controllerPort: number;
  /** Whether the generated listeners carry UDP; shown so the user is not surprised. */
  udp: boolean;
  listId: string;
  /** Global-chain mode with the chain left empty: local exits may get chained (see the note). */
  chainDefault?: boolean;
}>();
const emit = defineEmits<{ "update:modelValue": [value: PortMappingRow[]]; dirty: [] }>();

const rows = computed(() => props.modelValue || []);
const problem = computed(() => portMappingProblem(rows.value, props.mixedPort, props.controllerPort));

function commit(next: PortMappingRow[]): void {
  emit("update:modelValue", next);
  emit("dirty");
}

function update(index: number, key: keyof PortMappingRow, value: string): void {
  commit(rows.value.map((row, i) => (i === index ? { ...row, [key]: value } : row)));
}

function nextPort(): string {
  const used = rows.value.map((row) => Number(row.port)).filter((port) => port > 0 && port < 65535);
  return used.length ? String(Math.max(...used) + 1) : "";
}
</script>

<template>
  <div class="picker-field">
    <ol v-if="rows.length" class="chain-list">
      <li v-for="(row, index) in rows" :key="index" class="port-mapping-row">
        <input
          type="number" min="1" max="65535" :value="row.port" aria-label="映射端口" placeholder="端口"
          @input="update(index, 'port', ($event.target as HTMLInputElement).value)"
        >
        <span class="chain-index">→</span>
        <input
          :value="row.proxy" :list="listId" aria-label="目标节点或代理组" placeholder="目标节点或代理组"
          @input="update(index, 'proxy', ($event.target as HTMLInputElement).value)"
        >
        <button type="button" class="chip-remove" aria-label="移除" @click="commit(rows.filter((_, i) => i !== index))">×</button>
      </li>
    </ol>
    <p v-else class="field-note">未设置。每条映射多开一个混合端口，流量不经规则，直接走指定的节点或代理组。</p>
    <datalist :id="listId">
      <option v-for="name in options" :key="name" :value="name" />
    </datalist>

    <div class="field-inline">
      <button type="button" @click="commit([...rows, { port: nextPort(), proxy: '' }])">添加映射</button>
    </div>

    <p v-if="rows.length && chainDefault" class="field-note warn">
      链路顺序留空时，如果配置档本身也有节点，本地节点会被依次串成一条链，映射到后面的本地节点要先经过前面的。各出口要独立，请把链路顺序设为只有「节点选择」。
    </p>
    <p v-if="problem" class="field-note error">{{ problem }}</p>
    <p v-if="rows.length" class="field-note">
      监听地址同代理绑定地址；UDP {{ udp ? "开启" : "关闭（全局链式下跟随“UDP 也走链路”）" }}。mihomo 按客户端源地址复用 UDP 会话：需要按端口区分出口的 UDP 程序，每个入口（含主混合端口）要用不同的本地源端口，否则可能沿用上一个入口的出口。修改映射需先停止实例。
    </p>
  </div>
</template>
