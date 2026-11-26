import { MAX, events, allowedID } from "./policy.js";

// This is the only script executing in the page's JavaScript realm. Every
// worker message is untrusted, even if the guest VM has been terminated.
const script = document.currentScript;
const textTags = new Set(
  "BUTTON CODE DD DIV DT EM H1 H2 H3 H4 H5 H6 LABEL LI OUTPUT P SMALL SPAN STRONG TD TH".split(
    " ",
  ),
);
const hiddenTags = new Set([
  ...textTags,
  ..."ARTICLE ASIDE DETAILS FIELDSET FOOTER HEADER INPUT MAIN NAV OL SECTION SELECT TABLE TEXTAREA UL".split(
    " ",
  ),
]);
const inputTypes = new Set([
  "text",
  "search",
  "number",
  "range",
  "checkbox",
  "radio",
]);
function start() {
  const b64 = script.dataset.program;
  if (typeof b64 !== "string" || b64.length > Math.ceil(MAX.source / 3) * 4)
    throw Error();
  const raw = atob(b64);
  if (raw.length > MAX.source) throw Error();
  const program = new TextDecoder("utf-8", { fatal: true }).decode(
    Uint8Array.from(raw, (c) => c.charCodeAt(0)),
  );
  let worker,
    timer,
    ended = false,
    busy = true,
    total = 0,
    registrations = 0,
    eventsLeft = 4000;
  const nodes = new Map(),
    snapshots = [],
    pending = [],
    eventTimes = [];
  const controller = new AbortController();
  function stop(report = true) {
    if (ended) return;
    ended = true;
    clearTimeout(timer);
    controller.abort();
    worker?.terminate();
    pending.length = 0;
    if (report) {
      const notice = document.createElement("p");
      notice.setAttribute("role", "status");
      notice.textContent =
        "The interactive part of this page stopped. Reload the page to try again.";
      document.body.append(notice);
    }
  }
  const watch = (ms) => {
    clearTimeout(timer);
    timer = setTimeout(() => stop(), ms);
  };
  for (const el of document.body.querySelectorAll("[id]")) {
    const id = el.id,
      tag = el.tagName;
    if (!allowedID(id) || nodes.has(id) || !hiddenTags.has(tag)) continue;
    if (tag === "INPUT" && !inputTypes.has(el.type)) continue;
    if (nodes.size >= MAX.elements) throw Error();
    const props = { hidden: el.hidden === true };
    if (textTags.has(tag) && el.childElementCount === 0)
      props.textContent = el.textContent;
    if (["INPUT", "SELECT", "TEXTAREA"].includes(tag)) props.value = el.value;
    if (tag === "INPUT" && ["checkbox", "radio"].includes(el.type))
      props.checked = el.checked;
    if (["INPUT", "SELECT", "TEXTAREA", "BUTTON"].includes(tag))
      props.disabled = el.disabled;
    for (const val of Object.values(props))
      if (typeof val === "string") {
        total += val.length;
        if (val.length > MAX.field || total > MAX.total) throw Error();
      }
    snapshots.push({ id, props: { ...props } });
    nodes.set(id, { el, props, on: new Set() });
  }
  function send(event) {
    busy = true;
    watch(1500);
    worker.postMessage(event);
  }
  function enqueue(entry, event) {
    if (ended) return;
    const now = performance.now();
    while (eventTimes.length && now - eventTimes[0] >= 1000) eventTimes.shift();
    // Guest actions cannot synthesize DOM events. Bound real event bursts and
    // total work too, including on devices that trigger repeated key events.
    if (eventTimes.length >= 60 || eventsLeft-- <= 0) {
      stop();
      return;
    }
    eventTimes.push(now);
    const msg = { type: "event", id: entry.el.id, event, inputs: [] };
    // A click on one control can read another input. Snapshot only the fixed
    // value slots; no event object or native DOM capability crosses the worker.
    for (const [id, { el, props }] of nodes)
      if (Object.hasOwn(props, "value")) {
        const value = el.value;
        total += value.length - props.value.length;
        props.value = value;
        if (value.length > MAX.field || total > MAX.total) {
          stop();
          return;
        }
        msg.inputs.push([
          id,
          value,
          Object.hasOwn(props, "checked") ? el.checked : null,
        ]);
      }
    if (!busy) {
      send(msg);
      return;
    }
    const last = pending.at(-1);
    if (event === "input" && last?.event === "input" && last.id === msg.id) {
      pending[pending.length - 1] = msg;
      return;
    }
    if (pending.length >= MAX.queue) {
      stop();
      return;
    }
    pending.push(msg);
  }
  function apply(cmd) {
    if (!Array.isArray(cmd) || cmd.length < 3 || cmd.length > 4) throw Error();
    const [op, id, prop, value] = cmd,
      entry = nodes.get(id);
    if (!entry || typeof prop !== "string") throw Error();
    if (op === "on" && cmd.length === 3 && events.has(prop)) {
      if (entry.on.has(prop)) return;
      if (++registrations > MAX.listeners) throw Error();
      entry.on.add(prop);
      entry.el.addEventListener(
        prop,
        (event) => {
          if (event.isTrusted) enqueue(entry, prop);
        },
        { signal: controller.signal },
      );
      return;
    }
    if (
      op !== "set" ||
      cmd.length !== 4 ||
      !Object.hasOwn(entry.props, prop) ||
      typeof value !== typeof entry.props[prop]
    )
      throw Error();
    if (typeof value === "string") {
      if (value.length > MAX.field) throw Error();
      total += value.length - entry.props[prop].length;
      if (total > MAX.total) throw Error();
    }
    entry.props[prop] = value;
    // Each accepted property is a fixed primitive slot populated above. Never
    // allow URLs, CSS, raw HTML, attributes, or property paths from the guest.
    switch (prop) {
      case "textContent":
        entry.el.textContent = value;
        break;
      case "value":
        entry.el.value = value;
        break;
      case "checked":
        entry.el.checked = value;
        break;
      case "disabled":
        entry.el.disabled = value;
        break;
      case "hidden":
        entry.el.hidden = value;
        break;
      default:
        throw Error();
    }
  }
  worker = new Worker("/auth/runtime/worker.js");
  worker.onerror = () => stop();
  worker.onmessageerror = () => stop();
  worker.onmessage = ({ data }) => {
    if (ended) return;
    try {
      if (
        !busy ||
        data?.type !== "batch" ||
        !Array.isArray(data.messages) ||
        data.messages.length > MAX.commands
      )
        throw Error();
      for (const cmd of data.messages) apply(cmd);
      clearTimeout(timer);
      busy = false;
      if (pending.length) send(pending.shift());
    } catch {
      stop();
    }
  };
  window.addEventListener("pagehide", () => stop(false), {
    once: true,
    signal: controller.signal,
  });
  watch(10000);
  worker.postMessage({ type: "start", nodes: snapshots, program });
}
try {
  start();
} catch {
  const p = document.createElement("p");
  p.setAttribute("role", "status");
  p.textContent = "The interactive part of this page could not start.";
  document.body.append(p);
}
