const defaultConfig = `mixed-port: 7890
allow-lan: false
mode: rule
log-level: info
proxies: []
proxy-groups:
  - name: Proxy
    type: select
    proxies:
      - DIRECT
rules:
  - MATCH,DIRECT
`;

const newProfileValue = "__new__";

const state = {
  system: null,
  profiles: [],
  instances: [],
  activeId: localStorage.getItem("activeInstance") || "",
  activeTab: "overview",
  creating: false,
};

const el = {
  systemLine: document.querySelector("#systemLine"),
  systemWarning: document.querySelector("#systemWarning"),
  instanceSelect: document.querySelector("#instanceSelect"),
  instanceList: document.querySelector("#instanceList"),
  message: document.querySelector("#message"),
  newBtn: document.querySelector("#newBtn"),
  emptyCreate: document.querySelector("#emptyCreate"),
  createPanel: document.querySelector("#createPanel"),
  emptyPanel: document.querySelector("#emptyPanel"),
  detailPanel: document.querySelector("#detailPanel"),
  createName: document.querySelector("#createName"),
  createProfile: document.querySelector("#createProfile"),
  createProfileName: document.querySelector("#createProfileName"),
  createMixedPort: document.querySelector("#createMixedPort"),
  createControllerPort: document.querySelector("#createControllerPort"),
  createConfig: document.querySelector("#createConfig"),
  createSubmit: document.querySelector("#createSubmit"),
  createCancel: document.querySelector("#createCancel"),
  detailName: document.querySelector("#detailName"),
  detailMeta: document.querySelector("#detailMeta"),
  metricStatus: document.querySelector("#metricStatus"),
  metricPid: document.querySelector("#metricPid"),
  metricMixed: document.querySelector("#metricMixed"),
  metricController: document.querySelector("#metricController"),
  startBtn: document.querySelector("#startBtn"),
  stopBtn: document.querySelector("#stopBtn"),
  restartBtn: document.querySelector("#restartBtn"),
  deleteBtn: document.querySelector("#deleteBtn"),
  overviewMixed: document.querySelector("#overviewMixed"),
  overviewController: document.querySelector("#overviewController"),
  overviewProfile: document.querySelector("#overviewProfile"),
  overviewUserConfig: document.querySelector("#overviewUserConfig"),
  overviewRuntimeConfig: document.querySelector("#overviewRuntimeConfig"),
  overviewSelection: document.querySelector("#overviewSelection"),
  editName: document.querySelector("#editName"),
  editProfile: document.querySelector("#editProfile"),
  editMixedPort: document.querySelector("#editMixedPort"),
  editControllerPort: document.querySelector("#editControllerPort"),
  saveBasics: document.querySelector("#saveBasics"),
  configEditor: document.querySelector("#configEditor"),
  saveConfig: document.querySelector("#saveConfig"),
  proxiesList: document.querySelector("#proxiesList"),
  logs: document.querySelector("#logs"),
};

function active() {
  return state.instances.find((item) => item.id === state.activeId) || state.instances[0] || null;
}

function profileById(id) {
  return state.profiles.find((profile) => profile.id === id) || null;
}

async function api(path, options = {}) {
  const res = await fetch(path, {
    headers: { "Content-Type": "application/json", "X-Mihomo-Fleet": "1", ...(options.headers || {}) },
    ...options,
  });
  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`;
    try {
      const body = await res.json();
      if (body.error) message = body.error;
    } catch {
      // keep status message
    }
    throw new Error(message);
  }
  if (res.status === 204) return null;
  return res.json();
}

function showMessage(text, kind = "info") {
  if (!text) {
    el.message.classList.add("hidden");
    el.message.textContent = "";
    return;
  }
  el.message.textContent = text;
  el.message.className = `message ${kind === "error" ? "error" : ""}`;
}

async function refresh() {
  try {
    const [system, profiles, list] = await Promise.all([
      api("/api/system"),
      api("/api/profiles"),
      api("/api/instances"),
    ]);
    state.system = system;
    state.profiles = profiles.profiles || [];
    state.instances = list.instances || [];
    if (!state.activeId && state.instances.length) {
      state.activeId = state.instances[0].id;
    }
    if (state.activeId && !state.instances.some((item) => item.id === state.activeId)) {
      state.activeId = state.instances[0]?.id || "";
    }
    localStorage.setItem("activeInstance", state.activeId);
    render();
    await refreshActiveDetails();
  } catch (err) {
    showMessage(err.message, "error");
  }
}

function render() {
  const selected = active();
  renderSystem();
  renderSelector(selected);
  renderList(selected);
  renderPanels(selected);
  updateCreateProfileControls();
}

function renderSystem() {
  if (!state.system) return;
  const found = state.system.mihomoFound;
  el.systemLine.textContent = found
    ? `Controller 127.0.0.1:${state.system.port}, mihomo ${state.system.version || "detected"}`
    : `Controller 127.0.0.1:${state.system.port}, mihomo missing`;
  if (!found) {
    el.systemWarning.textContent = "mihomo was not found in PATH. You can create instances, but Start needs the binary or the -mihomo flag.";
    el.systemWarning.classList.remove("hidden");
  } else {
    el.systemWarning.classList.add("hidden");
  }
}

function renderSelector(selected) {
  el.instanceSelect.innerHTML = "";
  if (!state.instances.length) {
    const opt = document.createElement("option");
    opt.textContent = "No instances";
    opt.value = "";
    el.instanceSelect.append(opt);
    return;
  }
  for (const item of state.instances) {
    const opt = document.createElement("option");
    opt.value = item.id;
    opt.textContent = `${item.name} (${item.status})`;
    opt.selected = selected && selected.id === item.id;
    el.instanceSelect.append(opt);
  }
}

function renderList(selected) {
  el.instanceList.innerHTML = "";
  for (const item of state.instances) {
    const button = document.createElement("button");
    button.className = `instance-row ${selected && selected.id === item.id ? "active" : ""}`;
    button.type = "button";
    const profile = item.profileName || item.profileId || "no profile";
    const choice = item.selectedProxy ? ` · ${item.selectedGroup}/${item.selectedProxy}` : "";
    button.innerHTML = `
      <div class="row-main">
        <span class="row-name"></span>
        <span class="status ${item.status}">${item.status}</span>
      </div>
      <div class="row-meta"></div>
    `;
    button.querySelector(".row-name").textContent = item.name;
    button.querySelector(".row-meta").textContent = `mixed ${item.mixedPort} · ${profile}${choice}`;
    button.addEventListener("click", () => selectInstance(item.id));
    el.instanceList.append(button);
  }
}

function renderPanels(selected) {
  el.createPanel.classList.toggle("hidden", !state.creating);
  el.emptyPanel.classList.toggle("hidden", state.creating || state.instances.length > 0);
  el.detailPanel.classList.toggle("hidden", state.creating || !selected);
  if (!selected) return;

  el.detailName.textContent = selected.name;
  el.detailMeta.textContent = selected.lastError || `${selected.status} · ${selected.id}`;
  el.metricStatus.textContent = selected.status;
  el.metricPid.textContent = selected.pid || "none";
  el.metricMixed.textContent = selected.mixedPort;
  el.metricController.textContent = selected.controllerPort;
  el.overviewMixed.textContent = `127.0.0.1:${selected.mixedPort}`;
  el.overviewController.textContent = `127.0.0.1:${selected.controllerPort}`;
  el.overviewProfile.textContent = selected.profileName || selected.profileId || "none";
  el.overviewUserConfig.textContent = selected.profileConfigPath || selected.userConfigPath;
  el.overviewRuntimeConfig.textContent = selected.runtimeConfigPath;
  el.overviewSelection.textContent = selected.selectedProxy ? `${selected.selectedGroup} -> ${selected.selectedProxy}` : "none";
  el.editName.value = selected.name;
  renderProfileOptions(el.editProfile, selected.profileId, false);
  el.editMixedPort.value = selected.mixedPort;
  el.editControllerPort.value = selected.controllerPort;
  el.startBtn.disabled = selected.status === "running";
  el.stopBtn.disabled = selected.status !== "running";
  el.restartBtn.disabled = false;
}

function renderProfileOptions(select, selectedId, allowNew) {
  const current = selectedId || select.value;
  select.innerHTML = "";
  if (allowNew) {
    const opt = document.createElement("option");
    opt.value = newProfileValue;
    opt.textContent = "Create new profile";
    select.append(opt);
  }
  for (const profile of state.profiles) {
    const opt = document.createElement("option");
    opt.value = profile.id;
    opt.textContent = profile.name;
    select.append(opt);
  }
  if (current && [...select.options].some((opt) => opt.value === current)) {
    select.value = current;
  } else if (allowNew && state.profiles.length === 0) {
    select.value = newProfileValue;
  } else if (select.options.length > 0) {
    select.value = select.options[allowNew && state.profiles.length ? 1 : 0].value;
  }
}

async function updateCreateProfileControls() {
  if (!el.createProfile.options.length) {
    renderProfileOptions(el.createProfile, el.createProfile.value, true);
  }
  const createNew = el.createProfile.value === newProfileValue || state.profiles.length === 0;
  el.createProfileName.disabled = !createNew;
  el.createConfig.disabled = !createNew;
  if (createNew) {
    if (!el.createConfig.value) el.createConfig.value = defaultConfig;
    return;
  }
  const selectedProfile = profileById(el.createProfile.value);
  if (!selectedProfile || el.createConfig.dataset.profileId === selectedProfile.id) return;
  el.createConfig.dataset.profileId = selectedProfile.id;
  try {
    const payload = await api(`/api/profiles/${selectedProfile.id}/config`);
    el.createConfig.value = payload.config || "";
  } catch (err) {
    el.createConfig.value = err.message;
  }
}

function selectInstance(id) {
  if (state.activeId !== id) {
    el.configEditor.value = "";
    el.configEditor.dataset.id = "";
    el.configEditor.dataset.profileId = "";
    el.configEditor.dataset.dirty = "";
  }
  state.activeId = id;
  state.creating = false;
  localStorage.setItem("activeInstance", id);
  render();
  refreshActiveDetails();
}

function showCreate() {
  state.creating = true;
  el.createName.value = "";
  el.createMixedPort.value = "";
  el.createControllerPort.value = "";
  renderProfileOptions(el.createProfile, state.profiles[0]?.id || newProfileValue, true);
  el.createProfileName.value = "";
  el.createConfig.value = defaultConfig;
  el.createConfig.dataset.profileId = "";
  showMessage("");
  render();
}

async function refreshActiveDetails() {
  const selected = active();
  if (!selected || state.creating) return;
  if (
    el.configEditor.dataset.dirty !== "1" &&
    (!el.configEditor.value ||
      el.configEditor.dataset.id !== selected.id ||
      el.configEditor.dataset.profileId !== selected.profileId)
  ) {
    try {
      const cfg = await api(`/api/instances/${selected.id}/config`);
      el.configEditor.value = cfg.config;
      el.configEditor.dataset.id = selected.id;
      el.configEditor.dataset.profileId = selected.profileId;
      el.configEditor.dataset.dirty = "";
    } catch (err) {
      showMessage(err.message, "error");
    }
  }
  if (state.activeTab === "logs") await refreshLogs();
  if (state.activeTab === "proxies") await refreshProxies();
}

async function refreshLogs() {
  const selected = active();
  if (!selected) return;
  try {
    const payload = await api(`/api/instances/${selected.id}/logs`);
    el.logs.textContent = (payload.lines || []).join("\n") || "No process logs yet.";
    el.logs.scrollTop = el.logs.scrollHeight;
  } catch (err) {
    el.logs.textContent = err.message;
  }
}

async function refreshProxies() {
  const selected = active();
  if (!selected) return;
  if (selected.status !== "running") {
    el.proxiesList.innerHTML = `<div class="warning">Start this instance to query mihomo proxies.</div>`;
    return;
  }
  try {
    const payload = await api(`/api/mihomo/${selected.id}/proxies`);
    const proxies = payload.proxies || {};
    const groups = Object.values(proxies).filter((item) => Array.isArray(item.all));
    if (!groups.length) {
      el.proxiesList.innerHTML = `<div class="warning">No proxy groups returned by mihomo.</div>`;
      return;
    }
    el.proxiesList.innerHTML = "";
    for (const group of groups) {
      const row = document.createElement("div");
      row.className = "proxy-row";
      row.innerHTML = `
        <strong></strong>
        <select></select>
        <button type="button">Select</button>
      `;
      row.querySelector("strong").textContent = group.name;
      const select = row.querySelector("select");
      for (const name of group.all) {
        const opt = document.createElement("option");
        opt.value = name;
        opt.textContent = name;
        opt.selected = group.now === name;
        select.append(opt);
      }
      row.querySelector("button").addEventListener("click", async () => {
        try {
          const updated = await api(`/api/instances/${selected.id}/selection`, {
            method: "POST",
            body: JSON.stringify({ group: group.name, proxy: select.value, apply: true }),
          });
          state.instances = state.instances.map((item) => (item.id === updated.id ? updated : item));
          showMessage(`Saved ${group.name} -> ${select.value}.`);
          render();
          await refreshProxies();
        } catch (err) {
          showMessage(err.message, "error");
        }
      });
      el.proxiesList.append(row);
    }
  } catch (err) {
    el.proxiesList.innerHTML = `<div class="message error">${escapeHTML(err.message)}</div>`;
  }
}

function escapeHTML(input) {
  return String(input).replace(/[&<>"']/g, (ch) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#039;",
  }[ch]));
}

async function runAction(path, success) {
  try {
    await api(path, { method: "POST" });
    showMessage(success);
    await refresh();
  } catch (err) {
    showMessage(err.message, "error");
    await refresh();
  }
}

document.querySelectorAll(".tab").forEach((button) => {
  button.addEventListener("click", async () => {
    state.activeTab = button.dataset.tab;
    document.querySelectorAll(".tab").forEach((tab) => tab.classList.toggle("active", tab === button));
    document.querySelectorAll(".tab-panel").forEach((panel) => panel.classList.add("hidden"));
    document.querySelector(`#tab-${state.activeTab}`).classList.remove("hidden");
    await refreshActiveDetails();
  });
});

el.instanceSelect.addEventListener("change", (event) => selectInstance(event.target.value));
el.newBtn.addEventListener("click", showCreate);
el.emptyCreate.addEventListener("click", showCreate);
el.createProfile.addEventListener("change", () => {
  el.createConfig.dataset.profileId = "";
  updateCreateProfileControls();
});
el.createCancel.addEventListener("click", () => {
  state.creating = false;
  render();
});

el.createSubmit.addEventListener("click", async () => {
  try {
    let profileId = el.createProfile.value;
    if (profileId === newProfileValue || state.profiles.length === 0) {
      const profile = await api("/api/profiles", {
        method: "POST",
        body: JSON.stringify({
          name: el.createProfileName.value.trim() || `${el.createName.value.trim() || "Instance"} profile`,
          config: el.createConfig.value,
        }),
      });
      profileId = profile.id;
    }
    const payload = {
      name: el.createName.value.trim(),
      profileId,
      mixedPort: Number(el.createMixedPort.value) || 0,
      controllerPort: Number(el.createControllerPort.value) || 0,
    };
    const created = await api("/api/instances", {
      method: "POST",
      body: JSON.stringify(payload),
    });
    state.activeId = created.id;
    state.creating = false;
    el.configEditor.value = "";
    el.configEditor.dataset.id = "";
    el.configEditor.dataset.profileId = "";
    el.configEditor.dataset.dirty = "";
    showMessage("Instance created.");
    await refresh();
  } catch (err) {
    showMessage(err.message, "error");
  }
});

el.startBtn.addEventListener("click", () => {
  const selected = active();
  if (selected) runAction(`/api/instances/${selected.id}/start`, "Start requested.");
});

el.stopBtn.addEventListener("click", () => {
  const selected = active();
  if (selected) runAction(`/api/instances/${selected.id}/stop`, "Stop requested.");
});

el.restartBtn.addEventListener("click", () => {
  const selected = active();
  if (selected) runAction(`/api/instances/${selected.id}/restart`, "Restart requested.");
});

el.deleteBtn.addEventListener("click", async () => {
  const selected = active();
  if (!selected) return;
  if (!confirm(`Delete ${selected.name}?`)) return;
  try {
    await api(`/api/instances/${selected.id}`, { method: "DELETE" });
    state.activeId = "";
    el.configEditor.value = "";
    el.configEditor.dataset.id = "";
    el.configEditor.dataset.profileId = "";
    el.configEditor.dataset.dirty = "";
    showMessage("Instance deleted.");
    await refresh();
  } catch (err) {
    showMessage(err.message, "error");
  }
});

el.saveBasics.addEventListener("click", async () => {
  const selected = active();
  if (!selected) return;
  try {
    await api(`/api/instances/${selected.id}`, {
      method: "PUT",
      body: JSON.stringify({
        name: el.editName.value.trim(),
        profileId: el.editProfile.value,
        mixedPort: Number(el.editMixedPort.value),
        controllerPort: Number(el.editControllerPort.value),
      }),
    });
    showMessage("Basics saved.");
    await refresh();
  } catch (err) {
    showMessage(err.message, "error");
  }
});

el.saveConfig.addEventListener("click", async () => {
  const selected = active();
  if (!selected) return;
  try {
    await api(`/api/instances/${selected.id}`, {
      method: "PUT",
      body: JSON.stringify({ config: el.configEditor.value }),
    });
    showMessage("Profile config saved.");
    el.configEditor.dataset.dirty = "";
    await refresh();
  } catch (err) {
    showMessage(err.message, "error");
  }
});

el.configEditor.addEventListener("input", () => {
  el.configEditor.dataset.dirty = "1";
});

el.createConfig.value = defaultConfig;
refresh();
setInterval(refresh, 4000);
setInterval(() => {
  if (state.activeTab === "logs") refreshLogs();
  if (state.activeTab === "proxies") refreshProxies();
}, 1800);
