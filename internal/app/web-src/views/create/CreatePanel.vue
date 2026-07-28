<script setup lang="ts">
// Vue replacement for the inner content of <section id="createPanel">
// (index.html:113-169).
//
// Stays mounted for the app's lifetime (main.ts's mountShell() pattern,
// matching TopBar/SideBar/MessageBanner) rather than being gated behind
// `v-if`. That is a correction, not the original design: an earlier draft of
// this file wrapped everything in `v-if="store.creating"` to get a fresh
// form per open "for free". That is unsafe here specifically -- app.ts's
// bindElements() (app.ts:73) and bindEvents() (app.ts:1693) still run
// synchronously at boot, before store.creating can ever be true, and
// together issue ~150 non-null-asserted `querySelector`/`addEventListener`
// calls including every #createXxx id this component renders. A `v-if`'d
// root means those ids are absent from the DOM at that moment; the first
// missing one breaks the chain before app.ts ever reaches
// registerActions() (app.ts:1698), which would leave the bridge wired to
// no-ops for every view, not just this one. Staying permanently mounted
// keeps every #createXxx id present in the DOM at boot, exactly like the
// original static markup was, so those lookups keep succeeding.
//
// Visibility is therefore still app.ts's job, unchanged: renderPanels()
// (app.ts:421) toggles `.hidden` on the <section id="createPanel"> host
// itself, which mountShell() never touches (it only ever replaces a host's
// *children*). A `display: none` ancestor already hides everything this
// component renders, the same way it hid the original always-present static
// markup pre-migration -- so there is no need for a second, redundant
// hidden-class toggle inside this component's own template.
//
// FORM RESET: since the component is never destroyed/recreated, the field
// values below persist across opens unless something explicitly clears them.
// That "something" is the watcher on `store.creating` near the bottom of
// this file: it mirrors showCreate()'s field resets (app.ts:856-863) and its
// trailing fillSuggestedPorts() call (app.ts:866), running them every time
// `store.creating` flips false -> true, i.e. every time the panel opens.
import { computed, reactive, ref, watch } from "vue";
import { store } from "../../store.ts";
import { actions } from "../../bridge.ts";
import { defaultProxyBind, instanceModes } from "../../constants.ts";
import { chainFromText } from "../../format.ts";
import { profileReferenceCount } from "../../state.ts";
import type { FleetProfile } from "../../state.ts";
import { profileOptionLabel } from "../../app-logic.ts";

// Shape of the payload createInstanceFromForm() (app.ts:1196-1205) builds
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

// Shape GET /api/ports/suggest resolves to (mirrors app.ts:789-792's
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
  chain: string;
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
  chain: "",
});

// Mirrors updateCreateProfileControls()'s hasProfiles-derived disables
// (app.ts:780-784).
const hasProfiles = computed(() => store.profiles.length > 0);

// Mirrors applyModeFields("create", mode) (app.ts:473-476): toggles the
// chain-only fields' `.hidden` class rather than removing them from the DOM,
// so text typed into #createLocalProxies/#createChain survives switching
// the mode select back and forth. Only a fresh *open* clears them (via the
// reset watcher below), matching the original, which never cleared these
// fields on a mode change either.
const isChainMode = computed(() => form.mode === instanceModes.globalChain);

// Mirrors renderProfileOptions() (app.ts:480-495) as consumed for the
// create select specifically (app.ts:779, 861). The options list itself is
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

async function loadSuggestedPorts(): Promise<void> {
  // Mirrors the early return at app.ts:795. Always false right after a
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
// open" rather than "reset on boot". Mirrors showCreate()'s field resets
// (app.ts:856-863) and its trailing fillSuggestedPorts() call (app.ts:866).
watch(
  () => store.creating,
  (creating) => {
    if (!creating) return;
    form.name = "";
    form.profileId = store.profiles[0]?.id ?? "";
    form.mode = instanceModes.rule;
    form.mixedPort = "";
    form.proxyBind = defaultProxyBind;
    form.controllerPort = "";
    form.localProxies = "";
    form.chain = "";
    mixedPortPlaceholder.value = "自动";
    controllerPortPlaceholder.value = "自动";
    void loadSuggestedPorts();
  },
);

// Mirrors el.createSubmit.disabled = createGate.isRunning() (app.ts:424).
// The concurrency guard itself (createGate.begin()/.end()) stays inside
// app.ts's side of `actions.createInstance` -- this flag only needs to
// cover this component's own button, which a synchronous double-click
// cannot outrace since Vue applies :disabled before another click can land.
const submitting = ref(false);

async function submit(): Promise<void> {
  // Mirrors createInstanceFromForm()'s guard (app.ts:1189-1192) exactly.
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
      chain: isChainMode.value ? chainFromText(form.chain) : [],
    };
    await actions.createInstance(payload);
  } finally {
    submitting.value = false;
  }
}

// Mirrors the #createCancel click handler (app.ts:1417-1420): `state.creating
// = false; render();`. Needs a bridge action rather than setting
// store.creating directly, because render() also drives plain-DOM siblings
// this component cannot reach (el.emptyPanel/el.detailPanel's hidden
// toggles inside renderPanels()) -- skipping it would leave the workbench
// showing nothing until some unrelated action happened to trigger a render.
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
      <input id="createName" v-model="form.name" placeholder="香港网关">
    </label>
    <label>
      <span>配置档</span>
      <select id="createProfile" v-model="form.profileId" :disabled="!hasProfiles">
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
      <select id="createMode" v-model="form.mode">
        <option value="rule">规则分流</option>
        <option value="global-chain">全局链式</option>
      </select>
    </label>
  </div>
  <div class="form-grid two">
    <label>
      <span>混合端口</span>
      <input id="createMixedPort" v-model="form.mixedPort" type="number" min="1" max="65535" :placeholder="mixedPortPlaceholder">
    </label>
    <label>
      <span>代理绑定地址</span>
      <input id="createProxyBind" v-model="form.proxyBind" placeholder="127.0.0.1">
    </label>
    <label>
      <span>控制端口</span>
      <input id="createControllerPort" v-model="form.controllerPort" type="number" min="1" max="65535" :placeholder="controllerPortPlaceholder">
    </label>
  </div>
  <div id="createChainFields" class="chain-fields" :class="{ hidden: !isChainMode }">
    <label class="stacked">
      <span>本地节点 YAML</span>
      <textarea id="createLocalProxies" v-model="form.localProxies" class="compact-code" spellcheck="false" wrap="off" placeholder="- name: local-hop"></textarea>
    </label>
    <label class="stacked">
      <span>链路顺序</span>
      <textarea id="createChain" v-model="form.chain" class="compact-code" spellcheck="false" wrap="off" placeholder="local-hop&#10;节点选择"></textarea>
    </label>
  </div>
  <div class="actions">
    <button id="createSubmit" class="primary" type="button" :disabled="submitting || !hasProfiles" @click="submit">创建</button>
    <button id="createCancel" type="button" @click="cancel">取消</button>
  </div>
</template>
