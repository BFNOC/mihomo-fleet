import { computed, ref } from "vue";
import { store } from "../../store.ts";
import { profileById, profileReferences } from "../../state.ts";
import type { FleetInstance, FleetProfile } from "../../state.ts";
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

// The profile the inputs were last populated from. saveProfile() sends only the
// fields that differ from it, so a dirty form cannot write back a field it never
// touched over a newer value saved elsewhere.
let formBaseline: FleetProfile | null = null;

export function profileFormBaseline(): FleetProfile | null {
  return formBaseline;
}

export function populateProfileForm(profile: FleetProfile | null): void {
  formBaseline = profile;
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

// One definition, in services/navigation.ts. This copy used to omit
// store.createDirty, so opening the profile manager discarded a half-filled
// create-instance form without asking.
export { confirmDiscardChanges, hasUnsavedChanges } from "../../services/navigation.ts";

// ---------------------------------------------------------------------------
// Derived selection
// ---------------------------------------------------------------------------
export const activeProfile = computed<FleetProfile | null>(() => profileById(store, store.activeProfileId));
export const hasEditor = computed(() => store.profileCreating || Boolean(activeProfile.value));
export const isSubscription = computed(() =>
  store.profileCreating ? store.profileCreateSource === "subscription" : Boolean(activeProfile.value?.subscriptionUrl),
);
// The instances themselves, not just the count -- ProfileReferenceList.vue
// reads this to turn "N 个实例引用" into a jump list instead of a dead-end
// number. `references` (the count) derives from it so the two can never
// disagree about which profile they're describing.
export const referencingInstances = computed<FleetInstance[]>(() =>
  activeProfile.value ? profileReferences(store, activeProfile.value.id) : [],
);
export const references = computed(() => referencingInstances.value.length);

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
