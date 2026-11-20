import type { CreateInstancePayload } from "../../bridge.ts";
import { defaultProxyBind, instanceModes } from "../../constants.ts";
import { mappingsFromRows } from "../../port-mappings.ts";
import type { PortMappingRow } from "../../port-mappings.ts";

// The 新建实例 form as plain data: what a fresh open looks like and the POST
// body it turns into. CreatePanel.vue owns the reactive copy and its watchers.

export interface CreateFormState {
  name: string;
  profileId: string;
  mode: string;
  mixedPort: string;
  proxyBind: string;
  controllerPort: string;
  localProxies: string;
  // The chain is the array the payload sends, not text: it used to be a
  // newline-delimited <textarea> that chainFromText() had to reparse.
  chain: string[];
  portMappings: PortMappingRow[];
  autoRestart: boolean;
}

export function blankCreateForm(profileId: string): CreateFormState {
  return {
    name: "",
    profileId,
    mode: instanceModes.rule,
    mixedPort: "",
    proxyBind: defaultProxyBind,
    controllerPort: "",
    localProxies: "",
    chain: [],
    portMappings: [],
    autoRestart: false,
  };
}

/** Chain-only fields are sent empty outside global-chain mode; blank ports mean "allocate". */
export function createPayload(form: CreateFormState): CreateInstancePayload {
  const chainMode = form.mode === instanceModes.globalChain;
  return {
    name: form.name.trim(),
    profileId: form.profileId,
    mixedPort: Number(form.mixedPort) || 0,
    proxyBind: form.proxyBind.trim(),
    controllerPort: Number(form.controllerPort) || 0,
    mode: form.mode,
    localProxies: chainMode ? form.localProxies : "",
    chain: chainMode ? [...form.chain] : [],
    portMappings: mappingsFromRows(form.portMappings),
    autoRestart: form.autoRestart,
  };
}
