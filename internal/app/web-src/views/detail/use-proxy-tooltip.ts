import { nextTick, onMounted, onUnmounted, ref } from "vue";
import type { Ref } from "vue";

const edgeGap = 8;
const buttonGap = 8;

export interface ProxyTooltip {
  tooltipEl: Ref<HTMLElement | null>;
  tooltipVisible: Ref<boolean>;
  tooltipText: Ref<string>;
  tooltipLeft: Ref<string>;
  tooltipTop: Ref<string>;
  showTooltip: (button: HTMLElement, text: string) => void;
  showTooltipFromPointer: (event: PointerEvent, text: string) => void;
  hideTooltip: () => void;
}

/**
 * Hover/focus tooltip for proxy buttons, whose full names are usually elided.
 *
 * The element it drives is rendered through <Teleport to="body">, replacing the
 * pre-Vue module-scope `document.createElement("div")` appended straight to
 * document.body. Teleport gives the same "not clipped by any ancestor's
 * overflow" placement while keeping the node's lifecycle Vue-owned.
 *
 * Positioning is ported unchanged from the pre-Vue showProxyTooltip(). The
 * hover wiring is simpler though: the original listened for bubbling
 * pointerover/pointerout on the whole list and filtered by `event.relatedTarget`
 * to ignore moves within one button; pointerenter/pointerleave do not bubble, so
 * binding them per button needs no such filtering.
 */
export function useProxyTooltip(): ProxyTooltip {
  const tooltipEl = ref<HTMLElement | null>(null);
  const tooltipVisible = ref(false);
  const tooltipText = ref("");
  const tooltipLeft = ref("0px");
  const tooltipTop = ref("0px");
  const hoverQuery = window.matchMedia("(hover: hover) and (pointer: fine)");

  function positionTooltip(button: HTMLElement): void {
    const tooltip = tooltipEl.value;
    if (!tooltip) return;
    const buttonRect = button.getBoundingClientRect();
    const tooltipRect = tooltip.getBoundingClientRect();
    const maxLeft = Math.max(edgeGap, window.innerWidth - tooltipRect.width - edgeGap);
    const maxTop = Math.max(edgeGap, window.innerHeight - tooltipRect.height - edgeGap);
    const left = Math.min(Math.max(buttonRect.left, edgeGap), maxLeft);
    // Prefer above the button; flip below when there is no room.
    let top = buttonRect.top - tooltipRect.height - buttonGap;
    if (top < edgeGap) top = buttonRect.bottom + buttonGap;
    top = Math.min(Math.max(top, edgeGap), maxTop);
    tooltipLeft.value = `${left}px`;
    tooltipTop.value = `${top}px`;
  }

  function showTooltip(button: HTMLElement, text: string): void {
    if (!text) return;
    tooltipText.value = text;
    tooltipVisible.value = true;
    void nextTick(() => positionTooltip(button));
  }

  // Touch and coarse pointers get no hover tooltip -- it would latch open.
  function showTooltipFromPointer(event: PointerEvent, text: string): void {
    if (!hoverQuery.matches) return;
    showTooltip(event.currentTarget as HTMLElement, text);
  }

  function hideTooltip(): void {
    tooltipVisible.value = false;
  }

  onMounted(() => {
    window.addEventListener("resize", hideTooltip);
    window.addEventListener("scroll", hideTooltip, true);
  });
  onUnmounted(() => {
    window.removeEventListener("resize", hideTooltip);
    window.removeEventListener("scroll", hideTooltip, true);
  });

  return { tooltipEl, tooltipVisible, tooltipText, tooltipLeft, tooltipTop, showTooltip, showTooltipFromPointer, hideTooltip };
}
