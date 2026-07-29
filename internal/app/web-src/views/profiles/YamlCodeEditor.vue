<script setup lang="ts">
// The profiles view's full-height YAML editor: a Vue-managed host node and a
// lifecycle for yaml-editor.ts's imperative createYamlEditor()/YamlEditorHandle
// API (CodeMirror 6). Deliberately NOT a rewrite of that API.
//
// The lifecycle itself -- lazy chunk load, pre-load call buffering, teardown --
// lives in ../shared/use-yaml-editor.ts, shared with the instance form's inline
// editor. Its header documents the constraints that make this component's shape
// non-negotiable; the two that bind *this* file:
//   - The host <div id="configEditor"> has no reactive children and no v-if/v-for
//     anywhere in its own template or its ancestor chain (see ProfileManagerView.vue:
//     the panel root and every wrapper between it and this component are
//     always-mounted, `.hidden`-class-toggled, never v-if'd).
//   - This component stays a *synchronous* direct child of ProfileManagerView's
//     template. Wrapping it in defineAsyncComponent would make `editorRef.value`
//     null until the chunk landed, turning all ~12 of that view's
//     `editorRef.value?.foo()` call sites into silent no-ops.
import { ref } from "vue";

import { useYamlEditor } from "../shared/use-yaml-editor.ts";

const props = defineProps<{
  /**
   * Whether the profiles view is the one on screen. The CodeMirror chunk is
   * fetched the first time this goes true and never again -- mounting is not
   * the trigger, because main.ts mounts this component's whole tree at boot
   * (visibility is a CSS class, not v-if) and a mount-triggered import would
   * pull 512KB on every page load for a view most sessions never open.
   */
  active: boolean;
}>();

const emit = defineEmits<{
  /** Fires after a user-driven (non-programmatic) edit. Mirrors YamlEditorOptions.onChange. */
  change: [value: string, version: number];
  /** Fires on Mod-S while the editor has focus. Mirrors YamlEditorOptions.onSave. */
  save: [];
  /** Fires once CodeMirror has loaded and the host is a live editor. */
  ready: [];
  /** Fires if the CodeMirror chunk fails to load. The editor stays unusable. */
  loadError: [message: string];
}>();

const hostEl = ref<HTMLDivElement | null>(null);

const editor = useYamlEditor({
  host: () => hostEl.value,
  active: () => props.active,
  ariaLabel: "配置档 YAML 编辑器",
  onChange: (value, version) => emit("change", value, version),
  onSave: () => emit("save"),
  onReady: () => emit("ready"),
  onLoadError: (message) => emit("loadError", message),
});

const { getValue, setValue, setReadOnly, focusSearch, focus, getVersion } = editor;
defineExpose({ getValue, setValue, setReadOnly, focusSearch, focus, getVersion });
</script>

<template>
  <div id="configEditor" ref="hostEl" class="code" aria-labelledby="configEditorLabel"></div>
</template>
