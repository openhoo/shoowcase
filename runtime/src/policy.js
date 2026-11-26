// These limits are enforced by the trusted worker or page, independently of guest code.
export const MAX = Object.freeze({
  elements: 128,
  id: 64,
  field: 4096,
  total: 65536,
  commands: 128,
  source: 256 * 1024,
  response: 256 * 1024,
  queue: 32,
  listeners: 256,
});
export const events = new Set(["click", "input", "change"]);
export const allowedID = (id) =>
  typeof id === "string" && /^[A-Za-z][A-Za-z0-9_-]{0,63}$/.test(id);
