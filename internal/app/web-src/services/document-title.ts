import { ref, watchEffect } from "vue";
import { chrome } from "../bridge.ts";
import { fleetSeries } from "../dashboard.ts";
import { documentTitle } from "../format.ts";
import { seriesLatest } from "../traffic.ts";
import { store } from "../store.ts";

// Deliberately not reusing views/dashboard/dashboard-data.ts's currentUp and
// currentDown. Those hang off that module's `heartbeat`, which only advances
// while the dashboard is the active view -- correct for cards nobody can see,
// wrong for a tab title that is on screen from every view.
//
// MUST be declared before the watchEffect below: watchEffect runs during setup,
// and `const` is in its temporal dead zone until its own line executes.
const tabHidden = ref(document.visibilityState === "hidden");

/**
 * Drives document.title off the fleet's live traffic. Called once, from app.ts.
 *
 * The effect never stops, matching startPolling(): both live for the page's
 * whole lifetime, so there is nothing to tear down.
 */
export function startDocumentTitle(): void {
  document.addEventListener("visibilitychange", () => {
    tabHidden.value = document.visibilityState === "hidden";
  });

  watchEffect(() => {
    // dashboard.ts's sampler Map sits outside Vue's reactive graph, so reading
    // fleetSeries() alone would evaluate once and never invalidate again.
    // chrome.trafficTick, bumped by services/polling.ts after every sample, is
    // the real dependency. See bridge.ts's comment on the field.
    void chrome.trafficTick;
    const running = store.instances.filter((item) => item.status === "running");
    const latest = seriesLatest(fleetSeries(running));
    document.title = documentTitle({
      up: latest ? latest.up : 0,
      down: latest ? latest.down : 0,
      running: running.length,
      // "dev" mirrors TopBar.vue's own fallback for a build with no version
      // stamped in. The empty string is the distinct pre-boot case: no
      // GET /api/system response yet, so no version to claim either way.
      appVersion: store.system ? store.system.appVersion || "dev" : "",
      hidden: tabHidden.value,
    });
  });
}
