<script setup lang="ts">
// Reusable up/down dual-sparkline SVG. Used by DashboardView.vue's metrics
// strip, trend card, selected-instance card, and per-row instance sparkline
// (four call sites, one geometry implementation).
//
// Mirrors dashboard.ts's old dualSparkline()/sparkHead() string-builders
// exactly -- same classes, same draw order (down fill, down line, up fill,
// up line, then the two head dots) -- but emits bound SVG attributes instead
// of concatenating an HTML string. sparklineGeometry() itself (traffic.ts) is
// untouched, pure, and covered by the existing unit tests; only the DOM
// presentation moved here, per the migration split (pure geometry stays in
// traffic.ts, rendering becomes a template).
import { computed } from "vue";
import { seriesField, seriesPeak, sparklineGeometry } from "../../traffic.ts";
import type { SparklineGeometry, TrafficSeries } from "../../traffic.ts";

const props = defineProps<{
  series: TrafficSeries;
  width: number;
  height: number;
}>();

const upValues = computed(() => seriesField(props.series, "up"));
const downValues = computed(() => seriesField(props.series, "down"));

// Both directions share one ceiling so their vertical scales stay directly
// comparable -- matches dualSparkline()'s `max: ceiling` option.
const ceiling = computed(() => Math.max(seriesPeak(props.series, "up"), seriesPeak(props.series, "down")));

// A direction with no samples yet still draws a flat line at zero rather than
// nothing, matching dualSparkline()'s `down.length ? down : [0]` fallback.
const upGeometry = computed<SparklineGeometry | null>(() =>
  sparklineGeometry(upValues.value.length ? upValues.value : [0], { width: props.width, height: props.height, max: ceiling.value }),
);
const downGeometry = computed<SparklineGeometry | null>(() =>
  sparklineGeometry(downValues.value.length ? downValues.value : [0], { width: props.width, height: props.height, max: ceiling.value }),
);

// True only when there is real data to show; an all-empty series renders a
// bare <svg> with no paths, matching dualSparkline()'s early return.
const hasData = computed(() => upValues.value.length > 0 || downValues.value.length > 0);

// The newest sample is the one number the chart is actually being watched
// for, so it gets a dot -- drawn as a zero-length round-capped stroke (not a
// <circle>) because the SVG scales non-uniformly (preserveAspectRatio="none"),
// which would squash a circle into an ellipse but leaves a round cap round.
function headPoint(geometry: SparklineGeometry | null): { x: number; y: number } | null {
  if (!geometry || !geometry.points.length) return null;
  return geometry.points[geometry.points.length - 1] ?? null;
}
const upHead = computed(() => headPoint(upGeometry.value));
const downHead = computed(() => headPoint(downGeometry.value));
</script>

<template>
  <svg class="spark spark-dual" :viewBox="`0 0 ${width} ${height}`" preserveAspectRatio="none" aria-hidden="true">
    <template v-if="hasData">
      <path v-if="downGeometry" class="spark-area spark-down-fill" :d="downGeometry.area" />
      <path v-if="downGeometry" class="spark-line spark-down-stroke" :d="downGeometry.line" />
      <path v-if="upGeometry" class="spark-area spark-up-fill" :d="upGeometry.area" />
      <path v-if="upGeometry" class="spark-line spark-up-stroke" :d="upGeometry.line" />
      <path
        v-if="downHead"
        class="spark-head spark-down-head"
        :d="`M${downHead.x} ${downHead.y}L${downHead.x} ${downHead.y}`"
        vector-effect="non-scaling-stroke"
      />
      <path
        v-if="upHead"
        class="spark-head spark-up-head"
        :d="`M${upHead.x} ${upHead.y}L${upHead.x} ${upHead.y}`"
        vector-effect="non-scaling-stroke"
      />
    </template>
  </svg>
</template>
