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
  /**
   * Who is currently keeping this card up.
   *
   * Merging on text alone made the card a shared object with no owner: a poll
   * failure and a hand-triggered action that produce the identical error string
   * got the same id back, and then the poll's own recovery dismissed a card the
   * action still needed. An owner releases only its own claim; the card leaves
   * when the last claim does.
   *
   * An anonymous push (no owner) counts as one unnamed claim, which is why the
   * set can be empty while the card is up.
   */
  owners: Set<string>;
  anonymous: number;
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
export function pushNotice(text: string, tone: NoticeTone, owner = ""): number {
  const existing = notices.find((notice) => notice.text === text && notice.tone === tone);
  if (existing) {
    existing.count += 1;
    if (owner) existing.owners.add(owner);
    else existing.anonymous += 1;
    scheduleDismiss(existing);
    return existing.id;
  }
  const notice: Notice = {
    id: nextId++,
    text,
    tone,
    count: 1,
    owners: new Set(owner ? [owner] : []),
    anonymous: owner ? 0 : 1,
  };
  notices.push(notice);
  while (notices.length > maxNotices) evictOldest();
  scheduleDismiss(notice);
  return notice.id;
}

/**
 * Evicts the oldest entry that can afford to go: an info message expires on its
 * own anyway, so it is dropped ahead of any error.
 *
 * Errors are only evicted when the queue is nothing but errors. Dropping the
 * oldest entry unconditionally meant five later messages could silently retire
 * an error nobody had read -- and in a cascading failure the oldest error is
 * usually the closest one to the cause.
 */
function evictOldest(): void {
  const index = notices.findIndex((notice) => notice.tone !== "error");
  const [dropped] = notices.splice(index >= 0 ? index : 0, 1);
  if (dropped) clearTimer(dropped.id);
}

/**
 * Releases a claim on one entry, removing the card once no claim is left.
 *
 * `owner` must be passed by code that raised the message on someone's behalf
 * (services/fleet-refresh.ts, services/instance-alerts.ts). Omitting it is the
 * user's own dismissal -- the × on the card -- which takes the card down
 * regardless of who else is holding it, because the person looking at it has
 * decided they are done with it.
 *
 * A stale or already-dismissed id is a no-op.
 */
export function dismissNotice(id: number, owner = ""): void {
  const index = notices.findIndex((notice) => notice.id === id);
  if (index < 0) return;
  const notice = notices[index]!;
  if (owner) {
    notice.owners.delete(owner);
    if (notice.owners.size || notice.anonymous) return;
  }
  clearTimer(id);
  notices.splice(index, 1);
}

/** Removes every entry. Backs bridge.ts's `showMessage("")` clear-all path. */
export function dismissAllNotices(): void {
  for (const notice of notices) clearTimer(notice.id);
  notices.length = 0;
}
