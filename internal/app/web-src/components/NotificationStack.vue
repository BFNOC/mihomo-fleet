<script setup lang="ts">
// Renders notifications.ts's queue as a fixed overlay in the top-right corner.
// Replaces MessageBanner.vue, which put a single message in the content
// column's normal flow, where it could sit above the fold and go unread.
//
// This component holds no state and owns no timers -- notifications.ts owns
// both, because the stack mounts once for the app's lifetime and there is
// nothing per-instance to scope them to.
import { dismissNotice, notices } from "../notifications.ts";
import type { Notice } from "../notifications.ts";
import { localizedMessage } from "../messages.ts";

// Inherited verbatim from MessageBanner.vue, and still the reason this
// translation is not done at push time: the ~25 showMessage() call sites pass
// arbitrary text -- raw backend errors *and* plain Chinese literals -- and this
// is the single boundary every one of them passes through on the way to the
// DOM. localizedMessage() is a lookup-or-passthrough, so text with no matching
// pattern (which includes anything already Chinese) comes back unchanged.
function displayText(notice: Notice): string {
  return localizedMessage(notice.text);
}

/**
 * Pins a leaving card to where it already is.
 *
 * styles/notifications.css takes leaving cards out of flow so the survivors can
 * slide up over them, but an absolutely positioned child of a flex container
 * gets its static position from the container's content-box start -- the top.
 * Dismissing the second or third card therefore made it jump to the top of the
 * stack, land on the first card, and fade out from there. Reading offsetTop in
 * before-leave (while the element is still in flow) and writing it back is what
 * keeps it in place.
 */
function pinLeavingCard(el: Element): void {
  if (el instanceof HTMLElement) el.style.top = `${el.offsetTop}px`;
}
</script>

<template>
  <!--
    aria-live on the container, not on each card. The container is present from
    boot, so screen readers have it in the accessibility tree before anything
    is inserted -- announcing a node that only appears at the moment of its
    first update is unreliable, which is why MessageBanner.vue kept its element
    mounted and toggled `.hidden` instead of using v-if. A list has no single
    element to keep mounted, so the live region moves up one level and the
    cards themselves come and go freely.

    `role` per card is kept from the banner: errors are assertive alerts, plain
    messages are status updates.
  -->
  <div class="notification-stack" aria-live="polite" aria-relevant="additions">
    <TransitionGroup name="notification" @before-leave="pinLeavingCard">
      <article
        v-for="notice in notices"
        :key="notice.id"
        class="notification"
        :class="`notification-${notice.tone}`"
        :role="notice.tone === 'error' ? 'alert' : 'status'"
      >
        <span class="notification-icon" aria-hidden="true">{{ notice.tone === "error" ? "!" : "i" }}</span>
        <p class="notification-text">{{ displayText(notice) }}</p>
        <!--
          The repeat counter is why the queue dedups rather than stacking
          identical cards: a backend that is down produces the same message on
          every poll, and "the same failure, 7 times" is the useful reading of
          that, not seven cards.
        -->
        <span v-if="notice.count > 1" class="notification-count">×{{ notice.count }}</span>
        <button
          type="button"
          class="notification-dismiss"
          aria-label="关闭提示"
          title="关闭提示"
          @click="dismissNotice(notice.id)"
        >×</button>
      </article>
    </TransitionGroup>
  </div>
</template>
