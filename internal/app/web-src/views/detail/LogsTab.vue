<script setup lang="ts">
// Vue replacement for #tab-logs's markup (index.html:301-309) and
// refreshLogs()/isLogScrolledToBottom() (pre-Vue app.ts).
import { computed, nextTick, ref } from "vue";
import { store } from "../../store.ts";
import { api } from "../../api.ts";
import { activeInstance } from "../../state.ts";
import { logStickThreshold } from "../../constants.ts";
import { localizedMessage } from "../../messages.ts";
import { useTabPolling } from "./useTabPolling.ts";

const selected = computed(() => activeInstance(store));
const isActiveTab = computed(() => store.activeTab === "logs");

const logsEl = ref<HTMLPreElement | null>(null);
const logsText = ref("");
const logsError = ref("");
let lastInstanceId = "";

// Only the newest request may write, and only while its instance is still the
// active one -- mirrors proxy-groups.ts's refreshProxies() staleness guard, so
// a slow response for instance A can't land under instance B's header (and
// can't poison the next poll's shouldStick/lastInstanceId bookkeeping).
let requestSeq = 0;

// Mirrors isLogScrolledToBottom() (pre-Vue app.ts).
function isLogScrolledToBottom(): boolean {
  const el = logsEl.value;
  if (!el) return true;
  return el.scrollHeight - el.scrollTop - el.clientHeight <= logStickThreshold;
}

// Mirrors refreshLogs() (pre-Vue app.ts), plus a staleness guard (below) and
// an error path that reports into `logsError` instead of overwriting
// `logsText` -- a transient fetch failure should not blank out log content
// the user was already reading.
async function refreshLogs(): Promise<void> {
  const instance = selected.value;
  if (!instance) return;
  const seq = ++requestSeq;
  const isStale = () => seq !== requestSeq || store.activeId !== instance.id;
  try {
    const payload = await api<{ lines?: string[] }>(`/api/instances/${instance.id}/logs`);
    if (isStale()) return;
    const shouldStick = lastInstanceId !== instance.id || isLogScrolledToBottom();
    const text = (payload.lines || []).join("\n") || "还没有进程日志。";
    logsText.value = text;
    logsError.value = "";
    lastInstanceId = instance.id;
    if (shouldStick) {
      await nextTick();
      if (logsEl.value) logsEl.value.scrollTop = logsEl.value.scrollHeight;
    }
  } catch (err) {
    if (isStale()) return;
    const message = err instanceof Error ? err.message : String(err);
    logsError.value = localizedMessage(message);
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
    <div v-if="logsError" class="message error">{{ logsError }}</div>
    <pre id="logs" ref="logsEl" class="logs" tabindex="0" aria-label="实例日志">{{ logsText }}</pre>
  </section>
</template>
