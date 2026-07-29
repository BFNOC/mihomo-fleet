import { onMounted, onUnmounted, watch } from "vue";

import type { YamlEditorHandle } from "../../yaml-editor.ts";

/*
 * Lifecycle and pre-load buffering for a CodeMirror YAML editor. Extracted from
 * YamlCodeEditor.vue when a second editor (the instance form's 本地节点 YAML box)
 * needed the identical dance; both components now share this, so a fix to the
 * buffering cannot land in only one of them.
 *
 * The constraints it exists to satisfy, unchanged from that component:
 *   - The EditorView is constructed in onMounted against a real host node, never
 *     at module scope and never during setup().
 *   - The host must have no reactive children and no v-if/v-for on its ancestor
 *     chain: CodeMirror owns that subtree at runtime and Vue re-rendering into it
 *     would fight for ownership.
 *   - The ~580KB CodeMirror chunk is loaded through a dynamic import, the first
 *     time the editor is actually going to be seen -- not on mount, because the
 *     views that host these editors are mounted eagerly at boot and toggled with
 *     a class.
 *   - The split is here rather than at the component boundary
 *     (defineAsyncComponent) so callers' `editorRef.value?.foo()` calls stay live
 *     instead of turning into silent no-ops while the chunk is in flight -- which
 *     is what the buffering below is for.
 */

export interface YamlEditorController {
  getValue(): string;
  setValue(value: string): void;
  setReadOnly(readOnly: boolean): void;
  focusSearch(): void;
  focus(): void;
  getVersion(): number;
  requestMeasure(): void;
}

export interface YamlEditorOptions {
  /** The host element. Read lazily because a template ref is null during setup. */
  host: () => HTMLElement | null;
  /** True once the editor is on screen. Flipping this true is what triggers the chunk load. */
  active: () => boolean;
  ariaLabel: string;
  onChange?: (value: string, version: number) => void;
  onSave?: () => void;
  onReady?: () => void;
  onLoadError?: (message: string) => void;
}

export function useYamlEditor(options: YamlEditorOptions): YamlEditorController {
  let handle: YamlEditorHandle | null = null;
  let unmounted = false;
  let loadStarted = false;

  // Everything a caller asks for before CodeMirror lands. The two pending* flags
  // are one-shot intents, not state: replaying focus after the fact is only right
  // if nothing has since happened, and the only thing that can happen in that
  // window is another call to the same method.
  let pendingValue = "";
  let pendingReadOnly = false;
  let pendingFocusSearch = false;
  let pendingFocus = false;

  async function load(): Promise<void> {
    let createYamlEditor: typeof import("../../yaml-editor.ts").createYamlEditor;
    try {
      ({ createYamlEditor } = await import("../../yaml-editor.ts"));
    } catch (err) {
      if (unmounted) return;
      console.error("Failed to load the YAML editor.", err);
      options.onLoadError?.(err instanceof Error ? err.message : String(err));
      return;
    }
    // Both guards matter: the await above yields, so the component can be torn
    // down mid-flight, and constructing an EditorView against a detached (or
    // absent) host would leak a live editor nothing can ever destroy.
    const host = options.host();
    if (unmounted || !host) return;
    handle = createYamlEditor(host, {
      // Seeded through the constructor rather than set afterwards so the editor
      // never renders an empty document for a frame, and so version stays 0.
      value: pendingValue,
      ariaLabel: options.ariaLabel,
      onChange(value, version) {
        options.onChange?.(value, version);
      },
      onSave() {
        options.onSave?.();
      },
    });
    handle.setReadOnly(pendingReadOnly);
    if (pendingFocusSearch) handle.focusSearch();
    else if (pendingFocus) handle.focus();
    pendingFocusSearch = false;
    pendingFocus = false;
    options.onReady?.();
  }

  function loadIfActive(): void {
    if (!options.active()) return;
    if (loadStarted) {
      // Already built, and just came back on screen: anything CodeMirror measured
      // while the container was display:none is stale.
      handle?.requestMeasure();
      return;
    }
    loadStarted = true;
    void load();
  }

  // Two triggers, one load. onMounted covers booting straight into a view that
  // shows the editor; the watcher covers navigating in later. Neither can run
  // before the host node exists, which matters because a warm module cache makes
  // import() resolve in a microtask -- too fast for a setup-time trigger to have a
  // host to build on.
  onMounted(loadIfActive);
  watch(options.active, loadIfActive);

  onUnmounted(() => {
    unmounted = true;
    handle?.destroy();
    handle = null;
  });

  return {
    getValue() {
      return handle ? handle.getValue() : pendingValue;
    },
    setValue(value: string) {
      if (handle) handle.setValue(value);
      else pendingValue = String(value || "");
    },
    setReadOnly(readOnly: boolean) {
      if (handle) handle.setReadOnly(readOnly);
      else pendingReadOnly = readOnly;
    },
    focusSearch() {
      if (handle) handle.focusSearch();
      else pendingFocusSearch = true;
    },
    focus() {
      if (handle) handle.focus();
      else pendingFocus = true;
    },
    // 0 while pending is not a placeholder, it is the true answer: the counter only
    // advances on user edits, and there is no editor to type into yet. It also
    // matches where a freshly constructed handle starts, so a caller's
    // save-race guard stays correct across the moment the chunk lands.
    getVersion() {
      return handle ? handle.getVersion() : 0;
    },
    requestMeasure() {
      handle?.requestMeasure();
    },
  };
}
