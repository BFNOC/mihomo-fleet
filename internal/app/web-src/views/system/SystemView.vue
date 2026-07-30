<script setup lang="ts">
// #systemPanel's content (feature #3, docs/feature-roadmap-post-1.3.md): a
// small "系统 / 组件" panel showing current vs latest mihomo core version and
// geodata file status, with an update button for each. Mirrors
// ProfileManagerView.vue's shape (a top-level view fetched on open) rather
// than an instance-detail tab's continuous polling -- checking GitHub's
// release API on an interval would be wasteful and rate-limit-prone for
// information that only changes on upstream's own release cadence.
//
// All formatting/decision logic lives in system-update.ts (framework-free,
// unit-tested); this file is fetch-on-open + button wiring only.
import { ref, watch } from "vue";
import { store } from "../../store.ts";
import { localizedMessage } from "../../messages.ts";
import {
  applyCoreUpdate,
  applyGeoUpdate,
  fetchCoreUpdateStatus,
  fetchGeoUpdateStatus,
} from "../../api.ts";
import type { FleetCoreUpdateStatus, FleetGeoUpdateStatus } from "../../state.ts";
import {
  coreApplyDisabled,
  describeCoreChecksumNote,
  describeCoreStatus,
  describeGeoFile,
  describeGeoResult,
  geoApplyDisabled,
  geoFileLabel,
  geoSourcePath,
  geoSummaryText,
} from "./system-update.ts";
import BackupSection from "./BackupSection.vue";

const coreStatus = ref<FleetCoreUpdateStatus | null>(null);
const coreLoading = ref(false);
const coreError = ref("");
const coreApplying = ref(false);
const coreResult = ref("");

const geoStatus = ref<FleetGeoUpdateStatus | null>(null);
const geoLoading = ref(false);
const geoError = ref("");
const geoApplying = ref(false);
const geoResult = ref("");

async function refreshCoreStatus(): Promise<void> {
  coreLoading.value = true;
  coreError.value = "";
  try {
    coreStatus.value = await fetchCoreUpdateStatus();
  } catch (err) {
    coreError.value = localizedMessage(err instanceof Error ? err.message : String(err));
  } finally {
    coreLoading.value = false;
  }
}

async function refreshGeoStatus(): Promise<void> {
  geoLoading.value = true;
  geoError.value = "";
  try {
    geoStatus.value = await fetchGeoUpdateStatus();
  } catch (err) {
    geoError.value = localizedMessage(err instanceof Error ? err.message : String(err));
  } finally {
    geoLoading.value = false;
  }
}

async function onApplyCoreUpdate(): Promise<void> {
  coreApplying.value = true;
  coreResult.value = "";
  coreError.value = "";
  try {
    const result = await applyCoreUpdate();
    coreResult.value = `已更新到 ${result.version || "新版本"}。`;
    await refreshCoreStatus();
  } catch (err) {
    coreError.value = localizedMessage(err instanceof Error ? err.message : String(err));
  } finally {
    coreApplying.value = false;
  }
}

async function onApplyGeoUpdate(): Promise<void> {
  geoApplying.value = true;
  geoResult.value = "";
  geoError.value = "";
  try {
    const result = await applyGeoUpdate();
    geoResult.value = describeGeoResult(result.updated, result.errors);
    await refreshGeoStatus();
  } catch (err) {
    geoError.value = localizedMessage(err instanceof Error ? err.message : String(err));
  } finally {
    geoApplying.value = false;
  }
}

// Fetch once whenever the panel is opened, not continuously -- see this
// file's header comment.
watch(
  () => store.view,
  (view) => {
    if (view !== "system") return;
    void refreshCoreStatus();
    void refreshGeoStatus();
  },
  { immediate: true },
);
</script>

<template>
  <div class="panel-title">
    <div>
      <h2>系统组件</h2>
      <p>mihomo 核心与地理数据的版本检测与更新。</p>
    </div>
    <button type="button" :disabled="coreLoading || geoLoading" @click="refreshCoreStatus(); refreshGeoStatus()">重新检测</button>
  </div>

  <section class="system-section">
    <h3>mihomo 核心</h3>
    <p v-if="coreLoading && !coreStatus" class="warning">正在检测版本。</p>
    <template v-else-if="coreStatus">
      <p>{{ describeCoreStatus(coreStatus) }}</p>
      <p v-if="describeCoreChecksumNote(coreStatus)" class="warning">{{ describeCoreChecksumNote(coreStatus) }}</p>
      <div class="system-actions">
        <button
          type="button"
          class="primary"
          :disabled="coreApplyDisabled(coreStatus, coreApplying)"
          @click="onApplyCoreUpdate"
        >{{ coreApplying ? "更新中…" : "更新核心" }}</button>
      </div>
      <p v-if="coreResult" class="message">{{ coreResult }}</p>
    </template>
    <p v-if="coreError" class="message error">{{ coreError }}</p>
  </section>

  <section class="system-section">
    <h3>地理数据</h3>
    <p v-if="geoLoading && !geoStatus" class="warning">正在检测版本。</p>
    <template v-else-if="geoStatus">
      <p>{{ geoSummaryText(geoStatus) }}</p>
      <ul class="system-geo-list">
        <li v-for="file in geoStatus.files" :key="file.name">
          <div class="system-geo-info">
            <span class="system-geo-name">{{ geoFileLabel(file.name) }}</span>
            <span class="system-geo-note">{{ describeGeoFile(file) }}</span>
          </div>
          <span v-if="geoSourcePath(file)" class="system-geo-path" :title="geoSourcePath(file)">{{ geoSourcePath(file) }}</span>
          <span v-else class="system-geo-path system-geo-path--missing">未找到</span>
        </li>
      </ul>
      <div class="system-actions">
        <button
          type="button"
          class="primary"
          :disabled="geoApplyDisabled(geoStatus, geoApplying)"
          @click="onApplyGeoUpdate"
        >{{ geoApplying ? "更新中…" : "更新地理数据" }}</button>
      </div>
      <p v-if="geoResult" class="message">{{ geoResult }}</p>
    </template>
    <p v-if="geoError" class="message error">{{ geoError }}</p>
  </section>

  <section class="system-section">
    <h3>备份 / 迁移</h3>
    <p>
      导出整套实例与配置档到单个 JSON 文件；导入时会重新生成每个实例的控制器密钥，
      冲突的端口会自动重新分配，重名的配置档或实例会自动改名，不会覆盖已有数据。
      导出文件包含订阅地址等敏感信息，请妥善保管。
    </p>
    <BackupSection />
  </section>
</template>

<style scoped>
.system-section {
  margin-top: 20px;
  padding-top: 16px;
  border-top: 1px solid var(--line);
}

.system-section:first-of-type {
  margin-top: 16px;
}

.system-section h3 {
  margin: 0 0 8px;
  font-size: 15px;
}

.system-actions {
  margin-top: 10px;
}

.system-geo-list {
  list-style: none;
  margin: 10px 0 0;
  padding: 0;
  display: grid;
  gap: 6px;
}

.system-geo-list li {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 6px 10px;
  border: 1px solid var(--line);
  border-radius: var(--radius-sm);
  font-size: 13px;
}

.system-geo-info {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
}

.system-geo-name {
  font-weight: 600;
}

.system-geo-note {
  color: var(--muted);
  font-size: 12.5px;
}

.system-geo-path {
  font-size: 11.5px;
  font-family: var(--mono, monospace);
  color: var(--muted);
  word-break: break-all;
}

.system-geo-path--missing {
  color: var(--danger, #c00);
}
</style>
