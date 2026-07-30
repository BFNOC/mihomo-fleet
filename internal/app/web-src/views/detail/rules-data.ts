// Fetch + filter/format logic for RulesTab.vue (#tab-rules). Deliberately
// framework-free -- no vue, no store.ts -- so normalizeRules()/filterRules()
// below can be unit-tested with plain node:test the same way format.ts and
// chain-rules.ts are, instead of dragging in store.ts's reactive(createState())
// (which reads localStorage at module load and throws under plain Node).
//
// RulesTab.vue owns the actual refs (rules/filterText/loading/error) and calls
// fetchRules(instanceId) directly, mirroring LogsTab.vue's pattern (state local
// to the component) rather than proxy-groups.ts's module-scope refs -- there is
// no cross-component consumer of the rule list, so there is nothing to share.
import { api } from "../../api.ts";

/** One entry of mihomo's `GET /rules` response. */
export interface MihomoRule {
  type: string;
  payload: string;
  proxy: string;
  size?: number;
}

// mihomo's own JSON shape, read as `unknown` and narrowed field-by-field below
// -- same posture as api.ts's ApiErrorBody: never trust an external process's
// response to already match the interface above.
interface RawMihomoRule {
  type?: unknown;
  payload?: unknown;
  proxy?: unknown;
  size?: unknown;
}

interface RulesResponse {
  rules?: RawMihomoRule[];
}

/**
 * Coerces mihomo's raw `/rules` payload into MihomoRule[], dropping anything
 * that is not an object and defaulting missing/mistyped string fields to ""
 * rather than throwing -- one malformed entry must not blank the whole list.
 */
export function normalizeRules(raw: unknown): MihomoRule[] {
  if (!Array.isArray(raw)) return [];
  const rules: MihomoRule[] = [];
  for (const entry of raw) {
    if (!entry || typeof entry !== "object") continue;
    const item = entry as RawMihomoRule;
    rules.push({
      type: typeof item.type === "string" ? item.type : "",
      payload: typeof item.payload === "string" ? item.payload : "",
      proxy: typeof item.proxy === "string" ? item.proxy : "",
      size: typeof item.size === "number" ? item.size : undefined,
    });
  }
  return rules;
}

/**
 * Case-insensitive substring match against type/payload/proxy -- the three
 * columns RulesTab.vue renders, so "what you can see is what you can filter
 * by" holds. A blank filter returns `rules` unchanged (including its identity
 * when there is nothing to drop), matching filterRuntimeProxyGroups()'s style
 * in format.ts.
 */
export function filterRules(rules: MihomoRule[], filterText: string): MihomoRule[] {
  const filter = filterText.trim().toLowerCase();
  if (!filter) return rules;
  return rules.filter(
    (rule) =>
      rule.type.toLowerCase().includes(filter) ||
      rule.payload.toLowerCase().includes(filter) ||
      rule.proxy.toLowerCase().includes(filter),
  );
}

/** The proxy column's display text -- mihomo leaves this "" for some rule types (e.g. MATCH with no explicit target only if misconfigured); render an em dash instead of a blank cell. */
export function formatRuleProxy(rule: MihomoRule): string {
  return rule.proxy || "—";
}

/** GET /api/mihomo/{id}/rules through the generic passthrough (controller.go's handleMihomoProxy), normalized. */
export async function fetchRules(instanceId: string): Promise<MihomoRule[]> {
  const payload = await api<RulesResponse>(`/api/mihomo/${encodeURIComponent(instanceId)}/rules`);
  return normalizeRules(payload.rules);
}
