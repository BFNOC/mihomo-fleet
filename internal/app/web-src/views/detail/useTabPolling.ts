import { onUnmounted, watch } from "vue";
import type { Ref } from "vue";
import { fastPollIntervalMs } from "../../constants.ts";

/**
 * Runs `fetcher` immediately whenever `active` becomes true (or `key`
 * changes while already active), then keeps re-running it every
 * `fastPollIntervalMs` for as long as `active` stays true.
 *
 * Mirrors app.ts's scheduleFastPoll()/runFastPoll() self-rescheduling
 * setTimeout loop -- paused while `document.hidden`, resumed on the next
 * 'visibilitychange' -- but scoped to a single tab component's lifetime
 * instead of driving the whole app's poll cycle. app.ts's own fast poll
 * still runs independently (it drives the dashboard/traffic sampling, which
 * this view does not own); this is a second, narrower poll loop just for
 * whichever detail tab is both mounted and currently visible.
 */
export function useTabPolling(active: Ref<boolean>, key: Ref<string>, fetcher: () => Promise<void>): void {
  let timer: ReturnType<typeof setTimeout> | null = null;

  function stop(): void {
    if (timer !== null) {
      clearTimeout(timer);
      timer = null;
    }
  }

  function schedule(): void {
    stop();
    if (document.hidden || !active.value) return;
    timer = setTimeout(async () => {
      timer = null;
      if (!document.hidden && active.value) await fetcher();
      if (active.value) schedule();
    }, fastPollIntervalMs);
  }

  function onVisible(): void {
    if (document.hidden || !active.value) return;
    void fetcher();
    schedule();
  }

  watch(
    [active, key],
    ([isActive]) => {
      if (!isActive) {
        stop();
        return;
      }
      // Guards the immediate fetch the same way onVisible()/the timer
      // callback below already guard theirs. In practice `isActive` can only
      // just have become true, or `key` can only just have changed, as a
      // result of user interaction (a tab click, an instance-selector
      // change) or this component's own mount -- none of which can happen
      // while `document.hidden`, since a backgrounded tab receives no
      // input events. The check is here anyway so "never fetches while
      // hidden" is a property of this function's code, not an inference
      // left to the reader.
      if (!document.hidden) void fetcher();
      schedule();
    },
    { immediate: true },
  );

  document.addEventListener("visibilitychange", onVisible);
  onUnmounted(() => {
    stop();
    document.removeEventListener("visibilitychange", onVisible);
  });
}
