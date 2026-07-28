import { basicSetup } from "codemirror";
import { Compartment, EditorState } from "@codemirror/state";
import { linter, lintGutter } from "@codemirror/lint";
import type { Diagnostic } from "@codemirror/lint";
import { yaml as yamlLanguage } from "@codemirror/lang-yaml";
import { openSearchPanel } from "@codemirror/search";
import { EditorView, keymap } from "@codemirror/view";
import { parseDocument } from "yaml";

export * from "./app-logic.ts";

function yamlDiagnostics(view: EditorView): Diagnostic[] {
  const text = view.state.doc.toString();
  // `document` here is a yaml.Document.Parsed (parseDocument()'s return value),
  // not the global DOM `document`. It only shadows the global within this
  // function body.
  const document = parseDocument(text, { prettyErrors: false, strict: true });
  const length = text.length;
  return [...document.errors, ...document.warnings].map((problem): Diagnostic => {
    const positions = Array.isArray(problem.pos) ? problem.pos : [0, 0];
    const from = Math.max(0, Math.min(length, Number(positions[0]) || 0));
    const to = Math.max(from, Math.min(length, Number(positions[1]) || from));
    return {
      from,
      to,
      severity: document.errors.includes(problem) ? "error" : "warning",
      message: String(problem.message || problem),
    };
  });
}

/** Options accepted by {@link createYamlEditor}. */
export interface YamlEditorOptions {
  /** Initial document text. Defaults to an empty document. */
  value?: string;
  /** Accessible label applied to the editor's content element. */
  ariaLabel?: string;
  /** Called after a user-driven (non-programmatic) edit, with the full text and a monotonically increasing version counter. */
  onChange?: (value: string, version: number) => void;
  /** Called when the user presses Mod-S (Cmd/Ctrl-S) while the editor is focused. */
  onSave?: () => void;
}

/** Handle returned by {@link createYamlEditor} for controlling the mounted editor. */
export interface YamlEditorHandle {
  getValue(): string;
  setValue(value: string): void;
  setReadOnly(readOnly: boolean): void;
  focusSearch(): void;
  focus(): void;
  getVersion(): number;
  destroy(): void;
}

export function createYamlEditor(host: Element, options: YamlEditorOptions = {}): YamlEditorHandle {
  const editable = new Compartment();
  let suppressChange = false;
  let version = 0;
  const view = new EditorView({
    parent: host,
    state: EditorState.create({
      doc: options.value || "",
      extensions: [
        basicSetup,
        yamlLanguage(),
        lintGutter(),
        linter(yamlDiagnostics, { delay: 250 }),
        editable.of([EditorState.readOnly.of(false), EditorView.editable.of(true)]),
        EditorView.contentAttributes.of({ "aria-label": options.ariaLabel || "配置 YAML 编辑器" }),
        keymap.of([{
          key: "Mod-s",
          preventDefault: true,
          run() {
            options.onSave?.();
            return true;
          },
        }]),
        EditorView.updateListener.of((update) => {
          if (!update.docChanged || suppressChange) return;
          version += 1;
          options.onChange?.(update.state.doc.toString(), version);
        }),
        EditorView.theme({
          "&": { height: "100%" },
          ".cm-scroller": { fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace" },
        }),
      ],
    }),
  });

  return {
    getValue() {
      return view.state.doc.toString();
    },
    setValue(value) {
      const next = String(value || "");
      if (next === view.state.doc.toString()) return;
      suppressChange = true;
      try {
        view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: next } });
      } finally {
        suppressChange = false;
      }
    },
    setReadOnly(readOnly) {
      view.dispatch({
        effects: editable.reconfigure([
          EditorState.readOnly.of(Boolean(readOnly)),
          EditorView.editable.of(!readOnly),
        ]),
      });
    },
    focusSearch() {
      openSearchPanel(view);
      view.focus();
    },
    focus() {
      view.focus();
    },
    getVersion() {
      return version;
    },
    destroy() {
      view.destroy();
    },
  };
}
