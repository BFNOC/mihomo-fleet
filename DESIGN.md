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

The fleet dashboard is pinned to the viewport: on a window at least 760px tall it owns exactly the space under the top bar and nothing on it scrolls, so the two tables render only the rows their measured box holds (`fitTables`, keyed off the `--dash-fit` custom property the stylesheet sets). Shorter windows fall back to a scrolling page, because below that height the split leaves no usable table. Fixed height above the tables is the scarce resource: fleet health, the connection count, and both traffic directions share one metrics strip rather than four cards, and the instance table, the fleet chart and the selected instance sit side by side in a single row rather than stacking. Charts are elastic (`preserveAspectRatio="none"`) and yield height to the tables instead of holding a fixed size; each one marks its newest sample with a dot, drawn as a round-capped zero-length stroke so the non-uniform scaling cannot squash it into an ellipse. The mihomo version is shown as the build number alone -- the controller keeps the full `mihomo -v` banner for bug reports, but a line of chrome is not the place for it. It stays read-only -- the instance workbench stays dense, and "avoid marketing cards" continues to govern every other view.

A live connection table closes the dashboard: one row per open connection across the whole fleet (target host/IP, owning instance, exit node and matched rule, country, per-connection rate, age), searchable and sorted busiest-first so the rows that do not fit are the idle ones. It reuses the `/connections` payload the sparklines already poll, so it costs no extra request. Logs stay in the instance workbench -- the dashboard answers "what is the fleet doing right now", not "what did this instance say".

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

Source lives in `internal/app/web-src` as ES modules (`app.js`, `api.js`, `state.js`, `format.js`, `latency.js`, `dom.js`, `i18n.js`, `constants.js`, `yaml-editor.js`, `app-logic.js`, `dashboard.js`, `traffic.js`). `pnpm build:web` bundles into `internal/app/web/app.js` for Go embed.
