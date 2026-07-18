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

export function shouldApplyCreateProfileConfig({ requestSeq, currentSeq, requestedProfileId, currentProfileId }) {
  return requestSeq === currentSeq && requestedProfileId !== "" && requestedProfileId === currentProfileId;
}

export function shouldApplyConfigLoad({ requestedInstanceId, requestedProfileId, activeInstanceId, activeProfileId, dirty }) {
  return !dirty && requestedInstanceId === activeInstanceId && requestedProfileId === activeProfileId;
}

export function canClearSavedConfig({ savedInstanceId, savedProfileId, savedVersion, activeInstanceId, activeProfileId, currentVersion }) {
  return savedInstanceId === activeInstanceId && savedProfileId === activeProfileId && savedVersion === currentVersion;
}

export function profileOptionLabel(profile, referenceCount) {
  const name = String(profile?.name || "未命名配置档");
  const id = String(profile?.id || "unknown");
  const count = Math.max(0, Number(referenceCount) || 0);
  return `${name} · ${id} · ${count > 0 ? `${count} 实例` : "未使用"}`;
}
