// Preserve classic-script scope, hoisting, URLs, and source layout. The same
// assets are consumed by the standalone server, Go server, and release bundle.
import { stripTypeScriptTypes } from "node:module";
import ts from "typescript";
import {
  loadViewTemplates,
  materializeViewFixtures,
  assertUniqueTemplateMarkup,
} from "./templates.ts";
import { createRequire } from "node:module";
import { execFileSync } from "node:child_process";
import {
  readdir,
  readFile,
  writeFile,
  mkdir,
  rm,
  rename,
} from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { join, relative, dirname } from "node:path";

const root = fileURLToPath(new URL("../", import.meta.url));
const source = join(root, "src");
const destination = join(root, "dist");
const fixtures = join(root, "test", ".generated");
// Compile static component templates into the TypeScript browser build.
const componentDirectory = join(source, "components");
const templateNames = (await readdir(componentDirectory))
  .filter((name) => name.endsWith(".html"))
  .sort();
const templates = Object.fromEntries(
  await Promise.all(
    templateNames.map(async (name): Promise<[string, string]> => [
      name.slice(0, -5),
      (await readFile(join(componentDirectory, name), "utf8"))
        .replace(/\s+/g, " ")
        .replace(/>\s+</g, "><")
        .replace(/>\s+(\{\{\w+\}\})/g, ">$1")
        .replace(/(\{\{\w+\}\})\s+</g, "$1<")
        .trim(),
    ]),
  ),
);
const views = await loadViewTemplates(join(source, "views"));
assertUniqueTemplateMarkup({
  ...Object.fromEntries(
    Object.entries(templates).map(([name, body]) => [
      "component/" + name,
      body,
    ]),
  ),
  ...views,
});
const registryDirectory = join(root, ".generated");
await mkdir(registryDirectory, { recursive: true });
const registrySource =
  "/* Generated UI templates. */\nvar componentTemplates: Readonly<Record<string,string>> = " +
  JSON.stringify(templates) +
  ";\nvar viewTemplates: Readonly<Record<string,string>> = " +
  JSON.stringify(views) +
  ";\n";
await writeFile(join(registryDirectory, "templates.ts"), registrySource);
const compiler = createRequire(import.meta.url).resolve("typescript/bin/tsc");
for (const config of [
  "tsconfig.json",
  "test/tsconfig.json",
  "scripts/tsconfig.json",
]) {
  execFileSync(
    process.execPath,
    [compiler, "-p", join(root, config), "--noEmit"],
    { stdio: "inherit" },
  );
}
await rm(fixtures, { recursive: true, force: true });
await mkdir(fixtures, { recursive: true });
await rm(destination, { recursive: true, force: true });
await mkdir(destination, { recursive: true });
async function build(directory) {
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) await build(path);
    else if (
      entry.name.endsWith(".ts") &&
      !entry.name.endsWith(".d.ts") &&
      entry.name !== "server.ts"
    ) {
      const output = join(
        fixtures,
        relative(source, path).replace(/\.ts$/, ".js"),
      );
      let input = materializeViewFixtures(await readFile(path, "utf8"), views);
      const parsed = ts.createSourceFile(
        path,
        input,
        ts.ScriptTarget.Latest,
        true,
      );
      // Remove type-only wrappers before stripping so stable asset consumers
      // and source-extraction regression tests retain the existing syntax.
      const removals = [];
      function visit(node) {
        if (
          (ts.isCallExpression(node) || ts.isNewExpression(node)) &&
          node.typeArguments?.length
        ) {
          removals.push([
            node.typeArguments.pos - 1,
            node.typeArguments.end + 1,
          ]);
        }
        if (
          ts.isAsExpression(node) &&
          ts.isParenthesizedExpression(node.parent) &&
          (ts.isIdentifier(node.expression) ||
            ts.isCallExpression(node.expression) ||
            ts.isPropertyAccessExpression(node.expression))
        ) {
          removals.push(
            [node.expression.end, node.parent.end],
            [node.parent.getStart(parsed), node.expression.getStart(parsed)],
          );
        }
        ts.forEachChild(node, visit);
      }
      visit(parsed);
      for (const [start, end] of removals.sort((a, b) => b[0] - a[0])) {
        input = input.slice(0, start) + input.slice(end);
      }
      const code = stripTypeScriptTypes(input);
      await mkdir(dirname(output), { recursive: true });
      await writeFile(
        output,
        "// Generated from src/" +
          relative(source, path) +
          ". Run npm run build --workspace muxlio-ui.\n" +
          code.replace(/[ \t]+$/gm, ""),
      );
    }
  }
}
await build(source);
// Existing regression fixtures load scripts independently. Supply their shared
// component dependencies without exposing generated fixtures to the HTTP server.
const componentFiles = [
  "template",
  "view-template",
  "feedback",
  "execution-metadata",
  "data-display",
  "pagination",
  "run-table",
  "accounting-card",
  "fields",
  "form-dialog",
];
const componentExports = [
  "componentBody",
  "componentEscape",
  "renderNotice",
  "renderPlaceholder",
  "renderStatusBadge",
  "renderAttemptStatus",
  "renderExecutionMetadata",
  "renderDefinitionRow",
  "renderSmallContent",
  "renderViewTemplate",
  "viewValue",
  "renderComponent",
  "renderEmptyState",
  "renderDataTable",
  "renderMetricCard",
  "renderSummaryCards",
  "renderPagination",
  "renderRunTable",
  "renderAccountingCard",
  "renderInputField",
  "renderTextField",
  "openFormDialog",
];
const componentFixture =
  "(function(){\n" +
  stripTypeScriptTypes(registrySource) +
  "\n" +
  (
    await Promise.all(
      componentFiles.map((name) =>
        readFile(join(fixtures, "components", name + ".js"), "utf8"),
      ),
    )
  ).join("\n") +
  "\nObject.assign(globalThis,{" +
  componentExports.join(",") +
  "});\n})();\n";
await writeFile(join(fixtures, "components.js"), componentFixture);
for (const name of [
  "core",
  "workspace",
  "usage-summary",
  "operations-hub",
  "inspector",
  "operations",
  "live",
  "transcript",
  "analytics",
  "markdown",
]) {
  const path = join(fixtures, name + ".js");
  await writeFile(path, componentFixture + (await readFile(path, "utf8")));
}

// Production JavaScript is emitted by the TypeScript compiler. Individual
// stripped scripts above are retained for existing source-extraction fixtures.
execFileSync(
  process.execPath,
  [compiler, "-p", join(root, "tsconfig.build.json")],
  { stdio: "inherit" },
);

execFileSync(
  process.execPath,
  [compiler, "-p", join(root, "tsconfig.server.json")],
  { stdio: "inherit" },
);
await rename(join(destination, "server.js"), join(destination, "server.mjs"));

const styleOrder = ["base.css", "workspace.css", "operations.css", "theme.css"];
await writeFile(
  join(destination, "app.css"),
  (
    await Promise.all(
      styleOrder.map((name) => readFile(join(source, "styles", name), "utf8")),
    )
  ).join("\n"),
);
