import ts from "typescript";
import { readdir, readFile } from "node:fs/promises";
import { join } from "node:path";

/** Feature templates retain exact whitespace, including preformatted output. */
export async function loadViewTemplates(
  directory: string,
): Promise<Record<string, string>> {
  const result: Record<string, string> = {};
  for (const name of (await readdir(directory))
    .filter((name) => name.endsWith(".html"))
    .sort()) {
    const source = await readFile(join(directory, name), "utf8");
    for (const match of source.matchAll(
      /<template data-view="([^"]+)">\n([\s\S]*?)\n<\/template>/g,
    )) {
      const key = name.slice(0, -5) + "/" + match[1];
      if (Object.hasOwn(result, key))
        throw new Error(`Duplicate view template: ${key}`);
      result[key] = match[2];
    }
  }
  return result;
}

/** Old regression tests extract individual functions. Materialize their
 * templates for those fixtures; production always calls the bundled registry. */
export function materializeViewFixtures(
  source: string,
  templates: Readonly<Record<string, string>>,
): string {
  const parsed = ts.createSourceFile(
    "fixture.ts",
    source,
    ts.ScriptTarget.Latest,
    true,
  );
  const escape = (value: string) =>
    value.replace(/\\/g, "\\\\").replace(/`/g, "\\`").replace(/\$\{/g, "\\${");
  function transform(node: ts.Node): string {
    if (
      ts.isCallExpression(node) &&
      ts.isIdentifier(node.expression) &&
      node.expression.text === "renderViewTemplate"
    ) {
      const key = node.arguments[0];
      if (
        !key ||
        !ts.isStringLiteral(key) ||
        !Object.hasOwn(templates, key.text)
      )
        throw new Error("Unknown fixture view template");
      const array = node.arguments[1];
      const values =
        array && ts.isArrayLiteralExpression(array) ? array.elements : [];
      const parts = templates[key.text].split(/\{\{value(\d+)\}\}/g);
      return (
        "`" +
        parts
          .map((part, index) => {
            if (index % 2 === 0) return escape(part);
            let value = values[Number(part)];
            if (!value) throw new Error("Missing fixture template value");
            if (
              ts.isCallExpression(value) &&
              ts.isIdentifier(value.expression) &&
              value.expression.text === "viewValue"
            )
              value = value.arguments[0];
            return "${" + transform(value) + "}";
          })
          .join("") +
        "`"
      );
    }
    const start = node.getStart(parsed);
    let text = source.slice(start, node.end);
    const edits: [number, number, string][] = [];
    ts.forEachChild(node, (child) => {
      const replacement = transform(child);
      if (replacement !== source.slice(child.getStart(parsed), child.end))
        edits.push([
          child.getStart(parsed) - start,
          child.end - start,
          replacement,
        ]);
    });
    for (const [a, b, replacement] of edits.sort((a, b) => b[0] - a[0]))
      text = text.slice(0, a) + replacement + text.slice(b);
    return text;
  }
  let result = source;
  for (const node of [...parsed.statements].reverse())
    result =
      result.slice(0, node.getStart(parsed)) +
      transform(node) +
      result.slice(node.end);
  return result;
}

/** Product markup belongs to HTML templates, not view/controller literals. */
export function inlineMarkupLocations(
  source: string,
  file = "view.ts",
): number[] {
  const parsed = ts.createSourceFile(
    file,
    source,
    ts.ScriptTarget.Latest,
    true,
  );
  const pattern =
    /<\/?[a-z][\w:-]*(?:\s|>|\/|$)|<!--|<!doctype|(?:^|\s)(?:class|style|aria-[\w-]+|data-[\w-]+|type|href|id|role|min|max|minlength|maxlength|value|title|placeholder|colspan|rowspan|name|tabindex)\s*=/i;
  const lines: number[] = [];
  function visit(node: ts.Node) {
    const value = ts.isTemplateExpression(node)
      ? node.head.text +
        node.templateSpans
          .map((span) => "muxlioSlot" + span.literal.text)
          .join("")
      : ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)
        ? node.text
        : null;
    if (value !== null && pattern.test(value))
      lines.push(
        parsed.getLineAndCharacterOfPosition(node.getStart(parsed)).line + 1,
      );
    ts.forEachChild(node, visit);
  }
  visit(parsed);
  return lines;
}

/** Compare emitted markup independently of slot names. Whitespace remains
 * significant so preformatted content and inline spacing are preserved. */
export function assertUniqueTemplateMarkup(
  templates: Readonly<Record<string, string>>,
): void {
  const owners = new Map<string, string>();
  for (const [name, source] of Object.entries(templates)) {
    const slots = new Map<string, number>();
    const shape = source.replace(/\{\{(\w+)\}\}/g, (_, slot: string) => {
      if (!slots.has(slot)) slots.set(slot, slots.size);
      return "{{" + slots.get(slot) + "}}";
    });
    const owner = owners.get(shape);
    if (owner)
      throw new Error(
        `Duplicate template markup: ${name} duplicates ${owner}. Reuse the shared template.`,
      );
    owners.set(shape, name);
  }
}
