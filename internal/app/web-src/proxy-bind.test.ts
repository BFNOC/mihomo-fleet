import assert from "node:assert/strict";
import test from "node:test";

import {
  bindKey,
  bindListIncludes,
  bindListPreview,
  joinBindList,
  splitBindList,
  toggleBindAddress,
  validateBindAddress,
} from "./proxy-bind.ts";

test("splitBindList and joinBindList round-trip the stored field", () => {
  assert.deepEqual(splitBindList("127.0.0.1, 192.168.64.1 ,"), ["127.0.0.1", "192.168.64.1"]);
  assert.deepEqual(splitBindList(""), []);
  assert.deepEqual(splitBindList(null), []);
  assert.equal(joinBindList(["127.0.0.1", "0.0.0.0"]), "127.0.0.1,0.0.0.0");
});

test("bindKey folds the aliases the backend folds", () => {
  assert.equal(bindKey("localhost"), "127.0.0.1");
  assert.equal(bindKey("LOCALHOST."), "127.0.0.1");
  assert.equal(bindKey("all"), "0.0.0.0");
  assert.equal(bindKey("*"), "0.0.0.0");
  assert.equal(bindKey("[fe80::1%en0]"), "fe80::1%en0");
});

test("toggle uses the folded key so an alias unchecks its address", () => {
  assert.deepEqual(toggleBindAddress(["127.0.0.1"], "192.168.64.1"), ["127.0.0.1", "192.168.64.1"]);
  assert.deepEqual(toggleBindAddress(["localhost", "192.168.64.1"], "127.0.0.1"), ["192.168.64.1"]);
  assert.equal(bindListIncludes(["all"], "0.0.0.0"), true);
  assert.equal(bindListIncludes(["127.0.0.1"], "0.0.0.0"), false);
});

test("validateBindAddress mirrors normalizeProxyBindAddress", () => {
  for (const ok of ["127.0.0.1", "0.0.0.0", "localhost", "all", "*", "::1", "fe80::1%en0", "[::1]", "::ffff:192.168.0.1"]) {
    assert.equal(validateBindAddress(ok), "", `${ok} should be accepted`);
  }
  assert.match(validateBindAddress("127.0.0.1:7890"), /不要写端口/);
  assert.match(validateBindAddress("[::1"), /方括号不完整/);
  assert.match(validateBindAddress("192.168.0.0/24"), /无效/);
  assert.match(validateBindAddress("example.com"), /请填写 IP/);
  // net.ParseIP rejects leading zeros, so the picker must too.
  assert.match(validateBindAddress("01.2.3.4"), /请填写 IP/);
  assert.match(validateBindAddress("1:2:3"), /请填写 IP/);
  assert.equal(validateBindAddress("  "), "请输入地址。");
});

test("bindListPreview coalesces IPv4 behind the wildcard", () => {
  assert.deepEqual(bindListPreview(["127.0.0.1", "192.168.64.1"]), ["127.0.0.1", "192.168.64.1"]);
  // 0.0.0.0 already covers every IPv4 listener; keeping them implies it does not.
  assert.deepEqual(bindListPreview(["127.0.0.1", "all", "::1"]), ["0.0.0.0", "::1"]);
  assert.deepEqual(bindListPreview(["localhost", "127.0.0.1"]), ["localhost"]);
});
