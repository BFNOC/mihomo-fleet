import { computed, ref } from "vue";
import { store } from "../../store.ts";
import { actions } from "../../bridge.ts";
import { profileById } from "../../state.ts";
import { defaultConfig } from "../../constants.ts";
import { localizedMessage } from "../../messages.ts";
import { shouldApplyProfileConfigLoad } from "../../app-logic.ts";
import type YamlCodeEditor from "./YamlCodeEditor.vue";
import { activeProfile, isSubscription } from "./profile-context.ts";

// The YAML editor handle plus everything that sequences writes into it.
//
// `editorRef` is bound by ProfileManagerView.vue's `ref="editorRef"`. It stays a
// direct child ref of that template on purpose: wrapping YamlCodeEditor in
// another component would put a layer between them, and every `editorRef.value?.
// foo()` below would silently become a no-op rather than fail loudly.
export const editorRef = ref<InstanceType<typeof YamlCodeEditor> | null>(null);

export const configEditorErrorText = ref("");

// False until YamlCodeEditor.vue's dynamically imported CodeMirror chunk lands
// (see its header). Purely for the status line -- every editor call here is
// buffered on the other side, so nothing has to wait on it.
export const editorReady = ref(false);
export const editorLoadErrorText = ref("");

// Guards a slow GET /config response from clobbering a newer load or a
// since-typed edit.
let profileConfigLoadSeq = 0;

export function setConfigEditorError(message: string): void {
  configEditorErrorText.value = message ? localizedMessage(message) : "";
}

export function resetConfigEditor(): void {
  profileConfigLoadSeq += 1;
  editorRef.value?.setValue("");
  editorRef.value?.setReadOnly(true);
  store.profileConfigOwnerId = "";
  store.profileConfigDirty = false;
  setConfigEditorError("");
}

export function loadDefaultConfig(): void {
  editorRef.value?.setValue(defaultConfig);
  editorRef.value?.setReadOnly(false);
  store.profileConfigOwnerId = "__new__";
}

export async function loadProfileConfig(profileId: string): Promise<void> {
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

export async function discardConfig(): Promise<void> {
  if (!store.profileConfigDirty || !window.confirm("确定放弃当前 YAML 修改并重新加载吗？")) return;
  if (store.profileCreating) {
    editorRef.value?.setValue(defaultConfig);
    store.profileConfigDirty = false;
    return;
  }
  await loadProfileConfig(store.activeProfileId);
}

export function onEditorChange(): void {
  store.profileConfigDirty = true;
  setConfigEditorError("");
}

export function onEditorReady(): void {
  editorReady.value = true;
}

// Kept out of configEditorErrorText on purpose. That ref is transient -- every
// successful load/save clears it -- whereas a failed chunk load is permanent for
// the page's lifetime, and a permanently blank YAML box reads as "this profile
// has no config", which would invite saving over one that is in fact still on
// disk.
export function onEditorLoadError(message: string): void {
  editorLoadErrorText.value = `YAML 编辑器加载失败，无法编辑当前配置：${message}`;
}

// Which profile the editor's *content* belongs to, as opposed to the form's.
export const configContextMatches = computed(() => {
  const profile = activeProfile.value;
  return store.profileCreating
    ? store.profileConfigOwnerId === "__new__"
    : Boolean(profile && store.profileConfigOwnerId === profile.id);
});

// Per-operation flags, set by profile-operations.ts. chrome.profileBusy still
// drives every disabled state; these three exist only to say *which* operation
// is running in the status line below.
export const saving = ref(false);
export const deleting = ref(false);
export const refreshingSub = ref(false);

// Branch order is load-bearing: a permanent load failure must outrank a
// transient one, and both must outrank "still loading".
export const configEditorStatus = computed<{ text: string; state: string }>(() => {
  if (!activeProfile.value && !store.profileCreating) return { text: "未选择配置档", state: "idle" };
  if (saving.value) return { text: "正在保存", state: "saving" };
  if (deleting.value) return { text: "正在删除配置档", state: "saving" };
  if (refreshingSub.value) return { text: "正在更新订阅", state: "saving" };
  if (editorLoadErrorText.value) return { text: "编辑器不可用", state: "error" };
  if (configEditorErrorText.value) return { text: "操作失败，修改未丢失", state: "error" };
  if (!editorReady.value) return { text: "编辑器加载中", state: "loading" };
  if (isSubscription.value) return { text: "订阅缓存，只读", state: "readonly" };
  if (store.profileConfigDirty) return { text: "未保存修改", state: "dirty" };
  if (configContextMatches.value) return { text: "已保存", state: "saved" };
  return { text: "正在加载", state: "loading" };
});
