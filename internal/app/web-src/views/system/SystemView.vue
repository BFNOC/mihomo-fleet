<script setup lang="ts">
// #systemPanel's content: thin shell importing the system sections.
// All formatting/decision logic lives in system-update.ts; section-specific
// fetch-on-open + button wiring lives in each section component.
import { ref, watch } from "vue";
import { store } from "../../store.ts";
import { localizedMessage } from "../../messages.ts";
import { applyCoreUpdate, fetchCoreUpdateStatus } from "../../api.ts";
import type { FleetCoreUpdateStatus } from "../../state.ts";
import {
  coreApplyDisabled,
  describeCoreChecksumNote,
  describeCoreStatus,
} from "./system-update.ts";
import GeoUpdateSection from "./GeoUpdateSection.vue";
import BackupSection from "./BackupSection.vue";
import AlertsSection from "./AlertsSection.vue";

const coreStatus = ref<FleetCoreUpdateStatus | null>(null);
const coreLoading = ref(false);
const coreError = ref("");
const coreApplying = ref(false);
const coreResult = ref("");

const geoSection = ref<InstanceType<typeof GeoUpdateSection> | null>(null);

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

function refreshAll(): void {
  void refreshCoreStatus();
  geoSection.value?.refreshGeoStatus();
  geoSection.value?.refreshProxyInstances();
}

watch(
  () => store.view,
  (view) => {
    if (view !== "system") return;
    refreshAll();
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
    <button type="button" :disabled="coreLoading || geoSection?.geoLoading" @click="refreshAll">重新检测</button>
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

  <GeoUpdateSection ref="geoSection" />

  <section class="system-section">
    <h3>备份 / 迁移</h3>
    <p>
      导出整套实例与配置档到单个 JSON 文件；导入时会重新生成每个实例的控制器密钥，
      冲突的端口会自动重新分配，重名的配置档或实例会自动改名，不会覆盖已有数据。
      导出文件包含订阅地址等敏感信息，请妥善保管。
    </p>
    <AlertsSection />
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
</style>
