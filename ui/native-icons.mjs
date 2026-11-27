// Render only the Lucide icons referenced by Go's script-free forms.
import { readFile, readdir, writeFile } from "node:fs/promises";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import * as lucide from "lucide-react";

const web = "../internal/web";
const sources = [
  "../internal/auth/password.html",
  ...["app.go", "upload.go", "console.html", "siteconsole.html"].map(
    (f) => web + "/" + f,
  ),
  ...(await readdir(web + "/templates"))
    .filter((f) => f.endsWith(".html"))
    .map((f) => web + "/templates/" + f),
];
const icons = new Set();
for (const path of sources) {
  const source = await readFile(path, "utf8");
  for (const [, name] of source.matchAll(
    /\{\{template "icon-([A-Za-z0-9]+)"\}\}/g,
  ))
    icons.add(name);
}
const templates = [...icons].sort().map((name) => {
  if (!lucide[name]) throw Error("Unknown Lucide icon: " + name);
  const svg = renderToStaticMarkup(
    createElement(lucide[name], {
      className: "action-icon",
      size: 18,
      "aria-hidden": true,
      focusable: false,
    }),
  );
  return '{{define "icon-' + name + '"}}' + svg + "{{end}}";
});
await writeFile(
  "../internal/webui/action-icons.html",
  "{{/* Generated from lucide-react by ui/native-icons.mjs. */}}\n" +
    templates.join("\n") +
    "\n",
);
