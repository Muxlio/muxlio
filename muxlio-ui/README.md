# UI development

All authored UI code is TypeScript: browser views in `src/`, the HTTP server in
`src/server.ts`, build tooling in `scripts/`, and tests in `test/`. Styles live in
`src/styles/`. `index.html` is the static application shell.

Run from the repository root after `npm ci` (Node 22.13 or later):

```sh
npm run build --workspace muxlio-ui
npm start --workspace muxlio-ui
npm test --workspace muxlio-ui
npm run test:browser --workspace muxlio-ui
npm run check --workspace muxlio-ui
npm run format --workspace muxlio-ui
```

`build` checks browser, server, test, and tooling types, compiles with TypeScript,
and builds the stylesheet. `start` and both test commands build first. `test`
includes the HTTP smoke tests; `test:browser` uses installed Chrome and local mock
APIs to exercise the routes at desktop and phone widths. `check` checks types and
formatting, while `format` writes formatting changes.

Generated runtime files live only in ignored `dist/`: `app.js`, `app.js.map`,
`app.css`, and `server.mjs`. Browsers execute the compiled JavaScript; edit the
TypeScript sources, not these outputs. Both Node and Go serve generated files
from `/dist/` without a per-file asset registry. The source tree is not served.
Source-extraction regression fixtures are generated separately in ignored
`test/.generated/`; they are not shipped in releases.

The root workspace build, UI startup script, and release packaging build the UI.
A repository checkout must build before running Go tests that serve real assets.
Installed UI servers run the compiled output without TypeScript tooling.

The HTML shell loads one deferred browser entry and one stylesheet. It includes
semantic navigation, linked tabs/panels, dialogs outside hidden routes, and a
skip link. Shared interactions handle keyboard tabs, modal focus, mobile
navigation, and document titles. The visual system covers all routed views with
shared tokens, responsive controls, typography, and reduced-motion support.

Styles are global CSS, not CSS Modules. The current renderers share classes
across dynamic HTML strings. Adopting CSS Modules would require explicit style
imports in those renderers; renaming files alone would not provide scoping.

Typing remains incremental: dynamic payloads and legacy view helpers retain
permissive types, and strict mode is not enabled. Documented TS2630 annotations
cover existing classic-script view hooks. The compiler's file order keeps these
hooks compatible. `src/main.ts` starts routing after view state is initialized.

Accounting types retain exact readings, provenance, and unavailable values.
Provider protocols and normalization belong in adapters, never in UI views.

## Components

Reusable display and interaction components live in `src/components/`. Each
component has a typed TypeScript renderer and static `.html` templates. Views
supply data and handle coordinator requests; components do not fetch data or
interpret provider names.

The build compiles templates into ignored `.generated/templates.ts` outside
`src/` before type checking and emitting the browser bundle. Templates are not fetched
at runtime. `{{slot}}` values are escaped by default. Use `componentBody()` only
for markup assembled by trusted renderers; never wrap coordinator text directly.
Data tables accept explicitly marked row markup, while field values, titles,
headers, metric values, and accounting readings remain ordinary escaped values.

Shared components cover empty states, tables and run rows, metric and summary
cards, pagination, accounting cards, form fields, and form dialogs. Page loaders
and accounting reducers retain their existing contracts. The component tests
exercise escaping, composition, pagination actions, zero readings, missing
accounting, and exact large values; browser scenarios exercise dialog behavior
and integration with routed views.

Feature markup lives in `src/views/*.html`, grouped into named template fragments
for the inspector, graph, analytics, live output, workflow library, operations,
and other views. TypeScript prepares the fragment values and handles events;
`renderViewTemplate()` preserves the existing escaping and formatting at each
binding. Shared components continue to use escaped slots by default. Neither
view fragments nor shared templates require runtime HTTP requests.

The regression suite scans authored `src/**/*.ts` for inline HTML and attribute
templates so markup cannot silently move back into view logic. Generated
registries and source-extraction fixtures remain outside the authored source tree.

Reuse shared renderers before adding feature fragments. Feedback, placeholders,
empty states, definition rows, status badges, execution metadata, and pagination
have typed shared APIs. Plain text enters these APIs unescaped; shared escaping
happens once. Explicit `componentBody()` values are reserved for composed markup.
Live monitoring and transcript pages use the same execution-metadata component.

Byte-identical fragments that need the legacy positional binding contract live
in `src/views/shared.html`. The build rejects duplicate template markup across
both feature views and shared components, including copies with renamed slots.
Whitespace remains significant so inline spacing and preformatted content are
not combined incorrectly. Feature-specific layouts and accounting provenance
remain separate from these presentation primitives.
