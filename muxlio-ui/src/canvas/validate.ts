// Validate against the generated, application-owned schema before rendering.
const CANVAS_DOCUMENT_BYTES = 1024 * 1024;
const CANVAS_INLINE_BYTES = 256 * 1024;
const CANVAS_SOURCE_BYTES = 5 * 1024 * 1024;

async function readCanvasText(
  response: Response,
  limit: number,
): Promise<string> {
  if (Number(response.headers.get("content-length")) > limit) {
    await response.body?.cancel();
    throw new Error("Artifact exceeds the " + limit + " byte limit.");
  }
  const reader = response.body?.getReader();
  if (!reader) return "";
  const decoder = new TextDecoder();
  let size = 0;
  let text = "";
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > limit) {
        await reader.cancel();
        throw new Error("Artifact exceeds the " + limit + " byte limit.");
      }
      text += decoder.decode(value, { stream: true });
    }
    return text + decoder.decode();
  } finally {
    reader.releaseLock();
  }
}

let canvasDataRefTypeCache: ReadonlySet<string> | null = null;
// Components whose schema admits a dataRef; other `data` props are inline JSON.
function canvasDataRefTypes(): ReadonlySet<string> {
  canvasDataRefTypeCache ??= new Set(
    (canvasSchema.$defs?.node?.anyOf || [])
      .filter((variant: any) =>
        JSON.stringify(variant?.properties?.data ?? null).includes(
          '"#/$defs/dataRef"',
        ),
      )
      .map((variant: any) => variant.properties.type?.const),
  );
  return canvasDataRefTypeCache;
}

// Match the coordinator's encoding/json size, which escapes <, >, & and
// U+2028/U+2029 that JSON.stringify leaves literal.
function canvasMarshaledBytes(value: unknown): number {
  const text = JSON.stringify(value) ?? "null";
  let extra = 0;
  for (const char of text) {
    if (char === "<" || char === ">" || char === "&") extra += 5;
    else if (char === "\u2028" || char === "\u2029") extra += 3;
  }
  return new TextEncoder().encode(text).byteLength + extra;
}

function validateCanvasDocument(doc: unknown): asserts doc is CanvasDocument {
  const fail = (message: string): never => {
    throw new Error("Invalid Canvas: " + message);
  };
  // Only the vocabulary used by the canonical Canvas schema is needed here.
  const matches = (value: any, schema: any): boolean => {
    if (schema.$ref) {
      const target = schema.$ref
        .slice(2)
        .split("/")
        .reduce((s: any, k: string) => s[k], canvasSchema);
      return matches(value, target);
    }
    if (
      schema.oneOf &&
      schema.oneOf.filter((s: any) => matches(value, s)).length !== 1
    )
      return false;
    if (schema.anyOf && !schema.anyOf.some((s: any) => matches(value, s)))
      return false;
    if ("const" in schema && value !== schema.const) return false;
    if (schema.enum && !schema.enum.includes(value)) return false;
    const typeMatches = (type: string) =>
      type === "null"
        ? value === null
        : type === "array"
          ? Array.isArray(value)
          : type === "object"
            ? value !== null &&
              typeof value === "object" &&
              !Array.isArray(value)
            : type === "integer"
              ? Number.isInteger(value)
              : typeof value === type;
    if (
      schema.type &&
      !(Array.isArray(schema.type) ? schema.type : [schema.type]).some(
        typeMatches,
      )
    )
      return false;
    if (typeof value === "string") {
      const length = Array.from(value).length;
      if (schema.minLength !== undefined && length < schema.minLength)
        return false;
      if (schema.maxLength !== undefined && length > schema.maxLength)
        return false;
      if (schema.pattern && !new RegExp(schema.pattern).test(value))
        return false;
    }
    if (typeof value === "number") {
      if (schema.minimum !== undefined && value < schema.minimum) return false;
      if (schema.maximum !== undefined && value > schema.maximum) return false;
    }
    if (Array.isArray(value)) {
      if (schema.minItems !== undefined && value.length < schema.minItems)
        return false;
      if (schema.items && !value.every((v) => matches(v, schema.items)))
        return false;
    } else if (value !== null && typeof value === "object") {
      if (schema.required?.some((key: string) => !Object.hasOwn(value, key)))
        return false;
      for (const [key, child] of Object.entries(value)) {
        if (Object.hasOwn(schema.properties || {}, key)) {
          if (!matches(child, schema.properties[key])) return false;
        } else if (schema.additionalProperties === false) return false;
        else if (
          typeof schema.additionalProperties === "object" &&
          !matches(child, schema.additionalProperties)
        )
          return false;
      }
    }
    return true;
  };

  // Bound traversal before recursive schema matching or interaction setup.
  const value = doc as any;
  const forbidden = new Set(["__proto__", "prototype", "constructor"]);
  const values: Array<{ value: any; depth: number }> = [{ value, depth: 0 }];
  while (values.length) {
    const item = values.pop()!;
    if (item.depth > 128) fail("JSON nesting limit exceeded.");
    if (item.value && typeof item.value === "object") {
      for (const [key, child] of Object.entries(item.value)) {
        if (forbidden.has(key)) fail("Forbidden property " + key);
        values.push({ value: child, depth: item.depth + 1 });
      }
    }
  }
  const ids = new Set<string>();
  const controls = new Set<string>();
  const remember = (id: string) => {
    if (ids.has(id)) fail("Duplicate component/control id " + id);
    ids.add(id);
  };
  for (const ctrl of Object.values(value?.controls || {}) as any[]) {
    remember(ctrl?.id);
    controls.add(ctrl?.id);
  }
  let count = 0;
  const nodes: Array<{ node: any; depth: number }> = [
    { node: value?.content, depth: 1 },
  ];
  const filters: string[] = [];
  while (nodes.length) {
    const { node, depth } = nodes.pop()!;
    if (!node || typeof node !== "object") fail("Missing component node.");
    if (++count > 500) fail("Component count exceeds 500.");
    if (depth > 16) fail("Component depth exceeds 16.");
    remember(node.id);
    if (["select", "toggle", "search"].includes(node.type))
      controls.add(node.id);
    const ref = canvasDataRefTypes().has(node.type) ? node.data : undefined;
    if (ref && !Array.isArray(ref) && typeof ref === "object") {
      if (
        typeof ref.source === "string" &&
        !Object.hasOwn(value.sources || {}, ref.source)
      )
        fail("Unknown source " + ref.source);
      if (typeof ref.pointer === "string" && ref.pointer !== "") {
        if (
          !ref.pointer.startsWith("/") ||
          ref.pointer.split("/").some((p: string) => forbidden.has(p))
        )
          fail("Invalid JSON pointer.");
      }
      if (ref.filter?.equalsState) filters.push(ref.filter.equalsState);
    }
    if (Array.isArray(node.children))
      for (const child of node.children)
        nodes.push({ node: child, depth: depth + 1 });
    if (Array.isArray(node.tabs))
      for (const tab of node.tabs) {
        remember(tab?.id);
        if (Array.isArray(tab?.children))
          for (const child of tab.children)
            nodes.push({ node: child, depth: depth + 1 });
      }
  }
  if (filters.some((id) => !controls.has(id)))
    fail("Filter references an unknown control.");
  for (const source of Object.values(value?.sources || {}) as any[]) {
    if (
      source &&
      Object.hasOwn(source, "value") &&
      canvasMarshaledBytes(source.value) > CANVAS_INLINE_BYTES
    )
      fail("Inline data exceeds 256 KiB.");
  }
  if (!matches(doc, canvasSchema))
    fail("Document does not match the Canvas v1 schema.");
}
