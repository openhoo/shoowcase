#!/usr/bin/env bash
# Build release archives locally; publishing is a separate GitHub Actions job.
set -euo pipefail
cd "$(dirname "$0")/.."
version="${1:?usage: scripts/release.sh vX.Y.Z [empty-output-directory]}"
if [[ ! $version =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo 'expected stable tag vX.Y.Z' >&2; exit 2
fi
out="${2:-dist/$version}"
mkdir -p "$out"
if [[ -n $(ls -A "$out") ]]; then echo 'release output must be empty' >&2; exit 1; fi
out="$(cd "$out" && pwd)"
export GOTOOLCHAIN=local GOWORK=off GOENV=off GOFLAGS='' GOEXPERIMENT='' GOAMD64=v1 GOARM64=v8.0
if [[ $(go env GOVERSION) != go1.27.4 ]]; then
  echo 'Use Go 1.27.4 for reproducible release archives.' >&2; exit 1
fi
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
# An invalid local module, unknown flag, or failed target must not leave
# unvalidated archives or build paths in the requested release directory.
dest="$out"
out="$tmp/assets"
mkdir "$out"
SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct 2>/dev/null || echo 0)}"
export SOURCE_DATE_EPOCH
go mod verify
cp LICENSE NOTICE runtime/THIRD-PARTY-LICENSES.txt ui/UI-LICENSES.txt "$out/"
# The skills are text and small enough to store uncompressed. Fixed metadata and
# an explicit inventory make this ZIP independent of platform and file mtimes.
python3 - "$out/shoowcase-${version}-skills.zip" <<'PY'
from pathlib import Path
import stat, subprocess, sys, zipfile

target = Path(sys.argv[1])
skills = ("shoowcase-operate", "shoowcase-publish")
members = {"LICENSE": Path("LICENSE"), "NOTICE": Path("NOTICE")}
for name in skills:
    members[name + "/SKILL.md"] = Path("skills", name, "SKILL.md")

# A new tracked resource needs an explicit release decision; untracked local
# files (including editor backups and secrets) are never archive inputs.
tracked = subprocess.check_output(["git", "ls-files", "-z", "--", "skills", "LICENSE", "NOTICE"]).split(b"\0")
if {s for s in tracked if s} != {p.as_posix().encode() for p in members.values()}:
    raise SystemExit("unexpected tracked skills inventory; review the skills archive allowlist")

with zipfile.ZipFile(target, "x", compression=zipfile.ZIP_STORED) as out:
    for arcname, src in sorted(members.items()):
        if any(p.is_symlink() for p in (src, *src.parents)) or not stat.S_ISREG(src.stat().st_mode):
            raise SystemExit("expected regular release source: " + str(src))
        data = src.read_bytes()
        if arcname.endswith("/SKILL.md"):
            text = data.decode("utf-8").splitlines()
            if not text or text[0] != "---" or "---" not in text[1:]:
                raise SystemExit("missing skill front matter: " + arcname)
            header = text[1:text.index("---", 1)]
            if [line for line in header if line.startswith("name:")] != ["name: " + src.parent.name] or not any(line.startswith("description: ") and line[13:].strip() for line in header):
                raise SystemExit("unexpected skill name or missing description: " + arcname)
        entry = zipfile.ZipInfo(arcname, (1980, 1, 1, 0, 0, 0))
        entry.create_system = 3
        entry.external_attr = (0o100644 << 16)
        out.writestr(entry, data)
with zipfile.ZipFile(target) as check:
    if check.namelist() != sorted(members) or any(check.read(n) != members[n].read_bytes() for n in members):
        raise SystemExit("skills archive differs from its approved paths or source bytes")
PY
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os="${target%/*}"; arch="${target#*/}"
  name="shoowcase-${version}-${os}-${arch}"
  bin=shoowcase; if [[ $os == windows ]]; then bin=shoowcase.exe; fi
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -mod=readonly -buildvcs=false -trimpath \
    -ldflags="-s -w -buildid= -X main.version=$version" -o "$tmp/$bin" ./cmd/shoowcase
  go version -m -json "$tmp/$bin" > "$tmp/buildinfo.json"
  python3 - "$tmp/$bin" "$out/$name" "$version" <<'PY'
import datetime, gzip, hashlib, io, json, os, pathlib, sys, tarfile, urllib.parse, zipfile
binary, target, version = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2]), sys.argv[3]
payload = binary.read_bytes()
if binary.suffix == ".exe":
    entry = zipfile.ZipInfo(binary.name, (1980, 1, 1, 0, 0, 0))
    entry.external_attr = (0o100755 << 16); entry.create_system = 3; entry.compress_type = zipfile.ZIP_DEFLATED
    with zipfile.ZipFile(str(target) + ".zip", "w") as out:
        out.writestr(entry, payload)
        for name in ("LICENSE", "NOTICE", "runtime/THIRD-PARTY-LICENSES.txt", "ui/UI-LICENSES.txt"):
            note = zipfile.ZipInfo(pathlib.Path(name).name, (1980, 1, 1, 0, 0, 0)); note.external_attr = (0o100644 << 16); note.create_system = 3
            out.writestr(note, pathlib.Path(name).read_bytes())
else:
    with open(str(target) + ".tar.gz", "wb") as raw, gzip.GzipFile(filename="", mode="wb", fileobj=raw, compresslevel=9, mtime=0) as gz, tarfile.open(mode="w", fileobj=gz, format=tarfile.USTAR_FORMAT) as tar:
        entry = tarfile.TarInfo(binary.name); entry.mode = 0o755; entry.size = len(payload); entry.mtime = 0
        tar.addfile(entry, io.BytesIO(payload))
        for name in ("LICENSE", "NOTICE", "runtime/THIRD-PARTY-LICENSES.txt", "ui/UI-LICENSES.txt"):
            note = pathlib.Path(name).read_bytes(); entry = tarfile.TarInfo(pathlib.Path(name).name); entry.mode = 0o644; entry.size = len(note); entry.mtime = 0
            tar.addfile(entry, io.BytesIO(note))
# Take the linked dependency list from the actual binary. Release builds may
# only reference versioned modules verified by this checkout's go.sum.
info = json.loads((binary.parent / "buildinfo.json").read_text())
module = "github.com/openhoo/shoowcase"
if info.get("Main", {}).get("Path") != module or info.get("Path") != module + "/cmd/shoowcase":
    raise SystemExit("unexpected binary module identity")
sums = {tuple(line.split()) for line in pathlib.Path("go.sum").read_text().splitlines()}
for mod in info.get("Deps", []):
    path, v, h = mod.get("Path", ""), mod.get("Version", ""), mod.get("Sum", "")
    if mod.get("Replace") or path.startswith((".", "/")) or "\\" in path or ":" in path or not v.startswith("v") or not h.startswith("h1:") or (path, v, h) not in sums:
        raise SystemExit("release dependencies must be canonical versioned modules with locked checksums")
settings = {v["Key"]:v["Value"] for v in info["Settings"]}
if settings.get("-trimpath") != "true" or settings.get("CGO_ENABLED") != "0":
    raise SystemExit("release binary lacks deterministic build settings")
# Unknown compiler flags can contain private filesystem paths. Use this
# toolchain's deliberately small known set and reject unexpected settings.
allowed = {"-buildmode", "-compiler", "-trimpath", "DefaultGODEBUG", "CGO_ENABLED", "GOARCH", "GOOS", "GOAMD64", "GOARM64"}
if set(settings) - allowed or any("/" in v or "\\" in v for v in settings.values()):
    raise SystemExit("release build contains an unexpected compiler setting or filesystem path")
if (set(info) != {"GoVersion","Path","Main","Deps","Settings"}
        or set(info["Main"]) - {"Path","Version"} or info["Main"].get("Version") != "(devel)"
        or any(set(m) - {"Path","Version","Sum"} for m in info.get("Deps", []))):
    raise SystemExit("unrecognized Go binary metadata; review package format before distributing")
info["Deps"].sort(key=lambda d: (d["Path"], d["Version"]))
info["Settings"].sort(key=lambda d: d["Key"])
pathlib.Path(str(target) + ".buildinfo.json").write_text(json.dumps(info, sort_keys=True, indent=2) + "\n")
digest = hashlib.sha256(payload).hexdigest()
components = []
for mod in info.get("Deps", []):
    mod = mod.get("Replace") or mod
    p = mod["Path"]; v = mod["Version"]
    purl = "pkg:golang/" + urllib.parse.quote(p, safe="/") + "@" + urllib.parse.quote(v, safe="")
    components.append({"type":"library","name":p,"version":v,"purl":purl,"bom-ref":purl})
# These are shipped in the worker, not loaded by the deployment at runtime.
# build.mjs checks the actual bundler metafile against its runtime inventory.
locked=json.loads(pathlib.Path("runtime/package-lock.json").read_text())["packages"]
inventory=json.loads(pathlib.Path("runtime/components.json").read_text())
for name in inventory["npm"]:
    libversion=locked["node_modules/"+name]["version"]
    purl="pkg:npm/"+urllib.parse.quote(name,safe="/")+"@"+libversion
    components.append({"type":"library","name":name,"version":libversion,"purl":purl,"bom-ref":purl})
for lib in json.loads(pathlib.Path("ui/inventory.json").read_text()):
    name, version_ui = lib["name"], lib["version"]
    if name == "shadcn/ui":
        components.append({"type":"library","name":name,"version":version_ui,"bom-ref":"shadcn-"+version_ui})
        continue
    purl="pkg:npm/"+urllib.parse.quote(name,safe="/")+"@"+version_ui
    components.append({"type":"library","name":name,"version":version_ui,"purl":purl,"bom-ref":purl})
engine=inventory["quickjsCommit"]
components.append({"type":"library","name":"Bellard QuickJS","version":engine,"bom-ref":"quickjs-"+engine,
                   "externalReferences":[{"type":"vcs","url":"https://github.com/bellard/quickjs/commit/"+engine}]})
components.append({"type":"framework","name":"Go standard library","version":info["GoVersion"],"bom-ref":"go-stdlib"})
root = {"type":"application","name":"shoowcase","version":version,"bom-ref":"shoowcase-"+digest,"hashes":[{"alg":"SHA-256","content":digest}]}
bom = {"bomFormat":"CycloneDX","specVersion":"1.5","version":1,"metadata":{"component":root},"components":sorted(components, key=lambda c:c["bom-ref"])}
pathlib.Path(str(target) + ".cdx.json").write_text(json.dumps(bom, sort_keys=True, indent=2) + "\n")
created = datetime.datetime.fromtimestamp(int(os.environ.get("SOURCE_DATE_EPOCH") or "0"), datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
base = {"downloadLocation":"NOASSERTION","filesAnalyzed":False,"licenseConcluded":"NOASSERTION","licenseDeclared":"NOASSERTION","copyrightText":"NOASSERTION"}
main = dict(base, name="shoowcase", versionInfo=version, SPDXID="SPDXRef-Shoowcase", licenseDeclared="Apache-2.0", checksums=[{"algorithm":"SHA256","checksumValue":digest}])
pkgs = [main]; rel = [{"spdxElementId":"SPDXRef-DOCUMENT","relationshipType":"DESCRIBES","relatedSpdxElement":"SPDXRef-Shoowcase"}]
for n, c in enumerate(bom["components"], 1):
    key = "SPDXRef-Component-" + str(n)
    pkg = dict(base, name=c["name"], versionInfo=c["version"], SPDXID=key)
    if c.get("purl"):
        pkg["externalRefs"] = [{"referenceCategory":"PACKAGE-MANAGER","referenceType":"purl","referenceLocator":c["purl"]}]
    pkgs.append(pkg)
    rel.append({"spdxElementId":"SPDXRef-Shoowcase","relationshipType":"DEPENDS_ON","relatedSpdxElement":key})
spdx = {"spdxVersion":"SPDX-2.3","dataLicense":"CC0-1.0","SPDXID":"SPDXRef-DOCUMENT","name":target.name,"documentNamespace":"https://github.com/openhoo/shoowcase/spdx/"+digest,"creationInfo":{"created":created,"creators":["Tool: shoowcase-release"]},"packages":pkgs,"relationships":rel}
pathlib.Path(str(target) + ".spdx.json").write_text(json.dumps(spdx, sort_keys=True, indent=2) + "\n")

PY
done
# On the release runner, also bind the multi-platform OCI digest into the checksum file.
if [[ -n ${SHOOWCASE_RELEASE_IMAGE:-} ]]; then
  [[ $SHOOWCASE_RELEASE_IMAGE =~ ^ghcr\.io/[a-z0-9._/-]+@sha256:[a-f0-9]{64}$ ]] || { echo 'invalid OCI reference' >&2; exit 1; }
  printf '%s\n' "$SHOOWCASE_RELEASE_IMAGE" > "$out/image.txt"
fi
python3 - "$out" <<'PY'
import hashlib, pathlib, sys
root = pathlib.Path(sys.argv[1])
lines = [hashlib.sha256(f.read_bytes()).hexdigest()+"  "+f.name+"\n" for f in sorted(root.iterdir())]
(root/"SHA256SUMS").write_text("".join(lines))
PY
mv "$out/"* "$dest/"
printf 'Release files: %s\n' "$dest"
