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

const statusLabels = {
  stopped: "已停止",
  starting: "启动中",
  running: "运行中",
  error: "异常",
};

const errorLabels = {
  "mihomo binary not found. Install mihomo or start with -mihomo /path/to/mihomo": "未找到 mihomo 可执行文件。请安装 mihomo，或使用 -mihomo /path/to/mihomo 指定路径。",
  "stop the instance before changing ports": "修改端口前请先停止该实例。",
  "stop the instance before changing profile": "修改配置档前请先停止该实例。",
  "profileId and config cannot be changed in the same request": "不能在同一次请求中同时修改配置档和配置内容。",
  "group and proxy are required": "必须选择节点组和节点。",
  "method not allowed": "请求方法不允许。",
  "invalid host header": "Host 请求头无效。",
  "missing X-Mihomo-Fleet header": "缺少 X-Mihomo-Fleet 请求头。",
  "Content-Type must be application/json": "Content-Type 必须是 application/json。",
  "unable to allocate local ports": "无法自动分配本地端口。",
};

const errorPatterns = [
  [/^profile "(.+)" not found$/, (match) => `配置档 ${match[1]} 不存在。`],
  [/^instance "(.+)" not found$/, (match) => `实例 ${match[1]} 不存在。`],
  [/^mixed proxy port (\d+) is unavailable$/, (match) => `混合端口 ${match[1]} 不可用。`],
  [/^controller port (\d+) is unavailable$/, (match) => `控制端口 ${match[1]} 不可用。`],
  [/^mixed proxy port (\d+) is already in use$/, (match) => `混合端口 ${match[1]} 已被占用。`],
  [/^controller port (\d+) is already in use$/, (match) => `控制端口 ${match[1]} 已被占用。`],
  [/^process "(.+)" did not exit after force kill$/, (match) => `进程 ${match[1]} 强制结束后仍未退出。`],
  [/^mihomo config test failed: (.+)$/, (match) => `mihomo 配置测试失败：${match[1]}`],
  [/^mihomo controller unreachable: (.+)$/, (match) => `无法连接 mihomo 控制器：${match[1]}`],
  [/^mihomo returned (.+)$/, (match) => `mihomo 返回错误：${match[1]}`],
  [/^parse user config: (.+)$/, (match) => `解析用户配置失败：${match[1]}`],
];

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

function statusText(status) {
  return statusLabels[status] || status || "未知";
}

function statusClass(status) {
  return statusLabels[status] ? status : "unknown";
}

function localizedMessage(message) {
  const text = String(message || "");
  if (errorLabels[text]) return errorLabels[text];
  for (const [pattern, render] of errorPatterns) {
    const match = text.match(pattern);
    if (match) return render(match);
  }
  return text;
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
      // 保留状态消息
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
  el.message.textContent = localizedMessage(text);
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
  const appVersion = `Mihomo Fleet v${state.system.appVersion || "dev"}`;
  el.systemLine.textContent = found
    ? `${appVersion}，控制器 127.0.0.1:${state.system.port}，mihomo ${state.system.version || "已检测到"}`
    : `${appVersion}，控制器 127.0.0.1:${state.system.port}，未找到 mihomo`;
  if (!found) {
    el.systemWarning.textContent = "未在 Mihomo Fleet 同目录或 PATH 中找到 mihomo。你仍然可以创建实例，但启动需要同目录二进制文件，或通过 -mihomo 参数指定路径。";
    el.systemWarning.classList.remove("hidden");
  } else {
    el.systemWarning.classList.add("hidden");
  }
}

function renderSelector(selected) {
  el.instanceSelect.innerHTML = "";
  if (!state.instances.length) {
    const opt = document.createElement("option");
    opt.textContent = "暂无实例";
    opt.value = "";
    el.instanceSelect.append(opt);
    return;
  }
  for (const item of state.instances) {
    const opt = document.createElement("option");
    opt.value = item.id;
    opt.textContent = `${item.name}（${statusText(item.status)}）`;
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
    const profile = item.profileName || item.profileId || "未选择配置档";
    const choice = item.selectedProxy ? ` · ${item.selectedGroup}/${item.selectedProxy}` : "";
    button.innerHTML = `
      <div class="row-main">
        <span class="row-name"></span>
        <span class="status ${statusClass(item.status)}"></span>
      </div>
      <div class="row-meta"></div>
    `;
    button.querySelector(".row-name").textContent = item.name;
    button.querySelector(".status").textContent = statusText(item.status);
    button.querySelector(".row-meta").textContent = `混合端口 ${item.mixedPort} · ${profile}${choice}`;
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
  el.detailMeta.textContent = selected.lastError ? localizedMessage(selected.lastError) : `${statusText(selected.status)} · ${selected.id}`;
  el.metricStatus.textContent = statusText(selected.status);
  el.metricPid.textContent = selected.pid || "无";
  el.metricMixed.textContent = selected.mixedPort;
  el.metricController.textContent = selected.controllerPort;
  el.overviewMixed.textContent = `127.0.0.1:${selected.mixedPort}`;
  el.overviewController.textContent = `127.0.0.1:${selected.controllerPort}`;
  el.overviewProfile.textContent = selected.profileName || selected.profileId || "无";
  el.overviewUserConfig.textContent = selected.profileConfigPath || selected.userConfigPath;
  el.overviewRuntimeConfig.textContent = selected.runtimeConfigPath;
  el.overviewSelection.textContent = selected.selectedProxy ? `${selected.selectedGroup} -> ${selected.selectedProxy}` : "无";
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
    opt.textContent = "创建新配置档";
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
    el.createConfig.value = localizedMessage(err.message);
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
    el.logs.textContent = (payload.lines || []).join("\n") || "还没有进程日志。";
    el.logs.scrollTop = el.logs.scrollHeight;
  } catch (err) {
    el.logs.textContent = localizedMessage(err.message);
  }
}

async function refreshProxies() {
  const selected = active();
  if (!selected) return;
  if (selected.status !== "running") {
    el.proxiesList.innerHTML = `<div class="warning">启动该实例后才能读取 mihomo 节点。</div>`;
    return;
  }
  try {
    const payload = await api(`/api/mihomo/${selected.id}/proxies`);
    const proxies = payload.proxies || {};
    const groups = Object.values(proxies).filter((item) => Array.isArray(item.all));
    if (!groups.length) {
      el.proxiesList.innerHTML = `<div class="warning">mihomo 没有返回可选节点组。</div>`;
      return;
    }
    el.proxiesList.innerHTML = "";
    for (const group of groups) {
      const row = document.createElement("div");
      row.className = "proxy-row";
      row.innerHTML = `
        <strong></strong>
        <select></select>
        <button type="button">选择</button>
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
          showMessage(`已保存 ${group.name} -> ${select.value}。`);
          render();
          await refreshProxies();
        } catch (err) {
          showMessage(err.message, "error");
        }
      });
      el.proxiesList.append(row);
    }
  } catch (err) {
    el.proxiesList.innerHTML = `<div class="message error">${escapeHTML(localizedMessage(err.message))}</div>`;
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
          name: el.createProfileName.value.trim() || `${el.createName.value.trim() || "默认"}配置档`,
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
    showMessage("实例已创建。");
    await refresh();
  } catch (err) {
    showMessage(err.message, "error");
  }
});

el.startBtn.addEventListener("click", () => {
  const selected = active();
  if (selected) runAction(`/api/instances/${selected.id}/start`, "已请求启动。");
});

el.stopBtn.addEventListener("click", () => {
  const selected = active();
  if (selected) runAction(`/api/instances/${selected.id}/stop`, "已请求停止。");
});

el.restartBtn.addEventListener("click", () => {
  const selected = active();
  if (selected) runAction(`/api/instances/${selected.id}/restart`, "已请求重启。");
});

el.deleteBtn.addEventListener("click", async () => {
  const selected = active();
  if (!selected) return;
  if (!confirm(`确定删除 ${selected.name}？`)) return;
  try {
    await api(`/api/instances/${selected.id}`, { method: "DELETE" });
    state.activeId = "";
    el.configEditor.value = "";
    el.configEditor.dataset.id = "";
    el.configEditor.dataset.profileId = "";
    el.configEditor.dataset.dirty = "";
    showMessage("实例已删除。");
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
    showMessage("基础信息已保存。");
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
    showMessage("配置已保存。");
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
