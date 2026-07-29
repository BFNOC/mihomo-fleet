<script setup lang="ts">
// 本地节点 YAML: the CodeMirror editor, the 添加本地节点 generator, and the readback
// of what the backend actually parsed out of the text.
//
// The readback is not a second parser. It reuses the candidate list the chain
// picker already fetched (kind === "local"), so the names shown here are by
// construction the names the chain may reference and the save will accept -- one
// request, one answer, no chance of the two fields disagreeing about the draft.
//
// The generator form renders *after* the editor host in DOM order. A v-if there is
// a sibling of the host, not an ancestor, so its placeholder comment keeps the
// host element stable across toggles -- which is the property CodeMirror needs.
import { computed, ref } from "vue";

import InlineYamlEditor from "./InlineYamlEditor.vue";
import LocalProxyForm from "./LocalProxyForm.vue";
import { appendLocalProxyYaml } from "../../local-proxy-yaml.ts";
import type { ChainCandidatesState } from "./use-chain-candidates.ts";

const props = defineProps<{
  modelValue: string;
  candidates: ChainCandidatesState;
  hostId: string;
}>();
const emit = defineEmits<{ "update:modelValue": [value: string]; dirty: [] }>();

const formOpen = ref(false);

const localNames = computed(() =>
  props.candidates.candidates.filter((entry) => entry.kind === "local").map((entry) => entry.name));

function onInsert(yaml: string): void {
  emit("update:modelValue", appendLocalProxyYaml(props.modelValue, yaml));
  emit("dirty");
}
</script>

<template>
  <div class="picker-field">
    <div class="field-toolbar">
      <button type="button" class="link-button" aria-haspopup="dialog" @click="formOpen = true">
        ＋ 添加本地节点
      </button>
    </div>

    <InlineYamlEditor
      :model-value="modelValue"
      :host-id="hostId"
      editor-label="本地节点 YAML 编辑器"
      @update:model-value="emit('update:modelValue', $event)"
      @dirty="emit('dirty')"
    />

    <LocalProxyForm
      v-if="formOpen"
      :existing-names="localNames"
      @insert="onInsert"
      @close="formOpen = false"
    />

    <p v-if="candidates.localError" class="field-note error">{{ candidates.localError }}</p>
    <div v-else-if="localNames.length" class="field-chips readback" role="list">
      <span class="field-note">已识别节点</span>
      <span v-for="name in localNames" :key="name" class="field-chip" role="listitem">{{ name }}</span>
    </div>
    <p v-else class="field-note">留空表示不添加本地节点。</p>
  </div>
</template>
