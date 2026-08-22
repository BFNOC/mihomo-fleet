// The app's transient message queue. Replaces the single in-flow banner that
// used to sit at the top of the content column, where a message could be
// scrolled out of view by the time it arrived -- this queue is rendered as a
// fixed overlay by components/NotificationStack.vue.
//
// State and timers both live here rather than in the component: the stack is
// mounted exactly once for the app's whole lifetime, so there is no per-caller
// instance to scope them to, and keeping the timer next to the entry it
// dismisses means a repeat message can restart its own countdown without the
// component having to diff the list. NotificationStack.vue only renders and
// forwards clicks.
import { reactive } from "vue";

export type NoticeTone = "info" | "error";

export interface Notice {
  id: number;
  /**
   * The backend's raw string, untranslated. localizedMessage() runs at the
   * render boundary (NotificationStack.vue), which is the one place guaranteed
   * to see every entry regardless of which of the ~25 call sites pushed it.
   */
  text: string;
  tone: NoticeTone;
  /** How many times this exact message has arrived; rendered as ×N above 1. */
  count: number;
}

/**
 * Auto-dismiss delay for info messages, unchanged from the banner's timer.
 * Errors are deliberately absent from this: they stay until dismissed, either
 * by the user or by the code that raised them (services/fleet-refresh.ts
 * clears its own poll failure once a poll succeeds again).
 */
const infoDismissMs = 6000;

/**
 * Hard cap on visible entries, oldest evicted first.
 *
 * Errors never expire on their own, so without a cap a backend that stays down
 * would stack one unremovable card per failed poll until they covered the
 * screen. Dedup below handles the common shape of that (the same text
 * repeating), and this catches the rest.
 */
const maxNotices = 5;

export const notices = reactive<Notice[]>([]);

let nextId = 1;
const timers = new Map<number, ReturnType<typeof setTimeout>>();

function clearTimer(id: number): void {
  const timer = timers.get(id);
  if (timer !== undefined) {
    clearTimeout(timer);
    timers.delete(id);
  }
}

function scheduleDismiss(notice: Notice): void {
  clearTimer(notice.id);
  if (notice.tone === "error") return;
  timers.set(
    notice.id,
    setTimeout(() => dismissNotice(notice.id), infoDismissMs),
  );
}

/**
 * Adds a message, or refreshes the existing one when the same text and tone are
 * already on screen.
 *
 * The dedup is not cosmetic. services/polling.ts retries on a fixed interval
 * and services/latency.ts reports per target, so a single backend outage would
 * otherwise push an identical card every few seconds. Merging them keeps one
 * card with a running count and restarts its timer, which is what the old
 * single banner did implicitly by overwriting itself.
 *
 * Returns the entry's id so a caller that owns a sticky error can take it back
 * down later; ids are never reused.
 */
export function pushNotice(text: string, tone: NoticeTone): number {
  const existing = notices.find((notice) => notice.text === text && notice.tone === tone);
  if (existing) {
    existing.count += 1;
    scheduleDismiss(existing);
    return existing.id;
  }
  const notice: Notice = { id: nextId++, text, tone, count: 1 };
  notices.push(notice);
  while (notices.length > maxNotices) {
    // shift() on a non-empty array always yields an element; the assertion is
    // only there for noUncheckedIndexedAccess.
    clearTimer(notices.shift()!.id);
  }
  scheduleDismiss(notice);
  return notice.id;
}

/** Removes one entry by id. A stale or already-dismissed id is a no-op. */
export function dismissNotice(id: number): void {
  clearTimer(id);
  const index = notices.findIndex((notice) => notice.id === id);
  if (index >= 0) notices.splice(index, 1);
}

/** Removes every entry. Backs bridge.ts's `showMessage("")` clear-all path. */
export function dismissAllNotices(): void {
  for (const notice of notices) clearTimer(notice.id);
  notices.length = 0;
}
