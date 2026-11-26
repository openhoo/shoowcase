#!/usr/bin/env bash
# Install the exact Helm used by chart CI and release jobs on GitHub's Linux runner.
set -euo pipefail
dest="${1:?usage: scripts/install-chart-helm.sh BIN_DIRECTORY}"
[[ $(uname -s) == Linux && $(uname -m) == x86_64 ]] || { echo 'expected Linux amd64' >&2; exit 1; }
version=v4.3.2
archive="helm-$version-linux-amd64.tar.gz"
# https://get.helm.sh/helm-v4.3.2-linux-amd64.tar.gz.sha256sum
sha=d1c0278d46beab79582d7e50f5b3dc206944ba8e577cde618513fd07daa53983
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
curl --fail --silent --show-error --location --proto '=https' --retry 3 "https://get.helm.sh/$archive" -o "$tmp/archive"
(cd "$tmp" && printf '%s  archive\n' "$sha" | sha256sum --check)
tar -xzf "$tmp/archive" -C "$tmp" linux-amd64/helm
mkdir -p "$dest"
install -m 0755 "$tmp/linux-amd64/helm" "$dest/helm"
