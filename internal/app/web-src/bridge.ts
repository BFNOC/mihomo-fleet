// Action registry bridging Vue components to the behaviour still implemented in
// app.js. Components import `actions` and call `actions.selectInstance(id)`;
// app.js fills the table during boot via registerActions().
//
// This exists to keep the dependency one-way. Components must not import app.js
// directly: app.js touches the DOM at module scope, so importing it from a
// component would run it before Vue has mounted anything.
//
// Every entry is a no-op until app.js registers it, so a component rendered
// during the boot gap cannot throw.
const noop = () => {};

export const actions = {
  selectInstance: noop,
  showCreate: noop,
  openDashboard: noop,
  closeDashboard: noop,
  openProfileManager: noop,
  closeProfileManager: noop,
  startAll: noop,
  stopAll: noop,
  copyProxyValue: noop,
  showMessage: noop,
  dismissMessage: noop,
};

export function registerActions(table: Partial<typeof actions>) {
  Object.assign(actions, table);
}

// Transient UI state that belongs to the chrome rather than to the domain state
// in store.js: the message banner's current text and severity.
export const banner = {
  text: "",
  tone: "info",
};
