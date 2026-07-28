<script setup lang="ts">
// Thin wrapper around yaml-editor.ts's imperative createYamlEditor()/
// YamlEditorHandle API (CodeMirror 6). Deliberately NOT a rewrite: this
// component's only job is to give the existing handle a Vue-managed host
// node and a lifecycle, then get out of the way.
//
// CONSTRAINTS THIS FILE MUST NOT VIOLATE:
//   - The EditorView is constructed in onMounted, against a real host node
//     obtained via a template ref -- never at module scope, never during
//     setup(). See createYamlEditor()'s own module-scope call in app.ts
//     (being removed by the integrator) for the anti-pattern this replaces.
//   - The host <div id="configEditor"> has no reactive children and no
//     v-if/v-for anywhere in its own template or its ancestor chain (see
//     ProfileManagerView.vue: the panel root and every wrapper between it
//     and this component are always-mounted, `.hidden`-class-toggled, never
//     v-if'd). CodeMirror injects its own DOM under this node at runtime;
//     Vue must never re-render into it or it will fight CodeMirror for
//     ownership of that subtree.
//   - destroy() is called on unmount. YamlEditorHandle.destroy() is a real
//     teardown (EditorView.destroy()), not invented -- see yaml-editor.ts.
//     In normal operation this component's parent never unmounts (mounted
//     once by main.ts, visibility toggled via CSS, matching TopBar/SideBar),
//     so this fires only on a full app teardown, if ever. It is still wired
//     for correctness/hygiene rather than left as a documented leak.
import { onMounted, onUnmounted, ref } from "vue";
import { createYamlEditor } from "../../yaml-editor.ts";
import type { YamlEditorHandle } from "../../yaml-editor.ts";

const emit = defineEmits<{
  /** Fires after a user-driven (non-programmatic) edit. Mirrors YamlEditorOptions.onChange. */
  change: [value: string, version: number];
  /** Fires on Mod-S while the editor has focus. Mirrors YamlEditorOptions.onSave. */
  save: [];
}>();

const hostEl = ref<HTMLDivElement | null>(null);
let handle: YamlEditorHandle | null = null;

onMounted(() => {
  if (!hostEl.value) return;
  handle = createYamlEditor(hostEl.value, {
    ariaLabel: "配置档 YAML 编辑器",
    onChange(value, version) {
      emit("change", value, version);
    },
    onSave() {
      emit("save");
    },
  });
});

onUnmounted(() => {
  handle?.destroy();
  handle = null;
});

function getValue(): string {
  return handle ? handle.getValue() : "";
}

function setValue(value: string): void {
  handle?.setValue(value);
}

function setReadOnly(readOnly: boolean): void {
  handle?.setReadOnly(readOnly);
}

function focusSearch(): void {
  handle?.focusSearch();
}

function focus(): void {
  handle?.focus();
}

function getVersion(): number {
  return handle ? handle.getVersion() : 0;
}

defineExpose({ getValue, setValue, setReadOnly, focusSearch, focus, getVersion });
</script>

<template>
  <div id="configEditor" ref="hostEl" class="code" aria-labelledby="configEditorLabel"></div>
</template>
