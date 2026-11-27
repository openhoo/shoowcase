#!/bin/sh
# The console supplies the client, instance MCP URL and a short-lived setup code.
# Keep the installed path stable: stdio clients start this executable again later.
set +x
set -eu
umask 077
fail() { printf '%s\n' "$1" >&2; exit 1; }

main() {
    [ "$#" -eq 3 ] && [ -n "$1" ] && [ -n "$2" ] && [ -n "$3" ] ||
        fail 'Expected a client, MCP URL and setup code. Copy a new command from Shoowcase.'
    case ${HOME:-} in /*) ;; *) fail 'Could not find your home directory.' ;; esac
    case $(uname -s) in Darwin) os=darwin ;; Linux) os=linux ;; *) fail 'Use the Windows PowerShell command, or run this on macOS or Linux.' ;; esac
    case $(uname -m) in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) fail 'Shoowcase requires a 64-bit Intel/AMD or ARM processor.' ;; esac
    version=v0.6.1
    asset="shoowcase-$version-$os-$arch.tar.gz"
    base="https://github.com/openhoo/shoowcase/releases/download/$version"
    root="$HOME/.local/share/shoowcase"
    dest="$root/bin"
    for dir in "$root" "$dest"; do
        [ ! -L "$dir" ] || fail 'Refusing a linked Shoowcase install directory.'
        mkdir -p "$dir"
        chmod 700 "$dir"
    done
    stage=$(mktemp -d "$dest/.install.XXXXXXXX")
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
    [ ! -L "$dest/shoowcase" ] && [ ! -d "$dest/shoowcase" ] || fail 'Unexpected Shoowcase executable path.'
    mv -f "$stage/shoowcase" "$dest/shoowcase"
    rm -rf "$stage"
    trap - 0 HUP INT TERM
    exec "$dest/shoowcase" connect "$1" --url "$2" --setup-code "$3"
}

# Keep this invocation last: a truncated download must not start installation.
main "$@"
