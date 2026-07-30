import assert from "node:assert/strict";
import test from "node:test";

import { filterRules, formatRuleProxy, normalizeRules } from "./views/detail/rules-data.ts";
import type { MihomoRule } from "./views/detail/rules-data.ts";

test("normalizeRules coerces a well-formed mihomo payload", () => {
  const raw = [
    { type: "DOMAIN-SUFFIX", payload: "google.com", proxy: "Proxy", size: 3 },
    { type: "MATCH", payload: "", proxy: "DIRECT" },
  ];
  assert.deepEqual(normalizeRules(raw), [
    { type: "DOMAIN-SUFFIX", payload: "google.com", proxy: "Proxy", size: 3 },
    { type: "MATCH", payload: "", proxy: "DIRECT", size: undefined },
  ]);
});

test("normalizeRules defends against malformed/partial entries instead of throwing", () => {
  assert.deepEqual(normalizeRules(undefined), []);
  assert.deepEqual(normalizeRules(null), []);
  assert.deepEqual(normalizeRules("not an array"), []);
  assert.deepEqual(
    normalizeRules([null, 42, "x", { type: 7, payload: null, proxy: {} }, { type: "MATCH", proxy: "DIRECT", size: "not-a-number" }]),
    [
      { type: "", payload: "", proxy: "", size: undefined },
      { type: "MATCH", payload: "", proxy: "DIRECT", size: undefined },
    ],
  );
});

test("filterRules matches type, payload, or proxy case-insensitively", () => {
  const rules: MihomoRule[] = [
    { type: "DOMAIN-SUFFIX", payload: "google.com", proxy: "Proxy" },
    { type: "DOMAIN-KEYWORD", payload: "github", proxy: "DIRECT" },
    { type: "MATCH", payload: "", proxy: "Final" },
  ];
  assert.deepEqual(filterRules(rules, ""), rules);
  assert.deepEqual(filterRules(rules, "   "), rules);
  assert.deepEqual(filterRules(rules, "GOOGLE"), [rules[0]]);
  assert.deepEqual(filterRules(rules, "direct"), [rules[1]]);
  assert.deepEqual(filterRules(rules, "match"), [rules[2]]);
  assert.deepEqual(filterRules(rules, "no-such-match"), []);
});

test("formatRuleProxy falls back to an em dash for an empty proxy", () => {
  assert.equal(formatRuleProxy({ type: "MATCH", payload: "", proxy: "DIRECT" }), "DIRECT");
  assert.equal(formatRuleProxy({ type: "MATCH", payload: "", proxy: "" }), "—");
});
