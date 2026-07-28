// Every id below corresponds to a static element that always exists once the
// Vue shell has mounted (see main.ts's synchronous mountShell() calls, which
// all run before this module's bindElements() does). The non-null assertion
// on each querySelector call reflects that runtime guarantee rather than
// hiding an unchecked failure mode; a missing id would already have crashed
// the previous untyped code identically.
//
// This interface used to list every element app.ts's now-deleted render
// functions touched directly. Now that those views (dashboard/create/detail/
// profiles) render themselves through Vue, most of that surface moved with
// them; what remains is what app.ts itself still reads: the four top-level
// panel hosts it toggles `.hidden` on, the two chain-field wrappers
// applyModeFields() still drives, and latencyUrl/latencyTimeout, kept only
// because latency.ts imports `Pick<DomElements, "latencyUrl" |
// "latencyTimeout">` -- removing either would break that file, which this
// migration pass is not allowed to touch.
export interface DomElements {
  dashboardPanel: HTMLElement;
  profilePanel: HTMLElement;
  createPanel: HTMLElement;
  emptyPanel: HTMLElement;
  createChainFields: HTMLDivElement;
  editChainFields: HTMLDivElement;
  latencyUrl: HTMLInputElement;
  latencyTimeout: HTMLInputElement;
}

export function bindElements(root: ParentNode = document): DomElements {
  return {
    dashboardPanel: root.querySelector<HTMLElement>("#dashboardPanel")!,
    profilePanel: root.querySelector<HTMLElement>("#profilePanel")!,
    createPanel: root.querySelector<HTMLElement>("#createPanel")!,
    emptyPanel: root.querySelector<HTMLElement>("#emptyPanel")!,
    createChainFields: root.querySelector<HTMLDivElement>("#createChainFields")!,
    editChainFields: root.querySelector<HTMLDivElement>("#editChainFields")!,
    latencyUrl: root.querySelector<HTMLInputElement>("#latencyUrl")!,
    latencyTimeout: root.querySelector<HTMLInputElement>("#latencyTimeout")!,
  };
}
