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

The fleet dashboard is the single relaxed surface: status cards, fleet and per-instance 60-second traffic sparklines, and a master-detail instance list. Selecting a row keeps the user on the dashboard and updates the detail trend; opening the workbench is an explicit action. It is read-only and additive -- the instance workbench stays dense, and "avoid marketing cards" continues to govern every other view.

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
