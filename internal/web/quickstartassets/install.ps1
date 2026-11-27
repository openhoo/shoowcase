# Installs the verified native release for this Windows user. A stdio client
# will keep using this path after the setup command and terminal have closed.
param([string]$Client, [string]$McpUrl, [string]$SetupCode)
$ErrorActionPreference = 'Stop'
$Version = 'v0.7.0'
$Stage = $null
function Get-ShoowcaseHash([string]$Path) {
    $Stream = [IO.File]::OpenRead($Path)
    try {
        $Hasher = [Security.Cryptography.SHA256]::Create()
        try { return [BitConverter]::ToString($Hasher.ComputeHash($Stream)).Replace('-','') }
        finally { $Hasher.Dispose() }
    } finally { $Stream.Dispose() }
}
try {
    if (!$Client -or !$McpUrl -or !$SetupCode -or $args.Count -ne 0) { throw 'Copy the full setup command from Shoowcase.' }
    if ($env:OS -ne 'Windows_NT' -or $env:LOCALAPPDATA -notmatch '^[A-Za-z]:[\\/]') { throw 'Run this command with a local Windows user profile.' }
    $Native = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
    $Arch = switch ($Native) {
        'AMD64' { 'amd64' }
        'ARM64' { 'arm64' }
        default { throw 'Shoowcase requires 64-bit Intel/AMD or ARM Windows.' }
    }
    $Asset = "shoowcase-$Version-windows-$Arch.zip"
    $Base = "https://github.com/openhoo/shoowcase/releases/download/$Version"
    $Dest = $env:LOCALAPPDATA
    foreach ($Part in @('Shoowcase', 'bin')) {
        $Dest = [IO.Path]::Combine($Dest, $Part)
        $Dir = [IO.Directory]::CreateDirectory($Dest)
        if (($Dir.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'Refusing a linked Shoowcase install directory.' }
    }
    $Stage = Join-Path $Dest ('.install.' + [guid]::NewGuid().ToString('N'))
    [void][IO.Directory]::CreateDirectory($Stage)
    $Sums = Join-Path $Stage 'SHA256SUMS'
    $Zip = Join-Path $Stage 'release.zip'
    # curl.exe ships with current Windows. Ignore curlrc and reject HTTP downgrades.
    $Curl = (Get-Command curl.exe -CommandType Application -ErrorAction Stop | Select-Object -First 1).Source
    $Tls = @()
    if ($env:SHOOWCASE_CA_FILE) {
        $Tls = @('--cacert', $env:SHOOWCASE_CA_FILE, '--proxy-cacert', $env:SHOOWCASE_CA_FILE)
    }
    & $Curl -q -fsL --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 180 @Tls -o $Sums "$Base/SHA256SUMS" 2>$null
    if ($LASTEXITCODE -ne 0) { throw 'Could not download Shoowcase release checksums.' }
    & $Curl -q -fsL --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 180 @Tls -o $Zip "$Base/$Asset" 2>$null
    if ($LASTEXITCODE -ne 0) { throw 'Could not download the Shoowcase release.' }
    $Pattern = '^([0-9a-f]{64})  ' + [regex]::Escape($Asset) + '$'
    $Lines = @([IO.File]::ReadAllLines($Sums) | Where-Object { $_ -cmatch $Pattern })
    if ($Lines.Count -ne 1 -or (Get-ShoowcaseHash $Zip) -ine $Lines[0].Substring(0,64)) {
        throw 'The downloaded Shoowcase archive failed its SHA-256 check.'
    }
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $ZipFile = [IO.Compression.ZipFile]::OpenRead($Zip)
    $Exe = Join-Path $Stage 'shoowcase.exe'
    try {
        $Entries = @($ZipFile.Entries | Where-Object { $_.FullName -ceq 'shoowcase.exe' })
        if ($Entries.Count -ne 1 -or $Entries[0].Length -le 0) { throw 'The Shoowcase executable is missing.' }
        [IO.Compression.ZipFileExtensions]::ExtractToFile($Entries[0], $Exe)
    } finally { $ZipFile.Dispose() }
    $Reported = & $Exe version
    if ($LASTEXITCODE -ne 0 -or $Reported -cne $Version) { throw 'The executable reports an unexpected version.' }
    $Installed = Join-Path $Dest 'shoowcase.exe'
    # A running stdio client may hold the executable open; reuse identical bytes.
    if (![IO.File]::Exists($Installed)) {
        try { [IO.File]::Move($Exe, $Installed) } catch [IO.IOException] { if (![IO.File]::Exists($Installed)) { throw } }
    }
    if (([IO.File]::GetAttributes($Installed) -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'Unexpected linked Shoowcase executable.' }
    if ([IO.File]::Exists($Exe) -and (Get-ShoowcaseHash $Installed) -ne (Get-ShoowcaseHash $Exe)) {
        try { [IO.File]::Replace($Exe, $Installed, (Join-Path $Stage 'previous.exe')) } catch {
            throw 'Close any running Shoowcase client and try the setup command again.'
        }
    }
} catch {
    # A terminating PowerShell error may print the caller's entire pasted line,
    # including the setup code. Report the operation's message ourselves instead.
    [Console]::Error.WriteLine('Shoowcase setup failed: ' + $_.Exception.Message)
    $global:LASTEXITCODE = 1
    return
} finally {
    if ($Stage -and [IO.Directory]::Exists($Stage)) {
        try { [IO.Directory]::Delete($Stage, $true) } catch {
            [Console]::Error.WriteLine('Could not remove temporary Shoowcase installation files.')
        }
    }
}
try {
    & $Installed connect $Client --url $McpUrl --setup-code $SetupCode
} catch {
    [Console]::Error.WriteLine('Could not start the Shoowcase client. Open Quickstart and try the connection again.')
    $global:LASTEXITCODE = 1
}
