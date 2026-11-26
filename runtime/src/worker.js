import variant from "@jitl/quickjs-singlefile-browser-release-sync";
import { newQuickJSWASMModuleFromVariant } from "quickjs-emscripten-core";
import bootstrap from "./guest.js?text";
import { MAX, events, allowedID } from "./policy.js";

let vm,
  runtime,
  dispatch,
  started = false,
  deadline = 0;
let credit = 250,
  lastTurn = 0;
function take(result) {
  // Never format guest exceptions or dump arbitrary guest objects into the host.
  if (result.error) {
    result.error.dispose();
    throw Error("Site program stopped.");
  }
  return result.value;
}
function call(fn, text) {
  const input = vm.newString(text);
  try {
    return take(vm.callFunction(fn, vm.undefined, input));
  } finally {
    input.dispose();
  }
}
function flush(event) {
  const result = call(dispatch, event);
  try {
    if (vm.typeof(result) !== "string") throw Error();
    const batch = vm.getString(result);
    if (batch.length > MAX.response) throw Error();
    const messages = JSON.parse(batch);
    if (!Array.isArray(messages) || messages.length > MAX.commands)
      throw Error();
    self.postMessage({ type: "batch", messages });
  } finally {
    result.dispose();
  }
}
async function initialize(data) {
  if (
    started ||
    !Array.isArray(data.nodes) ||
    data.nodes.length > MAX.elements ||
    typeof data.program !== "string" ||
    data.program.length > MAX.source
  )
    throw Error();
  started = true;
  const engine = await newQuickJSWASMModuleFromVariant(variant);
  runtime = engine.newRuntime();
  runtime.setMemoryLimit(4 * 1024 * 1024);
  runtime.setMaxStackSize(128 * 1024);
  runtime.setInterruptHandler(() => performance.now() >= deadline);
  // No host module loader, promises, timers, I/O, or browser bindings.
  vm = runtime.newContext({
    intrinsics: {
      BaseObjects: true,
      Eval: true,
      MapSet: true,
      JSON: true,
      RegExp: true,
      StringNormalize: true,
    },
  });
  deadline = performance.now() + 120;
  const init = take(vm.evalCode(bootstrap, "dom.js", { type: "global" }));
  try {
    dispatch = call(init, JSON.stringify(data.nodes));
  } finally {
    init.dispose();
  }
  // Dynamic strings must not become guest code. Prototype constructors are
  // also removed; all other supported language features stay within QuickJS.
  take(
    vm.evalCode(
      "for(const f of [function(){},function*(){},async function(){},async function*(){}]){const p=Object.getPrototypeOf(f);if(p)Object.defineProperty(p,'constructor',{value:undefined})};Object.defineProperty(globalThis,'Function',{value:undefined});Object.defineProperty(globalThis,'eval',{value:undefined});",
      "setup.js",
      { type: "global" },
    ),
  ).dispose();
  deadline = performance.now() + 75;
  take(vm.evalCode(data.program, "site.js", { type: "global" })).dispose();
  flush("");
}
self.onmessage = async ({ data }) => {
  try {
    if (data?.type === "start") {
      await initialize(data);
      return;
    }
    if (
      !dispatch ||
      data?.type !== "event" ||
      !events.has(data.event) ||
      !allowedID(data.id)
    )
      throw Error();
    if (!Array.isArray(data.inputs) || data.inputs.length > MAX.elements)
      throw Error();
    let size = 0;
    for (const row of data.inputs) {
      if (
        !Array.isArray(row) ||
        row.length !== 3 ||
        !allowedID(row[0]) ||
        typeof row[1] !== "string" ||
        row[1].length > MAX.field ||
        (row[2] !== null && typeof row[2] !== "boolean")
      )
        throw Error();
      size += row[1].length;
      if (size > MAX.total) throw Error();
    }
    // Cap cumulative execution as well as any one turn: a short burst is
    // allowed, then CPU time replenishes at 100 ms per second.
    const start = performance.now();
    credit = Math.min(250, credit + (start - lastTurn) * 0.1);
    lastTurn = start;
    if (credit < 1) throw Error();
    deadline = start + Math.min(50, credit);
    flush(JSON.stringify([data.id, data.event, data.inputs]));
    credit -= performance.now() - start;
  } catch {
    // Worker termination releases the entire WASM heap, even after a trap/OOM.
    self.postMessage({ type: "stop" });
    self.close();
  }
};
