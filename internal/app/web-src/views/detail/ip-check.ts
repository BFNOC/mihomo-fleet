// 出口 IP 检测的状态与动作。InstanceDetail.vue 的标签行放触发按钮，
// OverviewTab.vue 显示结果和测试网址；两边共用这一份模块级状态
// （同 dashboard-data.ts 的模式：detail 区只挂载一次）。
import { reactive, ref } from "vue";
import { api } from "../../api.ts";
import { actions } from "../../bridge.ts";
import { defaultIpCheckUrl } from "../../constants.ts";
import { localizedMessage } from "../../messages.ts";

export interface IpCheckResult {
  ip: string;
  url: string;
  elapsedMs: number;
  at: number;
}

const IP_CHECK_URL_KEY = "fleetIpCheckUrl";

export const ipCheck = reactive({
  results: {} as Record<string, IpCheckResult>,
  running: new Set<string>(),
});

export const ipCheckUrl = ref(localStorage.getItem(IP_CHECK_URL_KEY) || defaultIpCheckUrl);

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
    actions.showMessage(localizedMessage(err instanceof Error ? err.message : String(err)), "error");
  } finally {
    ipCheck.running.delete(instanceId);
  }
}
