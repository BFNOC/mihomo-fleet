// Vite entry point. Boots the Vue shell, then hands off to the legacy vanilla
// application in app.ts.
//
// ORDER IS LOAD-BEARING. app.ts is not a passive module: at module scope it
// calls bindElements(), which runs non-null-asserted `document.querySelector`
// lookups for a handful of ids (the four top-level panel hosts it still
// toggles `.hidden` on, the two chain-field wrappers, and latencyUrl/
// latencyTimeout). Those ids only exist once the views below have mounted --
// #dashboardPanel/#profilePanel/#createPanel/#emptyPanel are the very hosts
// mounted here, and latencyUrl/latencyTimeout/the chain-field wrappers are
// rendered deeper inside InstanceDetail's and CreatePanel's own trees.
// Importing app.ts before that mount work runs would bind those fields to
// `null`, which then throws the first time render() touches them -- not at
// import time, so the failure would show up far from its real cause. That is
// why app.ts is pulled in with a dynamic import() below rather than a static
// one: static imports are hoisted and evaluated before this module's body,
// which would defeat the ordering entirely.
import { createApp, watchEffect } from "vue";
import type { Component } from "vue";

import MessageBanner from "./components/MessageBanner.vue";
import SideBar from "./components/SideBar.vue";
import TopBar from "./components/TopBar.vue";
import DashboardView from "./views/dashboard/DashboardView.vue";
import ProfileManagerView from "./views/profiles/ProfileManagerView.vue";
import CreatePanel from "./views/create/CreatePanel.vue";
import EmptyPanel from "./views/create/EmptyPanel.vue";
import InstanceDetail from "./views/detail/InstanceDetail.vue";
import { store } from "./store.ts";
import "./styles.css";

// mount() replaces the host's children, so each host keeps its own wrapper
// element (<header class="topbar">, <aside class="sidebar">) and the component
// supplies only the inner fragment. That keeps index.html's layout skeleton and
// every existing styles.css selector working unchanged.
function mountShell(component: Component, selector: string): void {
  const host = document.querySelector(selector);
  if (!host) {
    // Fail loudly. A missing host means index.html and the components have
    // drifted apart; silently skipping would leave a blank region that looks
    // like a data problem rather than a wiring one.
    throw new Error(`Vue shell host not found: ${selector}`);
  }
  createApp(component).mount(host);
}

mountShell(TopBar, ".topbar");
mountShell(SideBar, ".sidebar");
mountShell(MessageBanner, "#messageMount");
mountShell(DashboardView, "#dashboardPanel");
mountShell(ProfileManagerView, "#profilePanel");
mountShell(CreatePanel, "#createPanel");
mountShell(EmptyPanel, "#emptyPanel");
mountShell(InstanceDetail, "#detailMount");

// Taken over from renderPanels() (app.ts), which used to toggle this class as a
// side effect even though the condition it reads -- the active view -- is the
// shell's concern and <body> sits outside every Vue mount point.
watchEffect(() => {
  document.body.classList.toggle("view-dashboard", store.view === "dashboard");
});

// Every mount above renders synchronously, so by this line the shell DOM is in
// place and app.ts's bindElements() can find what it needs.
void import("./app.ts").catch((err: unknown) => {
  console.error("Failed to boot the legacy application shell.", err);
});
