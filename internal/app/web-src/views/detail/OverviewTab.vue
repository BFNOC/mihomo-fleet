<script setup lang="ts">
// Vue replacement for #tab-overview's markup (index.html:207-276): the
// read-only port/mode/chain summary panel and the "编辑基础信息" edit form.
//
// Ports four things from the pre-Vue app.ts: the edit-form half of
// renderPanels() (`if ((!state.editDirty || state.editInstanceId !==
// selected.id) && !editFormContainsFocus()) { ... }`), applyModeFields("edit",
// ...), markEditFormDirty(), and saveActiveBasics().
//
// EDIT FORM DIRTY-STATE CONTRACT: `store.editDirty`/`store.editInstanceId`/
// `store.editVersion` are shared FleetState fields (state.ts), not local to
// this component. TopBar.vue's instance selector still depends on them via
// app.ts's selectInstance()/confirmDiscardChanges(), which is unchanged by
// this migration -- so the population watcher below reproduces the exact
// same guard app.ts used ("only repopulate the form from fresh instance data
// when it is not dirty, or the dirty edits belong to a *different*
// instance -- and never while the form still has focus") rather than
// simplifying it, to keep that cross-component contract intact.
import { computed, ref, watch } from "vue";
import { store } from "../../store.ts";
import { actions } from "../../bridge.ts";
import { api } from "../../api.ts";
import { activeInstance, profileReferenceCount } from "../../state.ts";
import { defaultProxyBind, instanceModes } from "../../constants.ts";
import { createActionGate, profileOptionLabel } from "../../app-logic.ts";
import {
  chainSummary,
  instanceMode,
  modeLabel,
  proxyEndpointText,
  selectionSummary,
} from "../../format.ts";
import { refreshInstancesList } from "./instance-refresh.ts";
import ChainOrderField from "../shared/ChainOrderField.vue";
import LocalProxyField from "../shared/LocalProxyField.vue";
import ProxyBindField from "../shared/ProxyBindField.vue";
import { useChainCandidates } from "../shared/use-chain-candidates.ts";

const selected = computed(() => activeInstance(store));

const editFormEl = ref<HTMLElement | null>(null);
const editName = ref("");
const editProfile = ref("");
const editMode = ref<string>(instanceModes.rule);
const editMixedPort = ref("");
const editProxyBind = ref(defaultProxyBind);
const editControllerPort = ref("");
const editLocalProxies = ref("");
// The chain is held as the array the PUT body sends. It used to be newline text
// because a <textarea> could not hold anything else.
const editChain = ref<string[]>([]);

const showChainFields = computed(() => editMode.value === instanceModes.globalChain);

// Declared after showChainFields on purpose: useChainCandidates() watches its
// `active` getter with immediate: true, so it reads that computed during setup and
// referencing it from above would hit the const's temporal dead zone.
//
// Gated on this tab being the visible one because the request re-reads and
// re-parses the profile config, which can be a multi-MB subscription.
const chainCandidates = useChainCandidates(
  () => editProfile.value,
  () => editLocalProxies.value,
  () => showChainFields.value && store.activeTab === "overview",
);

function formHasFocus(): boolean {
  return Boolean(editFormEl.value && editFormEl.value.contains(document.activeElement));
}

// Mirrors markEditFormDirty() (pre-Vue app.ts) exactly.
function markDirty(): void {
  store.editInstanceId = selected.value?.id || store.editInstanceId;
  store.editDirty = true;
  store.editVersion += 1;
}

watch(
  selected,
  (instance) => {
    if (!instance) return;
    if ((!store.editDirty || store.editInstanceId !== instance.id) && !formHasFocus()) {
      store.editInstanceId = instance.id;
      store.editDirty = false;
      store.editVersion = 0;
      editName.value = instance.name;
      // Fall back to the first profile when the instance points at one that no
      // longer exists. The old renderProfileOptions() did this while rebuilding
      // the <option> list; with v-model the select would instead show nothing
      // selected, and a save from that state would submit an empty profileId.
      editProfile.value = store.profiles.some((profile) => profile.id === instance.profileId)
        ? instance.profileId
        : (store.profiles[0]?.id ?? "");
      editMode.value = instanceMode(instance);
      editMixedPort.value = String(instance.mixedPort);
      editProxyBind.value = instance.proxyBind || defaultProxyBind;
      editControllerPort.value = String(instance.controllerPort);
      editLocalProxies.value = instance.localProxies || "";
      editChain.value = Array.isArray(instance.chain) ? [...instance.chain] : [];
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
  if (!saveGate.begin()) return;
  saving.value = true;
  const editVersion = store.editVersion;
  try {
    await api(`/api/instances/${instance.id}`, {
      method: "PUT",
      body: JSON.stringify({
        name: editName.value.trim(),
        profileId: editProfile.value,
        mixedPort: Number(editMixedPort.value),
        proxyBind: editProxyBind.value.trim(),
        controllerPort: Number(editControllerPort.value),
        mode: editMode.value,
        localProxies: editMode.value === instanceModes.globalChain ? editLocalProxies.value : "",
        chain: editMode.value === instanceModes.globalChain ? [...editChain.value] : [],
      }),
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

// FleetInstance's optional overview fields fall back the same way
// renderPanels() did (pre-Vue app.ts).
const overviewMixed = computed(() => (selected.value ? proxyEndpointText(selected.value) : ""));
const overviewProxyBind = computed(() => selected.value?.proxyBind || defaultProxyBind);
const overviewController = computed(() => (selected.value ? `127.0.0.1:${selected.value.controllerPort}` : ""));
const overviewMode = computed(() => (selected.value ? modeLabel(instanceMode(selected.value)) : ""));
const overviewChain = computed(() => (selected.value ? chainSummary(selected.value) : ""));
const overviewProfile = computed(() => selected.value?.profileName || selected.value?.profileId || "无");
const overviewUserConfig = computed(() => selected.value?.profileConfigPath || selected.value?.userConfigPath || "");
const overviewRuntimeConfig = computed(() => selected.value?.runtimeConfigPath || "");
const overviewSelection = computed(() => (selected.value ? selectionSummary(selected.value) : ""));
</script>

<template>
  <div class="split">
    <section class="panel">
      <h3>端口</h3>
      <dl class="kv">
        <dt>本地代理</dt>
        <dd id="overviewMixed">{{ overviewMixed }}</dd>
        <dt>代理绑定</dt>
        <dd id="overviewProxyBind">{{ overviewProxyBind }}</dd>
        <dt>外部控制器</dt>
        <dd id="overviewController">{{ overviewController }}</dd>
        <dt>实例模式</dt>
        <dd id="overviewMode">{{ overviewMode }}</dd>
        <dt>生效链路</dt>
        <dd id="overviewChain">{{ overviewChain }}</dd>
        <dt>配置档</dt>
        <dd id="overviewProfile">{{ overviewProfile }}</dd>
        <dt>配置文件</dt>
        <dd id="overviewUserConfig">{{ overviewUserConfig }}</dd>
        <dt>运行配置</dt>
        <dd id="overviewRuntimeConfig">{{ overviewRuntimeConfig }}</dd>
        <dt>已保存节点</dt>
        <dd id="overviewSelection">{{ overviewSelection }}</dd>
      </dl>
    </section>
    <section id="editForm" ref="editFormEl" class="panel">
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
          <ChainOrderField v-model="editChain" :candidates="chainCandidates.state" @dirty="markDirty" />
        </div>
      </div>
      <button id="saveBasics" class="save-basics" type="button" :disabled="!selected || saving" @click="saveBasics">保存基础信息</button>
    </section>
  </div>
</template>
