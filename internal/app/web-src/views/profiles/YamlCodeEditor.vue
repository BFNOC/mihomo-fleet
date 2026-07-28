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
//
// WHY THE DYNAMIC import() BELOW: yaml-editor.ts pulls in CodeMirror 6 plus
// the yaml parser, which is ~580KB of the bundle -- and main.ts mounts this
// component's parent eagerly, so a static import put all of it in the entry
// chunk that every page load blocks on, for an editor only the profiles view
// ever shows.
//
// The split is here rather than at the component boundary (defineAsyncComponent
// on this file, in ProfileManagerView.vue) on purpose. That would make
// `editorRef.value` itself null until the chunk landed, and every one of
// ProfileManagerView's ~12 call sites already spells `editorRef.value?.foo()`
// -- so a slow chunk would turn them all into silent no-ops, losing the
// document startNewProfile() had just set. Keeping this component synchronous
// means the ref is always live and the buffering below has somewhere to live.
import { onMounted, onUnmounted, ref, watch } from "vue";
import type { YamlEditorHandle } from "../../yaml-editor.ts";

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
let handle: YamlEditorHandle | null = null;
let unmounted = false;

// Everything the parent asks for before CodeMirror lands. Without this the
// optional-chained calls in ProfileManagerView would evaporate: startNewProfile()
// sets the default config the instant the user clicks 新建配置档, which is well
// inside the window where this chunk is still in flight on a cold load.
//
// The two `pending*` flags are one-shot intents, not state -- replaying focus
// after the fact is only right if nothing has since happened, and the only
// thing that can happen in that window is another call to the same method.
let pendingValue = "";
let pendingReadOnly = false;
let pendingFocusSearch = false;
let pendingFocus = false;

// Two triggers, one load. onMounted covers booting straight into the profiles
// view (possible: showCreate() redirects there when no profile exists yet);
// the watcher covers navigating in later. Neither can run before the host node
// exists, which matters because a warm module cache makes import() resolve in a
// microtask -- too fast for a setup-time trigger to have a host to build on.
let loadStarted = false;

function loadIfActive(): void {
  if (loadStarted || !props.active) return;
  loadStarted = true;
  void load();
}

onMounted(loadIfActive);
watch(() => props.active, loadIfActive);

async function load(): Promise<void> {
  let createYamlEditor: typeof import("../../yaml-editor.ts").createYamlEditor;
  try {
    ({ createYamlEditor } = await import("../../yaml-editor.ts"));
  } catch (err) {
    if (unmounted) return;
    console.error("Failed to load the YAML editor.", err);
    emit("loadError", err instanceof Error ? err.message : String(err));
    return;
  }
  // Both guards matter: the await above yields, so the component can be torn
  // down mid-flight, and constructing an EditorView against a detached (or
  // absent) host would leak a live editor nothing can ever destroy.
  if (unmounted || !hostEl.value) return;
  handle = createYamlEditor(hostEl.value, {
    // Seeded rather than set afterwards so the editor never renders an empty
    // document for a frame, and so version stays 0 -- setValue() would not bump
    // it (suppressChange), but going through the constructor makes that
    // independent of yaml-editor.ts's internals.
    value: pendingValue,
    ariaLabel: "配置档 YAML 编辑器",
    onChange(value, version) {
      emit("change", value, version);
    },
    onSave() {
      emit("save");
    },
  });
  handle.setReadOnly(pendingReadOnly);
  if (pendingFocusSearch) handle.focusSearch();
  else if (pendingFocus) handle.focus();
  pendingFocusSearch = false;
  pendingFocus = false;
  emit("ready");
}

onUnmounted(() => {
  unmounted = true;
  handle?.destroy();
  handle = null;
});

function getValue(): string {
  return handle ? handle.getValue() : pendingValue;
}

function setValue(value: string): void {
  if (handle) handle.setValue(value);
  else pendingValue = String(value || "");
}

function setReadOnly(readOnly: boolean): void {
  if (handle) handle.setReadOnly(readOnly);
  else pendingReadOnly = readOnly;
}

function focusSearch(): void {
  if (handle) handle.focusSearch();
  else pendingFocusSearch = true;
}

function focus(): void {
  if (handle) handle.focus();
  else pendingFocus = true;
}

// 0 while pending is not a placeholder, it is the true answer: the counter only
// advances on user edits, and there is no editor to type into yet. It also
// matches where a freshly constructed handle starts, so the save-race guard in
// ProfileManagerView (savedConfigVersion vs. current) stays correct across the
// moment the chunk lands.
function getVersion(): number {
  return handle ? handle.getVersion() : 0;
}

defineExpose({ getValue, setValue, setReadOnly, focusSearch, focus, getVersion });
</script>

<template>
  <div id="configEditor" ref="hostEl" class="code" aria-labelledby="configEditorLabel"></div>
</template>
