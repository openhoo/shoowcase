# Workshop tally

Import these three site files into a new Shoowcase draft. From this directory:

```sh
zip workshop-tally.zip index.html style.css app.js
```

In **Sites**, create a site, choose **Import ZIP**, then **Preview draft**. Select **Add one** or **Reset**. The tally belongs to the current page and resets on reload. Use **Publish draft** to share the site with your workspace.

Opening this source HTML directly in a browser runs ordinary native JavaScript. Opening its Shoowcase URL runs the same program with Shoowcase’s limited DOM interface inside QuickJS. See [supported interactions](../../runtime/README.md).
