import assert from "node:assert/strict";
import test from "node:test";
import {navigationTarget, postTarget} from "../src/lib/navigation.ts";

test("console actions and directory links remain on their own origin", () => {
  globalThis.window = {location: new URL("https://control.example.org/admin?view=quotas")};
  assert.equal(postTarget("/sites/~new/edit"), "/sites/~new/edit");
  assert.equal(navigationTarget("#budget-a"), "#budget-a");
  assert.equal(navigationTarget("/admin?q=A%26B&sort=name"), "/admin?q=A%26B&sort=name");
  assert.equal(navigationTarget("https://accounts.example.net/security"), undefined);
  assert.equal(postTarget("https://accounts.example.net/security"), "/healthz");
  assert.equal(navigationTarget("https://accounts.example.net/security?me=1#password", true), "https://accounts.example.net/security?me=1#password");
  for (const invalid of ["//preview.example.net/", "https://user@preview.example.net/", "file:///page", "http://preview.example.net/"]) {
    assert.equal(navigationTarget(invalid, true), undefined);
  }
});
test("HTTP tenant previews can open only between local development hosts", () => {
  globalThis.window = {location: new URL("http://localhost:7878/sites/sample")};
  assert.equal(navigationTarget("http://sample.sites.localhost:7878/", true), "http://sample.sites.localhost:7878/");
  assert.equal(navigationTarget("http://sample.localhost.example.net:7878/", true), undefined);
});
