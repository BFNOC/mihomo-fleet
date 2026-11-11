// resetGeoLookups(): after a geo database install/update the connection table
// must ask again, both past the "no database" latch and for addresses already
// cached, and a lookup started before the reset must not repopulate the cache.
import test from "node:test";
import assert from "node:assert/strict";

const g = globalThis as unknown as Record<string, unknown>;
g.document = { createElement: () => ({ style: {} }) };
g.localStorage = { getItem: () => null, setItem: () => {}, removeItem: () => {} };

const { requestGeo, resetGeoLookups, resolveGeo, setGeoResolver } = await import("./dashboard.ts");
import type { GeoLookupResult } from "./dashboard.ts";

const flush = (): Promise<void> => new Promise((resolve) => setTimeout(resolve, 0));
const row = (ip: string) => ({ ip });

test("reset lifts the no-database latch and re-asks cached addresses", async () => {
  let answer: GeoLookupResult = { available: false, asnAvailable: false };
  const asked: string[][] = [];
  setGeoResolver(async (ips) => {
    asked.push(ips);
    return answer;
  });
  resetGeoLookups();

  requestGeo([row("8.8.8.8")]);
  await flush();
  requestGeo([row("1.1.1.1")]);
  await flush();
  assert.equal(asked.length, 1, "with no database, later lookups are suppressed");

  answer = { available: true, countries: { "8.8.8.8": "US" }, asnAvailable: false };
  resetGeoLookups();
  requestGeo([row("8.8.8.8")]);
  await flush();
  assert.equal(asked.length, 2, "reset must allow lookups again");
  assert.equal(resolveGeo("8.8.8.8").country, "US", "previously cached miss must be re-resolved");
});

test("a lookup in flight across a reset does not write its stale answer", async () => {
  let release: (value: GeoLookupResult) => void = () => {};
  setGeoResolver(() => new Promise<GeoLookupResult>((resolve) => { release = resolve; }));
  resetGeoLookups();

  requestGeo([row("9.9.9.9")]);
  resetGeoLookups();
  release({ available: true, countries: { "9.9.9.9": "OLD" } });
  await flush();
  assert.equal(resolveGeo("9.9.9.9").country, "");
});
