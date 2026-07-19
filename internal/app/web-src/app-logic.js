export function createActionGate() {
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

export function shouldApplyProfileConfigLoad({ requestSeq, currentSeq, requestedProfileId, activeProfileId, dirty }) {
  return !dirty && requestSeq === currentSeq && requestedProfileId !== "" && requestedProfileId === activeProfileId;
}

export function canClearSavedProfileConfig({ savedProfileId, savedVersion, activeProfileId, currentVersion }) {
  return savedProfileId === activeProfileId && savedVersion === currentVersion;
}

export function shouldApplyProfileOperation({
  requestContextSeq,
  currentContextSeq,
  requestedProfileId,
  activeProfileId,
  view,
}) {
  return requestContextSeq === currentContextSeq
    && view === "profiles"
    && requestedProfileId !== ""
    && requestedProfileId === activeProfileId;
}

export function profileOptionLabel(profile, referenceCount) {
  const name = String(profile?.name || "未命名配置档");
  const count = Math.max(0, Number(referenceCount) || 0);
  return `${name} · ${count > 0 ? `${count} 个实例` : "未使用"}`;
}
