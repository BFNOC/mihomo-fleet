<script setup lang="ts">
// Opt-in for desktop notifications when an instance fails. The watching and the
// firing live in services/instance-alerts.ts; this component owns only the
// switch and the explanation of what state it is in.
//
// The click handler is load-bearing, not a wrapper: Notification.requestPermission()
// is only honoured during a user gesture, so the request has to originate here
// rather than from the service's own module scope.
import { computed, ref } from "vue";
import {
  desktopAlertsEnabled,
  desktopAlertsPermission,
  setDesktopAlerts,
} from "../../services/instance-alerts.ts";

const busy = ref(false);

const statusNote = computed(() => {
  if (desktopAlertsPermission.value === "unsupported") return "当前浏览器不支持桌面通知。";
  if (desktopAlertsPermission.value === "denied") {
    return "浏览器已拒绝本站的通知权限，需要在浏览器的站点设置里改回“允许”。";
  }
  if (desktopAlertsEnabled.value) return "实例转为错误状态时会弹出系统通知。";
  return "开启后，实例转为错误状态时会弹出系统通知；页面内的提示不受此开关影响。";
});

async function toggle(): Promise<void> {
  busy.value = true;
  try {
    await setDesktopAlerts(!desktopAlertsEnabled.value);
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <section class="system-section">
    <h3>实例告警</h3>
    <p class="system-geo-desc">{{ statusNote }}</p>
    <div class="system-actions">
      <button
        type="button"
        :disabled="busy || desktopAlertsPermission === 'unsupported' || desktopAlertsPermission === 'denied'"
        :aria-pressed="desktopAlertsEnabled"
        @click="toggle"
      >{{ desktopAlertsEnabled ? "关闭桌面通知" : "开启桌面通知" }}</button>
    </div>
  </section>
</template>
