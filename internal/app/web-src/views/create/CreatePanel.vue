<script setup lang="ts">
// Vue replacement for the inner content of <section id="createPanel">
// (index.html:113-169).
//
// Stays mounted for the app's lifetime (main.ts's mountShell() pattern,
// matching TopBar/SideBar/MessageBanner) rather than being gated behind
// `v-if`.
//
// A previous version of this comment justified that with app.ts's
// bindElements()/bindEvents() needing the #createXxx ids present at boot.
// That reason is gone -- those functions no longer exist, app.ts does not
// touch the DOM at all, and panel visibility is main.ts's watchEffect
// toggling `.hidden` on the <section id="createPanel"> host. mountShell()
// only ever replaces a host's *children*, so a `display: none` ancestor
// already hides everything rendered here and no in-template hidden toggle is
// needed either.
//
// The reason that is still live is the form reset below. Because the
// component is never destroyed/recreated, its field values persist across
// opens, and the watcher on `store.creating` near the bottom of this file is
// what clears them -- it fires on every false -> true flip and also runs
// fillSuggestedPorts(). Gating the root on `v-if="store.creating"` would get
// a fresh form "for free" but mount the component only *after* the flag is
// already true, so that watcher would never see the transition: no port
// suggestions, ever. Keep the root unconditional.
import { computed, reactive, ref, watch } from "vue";
import { store } from "../../store.ts";
import { actions } from "../../bridge.ts";
import { defaultProxyBind, instanceModes } from "../../constants.ts";
import { profileReferenceCount } from "../../state.ts";
import type { FleetProfile } from "../../state.ts";
import { profileOptionLabel } from "../../app-logic.ts";
import ChainOrderField from "../shared/ChainOrderField.vue";
import LocalProxyField from "../shared/LocalProxyField.vue";
import ProxyBindField from "../shared/ProxyBindField.vue";
import { useChainCandidates } from "../shared/use-chain-candidates.ts";

// Shape of the payload createInstanceFromForm() (pre-Vue app.ts) builds
// and POSTs to /api/instances. Today that function reads these fields
// straight off el.createName/el.createProfile/etc. -- once this component
// owns that markup those DOM reads return nothing meaningful, so the
// function needs to take the payload as a parameter instead.
// `actions.createInstance` does not exist on FleetActions yet; reported to
// the integrator (see the handoff report) with this exact shape.
interface CreateInstancePayload {
  name: string;
  profileId: string;
  mixedPort: number;
  proxyBind: string;
  controllerPort: number;
  mode: string;
  localProxies: string;
  chain: string[];
}

// Shape GET /api/ports/suggest resolves to (mirrors pre-Vue app.ts's
// SuggestedPorts). fillSuggestedPorts() currently writes straight onto
// el.createMixedPort.placeholder/el.createControllerPort.placeholder; this
// component needs the value returned instead, so it can bind its own
// placeholder refs. `actions.suggestPorts` does not exist on FleetActions
// yet either -- same handoff.
interface SuggestedPorts {
  mixedPort?: number;
  controllerPort?: number;
}

interface CreateFormState {
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
}

const mixedPortPlaceholder = ref("自动");
const controllerPortPlaceholder = ref("自动");

// Initial values only matter for the very first paint (store.creating is
// always false at boot -- see state.ts's createState() default -- so this
// content is never visible until the watcher below has already run a reset
// against real data). The watcher, not these initializers, is what "resets
// the form" from here on.
const form = reactive<CreateFormState>({
  name: "",
  profileId: "",
  mode: instanceModes.rule,
  mixedPort: "",
  proxyBind: defaultProxyBind,
  controllerPort: "",
  localProxies: "",
  chain: [],
});

// Mirrors updateCreateProfileControls()'s hasProfiles-derived disables
// (pre-Vue app.ts).
const hasProfiles = computed(() => store.profiles.length > 0);

// Mirrors applyModeFields("create", mode) (pre-Vue app.ts): toggles the
// chain-only fields' `.hidden` class rather than removing them from the DOM,
// so text typed into #createLocalProxies/#createChain survives switching
// the mode select back and forth. Only a fresh *open* clears them (via the
// reset watcher below), matching the original, which never cleared these
// fields on a mode change either.
const isChainMode = computed(() => form.mode === instanceModes.globalChain);

// Declared *after* isChainMode on purpose: useChainCandidates() watches its
// `active` getter with immediate: true, so it reads that computed during setup --
// referencing it from above would hit the const's temporal dead zone and throw.
//
// Gated on the panel being open and in chain mode because the request re-reads and
// re-parses the selected profile's config, which can be a multi-MB subscription.
const chainCandidates = useChainCandidates(
  () => form.profileId,
  () => form.localProxies,
  () => store.creating && isChainMode.value,
);

// Mirrors the pre-Vue renderProfileOptions(), as consumed for the create
// select specifically. The options list itself is
// just a `v-for` over store.profiles below -- Vue's reactivity keeps that in
// sync for free, which is what renderProfileOptions had to do by hand by
// re-running on every render(). The one piece of behaviour that isn't "for
// free": renderProfileOptions falls back to the first profile when the
// previously-selected id is no longer in the list (e.g. the selected
// profile got deleted while this form was open). This watcher reproduces
// exactly that fallback, independent of the reset watcher below (this one
// runs any time the profile list changes, not just on open).
watch(
  () => store.profiles,
  (profiles) => {
    if (!profiles.some((profile) => profile.id === form.profileId)) {
      form.profileId = profiles[0]?.id ?? "";
    }
  },
);

function profileLabel(profile: FleetProfile): string {
  return profileOptionLabel(profile, profileReferenceCount(store, profile.id));
}

// Mirrors OverviewTab.vue's markDirty()/ProfileFormFields.vue's
// markProfileFormDirty(): an explicit per-field handler rather than a deep
// watch on `form`, so navigation.ts's hasUnsavedChanges() (and therefore
// selectInstance()/showCreate()'s discard prompts) knows this form has
// content worth confirming before it is thrown away.
function markCreateDirty(): void {
  store.createDirty = true;
}

async function loadSuggestedPorts(): Promise<void> {
  // Mirrors the early return at pre-Vue app.ts. Always false right after a
  // reset (both fields are cleared below before this runs) -- kept for
  // exact parity in case that ever stops being true.
  if (form.mixedPort && form.controllerPort) return;
  const ports: SuggestedPorts = await actions.suggestPorts();
  mixedPortPlaceholder.value = ports.mixedPort ? `建议 ${ports.mixedPort}` : "自动";
  controllerPortPlaceholder.value = ports.controllerPort ? `建议 ${ports.controllerPort}` : "自动";
}

// The actual form reset. Fires on every false -> true transition of
// store.creating, i.e. every time showCreate()/the empty-state button opens
// this panel -- never on the initial (false) value, matching "reset on
// open" rather than "reset on boot". Mirrors the pre-Vue showCreate()'s field
// resets and its trailing fillSuggestedPorts() call.
watch(
  () => store.creating,
  (creating) => {
    // Clears on both edges: opening starts from a blank (not-yet-dirty) form,
    // and closing -- cancel or a successful createInstance() -- means there is
    // nothing left this flag should still be protecting. Done here rather than
    // at cancelCreate()/createInstance() (services/instances.ts, not this
    // component's file) because every store.creating = false transition,
    // wherever it originates, routes through this watcher.
    store.createDirty = false;
    if (!creating) return;
    form.name = "";
    form.profileId = store.profiles[0]?.id ?? "";
    form.mode = instanceModes.rule;
    form.mixedPort = "";
    form.proxyBind = defaultProxyBind;
    form.controllerPort = "";
    form.localProxies = "";
    form.chain = [];
    mixedPortPlaceholder.value = "自动";
    controllerPortPlaceholder.value = "自动";
    void loadSuggestedPorts();
  },
);

// Mirrors el.createSubmit.disabled = createGate.isRunning() (pre-Vue app.ts).
// The concurrency guard itself (createGate.begin()/.end()) stays inside
// app.ts's side of `actions.createInstance` -- this flag only needs to
// cover this component's own button, which a synchronous double-click
// cannot outrace since Vue applies :disabled before another click can land.
const submitting = ref(false);

async function submit(): Promise<void> {
  // Mirrors createInstanceFromForm()'s guard (pre-Vue app.ts) exactly.
  // Relocated here rather than left in app.ts because the field it reads --
  // the selected profile -- now lives in this component's local `form`
  // state instead of an el.createProfile DOM node app.ts can see.
  if (!store.profiles.length || !form.profileId) {
    actions.showMessage("请先创建并选择配置档。", "error");
    return;
  }
  if (submitting.value) return;
  submitting.value = true;
  try {
    const payload: CreateInstancePayload = {
      name: form.name.trim(),
      profileId: form.profileId,
      mixedPort: Number(form.mixedPort) || 0,
      proxyBind: form.proxyBind.trim(),
      controllerPort: Number(form.controllerPort) || 0,
      mode: form.mode,
      localProxies: isChainMode.value ? form.localProxies : "",
      chain: isChainMode.value ? [...form.chain] : [],
    };
    await actions.createInstance(payload);
  } finally {
    submitting.value = false;
  }
}

// Mirrors the #createCancel click handler in the pre-Vue app.ts:
// `state.creating = false; render();`.
//
// The bridge hop is no longer load-bearing: render()/renderPanels() are gone,
// and main.ts's watchEffect reacts to `store.creating` directly, so setting it
// here would repaint the sibling panels just fine. It stays a bridge action
// purely for consistency -- every other cross-view navigation goes through the
// action table, and cancelCreate() has exactly one owner there.
function cancel(): void {
  actions.cancelCreate();
}
</script>

<template>
  <div class="panel-title">
    <h2>新建实例</h2>
    <p>端口可以留空，系统会自动分配本机回环端口。</p>
  </div>
  <div class="form-grid two">
    <label>
      <span>名称</span>
      <input id="createName" v-model="form.name" placeholder="香港网关" @input="markCreateDirty">
    </label>
    <label>
      <span>配置档</span>
      <select id="createProfile" v-model="form.profileId" :disabled="!hasProfiles" @change="markCreateDirty">
        <option v-for="profile in store.profiles" :key="profile.id" :value="profile.id">{{ profileLabel(profile) }}</option>
      </select>
    </label>
  </div>
  <div id="createProfileRequired" class="inline-notice" :class="{ hidden: hasProfiles }">
    <span>请先创建配置档，再创建引用它的实例。</span>
    <button id="createManageProfiles" type="button" @click="actions.openProfileManager()">打开配置档管理</button>
  </div>
  <div class="form-grid two">
    <label>
      <span>实例模式</span>
      <select id="createMode" v-model="form.mode" @change="markCreateDirty">
        <option value="rule">规则分流</option>
        <option value="global-chain">全局链式</option>
      </select>
    </label>
  </div>
  <div class="form-grid two">
    <label>
      <span>混合端口</span>
      <input id="createMixedPort" v-model="form.mixedPort" type="number" min="1" max="65535" :placeholder="mixedPortPlaceholder" @input="markCreateDirty">
    </label>
    <div class="stacked">
      <span>代理绑定地址</span>
      <ProxyBindField v-model="form.proxyBind" input-id="createProxyBind" @dirty="markCreateDirty" />
    </div>
    <label>
      <span>控制端口</span>
      <input id="createControllerPort" v-model="form.controllerPort" type="number" min="1" max="65535" :placeholder="controllerPortPlaceholder" @input="markCreateDirty">
    </label>
  </div>
  <div id="createChainFields" class="chain-fields" :class="{ hidden: !isChainMode }">
    <div class="stacked">
      <span>本地节点 YAML</span>
      <LocalProxyField
        v-model="form.localProxies"
        :candidates="chainCandidates.state"
        host-id="createLocalProxies"
        @dirty="markCreateDirty"
      />
    </div>
    <div class="stacked">
      <span>链路顺序</span>
      <ChainOrderField v-model="form.chain" :candidates="chainCandidates.state" @dirty="markCreateDirty" />
    </div>
  </div>
  <div class="actions">
    <button id="createSubmit" class="primary" type="button" :disabled="submitting || !hasProfiles" @click="submit">创建</button>
    <button id="createCancel" type="button" @click="cancel">取消</button>
  </div>
</template>
