<script setup lang="ts">
// The "备份 / 迁移" section's controls (feature #7,
// docs/feature-roadmap-post-1.3.md #7): export downloads the whole fleet as
// one JSON file; import reads a chosen file back and POSTs it to
// /api/import. Split out of SystemView.vue -- which still owns this
// section's <h3>/intro paragraph, see that file -- purely to keep it under
// this codebase's ~300-line component ceiling (CLAUDE.md). All formatting/
// summary logic lives in backup.ts (framework-free, unit-tested); this file
// is fetch + file-picker wiring only, the same shape SystemView.vue already
// uses for its core/geo update sections.
import { ref } from "vue";
import { localizedMessage } from "../../messages.ts";
import { fetchExportBundle, importBundle } from "../../api.ts";
import { refresh } from "../../services/fleet-refresh.ts";
import { exportFilename, summarizeImportResult } from "./backup.ts";

const exporting = ref(false);
const exportError = ref("");

const importing = ref(false);
const importError = ref("");
const importSummary = ref<string[]>([]);
const fileInput = ref<HTMLInputElement | null>(null);

async function onExport(): Promise<void> {
  exporting.value = true;
  exportError.value = "";
  try {
    const bundle = await fetchExportBundle();
    const blob = new Blob([JSON.stringify(bundle, null, 2)], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    try {
      const link = document.createElement("a");
      link.href = url;
      link.download = exportFilename(new Date());
      document.body.append(link);
      link.click();
      link.remove();
    } finally {
      URL.revokeObjectURL(url);
    }
  } catch (err) {
    exportError.value = localizedMessage(err instanceof Error ? err.message : String(err));
  } finally {
    exporting.value = false;
  }
}

function onPickFile(): void {
  fileInput.value?.click();
}

async function onFileChange(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0] || null;
  input.value = ""; // allow re-selecting the exact same file again later
  if (!file) return;

  importing.value = true;
  importError.value = "";
  importSummary.value = [];
  try {
    const text = await file.text();
    const result = await importBundle(text);
    importSummary.value = summarizeImportResult(result);
    // The import just created profiles/instances behind the store's back;
    // pull the fresh lists so the rest of the UI (instance switcher, profile
    // manager) reflects them immediately instead of waiting for the next poll.
    await refresh({ forceInstances: true });
  } catch (err) {
    importError.value = localizedMessage(err instanceof Error ? err.message : String(err));
  } finally {
    importing.value = false;
  }
}
</script>

<template>
  <div class="backup-actions">
    <button type="button" class="primary" :disabled="exporting" @click="onExport">{{ exporting ? "导出中…" : "导出备份" }}</button>
    <button type="button" :disabled="importing" @click="onPickFile">{{ importing ? "导入中…" : "选择备份文件导入" }}</button>
    <input ref="fileInput" type="file" accept="application/json,.json" class="hidden" @change="onFileChange" />
  </div>
  <p v-if="exportError" class="message error">{{ exportError }}</p>
  <p v-if="importError" class="message error">{{ importError }}</p>
  <ul v-if="importSummary.length" class="backup-summary">
    <li v-for="(line, index) in importSummary" :key="index">{{ line }}</li>
  </ul>
</template>

<style scoped>
.backup-actions {
  margin-top: 10px;
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

.backup-summary {
  margin: 10px 0 0;
  padding: 0;
  list-style: none;
  display: grid;
  gap: 4px;
  font-size: 13px;
  color: var(--muted);
}
</style>
