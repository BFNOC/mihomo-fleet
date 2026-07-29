import { nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import type { Ref } from "vue";

const maxRowHeight = 120;
const resizeDebounceMs = 150;

export interface RowBudgetOptions {
  /** Rows shown before the first measurement lands. */
  initial: number;
}

export interface RowBudget {
  budget: Ref<number>;
  bodyEl: Ref<HTMLElement | null>;
  tableEl: Ref<HTMLTableElement | null>;
}

/**
 * Measures how many rows a table may show, and keeps that number current as the
 * dashboard resizes. Written as a `useX()` factory rather than module state
 * because the caller owns the elements being measured; the pre-Vue code measured
 * both dashboard tables from a single function, which is what kept them from
 * being separate components.
 *
 * Only the instance table uses this now -- the connection table scrolls inside
 * its own card instead (DashboardConnections.vue, dashboard-tables.css).
 *
 * `--dash-fit` is the custom property styles.css sets to mark viewport-fit mode
 * (see DESIGN.md). Outside that mode nothing is measured and nothing is capped:
 * the page scrolls there, so a cut would hide rows for no gain.
 */
export function useRowBudget({ initial }: RowBudgetOptions): RowBudget {
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
      budget.value = Infinity;
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

  // The table conditionally renders (DashboardInstances.vue's `v-else`) while
  // its data is empty, so `tableEl` starts null and mount's applyFit() above
  // hits the `!table` early return, leaving budget stuck at its initial value.
  // The table appearing later (zero instances -> some) does not resize
  // #dashboardPanel -- the host's own box is fixed by the grid layout, so the
  // ResizeObserver above never fires for it either. Watching the ref itself
  // catches exactly that empty -> non-empty transition, independent of both
  // triggers above.
  watch(tableEl, (next, previous) => {
    if (next && !previous) {
      applyFit();
      void nextTick(() => applyFit());
    }
  });

  onUnmounted(() => {
    resizeObserver?.disconnect();
    if (fitTimer !== null) clearTimeout(fitTimer);
  });

  return { budget, bodyEl, tableEl };
}
