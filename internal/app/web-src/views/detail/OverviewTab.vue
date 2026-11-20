<script setup lang="ts">
// Vue replacement for #tab-overview's markup (index.html:207-276): the
// "编辑基础信息" edit form, beside OverviewSummary's read-only panel.
//
// Ports four things from the pre-Vue app.ts: the edit-form half of
// renderPanels() (`if ((!state.editDirty || state.editInstanceId !==
// selected.id) && !editFormContainsFocus()) { ... }`), applyModeFields("edit",
// ...), markEditFormDirty(), and saveActiveBasics().
//
// EDIT FORM DIRTY-STATE CONTRACT: `store.editDirty`/`store.editInstanceId`/
// `store.editVersion` are shared FleetState fields (state.ts), not local to
// this component; navigation reads them to confirm before discarding. The
// population watcher below reseeds the whole form for a different instance or
// a clean form, and resyncs untouched fields of a dirty one (edit-fields.ts).
// It no longer waits for the form to lose focus: that gate froze the form on
// edits made elsewhere and, in browsers that keep focus in the input when a
// sidebar button is clicked, left one instance's values under another's name.
import { computed, ref, watch } from "vue";
import { store } from "../../store.ts";
import { actions } from "../../bridge.ts";
import { api } from "../../api.ts";
import { activeInstance, profileReferenceCount } from "../../state.ts";
import { defaultProxyBind, instanceModes } from "../../constants.ts";
import { createActionGate, profileOptionLabel } from "../../app-logic.ts";
import { refreshInstancesList } from "./instance-refresh.ts";
import { changedEditBody, editFieldLabel, editFieldsFromInstance, editPutBody, resyncEditFields } from "./edit-fields.ts";
import type { EditFields } from "./edit-fields.ts";
import OverviewSummary from "./OverviewSummary.vue";
import ChainOrderField from "../shared/ChainOrderField.vue";
import InlineYamlEditor from "../shared/InlineYamlEditor.vue";
import LocalProxyField from "../shared/LocalProxyField.vue";
import PortMappingsField from "../shared/PortMappingsField.vue";
import ProxyBindField from "../shared/ProxyBindField.vue";
import { useChainCandidates } from "../shared/use-chain-candidates.ts";
import { portMappingProblem, portMappingTargetOptions } from "../../port-mappings.ts";
import type { PortMappingRow } from "../../port-mappings.ts";

const selected = computed(() => activeInstance(store));

const editName = ref("");
const editProfile = ref("");
const editMode = ref<string>(instanceModes.rule);
const editMixedPort = ref("");
const editProxyBind = ref(defaultProxyBind);
const editControllerPort = ref("");
const editLocalProxies = ref("");
const editConfigOverride = ref("");
// The chain is held as the array the PUT body sends. It used to be newline text
// because a <textarea> could not hold anything else.
const editChain = ref<string[]>([]);
const editChainUdp = ref(false);
const editPortMappings = ref<PortMappingRow[]>([]);
const editAutoRestart = ref(false);

const showChainFields = computed(() => editMode.value === instanceModes.globalChain);
// Rule mode only needs the candidate request once mappings are in use.
const needsCandidates = computed(() => showChainFields.value || editPortMappings.value.length > 0);

// Declared after showChainFields on purpose: useChainCandidates() watches its
// `active` getter with immediate: true, so it reads that computed during setup and
// referencing it from above would hit the const's temporal dead zone.
//
// Gated on this tab being the visible one because the request re-reads and
// re-parses the profile config, which can be a multi-MB subscription.
const chainCandidates = useChainCandidates(
  () => editProfile.value,
  () => editLocalProxies.value,
  () => needsCandidates.value && store.activeTab === "overview",
);
const mappingOptions = computed(() =>
  portMappingTargetOptions(editMode.value, chainCandidates.state.candidates, chainCandidates.state.groupNames, chainCandidates.state.proxyNames));
const mappingUdp = computed(() => !showChainFields.value || editChainUdp.value);

// Mirrors markEditFormDirty() (pre-Vue app.ts) exactly.
function markDirty(): void {
  store.editInstanceId = selected.value?.id || store.editInstanceId;
  store.editDirty = true;
  store.editVersion += 1;
}

// What the backend held when the form was last seeded or resynced. Never the
// form's own fallbacks: comparing against it is how resync and save tell an
// untouched field from an edited one (edit-fields.ts).
let editBase: EditFields | null = null;

function currentForm(): EditFields {
  return {
    name: editName.value,
    profileId: editProfile.value,
    mode: editMode.value,
    mixedPort: editMixedPort.value,
    proxyBind: editProxyBind.value,
    controllerPort: editControllerPort.value,
    localProxies: editLocalProxies.value,
    configOverride: editConfigOverride.value,
    chain: [...editChain.value],
    chainUdp: editChainUdp.value,
    portMappings: editPortMappings.value.map((row) => ({ ...row })),
    autoRestart: editAutoRestart.value,
  };
}

function applyForm(fields: EditFields): void {
  editName.value = fields.name;
  editProfile.value = fields.profileId;
  editMode.value = fields.mode;
  editMixedPort.value = fields.mixedPort;
  editProxyBind.value = fields.proxyBind;
  editControllerPort.value = fields.controllerPort;
  editLocalProxies.value = fields.localProxies;
  editConfigOverride.value = fields.configOverride;
  editChain.value = [...fields.chain];
  editChainUdp.value = fields.chainUdp;
  editPortMappings.value = fields.portMappings.map((row) => ({ ...row }));
  editAutoRestart.value = fields.autoRestart;
}

// Fall back to the first profile when the instance points at one that no
// longer exists, so a save does not submit an empty profileId. Applied to the
// form only; the baseline keeps the dangling id, so the fallback counts as a
// change and is actually sent.
function withProfileFallback(fields: EditFields): EditFields {
  const exists = store.profiles.some((profile) => profile.id === fields.profileId);
  return exists ? fields : { ...fields, profileId: store.profiles[0]?.id ?? "" };
}

// A different instance, or a clean form, always reseeds; navigation has already
// asked before discarding unsaved edits. A dirty form on the same instance
// resyncs field by field instead of freezing entirely -- the
// old focus gate also blocked the reseed on an instance switch in browsers that
// do not move focus to a clicked button, leaving A's values under B's header.
watch(
  selected,
  (instance) => {
    if (!instance) return;
    const next = editFieldsFromInstance(instance);
    // Not dirty also covers navigation's "discard changes" (clearActiveDetailCache
    // and friends clear the flag): the form must snap back to the server copy.
    if (!editBase || store.editInstanceId !== instance.id || !store.editDirty) {
      store.editInstanceId = instance.id;
      store.editDirty = false;
      store.editVersion = 0;
      editBase = next;
      applyForm(withProfileFallback(next));
      return;
    }
    const { form, conflicts } = resyncEditFields(currentForm(), editBase, next);
    editBase = next;
    applyForm(form);
    if (conflicts.length) {
      actions.showMessage(`实例「${instance.name}」的${conflicts.map(editFieldLabel).join("、")}已在别处修改，保存会用你的修改覆盖。`, "error");
    }
  },
  { immediate: true },
);

const saveGate = createActionGate();
const saving = ref(false);

// Mirrors saveActiveBasics() (pre-Vue app.ts), narrowed to an
// instances-only refetch afterward -- see instance-refresh.ts.
async function saveBasics(): Promise<void> {
  const instance = selected.value;
  if (!instance) return;
  // The fallback above cannot produce an id when there are no profiles at all,
  // and the backend rejects an instance without one. Refuse locally so the user
  // gets a reason instead of a bare 400.
  if (!editProfile.value) {
    actions.showMessage("请先创建配置档，再保存实例。", "error");
    return;
  }
  const mappingProblem = portMappingProblem(editPortMappings.value, Number(editMixedPort.value), Number(editControllerPort.value));
  if (mappingProblem) {
    actions.showMessage(mappingProblem, "error");
    return;
  }
  // Only what differs from the server copy the form was seeded from, so an
  // untouched field cannot overwrite a newer value (edit-fields.ts).
  const body = editBase ? changedEditBody(currentForm(), editBase) : editPutBody(currentForm());
  if (!Object.keys(body).length) {
    store.editDirty = false;
    actions.showMessage("没有需要保存的修改。");
    return;
  }
  if (!saveGate.begin()) return;
  saving.value = true;
  const editVersion = store.editVersion;
  try {
    await api(`/api/instances/${instance.id}`, {
      method: "PUT",
      body: JSON.stringify(body),
    });
    if (store.editInstanceId === instance.id && store.editVersion === editVersion) {
      store.editDirty = false;
    }
    actions.showMessage("基础信息已保存。");
    await refreshInstancesList(store);
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    actions.showMessage(message, "error");
  } finally {
    saveGate.end();
    saving.value = false;
  }
}

function referenceCount(profileId: string): number {
  return profileReferenceCount(store, profileId);
}
</script>

<template>
  <div class="split">
    <OverviewSummary />
    <section id="editForm" class="panel">
      <h3>编辑基础信息</h3>
      <label class="stacked">
        <span>名称</span>
        <input id="editName" v-model="editName" @input="markDirty">
      </label>
      <label class="stacked">
        <span>配置档</span>
        <select id="editProfile" v-model="editProfile" @change="markDirty">
          <option v-for="profile in store.profiles" :key="profile.id" :value="profile.id">{{ profileOptionLabel(profile, referenceCount(profile.id)) }}</option>
        </select>
      </label>
      <label class="stacked">
        <span>实例模式</span>
        <select id="editMode" v-model="editMode" @change="markDirty">
          <option value="rule">规则分流</option>
          <option value="global-chain">全局链式</option>
        </select>
      </label>
      <div class="form-grid two">
        <label>
          <span>混合端口</span>
          <input id="editMixedPort" type="number" min="1" max="65535" v-model="editMixedPort" @input="markDirty">
        </label>
        <label>
          <span>控制端口</span>
          <input id="editControllerPort" type="number" min="1" max="65535" v-model="editControllerPort" @input="markDirty">
        </label>
        <div class="stacked proxy-bind-row">
          <span>代理绑定地址</span>
          <ProxyBindField v-model="editProxyBind" input-id="editProxyBind" @dirty="markDirty" />
        </div>
      </div>
      <label class="checkline">
        <input id="editAutoRestart" v-model="editAutoRestart" type="checkbox" @change="markDirty">
        <span>崩溃后自动重启</span>
      </label>
      <div id="editChainFields" class="chain-fields" :class="{ hidden: !showChainFields }">
        <div class="stacked">
          <span>本地节点 YAML</span>
          <LocalProxyField
            v-model="editLocalProxies"
            :candidates="chainCandidates.state"
            host-id="editLocalProxies"
            @dirty="markDirty"
          />
        </div>
        <div class="stacked">
          <span>链路顺序</span>
          <ChainOrderField v-model="editChain" :candidates="chainCandidates.state" @dirty="markDirty" @retry="chainCandidates.refresh()" />
        </div>
        <label class="checkline">
          <input id="editChainUdp" v-model="editChainUdp" type="checkbox" @change="markDirty">
          <span>UDP 也走链路（默认拒绝 UDP；链路中各跳需支持 UDP）</span>
        </label>
      </div>
      <div class="stacked">
        <span>端口映射</span>
        <PortMappingsField
          v-model="editPortMappings"
          :options="mappingOptions"
          :mixed-port="Number(editMixedPort)"
          :controller-port="Number(editControllerPort)"
          :udp="mappingUdp"
          :chain-default="showChainFields && !editChain.length"
          list-id="editPortMappingTargets"
          @dirty="markDirty"
        />
      </div>
      <div class="stacked">
        <span>配置覆盖 YAML</span>
        <InlineYamlEditor
          v-model="editConfigOverride"
          host-id="editConfigOverride"
          editor-label="配置覆盖 YAML 编辑器"
          @dirty="markDirty"
        />
        <p class="field-note">叠加在配置档之上生成运行配置：<code>prepend-*</code> / <code>append-*</code> 拼接列表，同名映射递归合并，其余键直接替换；不能改 <code>proxies</code> / <code>proxy-groups</code> / <code>proxy-providers</code>。例如订阅规则拒绝 UDP 时，<code>prepend-rules: ['NETWORK,udp,节点选择']</code> 可放行（规则整条要加引号，否则逗号会被拆成多项）。全局链式模式请用上面的“UDP 也走链路”，规则指向 <code>节点选择</code> 可能绕过链路。</p>
      </div>
      <button id="saveBasics" class="save-basics" type="button" :disabled="!selected || saving" @click="saveBasics">保存基础信息</button>
    </section>
  </div>
</template>
