import { build } from "esbuild";
import { readFile, writeFile, mkdir } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
const dir = path.dirname(fileURLToPath(import.meta.url));
const out = path.join(dir, "../internal/web/runtimeassets");
await mkdir(out, { recursive: true });
const licenses = await readFile(
  path.join(
    dir,
    "node_modules/@jitl/quickjs-singlefile-browser-release-sync/LICENSE",
  ),
  "utf8",
);
const opts = {
  absWorkingDir: dir,
  bundle: true,
  platform: "browser",
  format: "iife",
  target: ["es2022"],
  minify: true,
  charset: "ascii",
  legalComments: "none",
  banner: {
    js: "/* Shoowcase site runtime. See runtime/THIRD-PARTY-LICENSES.txt. */",
  },
};
await build({
  ...opts,
  entryPoints: ["src/bridge.js"],
  outfile: path.join(out, "bridge.js"),
});
const installed = await build({
  ...opts,
  metafile: true,
  footer: { js: "/*\n" + licenses + "\n*/" },
  entryPoints: ["src/worker.js"],
  outfile: path.join(out, "worker.js"),
  plugins: [
    {
      name: "guest-source",
      setup(b) {
        b.onResolve({ filter: /^\.\/guest\.js\?text$/ }, () => ({
          path: path.join(dir, "src/guest.js"),
          namespace: "guest",
        }));
        b.onLoad(
          { filter: /.*/, namespace: "guest" },
          async ({ path: file }) => ({
            contents: await readFile(file, "utf8"),
            loader: "text",
          }),
        );
      },
    },
  ],
});
await writeFile(path.join(dir, "THIRD-PARTY-LICENSES.txt"), licenses);
const modules = new Set();
for (const output of Object.values(installed.metafile.outputs))
  for (const [input, info] of Object.entries(output.inputs)) {
    if (!info.bytesInOutput) continue;
    const name = input.match(/^node_modules\/((?:@[^/]+\/)?[^/]+)/)?.[1];
    if (name) modules.add(name);
  }
console.log(
  "Packages included in browser worker:",
  [...modules].sort().join(", "),
);
const inventory = JSON.parse(
  await readFile(path.join(dir, "components.json"), "utf8"),
);
const expectedEngine = await readFile(
  path.join(
    dir,
    "node_modules/@jitl/quickjs-singlefile-browser-release-sync/README.md",
  ),
  "utf8",
);
if (
  JSON.stringify(inventory.npm) !== JSON.stringify([...modules].sort()) ||
  !expectedEngine.includes(
    "https://github.com/bellard/quickjs/commit/" +
      inventory.quickjsCommit +
      ")",
  )
) {
  throw Error(
    "The shipped runtime changed. Review components.json and release inventory.",
  );
}
