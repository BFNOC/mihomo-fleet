// Vite entry point. Boots the Vue shell, then hands off to the legacy vanilla
// application in app.ts.
//
// ORDER IS LOAD-BEARING. app.ts is not a passive module: at module scope it
// calls bindElements() and then createYamlEditor(el.configEditor, ...), which
// constructs a CodeMirror EditorView against a live DOM node. Importing it
// before the DOM exists throws immediately, before any of its own error
// handling can run. That is why app.ts is pulled in with a dynamic import()
// below rather than a static one: static imports are hoisted and evaluated
// before this module's body, which would defeat the ordering entirely.
import { createApp, watchEffect } from "vue";
import type { Component } from "vue";

import MessageBanner from "./components/MessageBanner.vue";
import SideBar from "./components/SideBar.vue";
import TopBar from "./components/TopBar.vue";
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
