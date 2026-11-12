import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import vm from "node:vm";
import test from "node:test";
import {
  inlineMarkupLocations,
  assertUniqueTemplateMarkup,
  loadViewTemplates,
  materializeViewFixtures,
} from "../scripts/templates.ts";

const root = new URL("../", import.meta.url);
test("authored UI TypeScript contains no inline HTML or attribute templates", async () => {
  async function inspect(directory: URL) {
    for (const entry of await readdir(directory, { withFileTypes: true })) {
      const path = new URL(
        entry.name + (entry.isDirectory() ? "/" : ""),
        directory,
      );
      if (entry.isDirectory()) await inspect(path);
      else if (entry.name.endsWith(".ts")) {
        const source = await readFile(path, "utf8");
        assert.deepEqual(
          inlineMarkupLocations(source, entry.name),
          [],
          `${path.pathname}: inline markup must live in an HTML template`,
        );
      }
    }
  }
  await inspect(new URL("src/", root));
});

test("view templates retain whitespace, nested markup and existing escaping", async () => {
  const templates = await loadViewTemplates(
    new URL("src/views/", root).pathname,
  );
  assert.ok(Object.keys(templates).length > 0);
  const source = await readFile(
    new URL("test/.generated/components.js", root),
    "utf8",
  );
  const { renderViewTemplate, viewValue } = vm.runInNewContext(
    source + ";({renderViewTemplate,viewValue})",
  );
  const name = "shared/paragraph";
  assert.equal(
    renderViewTemplate(name, [viewValue("&lt;script&gt;")]),
    "<p>&lt;script&gt;</p>",
  );
  assert.throws(() => renderViewTemplate(name), /Missing view template value/);
  assert.throws(() => renderViewTemplate("missing"), /Unknown view template/);
});

test("fixture materialization preserves evaluation order and literal template text", () => {
  const templates = {
    "example/wrapper": "<pre>\n{{value0}} {{value1}}\n</pre>",
    "example/child": "<b>{{value0}}</b>",
  };
  const source =
    'let order=[];const result=renderViewTemplate("example/wrapper",[viewValue((order.push(1),renderViewTemplate("example/child",[viewValue("&lt;tag&gt;")]))),viewValue((order.push(2),"${notCode}"))]);';
  const restored = materializeViewFixtures(source, templates);
  const result = vm.runInNewContext(restored + ";({result,order})");
  assert.equal(result.result, "<pre>\n<b>&lt;tag&gt;</b> ${notCode}\n</pre>");
  assert.deepEqual(Array.from(result.order), [1, 2]);
});

test("duplicate-template guard compares slot structure while preserving meaningful differences", () => {
  assert.throws(
    () =>
      assertUniqueTemplateMarkup({
        "feature/one": "<p>{{label}}</p>",
        "feature/two": "<p>{{value0}}</p>",
      }),
    /feature\/two duplicates feature\/one/,
  );
  assert.doesNotThrow(() =>
    assertUniqueTemplateMarkup({
      "feature/spaced": "<span> {{label}}</span>",
      "feature/compact": "<span>{{label}}</span>",
      "feature/repeated": "<span>{{first}}{{first}}</span>",
      "feature/distinct": "<span>{{first}}{{second}}</span>",
    }),
  );
});
