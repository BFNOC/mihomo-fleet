# mihomo-fleet

Go controller for running several mihomo instances side by side, with an
embedded Vue 3 web UI. See `DESIGN.md` for the UI's visual and structural
design, and `internal/app/web/README.md` for the build artifacts.

## Frontend code standard

These are hard requirements for new and modified frontend code.

1. Keep a feature's new business code under ~1000 lines.
2. Split by responsibility: pages, components, composables, utils, api,
   constants, styles. Do not let one file own two concerns.
3. Never pile logic into the app entry, a page file, or one oversized
   component.
4. A component should stay under 200-300 lines.
5. A function should stay under 80 lines.
6. Repeated UI and repeated logic must be factored out.
7. Styles stay lean. No large blocks of duplicated CSS, no hardcoded structure.
8. Never trade away readability, complete behaviour, or testability to hit a
   line count. A shorter file that hides a bug is worse than a longer one.
9. If a requirement genuinely cannot fit, say so first: explain why, give the
   split plan, and estimate the resulting size. Do not silently exceed.

### How this maps onto this codebase

Frontend source is `internal/app/web-src`. The layout that satisfies rule 2:

- `views/<area>/` — one directory per area, containing its components plus the
  plain `.ts` modules holding that area's state and logic. `dashboard-data.ts`,
  `profile-context.ts` and `proxy-groups.ts` are the pattern: module-scope
  reactive state and exported functions, not `useX()` factories, because each
  area mounts exactly once and its components must share one instance of that
  state. Reserve `useX()` for things that genuinely need per-caller instances
  and lifecycle hooks (`use-row-budget.ts`, `use-proxy-tooltip.ts`).
- `services/` — everything that talks to the backend or owns a global loop.
  Dependencies here run one way: `navigation → polling → fleet-refresh →
  profile-gates`. Keep it that way; a cycle here deadlocks module init.
- `app.ts` — boot order and the action table, nothing else. If you are adding
  behaviour to `app.ts`, it belongs in `services/`.
- Root-level modules (`api.ts`, `format.ts`, `state.ts`, `traffic.ts`,
  `constants.ts`, `messages.ts`) are framework-free and unit-tested. Pure logic
  belongs there, not in a component.
- `styles/` — `styles.css` is only an ordered `@import` list. Import order **is**
  the cascade order, since Vite inlines them at build time, so put a new concern
  next to the ones it relates to rather than appending it. `styles/responsive.css`
  sits mid-list on purpose. After any change here, prove the cascade is intact by
  diffing the built `internal/app/web/styles.css` against its previous bytes.

### Rule 8 in practice here

Two places deliberately stay large, and must not be "fixed" by splitting:

- `views/profiles/profile-operations.ts` — save/delete/refresh are one
  interlocking sequence (capture context → await → re-check context → mutate the
  store in a fixed order). Splitting the delete path across a module boundary is
  exactly how a `refresh()` once landed on the wrong side of an `await` and
  silently broke the guard on every successful delete.
- `YamlCodeEditor` stays a direct child of `ProfileManagerView.vue`'s template.
  Putting a component between them turns every `editorRef.value?.foo()` call
  into a silent no-op instead of a loud failure.

## Frontend traps

- **`watchEffect` runs during setup.** It must not read a `ref` declared later
  in the same `<script setup>`; `const` is in its temporal dead zone, so that
  throws `ReferenceError` rather than yielding a default. Computeds are lazy and
  hide the same ordering bug until something reads them.
- **`dashboard.ts`'s sampler Map and geo cache are outside Vue's reactive
  graph.** A `computed()` reading them evaluates once and never invalidates —
  charts freeze with no error. Read the heartbeat (`dashboard-data.ts`) or
  `chrome.trafficTick` first to get a real dependency.
- **`registerActions` is `Object.assign`, so last write wins.** Each key has
  exactly one owner; see the ownership rule in `bridge.ts`. A key registered
  twice silently takes whichever ran last — no error, the button just stops
  working.
- **Mount hosts are not components.** `mount()` replaces a host's children, so
  no component can set its own host's class. Panel visibility lives in
  `main.ts`.
- **Both lazy chunks must stay dynamic imports.** After any bundler or import
  change, grep the emitted `internal/app/web/app.js` for `from"./chunk` — it
  must find nothing. Reading the build log is not sufficient; an `export *`
  re-export silently turned a dynamic import static once already.

## Verifying frontend changes

```bash
pnpm typecheck                              # vue-tsc, must be 0 errors
node --test internal/app/web-src/*.test.ts  # pure-logic unit tests
pnpm build:web                              # writes internal/app/web/ for go:embed
go build ./... && go vet ./...
```

Build artifacts under `internal/app/web/` are **not** in git — releases rebuild
them before `go build`. Only `internal/app/web/README.md` is tracked, because
`go:embed web/*` fails to compile against an empty directory.

For UI changes, verify in a real browser rather than by inspection. There is no
Chrome here, but `playwright` + chromium are installed. Measure boxes and read
`textContent`; do not judge by screenshots — CJK fonts are deliberately not
installed, so Chinese text renders as empty boxes and that is expected.

## Conventions

- UI copy is hardcoded Chinese in templates. There is no i18n framework and
  none is planned. `messages.ts` only maps the Go backend's English error
  strings and status enum into Chinese.
- Commit messages: Chinese, terse, 3 paragraphs max. Cut anything the diff makes
  self-evident.
- Distribution is via GitHub Releases only. `go install` is not supported.
