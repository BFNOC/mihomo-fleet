// Vite entry point. Boots the Vue shell, then hands off to the legacy vanilla
// application in app.ts.
//
// ORDER STILL MATTERS, though less than it did: app.ts no longer reads the DOM
// at all (bindElements()/dom.ts are gone), but it does call refresh() at module
// scope, which writes to the shared store and then expects the views below to
// already be listening. Mounting first also means the first paint is the real
// UI rather than index.html's skeleton. app.ts is therefore pulled in with a
// dynamic import() at the bottom rather than a static one: static imports are
// hoisted and evaluated before this module's body, which would put its boot
// sequence ahead of every mount.
import { createApp, watchEffect } from "vue";
import type { Component } from "vue";

import NotificationStack from "./components/NotificationStack.vue";
import SideBar from "./components/SideBar.vue";
import TopBar from "./components/TopBar.vue";
import DashboardView from "./views/dashboard/DashboardView.vue";
import ProfileManagerView from "./views/profiles/ProfileManagerView.vue";
import CreatePanel from "./views/create/CreatePanel.vue";
import EmptyPanel from "./views/create/EmptyPanel.vue";
import InstanceDetail from "./views/detail/InstanceDetail.vue";
import SystemView from "./views/system/SystemView.vue";
import { store } from "./store.ts";
import "./styles.css";

// mount() replaces the host's children, so each host keeps its own wrapper
// element (<header class="topbar">, <aside class="sidebar">) and the component
// supplies only the inner fragment. That keeps index.html's layout skeleton and
// every existing styles.css selector working unchanged.
function mountShell(component: Component, selector: string): HTMLElement {
  const host = document.querySelector<HTMLElement>(selector);
  if (!host) {
    // Fail loudly. A missing host means index.html and the components have
    // drifted apart; silently skipping would leave a blank region that looks
    // like a data problem rather than a wiring one.
    throw new Error(`Vue shell host not found: ${selector}`);
  }
  createApp(component).mount(host);
  return host;
}

mountShell(TopBar, ".topbar");
mountShell(SideBar, ".sidebar");
mountShell(NotificationStack, "#notificationMount");
const dashboardPanel = mountShell(DashboardView, "#dashboardPanel");
const profilePanel = mountShell(ProfileManagerView, "#profilePanel");
const systemPanel = mountShell(SystemView, "#systemPanel");
const createPanel = mountShell(CreatePanel, "#createPanel");
const emptyPanel = mountShell(EmptyPanel, "#emptyPanel");
mountShell(InstanceDetail, "#detailMount");

// All of renderPanels() (app.ts), moved here wholesale.
//
// These four elements are Vue mount *hosts*: mount() replaces their children
// but never the host itself, so no component can set its own host's class the
// way InstanceDetail.vue sets #detailPanel's. Driving them from one
// watchEffect keeps them reactive anyway.
//
// This is also a bug fix. renderPanels() only ran from app.ts's render(), and
// the two navigation actions ProfileManagerView.vue registers
// (openProfileManager/closeProfileManager) set store.view without calling it --
// nothing in app.ts is reachable from there. Panel visibility therefore lagged
// a click by up to one slow-poll interval: measured at 374ms against the
// dashboard's 86ms, because the flip actually rode in on the next refresh().
watchEffect(() => {
  const profilesView = store.view === "profiles";
  const dashboardView = store.view === "dashboard";
  const systemView = store.view === "system";
  // Anything that is not the instance workbench hides the workbench panels.
  // Testing "not instances" rather than "is profiles" keeps this correct as
  // further views are added.
  const away = profilesView || dashboardView || systemView;
  profilePanel.classList.toggle("hidden", !profilesView);
  dashboardPanel.classList.toggle("hidden", !dashboardView);
  systemPanel.classList.toggle("hidden", !systemView);
  createPanel.classList.toggle("hidden", away || !store.creating);
  emptyPanel.classList.toggle("hidden", away || store.creating || store.instances.length > 0);
  document.body.classList.toggle("view-dashboard", dashboardView);
});

// Deliberately last, and deliberately dynamic. app.ts calls refresh() at module
// scope and registers the action table; both must land after every mount above
// has subscribed to the store, or the first refresh writes into a store nobody
// is listening to yet. (This is no longer about DOM lookups -- see the ORDER
// note at the top: bindElements()/dom.ts are gone.)
void import("./app.ts").catch((err: unknown) => {
  console.error("Failed to boot the legacy application shell.", err);
});
