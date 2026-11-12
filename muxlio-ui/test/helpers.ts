import type { Server } from "node:http";
import assert from "node:assert/strict";

/** Local HTTP fixtures must have bound to a TCP address before use. */
export function port(server: Server): number {
  const address = server.address();
  assert.ok(address && typeof address !== "string");
  return address.port;
}
