import { build } from "esbuild";
import { execFileSync } from "node:child_process";
import { readFile, writeFile, readdir, mkdir } from "node:fs/promises";
import { createHash } from "node:crypto";
import path from "node:path";
import { fileURLToPath } from "node:url";

process.chdir(fileURLToPath(new URL(".", import.meta.url)));
const out = "../internal/web/uiassets";
await mkdir(out, { recursive: true });
const { metafile } = await build({
  entryPoints: ["src/main.tsx"],
  outfile: out + "/app.js",
  bundle: true,
  format: "iife",
  target: ["es2022"],
  jsx: "automatic",
  minify: true,
  metafile: true,
  legalComments: "none",
  charset: "utf8",
  define: { "process.env.NODE_ENV": '"production"' },
});
execFileSync(
  process.execPath,
  [
    "node_modules/@tailwindcss/cli/dist/index.mjs",
    "-i",
    "src/styles.css",
    "-o",
    out + "/app.css",
    "--minify",
  ],
  { stdio: "inherit" },
);
// Record only packages that contribute bytes to the executable, plus Tailwind
// (its CSS is built separately) and the vendored shadcn/ui source.
const modules = new Set(["node_modules/tailwindcss"]);
const output = Object.values(metafile.outputs)[0];
for (const [file, info] of Object.entries(output.inputs)) {
  if (!info.bytesInOutput || !file.startsWith("node_modules/")) continue;
  const match = /^(.*node_modules\/(?:@[^/]+\/)?[^/]+)/.exec(file);
  if (match) modules.add(match[1]);
}
const records = [];
let licenses =
  "Shoowcase management UI\n\nshadcn/ui\n" +
  (await readFile("licenses/shadcn-LICENSE.txt", "utf8"));
for (const dir of [...modules].sort()) {
  const { name, version } = JSON.parse(
    await readFile(path.join(dir, "package.json")),
  );
  const license = (await readdir(dir)).find((p) =>
    /^licen[cs]e(?:\.(?:md|txt))?$/i.test(p),
  );
  if (!license) throw new Error("Missing license: " + name);
  records.push({ name, version });
  licenses +=
    "\n\n" +
    name +
    " " +
    version +
    "\n" +
    (await readFile(path.join(dir, license), "utf8"));
}
const hash = createHash("sha256");
for (const f of (await readdir("src/components/ui")).sort())
  hash.update(await readFile(path.join("src/components/ui", f)));
records.push({ name: "shadcn/ui", version: hash.digest("hex") });
await writeFile("UI-LICENSES.txt", licenses.trimEnd() + "\n");
await writeFile("inventory.json", JSON.stringify(records, null, 2) + "\n");
