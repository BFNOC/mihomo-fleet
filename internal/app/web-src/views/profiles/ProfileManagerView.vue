<script setup lang="ts">
// Vue replacement for the inner content of <section id="profilePanel">
// (index.html:24-111) and six app.ts render functions: renderProfileManager,
// renderProfileList, renderProfileOptions*, renderConfigEditorState,
// renderSubscriptionInfo, updateCreateProfileControls*.
//
// *renderProfileOptions/updateCreateProfileControls render zero pixels
// inside #profilePanel -- both only ever populate <select> elements that
// live in #createPanel (el.createProfile) and #detailPanel (el.editProfile),
// neither owned by this view. views/create/CreateForm.vue and
// views/detail/OverviewTab.vue have already replaced those two call sites
// with their own `v-for` over store.profiles + profileOptionLabel(), so
// nothing from those two functions is reproduced here; see the handoff
// report for the recommendation to delete them from app.ts.
//
// PENDING CONTRACT -- see the message sent to the coordinator ("main") for
// the full rationale. Until the coordinator applies it, this file
// references three FleetState fields that do not exist yet
// (profileFormOwnerId/profileConfigOwnerId/profileConfigDirty, replacing
// the DOM datasets el.profileEditor.dataset.profileId /
// el.configEditor.dataset.profileId / el.configEditor.dataset.dirty) and
// four FleetActions entries that do not exist yet (saveProfile/
// deleteProfile/refreshSubscriptionProfile/fetchProfileConfig, replacing
// app.ts functions that read el.* DOM directly and touched the
// module-scope `configEditor` handle this component now owns instead).
// Every such reference is a currently-expected vue-tsc error, not a bug in
// this file -- the same transitional state views/create and views/detail
// are already in (e.g. CreateForm.vue's `actions.createInstance`).
import { computed, nextTick, ref } from "vue";
import { store } from "../../store.ts";
import { actions, chrome, registerActions } from "../../bridge.ts";
import { activeInstance, profileById, profileReferenceCount } from "../../state.ts";
import type { FleetProfile, ProfileCreateSource } from "../../state.ts";
import { defaultConfig } from "../../constants.ts";
import { formatProfileUpdate, formatSubscriptionInfo, isHttpUrl } from "../../format.ts";
import { localizedMessage } from "../../i18n.ts";
import { canClearSavedProfileConfig, shouldApplyProfileConfigLoad, shouldApplyProfileOperation } from "../../app-logic.ts";
import YamlCodeEditor from "./YamlCodeEditor.vue";

// ---------------------------------------------------------------------
// Editor handle + form field refs
// ---------------------------------------------------------------------
const editorRef = ref<InstanceType<typeof YamlCodeEditor> | null>(null);
const profileNameInputRef = ref<HTMLInputElement | null>(null);

const profileNameInput = ref("");
const profileIdDisplay = ref("");
const subscriptionUrlInput = ref("");
const subscriptionAutoUpdateInput = ref(true);
const subscriptionIntervalInput = ref("360");
const configEditorErrorText = ref("");

// Per-operation running flags, local to this component. `chrome.profileBusy`
// (app.ts, aggregate of its three gates) still drives every disabled state,
// same as before this migration -- these three exist only to reconstruct
// renderConfigEditorState()'s "which operation" status text branches
// (saveProfileGate/deleteProfileGate/refreshSubscriptionGate were private
// app.ts closures, invisible to Vue; this component's own await duration
// for each action call is exactly that gate's running duration, so no new
// bridge field is needed to recover the distinction).
const saving = ref(false);
const deleting = ref(false);
const refreshingSub = ref(false);

// Local sequence counter mirroring app.ts's profileContextSeq /
// advanceProfileContext(). Only this component's own navigation functions
// (startNewProfile/selectProfile/openProfileManager/closeProfileManager) can
// advance it now, since they are the only things that change which profile
// is "in context" -- see operationContextMatches() below.
let profileContextSeq = 0;
// Mirrors app.ts's profileConfigLoadSeq: guards a slow GET /config response
// from clobbering a newer load or a since-typed edit.
let profileConfigLoadSeq = 0;

// ---------------------------------------------------------------------
// Derived state
// ---------------------------------------------------------------------
const activeProfile = computed<FleetProfile | null>(() => profileById(store, store.activeProfileId));
const hasEditor = computed(() => store.profileCreating || Boolean(activeProfile.value));
const isSubscription = computed(() =>
  store.profileCreating ? store.profileCreateSource === "subscription" : Boolean(activeProfile.value?.subscriptionUrl),
);
const references = computed(() => (activeProfile.value ? profileReferenceCount(store, activeProfile.value.id) : 0));

function referenceCount(profileId: string): number {
  return profileReferenceCount(store, profileId);
}

// Mirrors renderProfileManager()'s `contextMatches` local (app.ts:205-209 /
// 233), reused by both the status computed below and saveDisabled.
const configContextMatches = computed(() => {
  const profile = activeProfile.value;
  return store.profileCreating
    ? store.profileConfigOwnerId === "__new__"
    : Boolean(profile && store.profileConfigOwnerId === profile.id);
});

// Mirrors renderConfigEditorState() (app.ts:200-245) exactly, branch order
// included -- only the data sources changed (local refs/store fields
// instead of DOM reads and private gate closures).
const configEditorStatus = computed<{ text: string; state: string }>(() => {
  const profile = activeProfile.value;
  if (!profile && !store.profileCreating) return { text: "未选择配置档", state: "idle" };
  if (saving.value) return { text: "正在保存", state: "saving" };
  if (deleting.value) return { text: "正在删除配置档", state: "saving" };
  if (refreshingSub.value) return { text: "正在更新订阅", state: "saving" };
  if (configEditorErrorText.value) return { text: "操作失败，修改未丢失", state: "error" };
  if (isSubscription.value) return { text: "订阅缓存，只读", state: "readonly" };
  if (store.profileConfigDirty) return { text: "未保存修改", state: "dirty" };
  if (configContextMatches.value) return { text: "已保存", state: "saved" };
  return { text: "正在加载", state: "loading" };
});

const saveDisabled = computed(() => {
  const profile = activeProfile.value;
  return (!profile && !store.profileCreating)
    || (!isSubscription.value && !configContextMatches.value)
    || chrome.profileBusy;
});
const findDisabled = computed(() => (!activeProfile.value && !store.profileCreating) || isSubscription.value || chrome.profileBusy);
const discardDisabled = computed(() => !store.profileConfigDirty || chrome.profileBusy);
const deleteDisabled = computed(() => store.profileCreating || references.value > 0 || chrome.profileBusy);

const profileMetaText = computed(() => {
  if (store.profileCreating) return isSubscription.value ? "创建后会下载并缓存订阅 YAML。" : "手写配置可以直接编辑 YAML。";
  const profile = activeProfile.value;
  if (!profile) return "未选择配置档。";
  return isSubscription.value ? `订阅缓存：${formatProfileUpdate(profile)}` : "手写配置：修改会作用于所有引用实例。";
});
const referenceBadgeText = computed(() =>
  store.profileCreating ? "尚未创建" : (references.value > 0 ? `${references.value} 个实例引用` : "未使用"),
);
const deleteHintText = computed(() =>
  store.profileCreating
    ? ""
    : (references.value > 0 ? `该配置档仍被 ${references.value} 个实例引用，需先将这些实例改绑到其他配置档。` : "删除后无法恢复。"),
);
const subscriptionInfoText = computed(() => {
  const profile = activeProfile.value;
  return isSubscription.value && profile ? formatSubscriptionInfo(profile) : "";
});
const subscriptionHomeUrl = computed(() => {
  const profile = activeProfile.value;
  if (!isSubscription.value || !profile) return "";
  const homeUrl = (profile.homeUrl || "").trim();
  return isHttpUrl(homeUrl) ? homeUrl : "";
});

// ---------------------------------------------------------------------
// Dirty-state helpers -- see the coordinator handoff for why these three
// FleetState fields replace the old DOM datasets. hasUnsavedChanges()/
// confirmDiscardChanges() are reproduced here (not imported) because they
// are pure reads of shared store fields, not DOM state; app.ts keeps its
// own copies for the navigation functions it still owns
// (selectInstance/showCreate/openDashboard/openInstanceWorkbench).
// ---------------------------------------------------------------------
function hasUnsavedChanges(): boolean {
  return store.editDirty || store.profileFormDirty || store.profileConfigDirty;
}

function confirmDiscardChanges(action: string): boolean {
  if (!hasUnsavedChanges()) return true;
  return window.confirm(`有未保存的修改。确定放弃并${action}吗？`);
}

function markProfileFormDirty(): void {
  store.profileFormDirty = true;
  store.profileFormVersion += 1;
}

function clearProfileFormDirty(): void {
  store.profileFormDirty = false;
  store.profileConfigDirty = false;
}

function setConfigEditorError(message: string): void {
  configEditorErrorText.value = message ? localizedMessage(message) : "";
}

// ---------------------------------------------------------------------
// Operation-context guard -- mirrors activeProfileContextId() /
// captureProfileOperationContext() / profileOperationContextMatches()
// (app.ts:161-179) exactly, with el.profileEditor.dataset.profileId
// replaced by store.profileFormOwnerId.
// ---------------------------------------------------------------------
interface OperationContext {
  contextSeq: number;
  profileId: string;
}

function activeProfileContextId(): string {
  return store.profileCreating ? "__new__" : store.activeProfileId;
}

function captureOperationContext(profileId: string = activeProfileContextId()): OperationContext {
  return { contextSeq: profileContextSeq, profileId };
}

function operationContextMatches(context: OperationContext): boolean {
  const currentActiveId = activeProfileContextId();
  return store.profileFormOwnerId === currentActiveId
    && shouldApplyProfileOperation({
      requestContextSeq: context.contextSeq,
      currentContextSeq: profileContextSeq,
      requestedProfileId: context.profileId,
      activeProfileId: currentActiveId,
      view: store.view,
    });
}

// ---------------------------------------------------------------------
// Editor plumbing -- resetConfigEditor()/loadProfileConfig()/
// populateProfileForm() (app.ts:247-255, 610-648).
// ---------------------------------------------------------------------
function resetConfigEditor(): void {
  profileConfigLoadSeq += 1;
  editorRef.value?.setValue("");
  editorRef.value?.setReadOnly(true);
  store.profileConfigOwnerId = "";
  store.profileConfigDirty = false;
  setConfigEditorError("");
}

function populateProfileForm(profile: FleetProfile | null): void {
  store.profileFormOwnerId = profile?.id || "__new__";
  profileNameInput.value = profile?.name || "";
  profileIdDisplay.value = profile?.id || "创建后自动生成";
  subscriptionUrlInput.value = profile?.subscriptionUrl || "";
  subscriptionAutoUpdateInput.value = profile ? Boolean(profile.autoUpdate) : true;
  subscriptionIntervalInput.value = String(profile?.updateIntervalMinutes || "360");
}

async function loadProfileConfig(profileId: string): Promise<void> {
  const profile = profileById(store, profileId);
  if (!profile) return;
  resetConfigEditor();
  const requestSeq = ++profileConfigLoadSeq;
  try {
    const config = await actions.fetchProfileConfig(profile.id);
    if (!shouldApplyProfileConfigLoad({
      requestSeq,
      currentSeq: profileConfigLoadSeq,
      requestedProfileId: profile.id,
      activeProfileId: store.activeProfileId,
      dirty: store.profileConfigDirty,
    })) return;
    editorRef.value?.setValue(config || "");
    editorRef.value?.setReadOnly(Boolean(profile.subscriptionUrl));
    store.profileConfigOwnerId = profile.id;
    store.profileConfigDirty = false;
    setConfigEditorError("");
  } catch (err) {
    if (requestSeq !== profileConfigLoadSeq || store.activeProfileId !== profile.id) return;
    const message = err instanceof Error ? err.message : String(err);
    setConfigEditorError(message);
    actions.showMessage(message, "error");
  }
}

function onEditorChange(): void {
  store.profileConfigDirty = true;
  setConfigEditorError("");
}

async function discardConfig(): Promise<void> {
  if (!store.profileConfigDirty || !window.confirm("确定放弃当前 YAML 修改并重新加载吗？")) return;
  if (store.profileCreating) {
    editorRef.value?.setValue(defaultConfig);
    store.profileConfigDirty = false;
    return;
  }
  await loadProfileConfig(store.activeProfileId);
}

function setProfileCreateSource(source: ProfileCreateSource): void {
  if (!store.profileCreating || store.profileCreateSource === source) return;
  store.profileCreateSource = source;
  markProfileFormDirty();
  editorRef.value?.setReadOnly(source === "subscription");
}

// ---------------------------------------------------------------------
// Navigation -- startNewProfile()/selectProfile()/openProfileManager()/
// closeProfileManager() (app.ts:650-776). These four are the reason the
// last two are registered from this component below instead of staying in
// app.ts: all four need confirmDiscardChanges() plus direct editor calls,
// so they cannot be split the way saveProfile/deleteProfile/
// refreshSubscription were.
// ---------------------------------------------------------------------
function startNewProfile(): boolean {
  if (chrome.profileBusy) return false;
  if (!confirmDiscardChanges("新建配置档")) return false;
  profileContextSeq += 1;
  store.editDirty = false;
  store.view = "profiles";
  store.profileCreating = true;
  store.activeProfileId = "";
  store.profileCreateSource = "manual";
  store.profileFormVersion = 0;
  resetConfigEditor();
  populateProfileForm(null);
  editorRef.value?.setValue(defaultConfig);
  editorRef.value?.setReadOnly(false);
  store.profileConfigOwnerId = "__new__";
  clearProfileFormDirty();
  void nextTick(() => profileNameInputRef.value?.focus());
  return true;
}

interface SelectProfileOptions {
  force?: boolean;
  allowBusy?: boolean;
}

function selectProfile(profileId: string, options: SelectProfileOptions = {}): boolean {
  if (chrome.profileBusy && !options.allowBusy) return false;
  if (!options.force && !store.profileCreating && store.activeProfileId === profileId) {
    store.view = "profiles";
    if (store.profileConfigOwnerId !== profileId) void loadProfileConfig(profileId);
    return true;
  }
  if (!options.force && store.activeProfileId !== profileId && !confirmDiscardChanges("切换配置档")) return false;
  const profile = profileById(store, profileId);
  if (!profile) return false;
  profileContextSeq += 1;
  store.view = "profiles";
  store.profileCreating = false;
  store.activeProfileId = profile.id;
  store.profileFormVersion = 0;
  populateProfileForm(profile);
  clearProfileFormDirty();
  void loadProfileConfig(profile.id);
  return true;
}

function openProfileManager(profileId = ""): boolean {
  if (chrome.profileBusy) return false;
  if (store.view !== "profiles" && !confirmDiscardChanges("打开配置档管理")) return false;
  store.editDirty = false;
  store.view = "profiles";
  store.creating = false;
  const targetId = profileId || store.activeProfileId || activeInstance(store)?.profileId || store.profiles[0]?.id || "";
  if (targetId) return selectProfile(targetId, { force: true });
  return startNewProfile();
}

function closeProfileManager(): boolean {
  if (chrome.profileBusy) return false;
  if (!confirmDiscardChanges("返回实例")) return false;
  profileContextSeq += 1;
  store.view = "instances";
  store.profileCreating = false;
  store.profileFormDirty = false;
  resetConfigEditor();
  return true;
}

// Overrides app.ts's registrations for just these two keys. main.ts mounts
// this component (see the handoff report for the required addition) before
// app.ts's dynamic import runs, so app.ts's own registerActions({...}) call
// executes after this one -- it MUST drop openProfileManager/
// closeProfileManager from its object (and delete the now-dead function
// bodies) or its later call clobbers these back to broken,
// dead-`configEditor`-reference versions. CreateForm.vue already calls
// `actions.openProfileManager()`, so the action itself must stay valid.
registerActions({ openProfileManager, closeProfileManager });

// ---------------------------------------------------------------------
// Save / delete / refresh -- app.ts keeps the gated fetch (mutual exclusion
// via its existing saveProfileGate/deleteProfileGate/refreshSubscriptionGate,
// which is also what still drives chrome.profileBusy unchanged); this
// component owns the pre/post-flight orchestration that needs the editor
// handle and the local dirty/version bookkeeping. See the coordinator
// handoff message for the exact FleetActions signatures assumed below.
// ---------------------------------------------------------------------
interface SaveProfilePayload {
  creating: boolean;
  profileId: string;
  name: string;
  source: ProfileCreateSource;
  config?: string;
  subscriptionUrl?: string;
  autoUpdate?: boolean;
  updateIntervalMinutes?: number;
}

async function saveProfile(): Promise<void> {
  const profile = activeProfile.value;
  if (!profile && !store.profileCreating) return;
  if (chrome.profileBusy) return;
  const creating = store.profileCreating;
  // Guarded by the check above: `profile || store.profileCreating`, so
  // every `profile!` below only runs on the `!creating` branch, where that
  // guard proves `profile` non-null.
  const source: ProfileCreateSource = creating ? store.profileCreateSource : (profile!.subscriptionUrl ? "subscription" : "manual");
  const savedProfileId = creating ? "__new__" : profile!.id;
  const savedConfigVersion = editorRef.value?.getVersion() ?? 0;
  const savedFormVersion = store.profileFormVersion;
  const configMayChange = source === "manual"
    ? store.profileConfigDirty
    : !creating && subscriptionUrlInput.value.trim() !== profile!.subscriptionUrl;

  if (store.profileFormOwnerId !== savedProfileId || (source === "manual" && store.profileConfigOwnerId !== savedProfileId)) {
    setConfigEditorError("配置档已变化，请重新选择后再保存。");
    return;
  }
  setConfigEditorError("");
  const operationContext = captureOperationContext(savedProfileId);
  const payload: SaveProfilePayload = {
    creating,
    profileId: creating ? "" : profile!.id,
    name: profileNameInput.value.trim(),
    source,
  };
  if (source === "subscription") {
    payload.subscriptionUrl = subscriptionUrlInput.value.trim();
    payload.autoUpdate = subscriptionAutoUpdateInput.value;
    payload.updateIntervalMinutes = Number(subscriptionIntervalInput.value) || 0;
  } else {
    payload.config = editorRef.value?.getValue() ?? "";
  }
  saving.value = true;
  try {
    const saved = await actions.saveProfile(payload);
    if (!operationContextMatches(operationContext)) return;
    if (creating) profileContextSeq += 1;
    store.profileCreating = false;
    store.activeProfileId = saved.id;
    store.profileFormOwnerId = saved.id;
    store.profileConfigOwnerId = saved.id;
    const sameFormVersion = store.profileFormVersion === savedFormVersion;
    const sameConfigVersion = creating
      ? savedConfigVersion === (editorRef.value?.getVersion() ?? 0)
      : canClearSavedProfileConfig({
        savedProfileId,
        savedVersion: savedConfigVersion,
        activeProfileId: store.activeProfileId,
        currentVersion: editorRef.value?.getVersion() ?? 0,
      });
    if (sameFormVersion && sameConfigVersion) {
      clearProfileFormDirty();
      setConfigEditorError("");
      populateProfileForm(saved);
    }
    actions.showMessage(creating
      ? "配置档已创建。"
      : configMayChange ? "配置档已保存，引用它的运行中实例需要重启后生效。" : "配置档已保存。");
    if (source === "subscription" && sameFormVersion && store.view === "profiles" && !store.profileCreating && store.activeProfileId === saved.id) {
      await loadProfileConfig(saved.id);
    }
  } catch (err) {
    if (operationContextMatches(operationContext)) {
      const message = err instanceof Error ? err.message : String(err);
      setConfigEditorError(message);
      actions.showMessage(message, "error");
    }
  } finally {
    saving.value = false;
  }
}

async function deleteProfile(): Promise<void> {
  const profile = activeProfile.value;
  if (!profile) return;
  const refCount = profileReferenceCount(store, profile.id);
  if (refCount > 0) {
    actions.showMessage(`该配置档仍被 ${refCount} 个实例引用，无法删除。`, "error");
    return;
  }
  if (!window.confirm(`确定删除配置档 ${profile.name}？此操作不可撤销。`)) return;
  if (chrome.profileBusy) return;
  const operationContext = captureOperationContext(profile.id);
  deleting.value = true;
  try {
    // actions.deleteProfile() is expected to remove the row from
    // store.profiles itself (app.ts and this component share the same
    // reactive object), so store.profiles is already current once this
    // resolves -- no local splice needed.
    await actions.deleteProfile(profile.id);
    if (!operationContextMatches(operationContext)) return;
    profileContextSeq += 1;
    store.activeProfileId = store.profiles[0]?.id || "";
    store.profileFormDirty = false;
    resetConfigEditor();
    actions.showMessage("配置档已删除。");
    if (store.view === "profiles" && store.activeProfileId) {
      selectProfile(store.activeProfileId, { force: true, allowBusy: true });
    }
  } catch (err) {
    if (operationContextMatches(operationContext)) {
      const message = err instanceof Error ? err.message : String(err);
      actions.showMessage(message, "error");
    }
  } finally {
    deleting.value = false;
  }
}

async function refreshSubscription(): Promise<void> {
  const profile = activeProfile.value;
  if (!profile || store.profileFormDirty) {
    actions.showMessage("请先保存订阅设置，再立即更新。", "error");
    return;
  }
  if (chrome.profileBusy) return;
  const operationContext = captureOperationContext(profile.id);
  refreshingSub.value = true;
  try {
    const refreshed = await actions.refreshSubscriptionProfile(profile.id);
    if (!operationContextMatches(operationContext)) return;
    actions.showMessage("订阅已更新。运行中的实例需要重启后使用新的缓存配置。");
    populateProfileForm(refreshed);
    await loadProfileConfig(refreshed.id);
  } catch (err) {
    if (operationContextMatches(operationContext)) {
      const message = err instanceof Error ? err.message : String(err);
      actions.showMessage(message, "error");
    }
  } finally {
    refreshingSub.value = false;
  }
}
</script>

<template>
  <div class="profile-manager-head">
    <div class="panel-title">
      <div>
        <h2 id="profileManagerTitle">配置档管理</h2>
        <p>配置档保存共享 YAML 或订阅，多个实例可以同时引用同一份配置。</p>
      </div>
    </div>
    <button id="newProfileBtn" class="primary" type="button" :disabled="chrome.profileBusy" @click="startNewProfile">新建配置档</button>
  </div>
  <div class="profile-manager-grid">
    <nav class="profile-catalog" aria-label="配置档列表">
      <div class="profile-catalog-head">
        <h3>配置档</h3>
        <span id="profileCount">{{ store.profiles.length }} 个</span>
      </div>
      <div id="profileList" class="profile-list">
        <button
          v-for="profile in store.profiles"
          :key="profile.id"
          type="button"
          class="profile-row"
          :class="{ active: !store.profileCreating && store.activeProfileId === profile.id }"
          :disabled="chrome.profileBusy"
          :aria-current="!store.profileCreating && store.activeProfileId === profile.id ? 'true' : 'false'"
          :data-profile-id="profile.id"
          @click="selectProfile(profile.id)"
        >
          <span class="profile-row-main">{{ profile.name || "未命名配置档" }}</span>
          <span class="profile-row-meta">{{ profile.subscriptionUrl ? "订阅配置" : "手写配置" }} · {{ referenceCount(profile.id) > 0 ? `${referenceCount(profile.id)} 个实例` : "未使用" }}</span>
          <code class="profile-row-id">{{ profile.id }}</code>
        </button>
        <p v-if="!store.profiles.length" class="profile-list-empty">还没有配置档。</p>
      </div>
    </nav>
    <div class="profile-editor-pane">
      <div id="profileEditorEmpty" class="profile-editor-empty" :class="{ hidden: hasEditor }">
        <h3>选择配置档</h3>
        <p>从左侧选择已有配置档，或新建一份手写配置或订阅配置。</p>
      </div>
      <!--
        Always mounted, `.hidden`-toggled rather than v-if -- required so the
        CodeMirror host further down never sits behind a conditionally
        rendered ancestor (see YamlCodeEditor.vue's header comment).
      -->
      <section id="profileEditor" :class="{ hidden: !hasEditor }" aria-labelledby="profileEditorTitle">
        <div class="profile-editor-head">
          <div>
            <h3 id="profileEditorTitle">{{ store.profileCreating ? "新建配置档" : (activeProfile?.name || "") }}</h3>
            <p id="profileMeta">{{ profileMetaText }}</p>
          </div>
          <span id="profileReferenceBadge" class="reference-badge" :class="{ 'in-use': references > 0 }">{{ referenceBadgeText }}</span>
        </div>
        <div class="form-grid profile-basics">
          <label>
            <span>名称</span>
            <input id="profileName" ref="profileNameInputRef" v-model="profileNameInput" placeholder="我的订阅" :disabled="chrome.profileBusy" @input="markProfileFormDirty">
          </label>
          <label>
            <span>配置档 ID</span>
            <input id="profileId" :value="profileIdDisplay" readonly>
          </label>
        </div>
        <div id="profileSourceTabs" class="segmented" :class="{ hidden: !store.profileCreating }" role="group" aria-label="配置来源">
          <button id="profileManualMode" type="button" :class="{ active: store.profileCreateSource === 'manual' }" :disabled="chrome.profileBusy" @click="setProfileCreateSource('manual')">手写配置</button>
          <button id="profileSubscriptionMode" type="button" :class="{ active: store.profileCreateSource === 'subscription' }" :disabled="chrome.profileBusy" @click="setProfileCreateSource('subscription')">订阅链接</button>
        </div>
        <div id="subscriptionSettings" class="subscription-settings" :class="{ hidden: !isSubscription }">
          <label class="stacked">
            <span>订阅链接</span>
            <input id="subscriptionUrl" v-model="subscriptionUrlInput" placeholder="https://example.com/sub" :disabled="chrome.profileBusy" @input="markProfileFormDirty">
          </label>
          <div class="form-grid subscription-fields">
            <label>
              <span>更新间隔（分钟）</span>
              <input id="subscriptionInterval" v-model="subscriptionIntervalInput" type="number" min="15" placeholder="360" :disabled="chrome.profileBusy" @input="markProfileFormDirty" @change="markProfileFormDirty">
            </label>
            <label class="checkline">
              <input id="subscriptionAutoUpdate" v-model="subscriptionAutoUpdateInput" type="checkbox" :disabled="chrome.profileBusy" @change="markProfileFormDirty">
              <span>自动更新</span>
            </label>
            <div id="subscriptionInfo" class="subscription-info">
              <span>{{ subscriptionInfoText }}</span>
              <template v-if="subscriptionHomeUrl">
                <span> · 主页 </span>
                <a :href="subscriptionHomeUrl" target="_blank" rel="noopener noreferrer">{{ subscriptionHomeUrl }}</a>
              </template>
            </div>
          </div>
          <div class="actions">
            <button id="refreshSubscription" type="button" :disabled="store.profileCreating || store.profileFormDirty || chrome.profileBusy" @click="refreshSubscription">立即更新</button>
          </div>
        </div>
        <div id="profileConfigSection" :class="{ hidden: store.profileCreating && isSubscription }">
          <div class="config-editor-toolbar">
            <div class="config-editor-heading">
              <span id="configEditorLabel">YAML 配置</span>
              <span id="configEditorStatus" class="config-editor-status" role="status" aria-live="polite" :data-state="configEditorStatus.state">{{ configEditorStatus.text }}</span>
            </div>
            <div class="actions" role="toolbar" aria-label="配置编辑操作">
              <button id="findConfig" type="button" :disabled="findDisabled" @click="editorRef?.focusSearch()">查找</button>
              <button id="discardConfig" type="button" :disabled="discardDisabled" @click="discardConfig">放弃修改</button>
            </div>
          </div>
          <YamlCodeEditor ref="editorRef" @change="onEditorChange" @save="saveProfile" />
          <div id="configEditorError" class="config-editor-error" :class="{ hidden: !configEditorErrorText }" role="alert">{{ configEditorErrorText }}</div>
        </div>
        <p id="profileDeleteHint" class="profile-delete-hint">{{ deleteHintText }}</p>
        <div class="profile-editor-actions">
          <button id="saveProfile" class="primary" type="button" :disabled="saveDisabled" @click="saveProfile">保存配置档</button>
          <button id="deleteProfile" class="danger" type="button" :class="{ hidden: store.profileCreating }" :disabled="deleteDisabled" @click="deleteProfile">删除配置档</button>
        </div>
      </section>
    </div>
  </div>
</template>
