#!/usr/bin/env python3
"""Verify a published v0.2+ release against its commit, including its public container and build attestations."""
import argparse
import hashlib
import io
import json
from pathlib import Path
import re
import subprocess
import tarfile
from urllib.parse import urlencode, urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener
import zipfile

REPO = "openhoo/shoowcase"
API = "https://api.github.com/repos/" + REPO
REGISTRY = "https://ghcr.io/v2/" + REPO
ROOT = Path(__file__).resolve().parents[1]
NOTICES = ("LICENSE", "NOTICE", "THIRD-PARTY-LICENSES.txt")


def targets(tag):
    original = (("linux", "amd64"), ("linux", "arm64"), ("darwin", "arm64"), ("windows", "amd64"))
    return original + ((("darwin", "amd64"), ("windows", "arm64")) if tuple(map(int, tag[1:].split("."))) >= (0, 5, 0) else ())


def require(ok, reason):
    if not ok:
        raise RuntimeError(reason)


def digest(data):
    return hashlib.sha256(data).hexdigest()


class PublicRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        if urlsplit(newurl).scheme != "https":
            raise RuntimeError("refusing a public HTTP downgrade")
        redirected = super().redirect_request(req, fp, code, msg, headers, newurl)
        if redirected is not None and urlsplit(newurl).netloc != urlsplit(req.full_url).netloc:
            redirected.remove_header("Authorization")
        return redirected


def read_url(url, limit=1 << 20, headers=None):
    # No GitHub or registry credentials are loaded. OCI's public pull token comes
    # only from the anonymous challenge. Never print it or a download redirect.
    req = Request(url, headers={"User-Agent": "shoowcase-public-release-verification", **(headers or {})})
    with build_opener(PublicRedirect()).open(req, timeout=60) as res:
        data = res.read(limit + 1)
    require(len(data) <= limit, "response exceeded expected size")
    return data


def github(path):
    return json.loads(read_url(API + path))


def source(commit, path):
    return subprocess.check_output(["git", "-C", ROOT, "show", commit + ":" + path])


def command(*args):
    # All operands are controlled artifact names, digests, tags or local paths.
    out = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=180)
    require(out.returncode == 0, f"{args[0]} {args[1]} failed: {out.stdout[-1000:].decode(errors='replace')}")
    return out.stdout


def verify_repo(tag, commit):
    ref = github("/git/ref/tags/" + tag)["object"]
    if ref["type"] == "tag":
        ref = github("/git/tags/" + ref["sha"])["object"]
    require(ref["type"] == "commit" and ref["sha"] == commit, "published tag does not resolve to requested commit")
    r = github("/releases/tags/" + tag)
    require(r.get("immutable") is True and r["tag_name"] == tag and not r["draft"] and not r["prerelease"],
            "expected a published immutable stable release")
    return r


def notices(tag):
    return (*NOTICES, "UI-LICENSES.txt") if tuple(map(int, tag[1:].split("."))) >= (0, 10, 0) else NOTICES


def inventory(tag, has_chart):
    names = {"SHA256SUMS", "image.txt", *notices(tag), f"shoowcase-{tag}-skills.zip"}
    if has_chart:
        names.update({"chart.txt", f"shoowcase-{tag[1:]}.tgz"})
    for osname, arch in targets(tag):
        base = f"shoowcase-{tag}-{osname}-{arch}"
        for suffix in (".buildinfo.json", ".cdx.json", ".spdx.json", ".zip" if osname == "windows" else ".tar.gz"):
            names.add(base + suffix)
    return names


def get_assets(rel, tag, dest, has_chart):
    want = inventory(tag, has_chart)
    require(len(rel["assets"]) == len(want) and {x["name"] for x in rel["assets"]} == want, "unexpected release asset inventory")
    for asset in rel["assets"]:
        name = asset["name"]
        u = f"https://github.com/{REPO}/releases/download/{tag}/{name}"
        require(asset["browser_download_url"] == u and 0 < asset["size"] <= 64 << 20, "unexpected download URL or size: " + name)
        data = read_url(u, asset["size"])
        require(len(data) == asset["size"] and "sha256:" + digest(data) == asset["digest"], "GitHub asset digest mismatch: " + name)
        (dest / name).write_bytes(data)
    sums = (dest / "SHA256SUMS").read_text().splitlines()
    require(len(sums) == len(want) - 1, "incorrect checksum count")
    require(set(sums) == {digest((dest / name).read_bytes()) + "  " + name for name in want - {"SHA256SUMS"}}, "checksums differ")


def verify_chart(dest, tag, image):
    """Verify that anonymous OCI by tag and digest serves the attested release asset."""
    repo = REPO.split("/")[0] + "/charts/shoowcase"
    ref = (dest / "chart.txt").read_text().strip()
    name, _, expected = ref.partition("@")
    require(name == "ghcr.io/" + repo and re.fullmatch(r"sha256:[0-9a-f]{64}", expected), "unexpected chart OCI reference")
    token = json.loads(read_url("https://ghcr.io/token?" + urlencode({"service": "ghcr.io", "scope": f"repository:{repo}:pull"})))["token"]
    headers = {"Authorization": "Bearer " + token, "Accept": "application/vnd.oci.image.manifest.v1+json"}
    registry = "https://ghcr.io/v2/" + repo
    for selector in [tag[1:], expected]:
        raw = read_url(registry + "/manifests/" + selector, headers=headers)
        require("sha256:" + digest(raw) == expected, "OCI chart manifest digest mismatch")
    manifest = json.loads(raw)
    archive = dest / f"shoowcase-{tag[1:]}.tgz"
    layer_digest = "sha256:" + digest(archive.read_bytes())
    require(len(manifest["layers"]) == 1 and manifest["layers"][0]["mediaType"] == "application/vnd.cncf.helm.chart.content.v1.tar+gzip"
            and manifest["layers"][0]["digest"] == layer_digest, "OCI chart layer differs from release archive")
    data = read_url(registry + "/blobs/" + layer_digest, archive.stat().st_size, headers)
    require("sha256:" + digest(data) == layer_digest, "anonymous OCI chart bytes differ")
    with tarfile.open(archive) as tar:
        def textfile(name):
            entry = tar.getmember("shoowcase/" + name)
            require(entry.isfile() and entry.size <= 1 << 20, "unexpected chart member: " + name)
            return tar.extractfile(entry).read().decode()
        meta = textfile("Chart.yaml").splitlines()
        require("name: shoowcase" in meta and f"version: {tag[1:]}" in meta and f"appVersion: {tag}" in meta, "chart metadata differs from release")
        values = re.search(r"^image:\s*\n((?:[ \t][^\n]*\n)+)", textfile("values.yaml"), re.MULTILINE)
        repository, sha = image.split("@")
        require(values and f'  repository: "{repository}"' in values[1].splitlines() and f'  digest: "{sha}"' in values[1].splitlines(),
                "chart does not use the verified release image by default")
    return ref


def verify_skills(dest, tag, commit):
    name = f"shoowcase-{tag}-skills.zip"
    expected = {x + "/SKILL.md": "skills/" + x + "/SKILL.md" for x in ["shoowcase-operate", "shoowcase-publish"]}
    expected.update(LICENSE="LICENSE", NOTICE="NOTICE")
    with zipfile.ZipFile(dest / name) as z:
        require(sorted(z.namelist()) == sorted(expected), "unexpected skill ZIP inventory")
        for path, src in expected.items():
            require(z.read(path) == source(commit, src), "unexpected skill or license bytes: " + path)


def verify_binaries(dest, tag, commit):
    prefix = {"UI-LICENSES.txt": "ui/", "THIRD-PARTY-LICENSES.txt": "runtime/"}
    expected_notices = {name: source(commit, prefix.get(name, "") + name) for name in notices(tag)}
    for name, contents in expected_notices.items():
        require((dest / name).read_bytes() == contents, "release notice differs: " + name)
    packages = json.loads(source(commit, "runtime/package-lock.json"))["packages"]
    inventory = json.loads(source(commit, "runtime/components.json"))
    expected_libs = {(n, packages["node_modules/" + n]["version"]) for n in inventory["npm"]}
    expected_libs.add(("Bellard QuickJS", inventory["quickjsCommit"]))
    if "UI-LICENSES.txt" in expected_notices:
        expected_libs.update((p["name"], p["version"]) for p in json.loads(source(commit, "ui/inventory.json")))
    executables = {}
    for osname, arch in targets(tag):
        base = f"shoowcase-{tag}-{osname}-{arch}"
        exe = "shoowcase.exe" if osname == "windows" else "shoowcase"
        if osname == "windows":
            with zipfile.ZipFile(dest / (base + ".zip")) as z:
                require(sorted(z.namelist()) == sorted([exe, *expected_notices]), "unexpected Windows archive members")
                records = {p: z.read(p) for p in z.namelist()}
        else:
            with tarfile.open(dest / (base + ".tar.gz")) as t:
                members = t.getmembers()
                require(sorted(t.getnames()) == sorted([exe, *expected_notices]) and all(m.isfile() and m.size <= 64 << 20 for m in members), "unexpected tar archive members")
                require(t.getmember(exe).mode & 0o111, "executable has no execute bits")
                records = {m.name: t.extractfile(m).read() for m in members}
        require(all(records[p] == b for p, b in expected_notices.items()), "archive notices differ: " + base)
        hexdigest = digest(records[exe])
        executables[(osname, arch)] = hexdigest
        binpath = dest / (base + (".exe" if osname == "windows" else ""))
        binpath.write_bytes(records[exe])
        actual = json.loads(command("go", "version", "-m", "-json", str(binpath)))
        info = json.loads((dest / (base + ".buildinfo.json")).read_bytes())
        require(actual["Main"] == info["Main"] and actual["Path"] == info["Path"] == "github.com/openhoo/shoowcase/cmd/shoowcase" and
                actual["GoVersion"] == info["GoVersion"] and sorted(actual["Deps"], key=lambda m: m["Path"]) == info["Deps"] and
                sorted(actual["Settings"], key=lambda x: x["Key"]) == info["Settings"], "Go build information differs from downloaded binary: " + base)
        settings = {x["Key"]: x["Value"] for x in info["Settings"]}
        require((settings["GOOS"], settings["GOARCH"], settings["CGO_ENABLED"]) == (osname, arch, "0"), "wrong target: " + base)
        libs = expected_libs | {(d["Path"], d["Version"]) for d in info["Deps"]} | {("Go standard library", info["GoVersion"])}
        cdx = json.loads((dest / (base + ".cdx.json")).read_bytes())
        require(cdx["bomFormat"] == "CycloneDX" and {"alg": "SHA-256", "content": hexdigest} in cdx["metadata"]["component"]["hashes"] and
                {(p["name"], p["version"]) for p in cdx["components"]} == libs, "CycloneDX does not describe executable: " + base)
        spdx = json.loads((dest / (base + ".spdx.json")).read_bytes())
        roots = [p for p in spdx["packages"] if p["SPDXID"] == "SPDXRef-Shoowcase"]
        require(spdx["spdxVersion"] == "SPDX-2.3" and len(roots) == 1 and {"algorithm": "SHA256", "checksumValue": hexdigest} in roots[0]["checksums"] and
                {(p["name"], p["versionInfo"]) for p in spdx["packages"] if p not in roots} == libs, "SPDX does not describe executable: " + base)
        if osname == "darwin":
            binpath.chmod(0o700)
    return executables, expected_notices


def verify_image(dest, tag, commit, executables, notices):
    image = (dest / "image.txt").read_text().strip()
    repo, sep, expected = image.partition("@")
    require(repo == "ghcr.io/" + REPO and sep and re.fullmatch(r"sha256:[a-f0-9]{64}", expected), "unexpected release OCI reference")
    token = json.loads(read_url("https://ghcr.io/token?" + urlencode({"service":"ghcr.io","scope":f"repository:{REPO}:pull"})))["token"]
    headers = {"Authorization": "Bearer " + token, "Accept": "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json"}
    def obj(kind, selector, wanted, maximum=2 << 20):
        b = read_url(REGISTRY + "/" + kind + "/" + selector, maximum, headers)
        require("sha256:" + digest(b) == wanted, "OCI object digest mismatch")
        return b
    index = json.loads(obj("manifests", tag, expected))
    obj("manifests", expected, expected)
    linux = [m for m in index["manifests"] if m.get("platform", {}).get("os") == "linux"]
    require(sorted(m["platform"]["architecture"] for m in linux) == ["amd64", "arm64"], "unexpected Linux platform set")
    for m in linux:
        arch = m["platform"]["architecture"]
        manifest = json.loads(obj("manifests", m["digest"], m["digest"]))
        config_digest = manifest["config"]["digest"]
        config = json.loads(obj("blobs", config_digest, config_digest))
        ctr = config["config"]
        require((config["os"], config["architecture"], ctr["User"], ctr["Entrypoint"]) == ("linux", arch, "65532:65532", ["/shoowcase"]) and
                ctr["Labels"]["org.opencontainers.image.revision"] == commit and ctr["Labels"]["org.opencontainers.image.version"] == tag, "OCI config or source pin mismatch: " + arch)
        needed = {"usr/share/licenses/shoowcase/" + n: digest(b) for n, b in notices.items()}
        needed["shoowcase"] = executables[("linux", arch)]
        found = {}
        for layer in manifest["layers"]:
            require(0 <= layer["size"] <= 64 << 20, "unexpected OCI layer size")
            raw = obj("blobs", layer["digest"], layer["digest"], layer["size"])
            with tarfile.open(fileobj=io.BytesIO(raw)) as tar:
                for entry in tar:
                    p = entry.name.removeprefix("./")
                    require(not Path(p).name.startswith(".wh."), "unexpected whiteout in scratch image")
                    if p in needed:
                        require(entry.isfile() and entry.size <= 64 << 20, "unexpected file in scratch image")
                        found[p] = digest(tar.extractfile(entry).read())
        require(found == needed, "OCI and archive binary/notices differ for " + arch)
    return image


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("tag")
    p.add_argument("commit", help="trusted exact 40-digit release commit, present in this checkout")
    p.add_argument("--out", type=Path, required=True, help="new directory in which to retain all verified release artifacts")
    args = p.parse_args()
    require(re.fullmatch(r"v\d+\.\d+\.\d+", args.tag) and re.fullmatch(r"[0-9a-f]{40}", args.commit), "invalid stable tag or git commit")
    dest = args.out.resolve()
    dest.mkdir(mode=0o700)
    rel = verify_repo(args.tag, args.commit)
    # Earlier releases predate the chart. Derive the expected inventory from
    # the trusted commit, never from what the remote release happens to contain.
    has_chart = bool(subprocess.check_output(["git", "-C", ROOT, "ls-tree", "--name-only", args.commit, "charts/shoowcase/Chart.yaml"]).strip())
    get_assets(rel, args.tag, dest, has_chart)
    verify_skills(dest, args.tag, args.commit)
    exes, notices = verify_binaries(dest, args.tag, args.commit)
    image = verify_image(dest, args.tag, args.commit, exes, notices)
    print(f"PASS: {rel['html_url']}: immutable tag → {args.commit}, {len(rel['assets'])} anonymous assets, both skills, {len(exes)} Go binaries, SPDX/CycloneDX, notices, and both anonymous OCI platforms for {image}", flush=True)
    command("gh", "release", "verify", args.tag, "--repo", REPO)
    artifacts = [str(dest / "SHA256SUMS"), str(dest / f"shoowcase-{args.tag}-darwin-arm64.tar.gz"), str(dest / f"shoowcase-{args.tag}-skills.zip"), "oci://" + image]
    if has_chart:
        chart_ref = verify_chart(dest, args.tag, image)
        artifacts.extend([str(dest / f"shoowcase-{args.tag[1:]}.tgz"), "oci://" + chart_ref])
        print(f"PASS: chart archive, release image default and anonymous OCI pull for {chart_ref}", flush=True)
    for artifact in artifacts:
        command("gh", "attestation", "verify", artifact, "--repo", REPO, "--source-ref", "refs/tags/" + args.tag,
                "--source-digest", args.commit, "--signer-workflow", REPO + "/.github/workflows/release.yml", "--deny-self-hosted-runners")
    print("PASS: immutable release attestation and signed build provenance for checksums, native archive, skills ZIP, and OCI index.")


if __name__ == "__main__":
    main()
