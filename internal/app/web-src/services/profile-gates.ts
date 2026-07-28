import { createActionGate } from "../app-logic.ts";
import { chrome } from "../bridge.ts";

// Mutual exclusion for the three profile mutations. These live in their own
// module rather than beside the network calls in profiles.ts because
// fleet-refresh.ts also has to publish the derived busy flag, and putting them
// in profiles.ts would make refresh() -> profiles.ts -> refresh() a cycle.
//
// Imported straight from app-logic.ts, not through yaml-editor.ts's `export *`
// re-export: reaching createActionGate() that way made its importer a static
// importer of CodeMirror purely to get a 12-line helper.
export const saveProfileGate = createActionGate();
export const deleteProfileGate = createActionGate();
export const refreshSubscriptionGate = createActionGate();

export function profileOperationRunning(): boolean {
  return saveProfileGate.isRunning()
    || deleteProfileGate.isRunning()
    || refreshSubscriptionGate.isRunning();
}

/**
 * Publishes profileOperationRunning() onto the reactive `chrome` object.
 *
 * The gates above are plain closures outside Vue's reactive graph, so a
 * component reading them directly would never re-render. Mirroring them onto
 * `chrome` is the whole job -- everything else the pre-Vue render() did (panel
 * visibility, bulk controls, chain-field toggles) is now the components' own
 * business.
 */
export function syncProfileBusy(): void {
  chrome.profileBusy = profileOperationRunning();
}
