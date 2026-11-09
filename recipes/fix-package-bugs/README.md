# Find and fix package bugs

This recipe walks through packages in a checkout, one at a time. It uses the
same agent handoff as [implement-features](../implement-features/workflow.ts):

1. **Find and fix:** Kiro (with Cursor fallback) inspects a package, fixes confirmed bugs,
   and writes a findings report. A package with no bugs stays unchanged.
2. **Review:** Antigravity (with Codex fallback) checks the findings and diff, fixes any
   issues it finds, and writes a review report.
3. **Verify and repair:** command checks run for the package and repository. If
   they fail, Kiro (with Cursor fallback) repairs the failure and the checks run again.
   After 10 failed attempts, the run pauses for inspection.
4. **Commit:** the recipe makes a local commit when there are changes. It never
   pushes. Then it moves to the next package.

Start Muxlio with a worker authorized to write to the checkout. From the root
of that checkout, with dependencies installed:

```sh
node recipes/fix-package-bugs/discover.mjs --out recipes/fix-package-bugs/input.json
muxlio check recipes/fix-package-bugs/workflow.ts
muxlio run recipes/fix-package-bugs/workflow.ts recipes/fix-package-bugs/input.json \
  --server http://127.0.0.1:7600 --json
```

The discovery script lists Go packages in both modules, npm workspaces, and the
UI. Edit `input.json` to choose the packages and their order. Each item needs a
unique `id`, a workspace-relative `path`, a `kind` (`go`, `npm`, or `ui`), and a
`target` used by its package checks. For example:

```json
{
  "packages": [
    { "id": "core-plan", "path": "muxlio-core", "kind": "go", "target": "./internal/plan" },
    { "id": "sdk", "path": "muxlio-sdk", "kind": "npm", "target": "@muxlio/sdk" }
  ]
}
```

Start with a clean checkout and run only one writer workflow in it. Check the
run's reports and diff before resuming a paused run. Command checks include the
package's tests (and Go vet or npm build), the repository build and tests, UI
tests, and both Go test suites. Running those suites for every package can take
time.

Validate the recipe without starting agents:

```sh
npm run build --workspace muxlio-recipes
npm test --workspace muxlio-recipes
```
