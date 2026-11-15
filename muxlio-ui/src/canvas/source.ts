// Safe data-source resolution, JSON pointer navigation, filtering, and pagination.

function evaluateJsonPointer(target: unknown, pointer?: string): unknown {
  if (pointer === undefined || pointer === "" || pointer === null) {
    return target;
  }
  if (typeof pointer !== "string" || !pointer.startsWith("/")) {
    return undefined;
  }
  const parts = pointer.slice(1).split("/");
  let curr: any = target;
  for (const part of parts) {
    if (curr === null || typeof curr !== "object") {
      return undefined;
    }
    const token = part.replace(/~1/g, "/").replace(/~0/g, "~");
    if (
      token === "__proto__" ||
      token === "prototype" ||
      token === "constructor"
    ) {
      return undefined;
    }
    if (Array.isArray(curr)) {
      const idx = Number(token);
      if (!Number.isInteger(idx) || idx < 0 || idx >= curr.length) {
        return undefined;
      }
      curr = curr[idx];
    } else if (Object.prototype.hasOwnProperty.call(curr, token)) {
      curr = curr[token];
    } else {
      return undefined;
    }
  }
  return curr;
}

function filterDataRows(
  rows: readonly unknown[],
  filter?: CanvasDataFilter,
  state?: ReadonlyMap<string, CanvasStateValue>,
): readonly unknown[] {
  if (!filter || !filter.field || !Array.isArray(rows)) {
    return rows;
  }
  let targetVal: unknown = filter.equals;
  if (filter.equalsState && state) {
    targetVal = state.get(filter.equalsState);
  }
  if (targetVal === undefined || targetVal === "") {
    return rows;
  }
  return rows.filter((row) => {
    if (row === null || typeof row !== "object") return false;
    const val = (row as Record<string, unknown>)[filter.field];
    return String(val) === String(targetVal);
  });
}

function sortDataRows(
  rows: readonly unknown[],
  field?: string,
  direction: "asc" | "desc" = "asc",
): readonly unknown[] {
  if (!field || !Array.isArray(rows)) return rows;
  const sorted = [...rows];
  sorted.sort((a: any, b: any) => {
    const valA = a && typeof a === "object" ? a[field] : undefined;
    const valB = b && typeof b === "object" ? b[field] : undefined;
    if (valA === valB) return 0;
    if (valA === undefined || valA === null) return 1;
    if (valB === undefined || valB === null) return -1;
    if (typeof valA === "number" && typeof valB === "number") {
      return direction === "asc" ? valA - valB : valB - valA;
    }
    const strA = String(valA);
    const strB = String(valB);
    const cmp = strA.localeCompare(strB);
    return direction === "asc" ? cmp : -cmp;
  });
  return sorted;
}

function resolveCanvasData(
  ref: unknown,
  context: CanvasRendererContext,
): unknown {
  if (!ref || typeof ref !== "object") {
    return ref;
  }
  if (Array.isArray(ref)) {
    return ref;
  }
  const obj = ref as Record<string, unknown>;
  if (typeof obj.source === "string") {
    const rawSource = context.sources.get(obj.source);
    if (rawSource === undefined) {
      return undefined;
    }
    const extracted = evaluateJsonPointer(
      rawSource,
      typeof obj.pointer === "string" ? obj.pointer : undefined,
    );
    if (Array.isArray(extracted) && obj.filter) {
      return filterDataRows(
        extracted,
        obj.filter as CanvasDataFilter,
        context.state,
      );
    }
    return extracted;
  }
  return ref;
}
