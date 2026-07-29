<script setup lang="ts">
// A short, v-model'd CodeMirror YAML editor for use inside a form. Same engine and
// same lazy-load rules as the profiles view's YamlCodeEditor -- see
// ./use-yaml-editor.ts for the constraints both obey -- differing only in that this
// one is bound by value instead of driven imperatively, because a form field is
// what it is bound to.
//
// Visibility is detected with an IntersectionObserver rather than a prop. The hosts
// of this editor are hidden by an ancestor's `.hidden` class (the chain fields, the
// tab panel, the whole detail panel), and threading all three conditions down as
// props would silently rot the first time another one is added. The observer also
// answers the question the editor actually has -- "am I on screen" -- which is what
// decides both when to fetch the chunk and when CodeMirror's cached geometry has
// gone stale.
import { onUnmounted, ref, watch } from "vue";

import { useYamlEditor } from "./use-yaml-editor.ts";

const props = defineProps<{ modelValue: string; editorLabel: string; hostId: string }>();
const emit = defineEmits<{ "update:modelValue": [value: string]; dirty: [] }>();

const hostEl = ref<HTMLDivElement | null>(null);
const visible = ref(false);
const loadError = ref("");

const editor = useYamlEditor({
  host: () => hostEl.value,
  active: () => visible.value,
  ariaLabel: props.editorLabel,
  onChange: (value) => {
    emit("update:modelValue", value);
    emit("dirty");
  },
  onLoadError: (message) => {
    loadError.value = message;
  },
});

editor.setValue(props.modelValue);

// Only push down what the editor does not already hold. setValue() is a no-op on
// identical text, but comparing first also keeps a parent's repopulate from
// disturbing the cursor mid-edit when the text has not actually changed.
watch(
  () => props.modelValue,
  (value) => {
    if (editor.getValue() !== value) editor.setValue(value);
  },
);

let observer: IntersectionObserver | null = null;

watch(hostEl, (host) => {
  observer?.disconnect();
  observer = null;
  if (!host) return;
  observer = new IntersectionObserver((entries) => {
    visible.value = entries.some((entry) => entry.isIntersecting);
  });
  observer.observe(host);
}, { immediate: true });

onUnmounted(() => {
  observer?.disconnect();
  observer = null;
});

defineExpose({ focus: editor.focus, getValue: editor.getValue });
</script>

<template>
  <div class="inline-editor">
    <div :id="hostId" ref="hostEl" class="code code-inline"></div>
    <p v-if="loadError" class="field-note error">编辑器加载失败：{{ loadError }}</p>
  </div>
</template>
