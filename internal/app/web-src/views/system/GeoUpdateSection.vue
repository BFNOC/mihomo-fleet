<script setup lang="ts">
import { ref } from "vue";
import { localizedMessage } from "../../messages.ts";
import {
  applyGeoUpdateSSE,
  fetchGeoUpdateStatus,
  fetchProxyInstances,
} from "../../api.ts";
import type {
  FleetGeoDownloadEvent,
  FleetGeoUpdateStatus,
  FleetProxyInstance,
} from "../../state.ts";
import {
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

const geoStatus = ref<FleetGeoUpdateStatus | null>(null);
const geoLoading = ref(false);
const geoError = ref("");
const geoApplying = ref(false);
const geoResult = ref("");

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

defineExpose({ refreshGeoStatus, refreshProxyInstances, geoLoading });
</script>

<template>
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
</template>

<style scoped>
.system-section {
  margin-top: 20px;
  padding-top: 16px;
  border-top: 1px solid var(--line);
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
