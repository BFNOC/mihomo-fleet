/*
 * Turns the 添加本地节点 form into a mihomo proxy entry, so the 本地节点 YAML box
 * can be filled without knowing the schema by heart.
 *
 * Scope on purpose: the seven protocols below cover what a local hop is actually
 * built from, with only the fields mihomo requires plus the ones a hop normally
 * needs. Anything more exotic is still typed into the editor by hand -- the form
 * appends to that text, it never owns it, so the two paths compose.
 *
 * The emitted snippet must parse under parseLocalProxyItems() (internal/app/
 * config.go:413): a YAML sequence of maps, each with a non-empty `name`. Field
 * order in the output is stable so a generated node reads the same way twice.
 */

export type LocalProxyFieldKind = "text" | "number" | "password" | "select";

export interface LocalProxyFieldDef {
  key: string;
  label: string;
  kind: LocalProxyFieldKind;
  options?: readonly string[];
  placeholder?: string;
  /** Required fields refuse to generate while empty; mihomo would reject the node anyway. */
  required?: boolean;
  defaultValue?: string;
}

export interface LocalProxyTypeDef {
  type: string;
  label: string;
  fields: readonly LocalProxyFieldDef[];
}

const shadowsocksCiphers = [
  "aes-128-gcm",
  "aes-256-gcm",
  "chacha20-ietf-poly1305",
  "2022-blake3-aes-128-gcm",
  "2022-blake3-aes-256-gcm",
] as const;

// Every type starts with name/server/port; only the credential shape differs.
const commonFields: readonly LocalProxyFieldDef[] = [
  { key: "name", label: "名称", kind: "text", placeholder: "local-hop", required: true },
  { key: "server", label: "服务器", kind: "text", placeholder: "127.0.0.1", required: true },
  { key: "port", label: "端口", kind: "number", placeholder: "443", required: true },
];

export const localProxyTypes: readonly LocalProxyTypeDef[] = [
  {
    type: "ss",
    label: "Shadowsocks",
    fields: [
      ...commonFields,
      { key: "cipher", label: "加密", kind: "select", options: shadowsocksCiphers, defaultValue: "aes-128-gcm", required: true },
      { key: "password", label: "密码", kind: "password", required: true },
    ],
  },
  {
    type: "trojan",
    label: "Trojan",
    fields: [
      ...commonFields,
      { key: "password", label: "密码", kind: "password", required: true },
      { key: "sni", label: "SNI", kind: "text", placeholder: "可留空" },
    ],
  },
  {
    type: "vmess",
    label: "VMess",
    fields: [
      ...commonFields,
      { key: "uuid", label: "UUID", kind: "text", required: true },
      { key: "alterId", label: "alterId", kind: "number", defaultValue: "0", required: true },
      { key: "cipher", label: "加密", kind: "select", options: ["auto", "none", "aes-128-gcm", "chacha20-poly1305"], defaultValue: "auto", required: true },
    ],
  },
  {
    type: "vless",
    label: "VLESS",
    fields: [
      ...commonFields,
      { key: "uuid", label: "UUID", kind: "text", required: true },
      { key: "servername", label: "SNI", kind: "text", placeholder: "可留空" },
      { key: "flow", label: "flow", kind: "select", options: ["", "xtls-rprx-vision"] },
    ],
  },
  {
    type: "hysteria2",
    label: "Hysteria2",
    fields: [
      ...commonFields,
      { key: "password", label: "密码", kind: "password", required: true },
      { key: "sni", label: "SNI", kind: "text", placeholder: "可留空" },
    ],
  },
  {
    type: "socks5",
    label: "SOCKS5",
    fields: [
      ...commonFields,
      { key: "username", label: "用户名", kind: "text", placeholder: "可留空" },
      { key: "password", label: "密码", kind: "password", placeholder: "可留空" },
    ],
  },
  {
    type: "http",
    label: "HTTP",
    fields: [
      ...commonFields,
      { key: "username", label: "用户名", kind: "text", placeholder: "可留空" },
      { key: "password", label: "密码", kind: "password", placeholder: "可留空" },
    ],
  },
];

export function localProxyTypeDef(type: string): LocalProxyTypeDef | undefined {
  return localProxyTypes.find((def) => def.type === type);
}

/** Field defaults for a freshly opened form. */
export function localProxyFormDefaults(type: string): Record<string, string> {
  const def = localProxyTypeDef(type);
  const values: Record<string, string> = {};
  for (const field of def?.fields || []) {
    values[field.key] = field.defaultValue || "";
  }
  return values;
}

// A bare scalar is only safe when YAML cannot read it as anything else. Anything
// with structural characters, leading/trailing space, or a number/bool/null
// shape gets double-quoted -- a password of `*secret` or `yes` must survive as a
// string, and `name: 123` must not become an int the backend's
// `item["name"].(string)` assertion then rejects.
const plainScalar = /^[A-Za-z0-9_.@/+-]+$/;
const reservedScalars = new Set(["y", "n", "yes", "no", "true", "false", "on", "off", "null", "~"]);
// Only values YAML would actually retype need quoting. `127.0.0.1` has two dots
// and is a string either way, so it stays bare; `443`, `0x1f` and `.inf` do not.
const numberLike = /^[-+]?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?$/;
const specialNumberLike = /^([-+]?\.(inf|nan)|0[xob][0-9a-fA-F_]+)$/i;

function yamlScalar(value: string): string {
  const bare = plainScalar.test(value)
    && !reservedScalars.has(value.toLowerCase())
    && !numberLike.test(value)
    && !specialNumberLike.test(value);
  if (bare) return value;
  return `"${value.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
}

export interface LocalProxyYamlResult {
  yaml: string;
  error: string;
}

/**
 * Renders one `- name: ...` entry. `existingNames` are the names already parsed
 * out of the box; a collision is refused here because parseLocalProxyItems()
 * would refuse it on save with "local proxy name %q is duplicated".
 */
export function buildLocalProxyYaml(
  type: string,
  values: Record<string, string>,
  existingNames: readonly string[] = [],
): LocalProxyYamlResult {
  const def = localProxyTypeDef(type);
  if (!def) return { yaml: "", error: "请选择节点类型。" };
  const lines: string[] = [];
  for (const field of def.fields) {
    const raw = String(values[field.key] ?? "").trim();
    if (!raw) {
      if (field.required) return { yaml: "", error: `请填写${field.label}。` };
      continue;
    }
    if (field.key === "name") {
      if (existingNames.includes(raw)) {
        return { yaml: "", error: `本地节点 ${raw} 重名。` };
      }
      continue; // name is emitted first, below, not in field order
    }
    if (field.kind === "number") {
      if (!/^\d+$/.test(raw)) return { yaml: "", error: `${field.label}必须是数字。` };
      if (field.key === "port" && (Number(raw) < 1 || Number(raw) > 65535)) {
        return { yaml: "", error: "端口必须在 1-65535 之间。" };
      }
      lines.push(`  ${field.key}: ${Number(raw)}`);
      continue;
    }
    lines.push(`  ${field.key}: ${yamlScalar(raw)}`);
  }
  const name = String(values.name ?? "").trim();
  return {
    yaml: [`- name: ${yamlScalar(name)}`, `  type: ${def.type}`, ...lines].join("\n"),
    error: "",
  };
}

/** Appends a snippet to the box's current text, keeping exactly one blank-free join. */
export function appendLocalProxyYaml(existing: string, snippet: string): string {
  const base = String(existing || "").replace(/\s+$/, "");
  if (!base) return `${snippet}\n`;
  return `${base}\n${snippet}\n`;
}
