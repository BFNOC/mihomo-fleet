// Tells the user an instance fell over, without needing the page in front of
// them. The watchdog (manager.go) already tries to restart a crashed instance,
// but a fleet that quietly gave up looks exactly like a fleet that is fine
// until someone opens the dashboard.
//
// A leaf in the services dependency order: it imports the store and the message
// queue, and nothing under services/, so it cannot participate in a module-init
// cycle (CLAUDE.md).
import { ref, watch } from "vue";
import { showMessage } from "../bridge.ts";
import { localizedMessage } from "../messages.ts";
import { dismissNotice } from "../notifications.ts";
import { store } from "../store.ts";

const storageKey = "fleetInstanceAlerts";

/**
 * Whether desktop notifications are wanted, as opposed to whether they are
 * possible -- `Notification.permission` owns that half and the browser will not
 * hand it over without a user gesture.
 *
 * Read at module load rather than lazily: the watcher below starts on boot, and
 * an alert firing in the first seconds must already know the answer.
 */
export const desktopAlertsEnabled = ref(localStorage.getItem(storageKey) === "1");

/** Reflects `Notification.permission`, kept in a ref so the toggle re-renders. */
export const desktopAlertsPermission = ref<NotificationPermission | "unsupported">(
  typeof Notification === "undefined" ? "unsupported" : Notification.permission,
);

/**
 * Turns desktop alerts on or off. MUST be called from a click handler: browsers
 * only honour requestPermission() during a user gesture, and one silently
 * ignored request would leave the toggle stuck on with nothing ever appearing.
 *
 * Returns the state the toggle actually ended up in, which is not always the
 * one asked for -- a denied permission cannot be talked out of.
 */
export async function setDesktopAlerts(enabled: boolean): Promise<boolean> {
  if (!enabled) {
    desktopAlertsEnabled.value = false;
    localStorage.setItem(storageKey, "0");
    return false;
  }
  if (typeof Notification === "undefined") {
    desktopAlertsPermission.value = "unsupported";
    return false;
  }
  if (Notification.permission === "default") {
    desktopAlertsPermission.value = await Notification.requestPermission();
  } else {
    desktopAlertsPermission.value = Notification.permission;
  }
  const granted = desktopAlertsPermission.value === "granted";
  desktopAlertsEnabled.value = granted;
  localStorage.setItem(storageKey, granted ? "1" : "0");
  return granted;
}

/**
 * Instance id -> the queue id of the alert it currently owns, i.e. "this
 * instance is in a failure episode I have already reported".
 *
 * The queue id is what makes recovery clean: the alert is an error, so it never
 * expires on its own, and dismissing it by id can only ever remove this
 * module's own card -- the same contract services/fleet-refresh.ts uses for its
 * poll failure.
 *
 * A value of 0 means an episode that was already underway when the page
 * loaded: tracked so it cannot be announced later, but with no card to take
 * down, since none was ever shown.
 */
const openAlerts = new Map<string, number>();
let baselineTaken = false;

/**
 * Starts watching the fleet. Called once, from app.ts. Never stops: it lives
 * for the page's lifetime, like startPolling() and startDocumentTitle().
 */
export function startInstanceAlerts(): void {
  watch(
    () => store.instances,
    (instances) => {
      // The first populated snapshot is a baseline, not news. Without this,
      // opening the page on an already-failed instance would announce a crash
      // that happened hours ago -- and would do it again on every reload.
      if (!baselineTaken) {
        if (!instances.length) return;
        // Recorded as episodes-in-progress rather than merely skipped: without
        // this the very next tick would find an instance still in error with no
        // alert open and announce it after all.
        for (const item of instances) {
          if (item.status === "error") openAlerts.set(item.id, 0);
        }
        baselineTaken = true;
        return;
      }
      const seen = new Set<string>();
      for (const item of instances) {
        seen.add(item.id);
        // Gated on "does this instance already own an open alert", NOT on the
        // previous status. A crash-looping instance cycles
        // error -> stopped -> starting -> error, so a previous-status test
        // reads every lap as a fresh failure: the card looked right (the id
        // dedup replaced it in place) while the desktop notification fired
        // once per lap. Measured, not theorised -- three banners for one crash.
        if (item.status === "error" && !openAlerts.has(item.id)) {
          raiseAlert(item.id, alertLabel(item, instances), item.lastError || "");
          continue;
        }
        // Re-armed only by a return to running. That also means a card the user
        // dismissed by hand stays dismissed for the rest of the episode, which
        // is the point of dismissing it.
        if (item.status === "running") clearAlert(item.id);
      }
      // A deleted instance takes its alert with it.
      for (const id of [...openAlerts.keys()]) {
        if (!seen.has(id)) clearAlert(id);
      }
    },
    { immediate: true },
  );
}

/**
 * How an instance is named in its alert: its display name, disambiguated with
 * its id when another instance currently answers to the same name.
 *
 * Nothing stops two instances sharing a name -- the store only guarantees ids
 * are unique -- and two same-named instances failing on the same error produced
 * a single card, because the queue merges identical text. That is fine as
 * display, but it made one card the recovery handle for two failures: whichever
 * instance recovered first dismissed it, and the other's entry in openAlerts
 * then blocked a replacement from ever being raised. The id in the label keeps
 * the two sentences distinct, and only appears when it has to.
 */
function alertLabel(item: { id: string; name?: string }, instances: readonly { name?: string }[]): string {
  const name = item.name || item.id;
  const shared = instances.filter((other) => (other.name || "") === (item.name || "")).length > 1;
  return shared ? `${name}（${item.id}）` : name;
}

function raiseAlert(id: string, name: string, lastError: string): void {
  // Pre-localised here rather than left to the render boundary: the reason is
  // interpolated into a Chinese sentence, and errorPatterns matches whole
  // strings, so an embedded raw error would never match. localizedMessage is a
  // lookup-or-passthrough, so text it does not recognise survives unchanged.
  const reason = lastError ? localizedMessage(lastError) : "";
  const text = reason ? `实例「${name}」运行出错：${reason}` : `实例「${name}」运行出错。`;
  openAlerts.set(id, showMessage(text, "error", alertOwner(id)));
  notifyDesktop(id, text);
}

function clearAlert(id: string): void {
  const noticeId = openAlerts.get(id);
  if (noticeId) dismissNotice(noticeId, alertOwner(id));
  openAlerts.delete(id);
}

// One claim per instance, so a recovering instance releases only its own hold
// on a card that a second instance may still be sharing.
function alertOwner(id: string): string {
  return `instance-alert:${id}`;
}

/**
 * Fires the OS-level notification, when the user has asked for one.
 *
 * Deliberately NOT gated on `document.hidden`. A tab can be the active tab of a
 * window that is behind everything else -- `hidden` is false, the toast is
 * technically on screen, and nobody sees it. That case is the whole reason this
 * feature exists, so the mild duplication of showing both is the right trade.
 */
function notifyDesktop(id: string, text: string): void {
  if (!desktopAlertsEnabled.value) return;
  if (typeof Notification === "undefined" || Notification.permission !== "granted") return;
  try {
    // The tag is per instance, not per feature. A shared tag tells the browser
    // these are the same notification, so a second instance failing seconds
    // later would silently replace the first one's banner and the first failure
    // would never have been seen.
    new Notification("Mihomo Fleet", { body: text, tag: `mihomo-fleet-instance-${id}` });
  } catch {
    // Some browsers reject the constructor outright (notably older mobile
    // Chrome, which only allows notifications through a service worker). The
    // in-page card already carried the message, so there is nothing to recover.
  }
}
