import { onUnmounted, reactive, watch } from "vue";

import { api } from "../../api.ts";
import { localizedMessage } from "../../messages.ts";
import type { ChainCandidate } from "../../chain-rules.ts";

/*
 * Names the chain picker may offer, from POST /api/instances/chain-candidates.
 *
 * A useX() factory, not module state: the create panel and the instance edit form
 * ask about different (profile, draft YAML) pairs at the same time, and one shared
 * result would make whichever form the user is not looking at overwrite the one
 * they are.
 *
 * The draft YAML is deliberately sent to the backend instead of parsed here. The
 * candidate set is defined by buildGlobalChainPlan(), and the only way to be sure
 * the picker never offers a name that save would reject is to ask the code that
 * rejects it.
 */
const debounceMs = 400;

export interface ChainCandidatesState {
  candidates: ChainCandidate[];
  providerNames: string[];
  /** A parse/collision problem in the draft local YAML. Already localized. */
  localError: string;
  /** The request itself failed. Already localized. */
  error: string;
  loading: boolean;
  truncated: boolean;
}

interface ChainCandidatesResponse {
  candidates?: ChainCandidate[];
  providerNames?: string[];
  localError?: string;
  truncated?: boolean;
}

export function useChainCandidates(
  profileId: () => string,
  localProxies: () => string,
  active: () => boolean,
): { state: ChainCandidatesState; refresh: () => void } {
  const state: ChainCandidatesState = reactive<ChainCandidatesState>({
    candidates: [],
    providerNames: [],
    localError: "",
    error: "",
    loading: false,
    truncated: false,
  });

  let timer: ReturnType<typeof setTimeout> | null = null;
  // Responses can land out of order once the YAML box is being typed into. Only
  // the newest request may write to state; an older one that resolves later would
  // otherwise reinstate candidates for text the user has already changed.
  let requestId = 0;

  async function fetchNow(): Promise<void> {
    const id = ++requestId;
    const profile = profileId();
    if (!profile) {
      state.candidates = [];
      state.providerNames = [];
      state.localError = "";
      state.error = "";
      state.loading = false;
      return;
    }
    state.loading = true;
    try {
      const body = await api<ChainCandidatesResponse>("/api/instances/chain-candidates", {
        method: "POST",
        body: JSON.stringify({ profileId: profile, localProxies: localProxies() }),
      });
      if (id !== requestId) return;
      state.candidates = Array.isArray(body?.candidates) ? body.candidates : [];
      state.providerNames = Array.isArray(body?.providerNames) ? body.providerNames : [];
      state.localError = body?.localError ? localizedMessage(body.localError) : "";
      state.truncated = Boolean(body?.truncated);
      state.error = "";
    } catch (err: unknown) {
      if (id !== requestId) return;
      state.error = localizedMessage(err instanceof Error ? err.message : err);
    } finally {
      if (id === requestId) state.loading = false;
    }
  }

  function schedule(delay: number): void {
    if (timer) clearTimeout(timer);
    if (!active()) return;
    timer = setTimeout(() => {
      timer = null;
      void fetchNow();
    }, delay);
  }

  // Becoming active or switching profile is a discrete user action, so it fetches
  // straight away; keystrokes in the YAML box debounce, because every character
  // would otherwise re-read and re-parse a possibly multi-MB subscription config.
  watch([active, profileId], () => schedule(0), { immediate: true });
  watch(localProxies, () => schedule(debounceMs));

  onUnmounted(() => {
    if (timer) clearTimeout(timer);
    // Invalidate anything still in flight so a late response cannot touch state
    // belonging to a torn-down form.
    requestId += 1;
  });

  return { state, refresh: () => schedule(0) };
}
