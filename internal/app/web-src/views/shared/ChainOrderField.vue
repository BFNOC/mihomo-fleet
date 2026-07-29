<script setup lang="ts">
// 链路顺序: the global-chain `chain` array as an ordered picker instead of a
// free-text box. Members can only come from the candidate list the backend
// computed, so "chain references unknown proxy or group" is no longer reachable
// by typing -- the remaining refusals (duplicates, an emptied 节点选择) are
// reproduced by chainProblem() and shown in place.
//
// modelValue is the array itself, not text: chainFromText()/chainToText() only
// existed to squeeze it through a <textarea>.
import { computed } from "vue";

import {
  addChainMember,
  candidateKindLabel,
  chainProblem,
  defaultChain,
  moveChainMember,
  removeChainMember,
  unusedCandidates,
} from "../../chain-rules.ts";
import type { ChainCandidate } from "../../chain-rules.ts";
import type { ChainCandidatesState } from "./use-chain-candidates.ts";

const props = defineProps<{ modelValue: string[]; candidates: ChainCandidatesState }>();
const emit = defineEmits<{ "update:modelValue": [value: string[]]; dirty: [] }>();

const chain = computed(() => props.modelValue || []);
const available = computed(() => unusedCandidates(props.candidates.candidates, chain.value));
const problem = computed(() =>
  chainProblem(chain.value, props.candidates.candidates, props.candidates.providerNames));
const fallback = computed(() =>
  defaultChain(props.candidates.candidates, props.candidates.providerNames).join(" → "));

function commit(values: string[]): void {
  emit("update:modelValue", values);
  emit("dirty");
}

function kindOf(name: string): string {
  const candidate: ChainCandidate | undefined = props.candidates.candidates
    .find((entry) => entry.name === name);
  return candidate ? candidateKindLabel(candidate.kind) : "未知";
}

function onAdd(event: Event): void {
  const select = event.target as HTMLSelectElement;
  const name = select.value;
  select.value = "";
  if (name) commit(addChainMember(chain.value, name));
}
</script>

<template>
  <div class="picker-field">
    <ol v-if="chain.length" class="chain-list">
      <li v-for="(name, index) in chain" :key="`${name}-${index}`" class="chain-row">
        <span class="chain-index">{{ index + 1 }}</span>
        <span class="chain-name">{{ name }}</span>
        <span class="chain-kind">{{ kindOf(name) }}</span>
        <span class="chain-move">
          <button type="button" :disabled="index === 0" aria-label="上移" @click="commit(moveChainMember(chain, index, -1))">↑</button>
          <button type="button" :disabled="index === chain.length - 1" aria-label="下移" @click="commit(moveChainMember(chain, index, 1))">↓</button>
          <button type="button" class="chip-remove" aria-label="移除" @click="commit(removeChainMember(chain, index))">×</button>
        </span>
      </li>
    </ol>
    <p v-else class="field-note">未设置，将使用默认链路：{{ fallback }}</p>

    <div class="field-inline">
      <select aria-label="添加链路节点" :disabled="!available.length" @change="onAdd">
        <option value="">{{ available.length ? "添加节点…" : "没有可添加的节点" }}</option>
        <option v-for="candidate in available" :key="candidate.name" :value="candidate.name">
          {{ candidate.name }}（{{ candidateKindLabel(candidate.kind) }}）
        </option>
      </select>
    </div>

    <p v-if="candidates.loading" class="field-note">正在读取可用节点…</p>
    <p v-if="problem" class="field-note error">{{ problem }}</p>
    <p v-if="candidates.localError" class="field-note error">{{ candidates.localError }}</p>
    <p v-if="candidates.error" class="field-note error">{{ candidates.error }}</p>
    <p v-if="candidates.truncated" class="field-note">节点过多，仅列出前 5000 个。</p>
  </div>
</template>
