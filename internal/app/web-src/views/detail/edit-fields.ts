import { defaultProxyBind, instanceModes } from "../../constants.ts";
import { instanceMode } from "../../format.ts";
import type { FleetInstance } from "../../state.ts";

// The 概况 edit form as plain data, plus the two rules that keep it from writing
// stale values back:
//
// - resyncEditFields(): when the same instance arrives with new data (a poll
//   after an edit in another tab), every field the user has not touched takes
//   the new value. Before, the whole form stayed frozen while it was dirty or
//   merely focused, and a later save resent all of it.
// - changedEditBody(): the PUT carries only fields whose value differs from the
//   baseline the form was seeded from. Every PUT field is optional on the
//   backend (UpdateWithOptions keeps what is omitted), so an untouched field
//   can never overwrite a newer value, even inside the poll interval.
//
// Two edits to the *same* field still resolve last-write-wins; the form warns
// when it sees that happen (see resyncEditFields' `conflicts`).

export interface EditFields {
  name: string;
  profileId: string;
  mode: string;
  mixedPort: string;
  proxyBind: string;
  controllerPort: string;
  localProxies: string;
  configOverride: string;
  chain: string[];
  chainUdp: boolean;
  autoRestart: boolean;
}

export type EditFieldKey = keyof EditFields;

const fieldKeys: EditFieldKey[] = [
  "name", "profileId", "mode", "mixedPort", "proxyBind", "controllerPort",
  "localProxies", "configOverride", "chain", "chainUdp", "autoRestart",
];

/** The instance exactly as the backend holds it -- the baseline, no fallbacks. */
export function editFieldsFromInstance(instance: FleetInstance): EditFields {
  return {
    name: instance.name,
    profileId: instance.profileId,
    mode: instanceMode(instance),
    mixedPort: String(instance.mixedPort),
    proxyBind: instance.proxyBind || defaultProxyBind,
    controllerPort: String(instance.controllerPort),
    localProxies: instance.localProxies || "",
    configOverride: instance.configOverride || "",
    chain: Array.isArray(instance.chain) ? [...instance.chain] : [],
    chainUdp: Boolean(instance.chainUdp),
    autoRestart: Boolean(instance.autoRestart),
  };
}

function sameValue(a: unknown, b: unknown): boolean {
  return Array.isArray(a) && Array.isArray(b)
    ? a.length === b.length && a.every((item, index) => item === b[index])
    : a === b;
}

export interface ResyncResult {
  form: EditFields;
  /** Fields the user edited that also changed on the server meanwhile. */
  conflicts: EditFieldKey[];
}

export function resyncEditFields(form: EditFields, base: EditFields, next: EditFields): ResyncResult {
  const merged = { ...form };
  const conflicts: EditFieldKey[] = [];
  for (const key of fieldKeys) {
    const untouched = sameValue(form[key], base[key]);
    if (untouched) (merged as Record<EditFieldKey, unknown>)[key] = next[key];
    else if (!sameValue(next[key], base[key]) && !sameValue(next[key], form[key])) conflicts.push(key);
  }
  return { form: merged, conflicts };
}

/** The full PUT body the form represents, mode-dependent fields resolved. */
export function editPutBody(fields: EditFields): Record<string, unknown> {
  const chainMode = fields.mode === instanceModes.globalChain;
  return {
    name: fields.name.trim(),
    profileId: fields.profileId,
    mixedPort: Number(fields.mixedPort),
    proxyBind: fields.proxyBind.trim(),
    controllerPort: Number(fields.controllerPort),
    mode: fields.mode,
    localProxies: chainMode ? fields.localProxies : "",
    configOverride: fields.configOverride,
    chain: chainMode ? [...fields.chain] : [],
    chainUdp: fields.chainUdp,
    autoRestart: fields.autoRestart,
  };
}

/**
 * Only the body keys whose resolved value differs from the baseline's. Diffed
 * after editPutBody() so mode-dependent clearing still goes out: switching
 * global-chain -> rule sends `localProxies: ""` and `chain: []` exactly as the
 * full body did.
 */
export function changedEditBody(form: EditFields, base: EditFields): Record<string, unknown> {
  const wanted = editPutBody(form);
  const original = editPutBody(base);
  const body: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(wanted)) {
    if (!sameValue(value, original[key])) body[key] = value;
  }
  return body;
}

const fieldLabels: Record<EditFieldKey, string> = {
  name: "名称",
  profileId: "配置档",
  mode: "实例模式",
  mixedPort: "本地代理端口",
  proxyBind: "代理绑定",
  controllerPort: "外部控制器端口",
  localProxies: "本地节点",
  configOverride: "配置覆盖",
  chain: "链路顺序",
  chainUdp: "UDP 走链路",
  autoRestart: "崩溃自动重启",
};

export function editFieldLabel(key: EditFieldKey): string {
  return fieldLabels[key];
}
