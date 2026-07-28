<script setup lang="ts">
// Vue replacement for app.ts's showMessage()/#message div (app.ts:259-276,
// index.html:53). app.ts (or whatever future call site owns the equivalent
// of showMessage()) writes `banner.text`/`banner.tone`; this component is
// now the only thing that reads that state and puts it on screen.
import { computed, onUnmounted, watch } from "vue";
import { banner } from "../bridge.ts";
import { localizedMessage } from "../i18n.ts";

// localizedMessage() used to run right where the old showMessage() wrote to
// the DOM (app.ts:270), not at any of its ~25 call sites -- those just pass
// arbitrary text (raw backend errors *and* plain Chinese literals) and let
// showMessage() sort it out. This component is the new DOM-writing boundary,
// so it keeps that transform here rather than pushing it onto whatever
// writes `banner.text`:
//   - it guarantees "no raw backend string reaches the DOM untranslated"
//     from a single place, independent of how banner.text ends up getting
//     set (app.ts's showMessage() is being migrated concurrently by another
//     agent; this component must be correct without depending on the shape
//     that migration lands in);
//   - it keeps the translation table (i18n.ts/constants.ts) out of caller
//     code, matching how it was isolated before.
// localizedMessage() is a lookup-or-passthrough -- text that doesn't match a
// known backend error string/pattern (which includes any already-Chinese
// text) comes back unchanged -- so this is also safe even if a caller still
// pre-localizes before writing `banner.text`. That should not be relied on
// as the contract, though: banner.text is meant to carry the raw string.
const displayText = computed(() => localizedMessage(banner.text));
const isError = computed(() => banner.tone === "error");

// Mirrors app.ts's messageClearTimer: non-error messages auto-dismiss after
// 6s (app.ts:273-275). Errors are never auto-dismissed.
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
  >{{ displayText }}</div>
</template>
