import test from "node:test";
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile, access } from "node:fs/promises";
import { once } from "node:events";
import { chromium } from "playwright-core";

const cases = {
  counter: `let n=0; const out=document.querySelector('#count'), b=document.getElementById('add'); out.textContent='Ready'; b.addEventListener('click',()=>out.textContent=String(++n)); const name=document.getElementById('name');name.addEventListener('input',(e)=>{document.getElementById('echo').textContent=e.target.value;});`,
  globals: `document.getElementById('count').textContent=[typeof fetch,typeof XMLHttpRequest,typeof WebSocket,typeof process,typeof self,typeof window,typeof localStorage,typeof document.cookie,typeof Function,typeof eval,typeof WebAssembly,typeof ArrayBuffer,typeof Uint8Array,typeof Promise,typeof (function*(){}).constructor,typeof (async function(){}).constructor].join(',');`,
  empty: "",
  cross: `document.getElementById("add").addEventListener("click",()=>document.getElementById("echo").textContent=document.getElementById("name").value);`,
  bad: `throw Error('fixture')`,
  busy: `while(true){}`,
  burst: `for(let i=0;i<150;i++) document.getElementById('count').textContent='x';`,
  flood: `for(let i=0;i<20;i++) document.getElementById('item'+i).textContent='a'.repeat(4000);`,
  html: `document.getElementById('count').textContent='<b>plain text</b>';document.getElementById('echo').textContent=String(document.getElementById('internal')===null);`,
};
const body =
  '<button type="button" id="add">Add</button><output id="count">0</output><label for="name">Name</label><input type="text" id="name"><p id="echo"></p>' +
  Array.from({ length: 20 }, (_, i) => '<span id="item' + i + '"></span>').join(
    "",
  );
async function chromePath() {
  for (const path of [
    process.env.CHROME_BIN,
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
    "/usr/bin/google-chrome",
    "/usr/bin/chromium",
    "/usr/bin/chromium-browser",
  ].filter(Boolean)) {
    try {
      await access(path);
      return path;
    } catch {}
  }
  throw Error("Set CHROME_BIN to an installed Chrome or Chromium executable.");
}
test(
  "bundled site interpreter in a real browser",
  { timeout: 60000 },
  async (t) => {
    const observed = new Set();
    const server = createServer(async (req, res) => {
      const url = new URL(req.url, "http://localhost");
      observed.add(url.pathname);
      const asset = url.pathname.match(
        /^\/auth\/runtime\/(bridge|worker)\.js$/,
      );
      if (asset) {
        res.writeHead(200, {
          "Content-Type": "text/javascript",
          "X-Content-Type-Options": "nosniff",
          "Content-Security-Policy":
            "default-src 'none'; script-src 'wasm-unsafe-eval'; connect-src 'none'",
        });
        res.end(
          await readFile(
            new URL(
              "../../internal/web/runtimeassets/" + asset[1] + ".js",
              import.meta.url,
            ),
          ),
        );
        return;
      }
      if (
        url.pathname !== "/" ||
        !Object.hasOwn(cases, url.searchParams.get("case"))
      ) {
        res.writeHead(404);
        res.end();
        return;
      }
      const nonce = "bmljZS1sb25nLXRlc3Qtbm9uY2U=";
      res.writeHead(200, {
        "Content-Type": "text/html; charset=utf-8",
        "Content-Security-Policy":
          "default-src 'none'; script-src 'nonce-" +
          nonce +
          "'; worker-src 'self'; connect-src 'none'; base-uri 'none'; object-src 'none'; form-action 'none'",
      });
      res.end(
        '<!doctype html><html lang="en"><title>Runtime fixture</title><body>' +
          body +
          '<script id="internal" nonce="' +
          nonce +
          '" src="/auth/runtime/bridge.js" data-program="' +
          Buffer.from(cases[url.searchParams.get("case")]).toString("base64") +
          '" defer></script></body></html>',
      );
    }).listen(0, "127.0.0.1");
    await once(server, "listening");
    const base = "http://127.0.0.1:" + server.address().port;
    t.after(() => server.close());
    const browser = await chromium.launch({
      executablePath: await chromePath(),
      headless: true,
    });
    t.after(() => browser.close());
    const page = await browser.newPage();
    if (process.env.RUNTIME_DEBUG) {
      page.on("console", (m) => console.log("browser:", m.text()));
      page.on("pageerror", (e) => console.error("browser:", e));
    }
    async function open(name) {
      await page.goto(base + "/?case=" + name);
    }
    const output = page.locator("#count");
    await t.test("guest reacts to clicks and text inputs", async () => {
      await open("counter");
      await output.filter({ hasText: "Ready" }).waitFor({ timeout: 8000 });
      for (let i = 1; i <= 2; i++) {
        const button = page.getByRole("button", { name: "Add" });
        if (i === 1) await button.press("Enter");
        else await button.click();
        await output.filter({ hasText: String(i) }).waitFor();
      }
      await page.getByLabel("Name").fill("Team North");
      await page.locator("#echo").filter({ hasText: "Team North" }).waitFor();
    });
    await t.test(
      "click handler reads the current value of a different field",
      async () => {
        await open("cross");
        await page.getByLabel("Name").fill("Fresh value");
        await page.getByRole("button", { name: "Add" }).click();
        await page
          .locator("#echo")
          .filter({ hasText: "Fresh value" })
          .waitFor();
      },
    );
    await t.test(
      "guest has no browser globals, raw HTML, or access to the harness",
      async () => {
        await open("globals");
        await output.filter({ hasText: "undefined" }).waitFor();
        assert.equal(
          await output.textContent(),
          Array(16).fill("undefined").join(","),
        );
        await open("html");
        await page.locator("#echo").filter({ hasText: "true" }).waitFor();
        assert.equal(await output.innerHTML(), "&lt;b&gt;plain text&lt;/b&gt;");
      },
    );
    await t.test(
      "accepts empty and maximum-length scripts and rejects longer programs",
      async () => {
        await open("empty");
        const result = await page.evaluate(async () => {
          const run = (length) =>
            new Promise((resolve, reject) => {
              const w = new Worker("/auth/runtime/worker.js");
              const timeout = setTimeout(() => {
                w.terminate();
                reject(Error("Worker did not answer."));
              }, 5000);
              w.onmessage = ({ data }) => {
                clearTimeout(timeout);
                w.terminate();
                resolve(data.type);
              };
              w.postMessage({
                type: "start",
                program: " ".repeat(length),
                nodes: [],
              });
            });
          return [
            await run(0),
            await run(256 * 1024),
            await run(256 * 1024 + 1),
          ];
        });
        assert.deepEqual(result, ["batch", "batch", "stop"]);
      },
    );
    for (const name of ["bad", "busy", "burst", "flood"]) {
      await t.test("stops bounded fixture: " + name, async () => {
        await open(name);
        await page
          .getByText(
            "The interactive part of this page stopped. Reload the page to try again.",
          )
          .waitFor({ timeout: 7000 });
        assert.equal(
          await page.getByRole("button", { name: "Add" }).isEnabled(),
          true,
        );
      });
    }
    assert.deepEqual([...observed].filter((p) => p !== "/favicon.ico").sort(), [
      "/",
      "/auth/runtime/bridge.js",
      "/auth/runtime/worker.js",
    ]);
  },
);
