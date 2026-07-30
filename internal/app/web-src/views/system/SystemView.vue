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
  applyGeoUpdateSSE,
  fetchCoreUpdateStatus,
  fetchGeoUpdateStatus,
  fetchProxyInstances,
} from "../../api.ts";
import type {
  FleetCoreUpdateStatus,
  FleetGeoDownloadEvent,
  FleetGeoUpdateStatus,
  FleetProxyInstance,
} from "../../state.ts";
import {
  coreApplyDisabled,
  describeCoreChecksumNote,
  describeCoreStatus,
  describeGeoFile,
  describeGeoResult,
  formatBytes,
  formatGeoProgress,
  formatSpeed,
  geoApplyDisabled,
  geoFileDescription,
  geoFileLabel,
  geoProgressPercent,
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

// P2 (docs/geo-update-enhancements.md section 3): download-source picker.
// Empty string means "直连" (direct, the historical default) -- only ever
// populated with the id of a currently-running managed instance, never
// free-form input, matching proxyClientForInstance's "own managed instances
// only" constraint on the backend.
const proxyInstances = ref<FleetProxyInstance[]>([]);
const selectedProxyInstanceId = ref("");

interface GeoProgressState {
  file: string;
  index: number;
  total: number;
  downloaded: number;
  totalSize: number;
  speed: number;
}

const geoProgress = ref<GeoProgressState | null>(null);

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

// Silent on failure -- the picker just falls back to "no running instances"
// (i.e. hidden, see the template's v-if) rather than surfacing a second
// error banner alongside coreError/geoError for what is a minor, optional
// convenience feature.
async function refreshProxyInstances(): Promise<void> {
  try {
    proxyInstances.value = await fetchProxyInstances();
  } catch {
    proxyInstances.value = [];
  }
  if (!proxyInstances.value.some((instance) => instance.id === selectedProxyInstanceId.value)) {
    selectedProxyInstanceId.value = "";
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

// onGeoDownloadEvent turns one SSE frame into geoProgress's next value:
// "start"/"progress" set the current file's progress block (a "start"
// frame carries no downloaded/totalSize/speed yet, so those read as 0/0/0
// until the first "progress" frame arrives), "done" clears it between
// files, and "complete" is the terminal frame -- its updated/errors become
// the same result text applyGeoUpdate's old JSON response used to produce.
function onGeoDownloadEvent(event: FleetGeoDownloadEvent): void {
  switch (event.event) {
    case "start":
    case "progress":
      geoProgress.value = {
        file: event.file || "",
        index: event.index || 0,
        total: event.total || 0,
        downloaded: event.downloaded || 0,
        totalSize: event.totalSize || 0,
        speed: event.speed || 0,
      };
      break;
    case "done":
      geoProgress.value = null;
      break;
    case "complete":
      geoResult.value = describeGeoResult(event.updated, event.errors);
      break;
  }
}

async function onApplyGeoUpdate(): Promise<void> {
  geoApplying.value = true;
  geoResult.value = "";
  geoError.value = "";
  geoProgress.value = null;
  try {
    await applyGeoUpdateSSE(onGeoDownloadEvent, selectedProxyInstanceId.value || undefined);
    await refreshGeoStatus();
  } catch (err) {
    geoError.value = localizedMessage(err instanceof Error ? err.message : String(err));
  } finally {
    geoApplying.value = false;
    geoProgress.value = null;
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
    void refreshProxyInstances();
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
    <button type="button" :disabled="coreLoading || geoLoading" @click="refreshCoreStatus(); refreshGeoStatus(); refreshProxyInstances()">重新检测</button>
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
          <span v-if="geoFileDescription(file.name)" class="system-geo-desc">{{ geoFileDescription(file.name) }}</span>
          <span v-if="geoSourcePath(file)" class="system-geo-path" :title="geoSourcePath(file)">{{ geoSourcePath(file) }}</span>
          <span v-else class="system-geo-path system-geo-path--missing">未找到</span>
        </li>
      </ul>
      <div v-if="geoProgress" class="geo-download-progress">
        <div class="geo-download-file">{{ geoFileLabel(geoProgress.file) }}（{{ geoProgress.index + 1 }}/{{ geoProgress.total }}）</div>
        <div
          class="geo-download-bar"
          role="progressbar"
          :aria-valuenow="Math.round(geoProgressPercent(geoProgress.downloaded, geoProgress.totalSize))"
          aria-valuemin="0"
          aria-valuemax="100"
        >
          <div class="geo-download-bar-fill" :style="{ width: geoProgressPercent(geoProgress.downloaded, geoProgress.totalSize) + '%' }"></div>
        </div>
        <div class="geo-download-stats">
          <span>{{ formatBytes(geoProgress.downloaded) }}<template v-if="geoProgress.totalSize"> / {{ formatBytes(geoProgress.totalSize) }}</template></span>
          <span>{{ formatSpeed(geoProgress.speed) }}</span>
        </div>
        <span class="sr-only" aria-live="polite">{{ formatGeoProgress(geoProgress) }}</span>
      </div>
      <label v-if="proxyInstances.length" class="system-proxy-picker">
        <span>下载方式</span>
        <select v-model="selectedProxyInstanceId" :disabled="geoApplying">
          <option value="">直连</option>
          <option v-for="instance in proxyInstances" :key="instance.id" :value="instance.id">
            实例：{{ instance.name }} (:{{ instance.mixedPort }})
          </option>
        </select>
      </label>
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

.system-geo-desc {
  color: var(--muted);
  font-size: 11.5px;
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

.system-proxy-picker {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 10px;
  font-size: 13px;
  color: var(--muted);
}

.system-proxy-picker select {
  flex: 1;
  min-width: 0;
}

.geo-download-progress {
  margin-top: 10px;
  display: grid;
  gap: 4px;
  font-size: 12.5px;
}

.geo-download-file {
  font-weight: 600;
}

.geo-download-bar {
  height: 6px;
  border-radius: var(--radius-xs);
  background: var(--accent-soft);
  overflow: hidden;
}

.geo-download-bar-fill {
  height: 100%;
  background: var(--accent);
  transition: width 0.2s ease;
}

.geo-download-stats {
  display: flex;
  justify-content: space-between;
  color: var(--muted);
}

.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}
</style>
