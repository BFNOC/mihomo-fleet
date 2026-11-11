/**
 * Return shape of `createActionGate()`. Exported so callers (e.g. app.ts's
 * `beginProfileOperation(gate)` helper) can annotate a gate parameter without
 * redefining this shape.
 */
export interface ActionGate {
  begin(): boolean;
  end(): void;
  isRunning(): boolean;
}

export function createActionGate(): ActionGate {
  let running = false;
  return {
    begin() {
      if (running) return false;
      running = true;
      return true;
    },
    end() {
      running = false;
    },
    isRunning() {
      return running;
    },
  };
}

export interface ShouldApplyProfileConfigLoadParams {
  requestSeq: number;
  currentSeq: number;
  requestedProfileId: string;
  activeProfileId: string;
  dirty: boolean;
}

export function shouldApplyProfileConfigLoad({
  requestSeq,
  currentSeq,
  requestedProfileId,
  activeProfileId,
  dirty,
}: ShouldApplyProfileConfigLoadParams): boolean {
  return !dirty && requestSeq === currentSeq && requestedProfileId !== "" && requestedProfileId === activeProfileId;
}

export interface CanClearSavedProfileConfigParams {
  savedProfileId: string;
  savedVersion: number;
  activeProfileId: string;
  currentVersion: number;
}

export function canClearSavedProfileConfig({
  savedProfileId,
  savedVersion,
  activeProfileId,
  currentVersion,
}: CanClearSavedProfileConfigParams): boolean {
  return savedProfileId === activeProfileId && savedVersion === currentVersion;
}

export interface ShouldApplyProfileOperationParams {
  requestContextSeq: number;
  currentContextSeq: number;
  requestedProfileId: string;
  activeProfileId: string;
  // The app's top-level view is owned by state.ts (not typed as a literal
  // union there yet), so this is kept as a plain string comparison rather
  // than importing/duplicating a union type from a module outside this
  // file's scope.
  view: string;
}

export function shouldApplyProfileOperation({
  requestContextSeq,
  currentContextSeq,
  requestedProfileId,
  activeProfileId,
  view,
}: ShouldApplyProfileOperationParams): boolean {
  return requestContextSeq === currentContextSeq
    && view === "profiles"
    && requestedProfileId !== ""
    && requestedProfileId === activeProfileId;
}

/**
 * Minimal shape needed to render a profile's option label. Narrower than the
 * full Profile domain object (owned by state.ts) on purpose: this module only
 * ever reads `.name`. `id` is listed too (unused here) only so object
 * literals shaped like a real profile (id + name, as used in tests and by
 * callers) don't trip TypeScript's excess-property check.
 */
export interface ProfileLabelSource {
  id?: string;
  name?: string;
}

export function profileOptionLabel(profile: ProfileLabelSource | null | undefined, referenceCount: number): string {
  const name = String(profile?.name || "未命名配置档");
  const count = Math.max(0, Number(referenceCount) || 0);
  return `${name} · ${count > 0 ? `${count} 个实例` : "未使用"}`;
}

export interface ProfileFormBaseline {
  name: string;
  subscriptionUrl?: string;
  autoUpdate: boolean;
  updateIntervalMinutes?: number;
}

export interface ProfileFormInputs {
  name: string;
  subscriptionUrl: string;
  autoUpdate: boolean;
  interval: string;
}

export interface ProfileFieldChanges {
  /** "" means "keep": the backend's PUT ignores an empty name. */
  name: string;
  subscriptionUrl?: string;
  autoUpdate?: boolean;
  updateIntervalMinutes?: number;
}

/**
 * The profile PUT fields the user actually changed relative to the profile the
 * form was populated from. Omitted fields keep whatever the backend holds now,
 * so a field the user never touched cannot overwrite a newer value saved in
 * another tab or set by a subscription refresh. With no baseline (creating),
 * every field is sent.
 */
export function changedProfileFields(base: ProfileFormBaseline | null, input: ProfileFormInputs): ProfileFieldChanges {
  const interval = Number(input.interval) || 0;
  if (!base) {
    return { name: input.name, subscriptionUrl: input.subscriptionUrl, autoUpdate: input.autoUpdate, updateIntervalMinutes: interval };
  }
  const changes: ProfileFieldChanges = { name: input.name !== base.name ? input.name : "" };
  if (input.subscriptionUrl !== (base.subscriptionUrl || "")) changes.subscriptionUrl = input.subscriptionUrl;
  if (input.autoUpdate !== Boolean(base.autoUpdate)) changes.autoUpdate = input.autoUpdate;
  // Compared as the form displays it: populateProfileForm() shows "360" for an
  // unset interval, so an untouched box must not count as a change.
  if (input.interval !== String(base.updateIntervalMinutes || "360")) changes.updateIntervalMinutes = interval;
  return changes;
}
