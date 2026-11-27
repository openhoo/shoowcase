#!/bin/sh
# Remove the current user's Quickstart connector with a verified helper.
set +x
set -eu
umask 077
fail() { printf '%s\n' "$1" >&2; exit 1; }

main() {
    [ "$#" -eq 0 ] || fail 'The Shoowcase uninstaller takes no arguments.'
    case $(uname -s) in Darwin) os=darwin ;; Linux) os=linux ;; *) fail 'Use the Windows PowerShell command, or run this on macOS or Linux.' ;; esac
    case $(uname -m) in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) fail 'Shoowcase requires a 64-bit Intel/AMD or ARM processor.' ;; esac
    version=v0.10.6
    asset="shoowcase-$version-$os-$arch.tar.gz"
    base="https://github.com/openhoo/shoowcase/releases/download/$version"
    stage=$(mktemp -d "${TMPDIR:-/tmp}/shoowcase-uninstall.XXXXXXXX")
    trap 'rm -rf "$stage"' 0
    trap 'exit 1' HUP INT TERM
    # Ignore curl configuration files; prohibit HTTP, including on redirects.
    download() {
        if [ -n "${SHOOWCASE_CA_FILE:-}" ]; then
            set -- --cacert "$SHOOWCASE_CA_FILE" --proxy-cacert "$SHOOWCASE_CA_FILE" "$@"
        fi
        curl -q -fsL --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 180 "$@" 2>/dev/null ||
            fail 'Could not download the Shoowcase release. Check connectivity, proxy and certificate settings.'
    }
    download -o "$stage/SHA256SUMS" "$base/SHA256SUMS"
    download -o "$stage/archive" "$base/$asset"
    expected=$(awk -v f="$asset" 'NF == 2 && $2 == f { n++; h=$1 } END { if (n != 1 || length(h) != 64 || h ~ /[^0-9a-f]/) exit 1; print h }' "$stage/SHA256SUMS") ||
        fail 'The release is missing an unambiguous checksum for this platform.'
    if command -v sha256sum >/dev/null 2>&1; then
        result=$(sha256sum < "$stage/archive")
    elif command -v shasum >/dev/null 2>&1; then
        result=$(shasum -a 256 < "$stage/archive")
    else
        fail 'Install sha256sum or shasum to verify the download.'
    fi
    [ "${result%% *}" = "$expected" ] || fail 'The downloaded Shoowcase archive failed its SHA-256 check.'
    # Read the one expected member; archive paths never become filesystem paths.
    tar -xzOf "$stage/archive" shoowcase > "$stage/shoowcase"
    [ -s "$stage/shoowcase" ] || fail 'The Shoowcase executable is missing.'
    chmod 700 "$stage/shoowcase"
    [ "$("$stage/shoowcase" version)" = "$version" ] || fail 'The executable reports an unexpected version.'
    "$stage/shoowcase" uninstall
}

# A truncated download must not start the uninstall.
main "$@"
