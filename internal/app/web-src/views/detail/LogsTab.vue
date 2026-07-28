<script setup lang="ts">
// Vue replacement for #tab-logs's markup (index.html:301-309) and
// refreshLogs()/isLogScrolledToBottom() (app.ts:888-907).
import { computed, nextTick, ref } from "vue";
import { store } from "../../store.ts";
import { api } from "../../api.ts";
import { activeInstance } from "../../state.ts";
import { logStickThreshold } from "../../constants.ts";
import { localizedMessage } from "../../i18n.ts";
import { useTabPolling } from "./useTabPolling.ts";

const selected = computed(() => activeInstance(store));
const isActiveTab = computed(() => store.activeTab === "logs");

const logsEl = ref<HTMLPreElement | null>(null);
const logsText = ref("");
let lastInstanceId = "";

// Mirrors isLogScrolledToBottom() (app.ts:905-907).
function isLogScrolledToBottom(): boolean {
  const el = logsEl.value;
  if (!el) return true;
  return el.scrollHeight - el.scrollTop - el.clientHeight <= logStickThreshold;
}

// Mirrors refreshLogs() (app.ts:888-903).
async function refreshLogs(): Promise<void> {
  const instance = selected.value;
  if (!instance) return;
  try {
    const payload = await api<{ lines?: string[] }>(`/api/instances/${instance.id}/logs`);
    const shouldStick = lastInstanceId !== instance.id || isLogScrolledToBottom();
    const text = (payload.lines || []).join("\n") || "还没有进程日志。";
    logsText.value = text;
    lastInstanceId = instance.id;
    if (shouldStick) {
      await nextTick();
      if (logsEl.value) logsEl.value.scrollTop = logsEl.value.scrollHeight;
    }
  } catch (err) {
    lastInstanceId = "";
    const message = err instanceof Error ? err.message : String(err);
    logsText.value = localizedMessage(message);
  }
}

useTabPolling(isActiveTab, computed(() => selected.value?.id || ""), refreshLogs);
</script>

<template>
  <section class="panel">
    <div class="panel-title">
      <h3>进程日志</h3>
      <p>当前实例最近的标准输出和错误输出。</p>
    </div>
    <pre id="logs" ref="logsEl" class="logs" tabindex="0" aria-label="实例日志">{{ logsText }}</pre>
  </section>
</template>
