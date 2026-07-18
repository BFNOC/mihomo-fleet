import { basicSetup } from "codemirror";
import { Compartment, EditorState } from "@codemirror/state";
import { linter, lintGutter } from "@codemirror/lint";
import { yaml as yamlLanguage } from "@codemirror/lang-yaml";
import { openSearchPanel } from "@codemirror/search";
import { EditorView, keymap } from "@codemirror/view";
import { parseDocument } from "yaml";

export * from "./app-logic.js";

function yamlDiagnostics(view) {
  const text = view.state.doc.toString();
  const document = parseDocument(text, { prettyErrors: false, strict: true });
  const length = text.length;
  return [...document.errors, ...document.warnings].map((problem) => {
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

export function createYamlEditor(host, options = {}) {
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
