#!/usr/bin/env bash
# Build a release chart from the source chart; never edit chart defaults in place.
set -euo pipefail
tag="${1:?usage: scripts/package-chart.sh vX.Y.Z REGISTRY/IMAGE@sha256:DIGEST OUTPUT_DIRECTORY}"
image="${2:?supply a GHCR image pinned by SHA-256}"
out="${3:?supply an output directory}"
[[ $tag =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || { echo 'expected stable tag vX.Y.Z' >&2; exit 2; }
[[ $image =~ ^ghcr\.io/[a-z0-9._/-]+@sha256:[0-9a-f]{64}$ ]] || { echo 'expected a GHCR image pinned by SHA-256' >&2; exit 2; }
version=${tag#v}
mkdir -p "$out"
out=$(cd "$out" && pwd)
archive="shoowcase-$version.tgz"
[[ ! -e $out/$archive ]] || { echo "refusing to overwrite $archive" >&2; exit 1; }
cd "$(dirname "$0")/.."
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
export SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct)}
if [[ -n $(find charts/shoowcase -type l -print -quit) ]]; then echo 'unexpected symlink in source chart' >&2; exit 1; fi
cp -R charts/shoowcase "$tmp/"
cp LICENSE NOTICE "$tmp/shoowcase/"
python3 - "$tmp/shoowcase" "$image" <<'PY'
from pathlib import Path
import os, re, sys

chart = Path(sys.argv[1])
repo, digest = sys.argv[2].split('@')
path = chart / 'values.yaml'
values = path.read_text()
# Update only scalar values inside the image mapping. Abort if its layout changes.
block = re.search(r'^image:[^\S\n]*\n(?:[ \t][^\n]*\n)+', values, re.MULTILINE)
if not block:
    raise SystemExit('missing image mapping in values.yaml')
updated = block[0]
for key, value in [('repository', repo), ('digest', digest)]:
    updated, count = re.subn(rf'^  {key}: [^\n]+$', f'  {key}: "{value}"', updated, flags=re.MULTILINE)
    if count != 1:
        raise SystemExit(f'expected one image.{key} in values.yaml')
path.write_text(values[:block.start()] + updated + values[block.end():])
# Helm 4 preserves input mtimes. Normalize them to make identical inputs package identically.
for entry in chart.rglob('*'):
    if entry.is_file():
        os.utime(entry, (int(os.environ['SOURCE_DATE_EPOCH']),) * 2)
PY
helm package "$tmp/shoowcase" --version "$version" --app-version "$tag" -d "$tmp"
packed="$tmp/$archive"
actual_repo=$(helm show values "$packed" --jsonpath '{.image.repository}')
actual_digest=$(helm show values "$packed" --jsonpath '{.image.digest}')
[[ "$actual_repo@$actual_digest" == "$image" ]] || { echo 'packaged chart image differs from released image' >&2; exit 1; }
# Helm uses the archive mtime for its OCI manifest creation timestamp.
python3 -c 'import os,sys; os.utime(sys.argv[1], (int(os.environ["SOURCE_DATE_EPOCH"]),) * 2)' "$packed"
mv "$packed" "$out/$archive"
