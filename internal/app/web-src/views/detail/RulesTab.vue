<script setup lang="ts">
// #tab-rules. Mirrors LogsTab.vue's shape (component-local refs, no shared
// module state -- nothing else in the app reads the rule list) rather than
// ProxiesTab.vue/proxy-groups.ts's module-scope refs, since rules-data.ts is
// deliberately framework-free (see its own header comment) and holds no refs
// of its own.
import { computed, ref } from "vue";
import { store } from "../../store.ts";
import { activeInstance } from "../../state.ts";
import { localizedMessage } from "../../messages.ts";
import { useTabPolling } from "./useTabPolling.ts";
import { fetchRules, filterRules, formatRuleProxy } from "./rules-data.ts";
import type { MihomoRule } from "./rules-data.ts";

const selected = computed(() => activeInstance(store));
const isActiveTab = computed(() => store.activeTab === "rules");

const rules = ref<MihomoRule[]>([]);
const filterText = ref("");
const rulesLoading = ref(false);
const loadError = ref("");

let lastInstanceId = "";
// Only the newest request may write, and only while its instance is still the
// active one -- same staleness guard as LogsTab.vue's refreshLogs() /
// proxy-groups.ts's refreshProxies().
let requestSeq = 0;

async function refreshRules(): Promise<void> {
  const instance = selected.value;
  if (!instance) return;
  // A stopped instance has no live mihomo controller: the passthrough 409s on
  // every poll tick and paints a red error over the intended empty state. Clear
  // and bail so line-84's "启动实例后读取" copy renders -- same running-gate as
  // proxy-groups.ts's refreshProxies().
  if (instance.status !== "running") {
    // Invalidate any request already in flight from before the stop -- its
    // isStale() check reads requestSeq, so bumping it here means that
    // response can no longer pass and overwrite the clear below.
    ++requestSeq;
    rules.value = [];
    loadError.value = "";
    rulesLoading.value = false;
    lastInstanceId = "";
    return;
  }
  const seq = ++requestSeq;
  const isStale = () => seq !== requestSeq || store.activeId !== instance.id;
  const isFirstLoad = instance.id !== lastInstanceId;
  if (isFirstLoad) rulesLoading.value = true;
  try {
    const fetched = await fetchRules(instance.id);
    if (isStale()) return;
    rules.value = fetched;
    loadError.value = "";
    lastInstanceId = instance.id;
  } catch (err) {
    if (isStale()) return;
    const message = err instanceof Error ? err.message : String(err);
    loadError.value = localizedMessage(message);
  } finally {
    if (isFirstLoad && !isStale()) rulesLoading.value = false;
  }
}

const displayRules = computed(() => filterRules(rules.value, filterText.value));

useTabPolling(isActiveTab, computed(() => selected.value?.id || ""), refreshRules);
</script>

<template>
  <section class="panel">
    <div class="panel-title">
      <h3>Mihomo 规则</h3>
      <p>当前实例运行时生效的规则列表，按匹配顺序排列。</p>
    </div>
    <input id="ruleFilter" v-model="filterText" class="proxy-filter" placeholder="筛选类型 / 匹配内容 / 代理" aria-label="筛选规则">
    <div id="rulesList" class="rule-list">
      <div v-if="loadError" class="message error">{{ loadError }}</div>
      <template v-else>
        <table v-if="displayRules.length" class="rule-table">
          <thead>
            <tr>
              <th scope="col">类型</th>
              <th scope="col">匹配内容</th>
              <th scope="col">代理</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(rule, index) in displayRules" :key="`${rule.type}|${rule.payload}|${index}`">
              <td>{{ rule.type }}</td>
              <td class="rule-payload">{{ rule.payload || "—" }}</td>
              <td>{{ formatRuleProxy(rule) }}</td>
            </tr>
          </tbody>
        </table>
        <div v-else-if="rules.length" class="warning">没有匹配的规则。</div>
        <div v-else-if="rulesLoading" class="warning">正在加载规则。</div>
        <div v-else class="warning">没有可显示的规则。启动实例后读取 mihomo 运行态规则。</div>
      </template>
    </div>
  </section>
</template>

<style scoped>
.rule-list {
  margin-top: 12px;
}

.rule-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

.rule-table th,
.rule-table td {
  padding: 6px 10px;
  text-align: left;
  border-bottom: 1px solid var(--line);
  vertical-align: top;
}

.rule-table th {
  color: var(--muted);
  font-weight: 600;
  font-size: 12px;
}

.rule-payload {
  font: 12px/1.4 var(--mono);
  overflow-wrap: anywhere;
}
</style>
