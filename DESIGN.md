# Design

## Tone

Quiet workstation utility. The UI should feel like a reliable local control surface, not a landing page.

## Theme

Light theme by default. The operator is likely configuring ports and instances during normal desktop work, with terminal windows and browser docs open, where a clear light surface is easier to scan.

## Color

Use restrained tinted neutrals with one green-blue accent for active control state and a muted amber for warnings. Avoid dark blue/slate dominance and avoid purple gradients.

## Typography

Use system UI fonts. Keep labels compact, numeric fields aligned, and large headings rare.

## Layout

Persistent top bar with the active instance selector. Main workspace uses a sidebar for fleet membership and a detail pane for selected instance controls. Use tables and compact panels rather than marketing cards.

## Components

- Dropdown for active instance selection.
- Icon buttons for lifecycle actions.
- Segmented controls for views.
- Inline forms for create/edit; avoid modal-first workflows.
- Fixed-height log and config editor surfaces.
