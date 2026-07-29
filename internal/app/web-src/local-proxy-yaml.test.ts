import assert from "node:assert/strict";
import test from "node:test";

import {
  appendLocalProxyYaml,
  buildLocalProxyYaml,
  localProxyFormDefaults,
  localProxyTypes,
} from "./local-proxy-yaml.ts";

test("defaults come from the type definition", () => {
  assert.equal(localProxyFormDefaults("ss").cipher, "aes-128-gcm");
  assert.equal(localProxyFormDefaults("vmess").alterId, "0");
  assert.deepEqual(localProxyFormDefaults("nope"), {});
});

test("a generated shadowsocks node emits name and type first", () => {
  const { yaml, error } = buildLocalProxyYaml("ss", {
    name: "local-hop",
    server: "127.0.0.1",
    port: "44212",
    cipher: "aes-128-gcm",
    password: "s3cret",
  });
  assert.equal(error, "");
  assert.equal(
    yaml,
    [
      '- name: local-hop',
      '  type: ss',
      '  server: 127.0.0.1',
      '  port: 44212',
      '  cipher: aes-128-gcm',
      '  password: s3cret',
    ].join("\n"),
  );
});

test("scalars that YAML would retype are quoted", () => {
  const { yaml } = buildLocalProxyYaml("ss", {
    name: "123",
    server: "127.0.0.1",
    port: "443",
    cipher: "aes-128-gcm",
    password: 'no" yes',
  });
  assert.match(yaml, /- name: "123"/);
  assert.match(yaml, /password: "no\\" yes"/);
});

test("required, numeric and duplicate rules refuse before the backend does", () => {
  assert.match(buildLocalProxyYaml("ss", { name: "", server: "1.1.1.1", port: "1" }).error, /请填写名称/);
  assert.match(buildLocalProxyYaml("ss", { name: "a", server: "1.1.1.1", port: "0", cipher: "aes-128-gcm", password: "p" }).error, /1-65535/);
  assert.match(buildLocalProxyYaml("ss", { name: "a", server: "1.1.1.1", port: "x", cipher: "aes-128-gcm", password: "p" }).error, /必须是数字/);
  assert.match(
    buildLocalProxyYaml("ss", { name: "dup", server: "1.1.1.1", port: "1", cipher: "aes-128-gcm", password: "p" }, ["dup"]).error,
    /重名/,
  );
  assert.match(buildLocalProxyYaml("nope", {}).error, /选择节点类型/);
});

test("optional fields drop out and every type stays generatable", () => {
  const { yaml } = buildLocalProxyYaml("socks5", { name: "s", server: "1.1.1.1", port: "1080" });
  assert.equal(yaml.includes("username"), false);
  for (const def of localProxyTypes) {
    const values: Record<string, string> = {};
    for (const field of def.fields) {
      values[field.key] = field.defaultValue || (field.kind === "number" ? "1" : `${field.key}-value`);
    }
    values.name = "n";
    values.server = "1.1.1.1";
    values.port = "443";
    const result = buildLocalProxyYaml(def.type, values);
    assert.equal(result.error, "", `${def.type}: ${result.error}`);
    assert.match(result.yaml, new RegExp(`type: ${def.type}`));
  }
});

test("appending keeps the box parseable as one sequence", () => {
  assert.equal(appendLocalProxyYaml("", "- name: a"), "- name: a\n");
  assert.equal(appendLocalProxyYaml("- name: a\n\n\n", "- name: b"), "- name: a\n- name: b\n");
});
