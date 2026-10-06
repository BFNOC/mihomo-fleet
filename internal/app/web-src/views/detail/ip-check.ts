// 出口 IP 检测的状态与动作。InstanceDetail.vue 的标签行放触发按钮，
// OverviewTab.vue 显示结果和测试网址；两边共用这一份模块级状态
// （同 dashboard-data.ts 的模式：detail 区只挂载一次）。
import { computed, reactive, ref } from "vue";
import { api } from "../../api.ts";
import { actions } from "../../bridge.ts";
import { defaultIpCheckUrl, ipCheckPresets } from "../../constants.ts";
import { localizedMessage } from "../../messages.ts";

// 失败也记一条（error 非空），覆盖上次的成功结果，免得概览继续显示过期的 IP。
export interface IpCheckResult {
  ip: string;
  url: string;
  elapsedMs: number;
  at: number;
  error?: string;
}

const IP_CHECK_URL_KEY = "fleetIpCheckUrl";

export const ipCheck = reactive({
  results: {} as Record<string, IpCheckResult>,
  running: new Set<string>(),
});

export const ipCheckUrl = ref(localStorage.getItem(IP_CHECK_URL_KEY) || defaultIpCheckUrl);

// 下拉当前选中项；网址不在预设里时选“自定义”（空串）。
export const ipCheckPreset = computed(() => (ipCheckPresets.includes(ipCheckUrl.value) ? ipCheckUrl.value : ""));

export function pickIpCheckPreset(event: Event): void {
  const value = (event.target as HTMLSelectElement).value;
  if (!value) return;
  ipCheckUrl.value = value;
  persistIpCheckUrl();
}

export function persistIpCheckUrl(): void {
  const value = ipCheckUrl.value.trim();
  ipCheckUrl.value = value || defaultIpCheckUrl;
  localStorage.setItem(IP_CHECK_URL_KEY, ipCheckUrl.value);
}

export async function runIpCheck(instanceId: string): Promise<void> {
  if (!instanceId || ipCheck.running.has(instanceId)) return;
  ipCheck.running.add(instanceId);
  try {
    const payload = await api<{ ip: string; url: string; elapsedMs: number }>(`/api/instances/${instanceId}/ip`, {
      method: "POST",
      body: JSON.stringify({ url: ipCheckUrl.value }),
    });
    ipCheck.results[instanceId] = { ...payload, at: Date.now() };
    actions.showMessage(`出口 IP：${payload.ip}`);
  } catch (err) {
    const message = localizedMessage(err instanceof Error ? err.message : String(err));
    ipCheck.results[instanceId] = { ip: "", url: ipCheckUrl.value, elapsedMs: 0, at: Date.now(), error: message };
    actions.showMessage(message, "error");
  } finally {
    ipCheck.running.delete(instanceId);
  }
}
