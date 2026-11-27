// SPDX-License-Identifier: Apache-2.0

package web

import "strings"

// The first download is itself behind the proxy, so the initial routing and
// local, hidden prompt must be part of the copied command. Only the validated
// control origin, fixed client ID and base64url setup code come from the server.
func quickProxyCommand(base, client, platform, code string) string {
	body := quickProxyUnix
	if platform == "windows" {
		body = quickProxyWindows
	}
	return strings.NewReplacer("@BASE@", base, "@CLIENT@", client, "@CODE@", code).Replace(body)
}

const quickProxyUnix = `sh -s -- '@BASE@' '@CLIENT@' '@CODE@' <<'SHOOWCASE_SETUP'
set +x
set -eu
fail() { printf '%s\n' "$1" >&2; exit 1; }
HTTPS_PROXY=${HTTPS_PROXY:-${https_proxy:-${ALL_PROXY:-${all_proxy:-}}}}
if [ -z "$HTTPS_PROXY" ]; then
  exec 3<>/dev/tty
  tty_state=$(stty -g <&3) || fail 'Could not read your terminal settings.'
  trap 'stty "$tty_state" <&3; printf "\n" >&3' 0
  trap 'exit 1' HUP INT TERM
  stty -echo <&3
  printf 'Company proxy URL (http:// or https://; input hidden): ' >&3
  IFS= read -r HTTPS_PROXY <&3 || exit 1
  stty "$tty_state" <&3; printf '\n' >&3
  trap - 0 HUP INT TERM
  exec 3>&-
fi
export HTTPS_PROXY
# Validate without passing the value to a program in its arguments.
HTTPS_PROXY=$(LC_ALL=C awk 'BEGIN {
  p=ENVIRON["HTTPS_PROXY"]
  if (length(p)>4096 || p !~ /^https?:\/\// || p ~ /[[:space:][:cntrl:]\\?#]/) exit 1
  sub(/\/$/,"",p); a=p; sub(/^https?:\/\//,"",a)
  e=a; gsub(/%[0-9a-fA-F][0-9a-fA-F]/,"",e)
  if (a ~ /\// || e ~ /%/ || tolower(a) ~ /%(0[0-9a-f]|1[0-9a-f]|7f)/) exit 1
  if (split(a,v,"@")>2) exit 1
  h=a; sub(/^[^@]*@/,"",h)
  if (h !~ /^([A-Za-z0-9_.-]+|\[[0-9A-Fa-f:.]+\])(:[0-9]+)?$/) exit 1
  if (h ~ /:[0-9]+$/) { sub(/^.*:/,"",h); if (h+0<1 || h+0>65535) exit 1 }
  else p=p (p ~ /^https:/ ? ":443" : ":80")
  print p
}') || fail 'Invalid proxy: use an HTTP or HTTPS URL, with an optional port and URL-encoded credentials.'
https_proxy=$HTTPS_PROXY; NO_PROXY=${NO_PROXY:-${no_proxy:-}}; no_proxy=$NO_PROXY
export https_proxy NO_PROXY no_proxy
SHOOWCASE_CA_FILE=${SHOOWCASE_CA_FILE:-${CURL_CA_BUNDLE:-${SSL_CERT_FILE:-}}}; export SHOOWCASE_CA_FILE
fetch() {
  if [ -n "${SHOOWCASE_CA_FILE:-}" ]; then
    case $SHOOWCASE_CA_FILE in /*) ;; *) fail 'Use an absolute path for SHOOWCASE_CA_FILE.' ;; esac
    set -- --cacert "$SHOOWCASE_CA_FILE" --proxy-cacert "$SHOOWCASE_CA_FILE" "$@"
  fi
  curl -q -fsL --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 90 "$@" 2>/dev/null
}
installer=$(fetch "$1/quickstart/install.sh") || fail 'Could not download the Shoowcase installer. Check the proxy, NO_PROXY and certificate settings.'
printf '%s\n' "$installer" | sh -s -- "$2" "$1/mcp" "$3"
SHOOWCASE_SETUP`

const quickProxyWindows = `& {
  $ErrorActionPreference = 'Stop'
  $previous = $env:HTTPS_PROXY
  $previousCA = $env:SHOOWCASE_CA_FILE
  try {
    $p = if ($env:HTTPS_PROXY) { $env:HTTPS_PROXY } else { $env:ALL_PROXY }
    if (!$p) {
      $hidden = Read-Host 'Company proxy URL (http:// or https://; input hidden)' -AsSecureString
      $ptr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($hidden)
      try { $p = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($ptr) }
      finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($ptr); $hidden.Dispose() }
    }
    $u = $null
    if ($p.Length -gt 4096 -or $p -cnotmatch '^https?://[^/\\?#\s\x00-\x1f\x7f]+/?$' -or $p -match '%(?![0-9a-f]{2})|%(0[0-9a-f]|1[0-9a-f]|7f)' -or ![Uri]::TryCreate($p, [UriKind]::Absolute, [ref]$u) -or !$u.Host -or $u.Port -lt 1) {
      [Console]::Error.WriteLine('Invalid proxy: use an HTTP or HTTPS URL, with an optional port and URL-encoded credentials.')
      $global:LASTEXITCODE = 1; return
    }
    $p = $p.TrimEnd('/')
    $authority = ($p -split '://',2)[1] -split '@'
    if ($authority.Count -gt 2 -or $authority[-1] -notmatch '^([a-z0-9_.-]+|\[[0-9a-f:.]+\])(:[0-9]+)?$') {
      [Console]::Error.WriteLine('Invalid proxy host or port.')
      $global:LASTEXITCODE = 1; return
    }
    if ($authority[-1] -notmatch ':[0-9]+$') { $p += ':' + $u.Port }
    $env:HTTPS_PROXY = $p
    if (!$env:SHOOWCASE_CA_FILE) { $env:SHOOWCASE_CA_FILE = if ($env:CURL_CA_BUNDLE) { $env:CURL_CA_BUNDLE } else { $env:SSL_CERT_FILE } }
    $tls = @()
    if ($env:SHOOWCASE_CA_FILE) {
      if ($env:SHOOWCASE_CA_FILE -notmatch '^(?:[A-Za-z]:[\\/]|\\\\[^\\]+\\[^\\]+\\)') {
        [Console]::Error.WriteLine('Use an absolute path for SHOOWCASE_CA_FILE.'); $global:LASTEXITCODE = 1; return
      }
      $tls = @('--cacert',$env:SHOOWCASE_CA_FILE,'--proxy-cacert',$env:SHOOWCASE_CA_FILE)
    }
    $curl = (Get-Command curl.exe -CommandType Application | Select-Object -First 1).Source
    $script = & $curl -q -fsL --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 90 @tls '@BASE@/quickstart/install.ps1' 2>$null
    if ($LASTEXITCODE -ne 0 -or !$script) { throw 'Download failed' }
    & ([ScriptBlock]::Create(($script -join [Environment]::NewLine))) '@CLIENT@' '@BASE@/mcp' '@CODE@'
  } catch {
    [Console]::Error.WriteLine('Could not run the Shoowcase installer. Check the proxy, NO_PROXY and certificate settings.')
    $global:LASTEXITCODE = 1
  } finally { $env:HTTPS_PROXY = $previous; $env:SHOOWCASE_CA_FILE = $previousCA }
}`
