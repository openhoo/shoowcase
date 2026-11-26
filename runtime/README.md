# Interactive sites

A page can use one local JavaScript file for a calculator, checklist, or similar interaction. The program can read controls and update existing text. Its state lasts until the visitor reloads or leaves the page.

Upload these two files in the same draft:

**index.html**

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <title>Headcount</title>
    <script src="app.js"></script>
  </head>
  <body>
    <h1>Headcount</h1>
    <button id="add" type="button">Add a person</button>
    <p>People: <output id="total" aria-live="polite">0</output></p>
  </body>
</html>
```

**app.js**

```js
let total = 0;
const output = document.getElementById("total");
document.querySelector("#add").addEventListener("click", () => {
  output.textContent = String(++total);
});
```

Preview the draft, use the button with a keyboard, and check the updated count before publishing. Keep labels in the HTML so visitors can identify controls before the program starts.

## Available APIs

Give each interactive element a unique ID starting with an ASCII letter. Follow it with letters, digits, hyphens, or underscores; the limit is 64 characters. You can look up an element using `document.getElementById("name")` or `document.querySelector("#name")`. Missing or unsupported elements return `null`. The program starts after the HTML has been parsed.

| Member | Available on | Behavior |
| --- | --- | --- |
| `textContent` | Text-only buttons, outputs, labels, headings, paragraphs, table cells, list items, and simple inline or block text elements | Reads or replaces plain text. Use a child span with its own ID if the parent contains other elements. |
| `value` | Inputs, selects, textareas | A string; convert with `Number()` when needed. A select exposes its first selected value. |
| `checked` | Checkbox and radio inputs | Reads or sets a boolean. |
| `disabled` | Buttons, inputs, selects, textareas | Reads or sets a boolean. |
| `hidden` | Supported text elements, controls and containers | Reads or sets a boolean. |
| `addEventListener(type, callback)` | Elements exposed to the program | Receives a real browser `click`, `input` or `change`. The event has `type`, `target`, and `currentTarget`; both targets refer to the registered element. Each event includes the current values of the page’s exposed inputs. |

Supported input types: text, search, number, range, checkbox and radio. The exact element and property allowlist is in [src/bridge.js](src/bridge.js). Site code cannot add elements, set attributes or HTML, change URLs, submit forms, navigate, or click elements programmatically. There are no browser cookies, storage, network APIs, timers, promises, modules or native DOM objects. This environment runs synchronous JavaScript; it does not run typical bundled frontend applications.

## How it runs

1. The Go server resolves the page’s script from the exact site revision and replaces the uploaded script tag with its own nonce-bearing bridge. The original source is data. It is never evaluated by the browser’s native JavaScript engine.
2. The bridge reads a small snapshot of the page’s named elements and starts a dedicated worker. The worker contains the QuickJS interpreter compiled to WebAssembly. It needs no CDN, separate Wasm download, or module loader.
3. QuickJS receives the program and plain values. The worker sends proposed updates back as data. The bridge checks each operation, element and value before changing the page. Static pages load neither asset.
4. On a runtime error or exceeded budget, the bridge terminates the worker, leaves the page readable and adds a status message. It never renders guest exceptions or logs user input.

### Limits

- Program: 256 KiB UTF-8. QuickJS heap: 4 MiB. Stack: 128 KiB.
- Each page exposes up to 128 named elements. No single text field can exceed 4,096 UTF-16 code units; their combined size is limited to 65,536.
- Up to 256 event registrations, 128 operations per turn and 32 queued events. Consecutive input events for the same control can coalesce.
- A guest has 75 ms at startup and up to 50 ms per event. Events share a 250 ms CPU budget that replenishes at 100 ms per second. The bridge also enforces independent wall-clock timeouts, an input burst limit of 60 events per second and a lifetime limit of 4,000 events.
- These are application limits, not measurements of total browser memory or deployment capacity. The browser, worker, Wasm engine and libraries have their own overhead. The interpreter is a third-party dependency; the containment tests are not a security audit.

## Rebuild and test

The Go binary embeds the generated files in `internal/web/runtimeassets/`. Operators do not need Node.js at runtime. Contributors changing the interpreter or bridge need Node.js 26.13.1, npm and installed Chrome or Chromium:

```sh
cd runtime
npm ci --ignore-scripts --no-audit --no-fund
npm audit
npm run format:check
npm run build
npm test
git diff --exit-code -- ../internal/web/runtimeassets THIRD-PARTY-LICENSES.txt
```

The final command checks that the committed bundles and license match a fresh build. After changing runtime source, review and commit the regenerated assets too. On systems where Chromium is not in a default location, set `CHROME_BIN` to its executable.

The browser test serves the built files on loopback under a restrictive CSP. It checks a keyboard-accessible counter, form values, literal text output, the absence of browser globals, the size boundary and termination on errors or excessive work. It also counts asset requests to catch a missing embedded dependency. The main CI job runs these checks before the Go verification.

## Dependencies

The built worker contains `quickjs-emscripten-core`, `@jitl/quickjs-ffi-types` and `@jitl/quickjs-singlefile-browser-release-sync` at 0.32.0. The variant includes Bellard QuickJS [2025-09-13+f1139494](https://github.com/bellard/quickjs/commit/f1139494d18a2053630c5ed3384a42bb70db3c53). Upstream’s MIT notices accompany the [source](THIRD-PARTY-LICENSES.txt) and the worker download. Build and test tools are development dependencies only.

Review the engine commit and upstream security reports when upgrading. npm advisories alone do not cover embedded C code. For example, upstream [issue 492](https://github.com/bellard/quickjs/issues/492) reports a typed-array issue against this commit; this runtime disables ArrayBuffer and typed arrays, and verifies their absence in Chrome.
