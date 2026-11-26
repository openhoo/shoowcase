// Evaluated INSIDE QuickJS before site code. No host callback is installed.
// The only value returned to the trusted worker is a closure sending JSON text.
(payload) => {
  "use strict";
  const parse = JSON.parse,
    stringify = JSON.stringify,
    freeze = Object.freeze;
  const nodes = new Map(),
    listeners = new Map();
  const commands = [];
  let registered = 0;
  function send(cmd) {
    if (commands.length >= 128) throw Error("Too many changes in one event.");
    commands.push(cmd);
  }
  for (const snap of parse(payload)) {
    const node = Object.create(null),
      state = Object.assign(Object.create(null), snap.props);
    Object.defineProperty(node, "id", { value: snap.id, enumerable: true });
    for (const prop of Object.keys(state)) {
      Object.defineProperty(node, prop, {
        enumerable: true,
        get: () => state[prop],
        set: (value) => {
          if (
            typeof value !== typeof state[prop] ||
            (typeof value === "string" && value.length > 4096)
          )
            throw TypeError("Invalid element value.");
          send(["set", snap.id, prop, value]);
          state[prop] = value;
        },
      });
    }
    node.addEventListener = (type, fn) => {
      if (
        !["click", "input", "change"].includes(type) ||
        typeof fn !== "function"
      )
        throw TypeError("Unsupported event.");
      const key = snap.id + ":" + type,
        callbacks = listeners.get(key) || [];
      if (callbacks.includes(fn)) return;
      if (++registered > 256) throw Error("Too many listeners.");
      if (callbacks.length === 0) send(["on", snap.id, type]);
      callbacks.push(fn);
      listeners.set(key, callbacks);
    };
    nodes.set(snap.id, { node: freeze(node), state });
  }
  const doc = freeze({
    getElementById: (id) => nodes.get(id)?.node ?? null,
    querySelector: (selector) =>
      typeof selector === "string" && selector.startsWith("#")
        ? (nodes.get(selector.slice(1))?.node ?? null)
        : null,
  });
  Object.defineProperty(globalThis, "document", { value: doc });
  return (eventJSON) => {
    if (eventJSON) {
      const [id, type, inputs] = parse(eventJSON),
        entry = nodes.get(id);
      for (const [key, value, checked] of inputs) {
        const mirror = nodes.get(key);
        if (mirror && typeof mirror.state.value === "string")
          mirror.state.value = value;
        if (
          mirror &&
          typeof checked === "boolean" &&
          typeof mirror.state.checked === "boolean"
        )
          mirror.state.checked = checked;
      }
      if (entry) {
        const event = freeze({
          type,
          target: entry.node,
          currentTarget: entry.node,
        });
        for (const fn of listeners.get(id + ":" + type) || [])
          fn.call(entry.node, event);
      }
    }
    return stringify(commands.splice(0));
  };
};
