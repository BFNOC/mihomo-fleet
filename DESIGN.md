# Design

## Tone

Quiet workstation utility. The UI should feel like a reliable local control surface, not a landing page.

## Theme

Light theme only. Warm paper neutrals under normal desktop ambient light. No dark mode.

## Color

Restrained strategy: warm tinted neutrals (paper beige / soft stone) with one teal-green accent for active control state, muted amber for warnings, and coral for danger. Avoid slate-blue dominance, purple gradients, and pure black/white.

Dashboard traffic charts reuse amber for upload and teal for download so the two directions separate on one axis without introducing a third brand hue.

## Typography

System UI stack with CJK fallbacks. Compact labels, tabular numbers for ports and latency, rare large headings.

## Layout

Sticky top bar with brand, profile-management command, and active instance selector. Fleet overview is a primary sidebar nav entry above the instance list.

The fleet dashboard is pinned to the viewport: on a window at least 760px tall it owns exactly the space under the top bar and the page itself never scrolls, so the instance table renders only the rows its measured box holds (`use-row-budget.ts`, keyed off the `--dash-fit` custom property the stylesheet sets) -- it repeats what the sidebar already lists, so a cut-off tail costs nothing. The connection table is the exception and scrolls inside its own card; see below. Shorter windows fall back to a scrolling page, because below that height the split leaves no usable table. Fixed height above the tables is the scarce resource: fleet health, the connection count, and both traffic directions share one metrics strip rather than four cards, and the instance table, the fleet chart and the selected instance sit side by side in a single row rather than stacking. Charts are elastic (`preserveAspectRatio="none"`) and yield height to the tables instead of holding a fixed size; each one marks its newest sample with a dot, drawn as a round-capped zero-length stroke so the non-uniform scaling cannot squash it into an ellipse. The mihomo version is shown as the build number alone -- the controller keeps the full `mihomo -v` banner for bug reports, but a line of chrome is not the place for it. It stays read-only -- the instance workbench stays dense, and "avoid marketing cards" continues to govern every other view.

A live connection table closes the dashboard: one row per open connection across the whole fleet (target host/IP, owning instance, exit node and matched rule, country, per-connection rate, age), searchable and sorted busiest-first. It is the only unbounded list on the dashboard, and the only card that scrolls: clipping it to the six rows that happen to fit answers "what fits", not "what is the fleet doing". The scroll stays inside the card -- the grid fixes the card's box, so the page below stays pinned -- and a 500-row ceiling bounds the per-heartbeat cost of recomputing every visible row's rate and age; sorting busiest-first means what the ceiling drops is the idle tail. It reuses the `/connections` payload the sparklines already poll, so it costs no extra request. Logs stay in the instance workbench -- the dashboard answers "what is the fleet doing right now", not "what did this instance say".

Country resolution is offline by construction. `/api/geoip` answers from a local MaxMind database read by a dependency-free decoder (`mmdb.go`); no destination address a user connects to ever leaves the machine, which is why proxying a public geolocation API was rejected. It looks in the same directories `geodata.go` stages instance geodata from, preferring `GeoLite2-Country.mmdb` over `Country.mmdb`: the mihomo-flavoured `Country.mmdb` answers with vendor tags (`GOOGLE`, `CLOUDFLARE`) where an ISO code belongs, which is fine for GEOIP rules but useless as a country column. Preferring a different filename keeps the column accurate without changing which file instances get staged, so no GEOIP rule changes meaning. The database is optional: without one the column simply stays empty. Private, loopback, link-local and CGNAT addresses are labelled in the browser and never cost a lookup.

Sidebar for fleet membership and port matrix. Detail pane for selected instance controls. Profile management is a separate two-column resource view with a profile catalog and an editor pane. Prefer compact panels, metric strips, and tables over marketing cards.

## Components

- Dropdown for active instance selection.
- Separate profile catalog for Profile CRUD; instance forms only reference existing profiles.
- Dense buttons for lifecycle actions.
- Segmented controls for create-source and detail tabs.
- Inline forms for create/edit; avoid modal-first workflows.
- Fixed-height light log and YAML editor surfaces.
- Status dots and pending-restart chips for runtime evidence.

## Frontend structure

Source lives in `internal/app/web-src`: Vue 3 SFCs with `<script setup lang="ts">` for the UI, plus framework-free TypeScript modules for the logic those components call (`api.ts`, `state.ts`, `format.ts`, `latency.ts`, `messages.ts`, `constants.ts`, `yaml-editor.ts`, `app-logic.ts`, `dashboard.ts`, `traffic.ts`). `pnpm build:web` runs Vite and writes `internal/app/web/` for Go embed. Those artifacts are **not** in git; releases build them before `go build`, and only `internal/app/web/README.md` is tracked, because `go:embed web/*` fails to compile against an empty directory.

There is no root `App.vue`. `main.ts` mounts eight independent roots into hosts that `index.html` supplies (topbar, sidebar, message banner, and the five panels), so `styles.css`'s global selectors and the existing layout skeleton kept working through the migration untouched. Panel visibility is one `watchEffect` in `main.ts`, not a component's business: those elements are mount *hosts*, so no component can set its own host's class.

Each view is a directory under `views/`: components alongside the plain `.ts` modules holding that view's shared state and logic. Those modules keep their state at module scope rather than behind a `useX()` factory, because every view mounts exactly once and its components must share one instance — `dashboard-data.ts` (sampler-backed computeds and the heartbeat that invalidates them), `profile-context.ts` (form fields plus the operation-context guard), `proxy-groups.ts` (group loading and view models). `useX()` is reserved for things that genuinely need per-caller instances and lifecycle hooks: `use-row-budget.ts`, which each dashboard table instantiates separately to measure itself, and `use-proxy-tooltip.ts`.

Backend calls and global loops live in `services/`, with `app.ts` reduced to boot order and action-table registration. Dependencies there run one way — `navigation → polling → fleet-refresh → profile-gates` — and a cycle would deadlock module initialisation, so the gates that publish `chrome.profileBusy` sit in their own leaf module rather than beside the profile network calls that use them.

`bridge.ts` holds the shared action table the components call into, plus the message banner and `showMessage()` that writes it. Each action key is owned by exactly one registrant — `app.ts` registers most of them at boot, `views/profiles/profile-navigation.ts` claims `openProfileManager`/`closeProfileManager` because they need the editor handle. Registration is `Object.assign`, so a key listed twice silently takes whichever registered last.

The dashboard is five sibling cards (strip, instances, trend, selected, connections) rather than one component. Each renders a single root `<article>`, adding no wrapper of its own — the viewport-fit media query targets `.dashboard > *`, so the four top-level nodes of the view's fragment are a structural contract, not a formatting choice.

Two chunks load lazily and both must stay dynamic imports: `chunk-app.ts` (polling and the profile network layer) and `chunk-yaml-editor.js` (CodeMirror 6, ~512KB, fetched only when the profiles view is first opened). Verify after any bundler change by grepping the emitted `app.js` for `from"./chunk`, which must find nothing.

No i18n framework, deliberately. UI copy is hardcoded Chinese in the templates; `messages.ts` only maps the Go backend's English error strings and status enum into Chinese.
