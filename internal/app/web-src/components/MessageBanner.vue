<script setup lang="ts">
// Vue replacement for app.ts's showMessage()/#message div (pre-Vue app.ts,
// index.html:53). app.ts (or whatever future call site owns the equivalent
// of showMessage()) writes `banner.text`/`banner.tone`; this component is
// now the only thing that reads that state and puts it on screen.
import { computed, onUnmounted, watch } from "vue";
import { actions, banner } from "../bridge.ts";
import { localizedMessage } from "../messages.ts";

// localizedMessage() used to run right where the old showMessage() wrote to
// the DOM (pre-Vue app.ts), not at any of its ~25 call sites -- those just pass
// arbitrary text (raw backend errors *and* plain Chinese literals) and let
// showMessage() sort it out. This component is the new DOM-writing boundary,
// so it keeps that transform here rather than pushing it onto whatever
// writes `banner.text`:
//   - it guarantees "no raw backend string reaches the DOM untranslated"
//     from a single place, independent of how banner.text ends up getting
//     set -- showMessage() now lives in bridge.ts and only assigns the raw
//     string plus a tone, which is the settled contract, not a moving target;
//   - it keeps the translation table (messages.ts/constants.ts) out of caller
//     code, matching how it was isolated before.
// localizedMessage() is a lookup-or-passthrough -- text that doesn't match a
// known backend error string/pattern (which includes any already-Chinese
// text) comes back unchanged -- so this is also safe even if a caller still
// pre-localizes before writing `banner.text`. That should not be relied on
// as the contract, though: banner.text is meant to carry the raw string.
const displayText = computed(() => localizedMessage(banner.text));
const isError = computed(() => banner.tone === "error");

// Mirrors app.ts's messageClearTimer: non-error messages auto-dismiss after
// 6s (pre-Vue app.ts). Errors are never auto-dismissed.
let dismissTimer: ReturnType<typeof setTimeout> | null = null;

function clearDismissTimer(): void {
  if (dismissTimer !== null) {
    clearTimeout(dismissTimer);
    dismissTimer = null;
  }
}

// Watches the whole banner object (not just `.text`) so a tone-only change
// also resets the timer, matching the old function's behaviour of
// re-evaluating from scratch on every call.
//
// banner.seq is why this still fires when a call repeats the exact same text:
// writing an unchanged value into a reactive object's property is not a
// mutation as far as Vue's proxy is concerned (Object.is comparison against
// the old value), so two showMessage() calls with identical text back-to-back
// would otherwise never re-trigger this watcher, leaving the *first* call's 6s
// timer running -- e.g. clicking 启动 twice 5s apart made the second
// "已请求启动。" vanish after 1s instead of getting its own 6s. showMessage()
// increments `seq` unconditionally on every call, so the watched object always
// has at least one property that genuinely changed.
watch(banner, ({ text, tone }) => {
  clearDismissTimer();
  if (!text || tone === "error") return;
  dismissTimer = setTimeout(() => {
    banner.text = "";
  }, 6000);
});

// Clears any pending timer on unmount so it cannot fire `banner.text = ""`
// against a component that no longer exists.
onUnmounted(clearDismissTimer);
</script>

<template>
  <!--
    Stays mounted (rather than v-if) and toggles the existing `.hidden`
    utility class (styles.css: `.hidden { display: none !important }`, the
    same pattern index.html already uses for #systemWarning/#dashboardPanel/
    #profilePanel) instead of unmounting the element. Two reasons:
      - aria-live="polite" only reliably announces updates to a node that
        was already present in the accessibility tree before its content
        changed; v-if would recreate the live region on every show, which
        risks the first announcement after each reappearance being dropped.
      - `.message`/`.message.error` in styles.css are the only rules this
        element needs, and `.hidden` already exists for exactly this "keep
        the node, hide it" case -- no new class names needed.
  -->
  <div
    id="message"
    class="message"
    :class="{ error: isError, hidden: !displayText }"
    :role="isError ? 'alert' : 'status'"
    aria-live="polite"
  >
    <span class="message-text">{{ displayText }}</span>
    <button
      type="button"
      class="message-dismiss"
      aria-label="关闭提示"
      title="关闭提示"
      @click="actions.dismissMessage()"
    >×</button>
  </div>
</template>

<style scoped>
/*
 * Minimal layout for the dismiss control -- workbench.css's shared `.message`
 * rule only ever had to lay out plain text, so the flex row lives here rather
 * than in styles/ (owned elsewhere in this batch). Scoped, so it cannot leak
 * onto any other `.message` usage; button color/border/focus-visible all
 * still come from the global button rules in styles/shell.css.
 */
.message {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}

.message-text {
  flex: 1;
  min-width: 0;
}

.message-dismiss {
  flex-shrink: 0;
  min-height: 0;
  width: 20px;
  height: 20px;
  padding: 0;
  line-height: 1;
  display: grid;
  place-items: center;
  border-radius: 999px;
  border-color: transparent;
  background: transparent;
  box-shadow: none;
  color: inherit;
}

.message-dismiss:hover {
  background: color-mix(in srgb, currentColor 14%, transparent);
}
</style>
