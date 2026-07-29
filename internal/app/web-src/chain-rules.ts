/*
 * Ordering and validity rules for the global-chain `chain` array, mirrored from
 * buildGlobalChainPlan() (internal/app/config.go:248-314) so the chain picker can
 * refuse a bad arrangement in place instead of letting the user click 保存 and
 * read a translated backend error.
 *
 * The backend stays the authority: every rule here has a counterpart there, and
 * the picker's job is only to stop the user reaching a state the counterpart
 * already rejects. Candidate names come from POST /api/instances/chain-candidates
 * -- not parsed client-side -- so this module never has to reason about YAML.
 */
import { chainRelayGroupName, chainSelectGroupName } from "./constants.ts";

/** One name the chain is allowed to reference. Mirrors Go's ChainCandidate. */
export interface ChainCandidate {
  name: string;
  /** "group" (节点选择) | "local" (draft local YAML) | "profile" (profile proxies). */
  kind: string;
}

const candidateKindLabels: Record<string, string> = {
  group: "策略组",
  local: "本地节点",
  profile: "配置档节点",
};

export function candidateKindLabel(kind: string): string {
  return candidateKindLabels[kind] || "节点";
}

/** Candidates not already used, in candidate order. The chain forbids duplicates. */
export function unusedCandidates(
  candidates: readonly ChainCandidate[],
  chain: readonly string[],
): ChainCandidate[] {
  const used = new Set(chain);
  return candidates.filter((candidate) => !used.has(candidate.name));
}

/** Moves the member at `index` by `delta`, or returns the input when that would fall off an end. */
export function moveChainMember(chain: readonly string[], index: number, delta: number): string[] {
  const target = index + delta;
  if (index < 0 || index >= chain.length || target < 0 || target >= chain.length) {
    return [...chain];
  }
  const next = [...chain];
  const [member] = next.splice(index, 1);
  next.splice(target, 0, member as string);
  return next;
}

export function removeChainMember(chain: readonly string[], index: number): string[] {
  if (index < 0 || index >= chain.length) return [...chain];
  return chain.filter((_, position) => position !== index);
}

/** Appends `name` unless it is blank or already present (Go rejects duplicates). */
export function addChainMember(chain: readonly string[], name: string): string[] {
  const member = String(name || "").trim();
  if (!member || chain.includes(member)) return [...chain];
  return [...chain, member];
}

/**
 * The chain the backend substitutes when the field is left empty
 * (buildGlobalChainPlan lines 249-255). Shown as a hint so an empty picker does
 * not read as "nothing will happen".
 */
export function defaultChain(
  candidates: readonly ChainCandidate[],
  providerNames: readonly string[] = [],
): string[] {
  const local = candidates.filter((candidate) => candidate.kind === "local").map((c) => c.name);
  const profile = candidates.filter((candidate) => candidate.kind === "profile");
  if (local.length && (profile.length || providerNames.length)) {
    return [...local, chainSelectGroupName];
  }
  return [chainSelectGroupName];
}

/**
 * "" when the backend would accept this chain, otherwise the Chinese reason.
 * Each branch mirrors one error in buildGlobalChainPlan; the wording matches the
 * localized strings in constants.ts so both paths read identically.
 */
export function chainProblem(
  chain: readonly string[],
  candidates: readonly ChainCandidate[],
  providerNames: readonly string[] = [],
): string {
  const known = new Set(candidates.map((candidate) => candidate.name));
  const seen = new Set<string>();
  for (const name of chain) {
    if (name === chainRelayGroupName) {
      return `链路顺序不能引用 ${name} 自身。`;
    }
    if (seen.has(name)) {
      return `链路顺序重复引用了 ${name}。`;
    }
    seen.add(name);
    if (name !== chainSelectGroupName && !known.has(name)) {
      return `链路顺序引用了不存在的节点或组：${name}。`;
    }
  }
  if (!chain.includes(chainSelectGroupName)) return "";
  // 节点选择 offers whatever proxy is not already pinned into the chain. Consume
  // them all with no provider to fall back on and the generated group is empty,
  // which is exactly the backend's "no selectable proxy" refusal.
  const selectable = candidates.filter(
    (candidate) => candidate.kind !== "group" && !seen.has(candidate.name),
  );
  if (!selectable.length && !providerNames.length) {
    return "链路节点移除后没有可选择的节点。请补充订阅/出口节点，或调整链路顺序。";
  }
  return "";
}
