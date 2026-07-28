import { nextTick, onMounted, onUnmounted, ref } from "vue";
import type { Ref } from "vue";

const maxRowHeight = 120;
const resizeDebounceMs = 150;

export interface RowBudgetOptions {
  /** Rows shown before the first measurement lands. */
  initial: number;
  /**
   * Rows shown when the window is too short for viewport-fit mode. The page
   * scrolls anyway there, so this is "a useful slice", not "what fits".
   */
  scrollFallback: number;
}

export interface RowBudget {
  budget: Ref<number>;
  bodyEl: Ref<HTMLElement | null>;
  tableEl: Ref<HTMLTableElement | null>;
}

/**
 * Measures how many rows one table may show, and keeps that number current as
 * the dashboard resizes. Each table owns its own instance -- the pre-Vue code
 * measured both from a single function, which meant the connections table and
 * the instances table could not be moved into separate components.
 *
 * `--dash-fit` is the custom property styles.css sets to mark viewport-fit mode
 * (see DESIGN.md). Outside that mode nothing is measured at all.
 */
export function useRowBudget({ initial, scrollFallback }: RowBudgetOptions): RowBudget {
  const budget = ref(initial);
  const bodyEl = ref<HTMLElement | null>(null);
  const tableEl = ref<HTMLTableElement | null>(null);

  // #dashboardPanel is the host main.ts mounts the dashboard into. The cards
  // have no single wrapping element of their own to hang a template ref on
  // (adding one would break styles.css's `.dashboard > *` rules), so the host is
  // read back by its stable id.
  let hostEl: HTMLElement | null = null;
  let resizeObserver: ResizeObserver | null = null;
  let fitTimer: ReturnType<typeof setTimeout> | null = null;

  function fitModeActive(): boolean {
    if (!hostEl || typeof getComputedStyle !== "function") return false;
    return getComputedStyle(hostEl).getPropertyValue("--dash-fit").trim() === "1";
  }

  function applyFit(): void {
    if (!fitModeActive()) {
      budget.value = scrollFallback;
      return;
    }
    const body = bodyEl.value;
    const table = tableEl.value;
    const firstRow = table?.tBodies?.[0]?.rows?.[0];
    if (!body || !table || !firstRow || !body.clientHeight) return;
    const rowHeight = Math.min(firstRow.offsetHeight || 0, maxRowHeight);
    if (rowHeight <= 0) return;
    const available = body.clientHeight - (table.tHead?.offsetHeight || 0);
    const fits = Math.max(1, Math.floor(available / rowHeight));
    if (fits !== budget.value) budget.value = fits;
  }

  function scheduleFit(): void {
    if (fitTimer !== null) clearTimeout(fitTimer);
    fitTimer = setTimeout(() => {
      fitTimer = null;
      applyFit();
    }, resizeDebounceMs);
  }

  onMounted(() => {
    hostEl = document.getElementById("dashboardPanel");
    applyFit();
    // One corrective pass: the first measures against the default-budget render,
    // and a budget change from that pass can itself change row height once Vue
    // repaints with it.
    void nextTick(() => applyFit());
    // A resized host also covers the show/hide toggle main.ts drives via the
    // `.hidden` class: a `display: none` element reports no box to
    // ResizeObserver, and becoming visible again delivers a resize entry with
    // its real size, so no separate "on view change" hook is needed.
    if (typeof ResizeObserver === "function" && hostEl) {
      resizeObserver = new ResizeObserver(() => scheduleFit());
      resizeObserver.observe(hostEl);
    }
  });

  onUnmounted(() => {
    resizeObserver?.disconnect();
    if (fitTimer !== null) clearTimeout(fitTimer);
  });

  return { budget, bodyEl, tableEl };
}
