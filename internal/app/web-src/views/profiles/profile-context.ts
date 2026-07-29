import { computed, ref } from "vue";
import { store } from "../../store.ts";
import { profileById, profileReferenceCount } from "../../state.ts";
import type { FleetProfile } from "../../state.ts";
import { shouldApplyProfileOperation } from "../../app-logic.ts";

// Which profile the editor is currently about, what the user has typed into it,
// and whether an operation that started earlier still refers to the same thing.
//
// These belong together: the guard at the bottom compares the form's owner id
// against the live selection, so the fields and the guard are one mechanism.
// Module scope rather than a useX() factory, so every profile module below
// shares one instance of this state.

// ---------------------------------------------------------------------------
// Form fields
// ---------------------------------------------------------------------------
export const profileNameInputRef = ref<HTMLInputElement | null>(null);
export const profileNameInput = ref("");
export const profileIdDisplay = ref("");
export const subscriptionUrlInput = ref("");
export const subscriptionAutoUpdateInput = ref(true);
export const subscriptionIntervalInput = ref("360");

export function populateProfileForm(profile: FleetProfile | null): void {
  store.profileFormOwnerId = profile?.id || "__new__";
  profileNameInput.value = profile?.name || "";
  profileIdDisplay.value = profile?.id || "创建后自动生成";
  subscriptionUrlInput.value = profile?.subscriptionUrl || "";
  subscriptionAutoUpdateInput.value = profile ? Boolean(profile.autoUpdate) : true;
  subscriptionIntervalInput.value = String(profile?.updateIntervalMinutes || "360");
}

// ---------------------------------------------------------------------------
// Dirty state
// ---------------------------------------------------------------------------
export function markProfileFormDirty(): void {
  store.profileFormDirty = true;
  store.profileFormVersion += 1;
}

export function clearProfileFormDirty(): void {
  store.profileFormDirty = false;
  store.profileConfigDirty = false;
}

export function hasUnsavedChanges(): boolean {
  return store.editDirty || store.profileFormDirty || store.profileConfigDirty;
}

export function confirmDiscardChanges(action: string): boolean {
  if (!hasUnsavedChanges()) return true;
  return window.confirm(`有未保存的修改。确定放弃并${action}吗？`);
}

// ---------------------------------------------------------------------------
// Derived selection
// ---------------------------------------------------------------------------
export const activeProfile = computed<FleetProfile | null>(() => profileById(store, store.activeProfileId));
export const hasEditor = computed(() => store.profileCreating || Boolean(activeProfile.value));
export const isSubscription = computed(() =>
  store.profileCreating ? store.profileCreateSource === "subscription" : Boolean(activeProfile.value?.subscriptionUrl),
);
export const references = computed(() => (activeProfile.value ? profileReferenceCount(store, activeProfile.value.id) : 0));

// ---------------------------------------------------------------------------
// Operation-context guard
// ---------------------------------------------------------------------------
// Only the navigation functions advance this counter -- they are the only things
// that change which profile is "in context".
let profileContextSeq = 0;

export function advanceProfileContext(): void {
  profileContextSeq += 1;
}

export interface OperationContext {
  contextSeq: number;
  profileId: string;
}

function activeProfileContextId(): string {
  return store.profileCreating ? "__new__" : store.activeProfileId;
}

export function captureOperationContext(profileId: string = activeProfileContextId()): OperationContext {
  return { contextSeq: profileContextSeq, profileId };
}

/**
 * True when the operation that captured `context` is still about the profile on
 * screen, so its result may be applied.
 *
 * Load-bearing across every await in profile-operations.ts. It compares
 * store.profileFormOwnerId against the live selection, which is why the delete
 * path must not re-poll the fleet before calling this: refreshFleet() moves
 * store.activeProfileId off a profile that no longer exists, and this then
 * reports "stale" on every successful delete.
 */
export function operationContextMatches(context: OperationContext): boolean {
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
