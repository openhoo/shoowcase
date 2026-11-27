#!/usr/bin/env python3
"""Run the public bootstrap unchanged against a native executable and local release archives."""
import hashlib
import io
import os
from pathlib import Path
import platform
import re
import subprocess
import tarfile
import tempfile
import zipfile

ROOT = Path(__file__).resolve().parents[1]
ASSETS = ROOT / "internal/web/quickstartassets"
TAG = re.search(r"^    version=(v[0-9.]+)$", (ASSETS / "install.sh").read_text(), re.M)[1]
CODE = "synthetic-setup-code-for-installer-fixture"
PROXY_PASSWORD = "synthetic-password-for-installer-fixture"
PROXY = f"http://user:{PROXY_PASSWORD}@proxy.invalid:8080"

# Run the real installer entrypoint without touching GitHub, the user's client,
# the real home directory, or a network service. curl and the archive executable
# are both native: PowerShell exercises its actual ZIP and process APIs.
FIXTURE = r'''package main
import ("fmt"; "os"; "path/filepath"; "slices"; "strings")
func main(){
  args:=os.Args[1:]
  // The bootstrap gives curl and connect the protected value through their
  // inherited environment only. Client-side configuration never needs it.
  if want:=os.Getenv("FIXTURE_WANT_PROXY");want!="" {
    if os.Getenv("HTTPS_PROXY")!=want || os.Getenv("NO_PROXY")!="intranet.invalid" { os.Exit(40) }
    for _,arg:=range args {if strings.Contains(arg,os.Getenv("FIXTURE_PROXY_PASSWORD")){os.Exit(41)}}
  }
  if strings.EqualFold(strings.TrimSuffix(filepath.Base(os.Args[0]),".exe"),"curl") {
    base:="https://github.com/openhoo/shoowcase/releases/download/"+os.Getenv("FIXTURE_TAG")+"/"
    if len(args)<5 || args[0]!="-q" || !slices.Contains(args,"--proto-redir") {os.Exit(20)}
    for _,f:= range []string{"--proto","--proto-redir"}{i:=slices.Index(args,f); if i<0||i+1>=len(args)||args[i+1]!="=https"{os.Exit(21)}}
    for _,f:=range []string{"--cacert","--proxy-cacert"}{
      i:=slices.Index(args,f); ca:=os.Getenv("SHOOWCASE_CA_FILE")
      if (ca=="" && i>=0) || (ca!="" && (i<0||i+1>=len(args)||args[i+1]!=ca)){os.Exit(26)}
    }
    if slices.Contains(args,"--insecure")||slices.Contains(args,"--proxy-insecure"){os.Exit(27)}
    name:=strings.TrimPrefix(args[len(args)-1],base)
    if name!="SHA256SUMS" && name!=os.Getenv("FIXTURE_ASSET"){os.Exit(22)}
    i:=slices.Index(args,"-o"); if i<0||i+1>=len(args){os.Exit(23)}
    b,e:=os.ReadFile(filepath.Join(os.Getenv("FIXTURE_ROOT"),name));if e!=nil{os.Exit(24)}
    if os.WriteFile(args[i+1],b,0600)!=nil{os.Exit(25)};return
  }
  if slices.Equal(args,[]string{"version"}){fmt.Println(os.Getenv("FIXTURE_TAG"));return}
  if slices.Equal(args,[]string{"uninstall"}) {
    p,e:=os.Executable();if e!=nil||os.WriteFile(os.Getenv("FIXTURE_RECEIPT"),[]byte(p),0600)!=nil {os.Exit(32)}
    return
  }
  if !slices.Equal(args,[]string{"connect","codex","--url","https://control.example/mcp","--setup-code",os.Getenv("FIXTURE_CODE")}){os.Exit(30)}
  p,e:=os.Executable();if e!=nil||os.WriteFile(os.Getenv("FIXTURE_RECEIPT"),[]byte(p),0600)!=nil {os.Exit(31)}
}'''


def main():
    # Run the actual clipboard command through a real authenticated CONNECT
    # proxy in the native shell on both installer CI operating systems.
    subprocess.run(["go", "test", "internal/web/quickproxy_command.go",
                    "internal/web/quickproxy_command_test.go"], cwd=ROOT, check=True)
    windows = os.name == "nt"
    system = {"Darwin": "darwin", "Linux": "linux", "Windows": "windows"}[platform.system()]
    arch = {"amd64": "amd64", "x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[platform.machine().lower()]
    ext = ".exe" if windows else ""
    name = "shoowcase" + ext
    asset = f"shoowcase-{TAG}-{system}-{arch}" + (".zip" if windows else ".tar.gz")
    with tempfile.TemporaryDirectory(prefix="shoowcase installer ") as tmp:
        root = Path(tmp)
        home = root / "user profile"
        home.mkdir()
        shim = root / ("curl" + ext)
        source = root / "fixture.go"
        source.write_text(FIXTURE)
        subprocess.run(["go", "build", "-trimpath", "-o", shim, source], check=True)
        archive = root / asset
        if windows:
            with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED) as z:
                z.write(shim, name)
                z.writestr("NOTICE", "fixture")
        else:
            with tarfile.open(archive, "w:gz") as tar:
                tar.add(shim, arcname=name)
                notice = tarfile.TarInfo("NOTICE")
                notice.size = 7
                tar.addfile(notice, io.BytesIO(b"fixture"))
        sha = hashlib.sha256(archive.read_bytes()).hexdigest()
        sums = root / "SHA256SUMS"
        sums.write_text(sha + "  " + asset + "\n")
        receipt = root / "invoked"
        clean = {k: v for k, v in os.environ.items() if k.upper() not in {
            "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "SHOOWCASE_CA_FILE", "CURL_CA_BUNDLE", "SSL_CERT_FILE"}}
        env = dict(clean, HOME=str(home), LOCALAPPDATA=str(home),
                   PATH=str(root) + os.pathsep + os.environ["PATH"], FIXTURE_CODE=CODE,
                   FIXTURE_TAG=TAG, FIXTURE_ASSET=asset, FIXTURE_ROOT=str(root), FIXTURE_RECEIPT=str(receipt))
        if windows:
            env["FIXTURE_SCRIPT"] = str(ASSETS / "install.ps1")
            # Same invocation shape as the copied command, without a network fetch.
            args = ["powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
                    "$ErrorActionPreference='Stop'; & ([scriptblock]::Create([IO.File]::ReadAllText($env:FIXTURE_SCRIPT))) "
                    f"'codex' 'https://control.example/mcp' '{CODE}'; exit $LASTEXITCODE"]
            data = None
            installed = home / "Shoowcase/bin" / name
        else:
            args = ["sh", "-s", "--", "codex", "https://control.example/mcp", CODE]
            data = (ASSETS / "install.sh").read_bytes()
            installed = home / ".local/share/shoowcase/bin" / name

        def invoke(expect_success):
            receipt.unlink(missing_ok=True)
            res = subprocess.run(args, input=data, env=env, capture_output=True, timeout=90)
            log = (res.stdout + res.stderr).decode(errors="replace")
            if CODE in log or PROXY_PASSWORD in log:
                raise AssertionError("installer printed a credential")
            if (res.returncode == 0) != expect_success:
                raise AssertionError("unexpected installer status: " + log)
            if expect_success:
                assert Path(receipt.read_text()).samefile(installed)
                assert installed.read_bytes() == shim.read_bytes()
                if not windows:
                    assert installed.stat().st_mode & 0o077 == 0
            else:
                assert not receipt.exists()
            assert not list(installed.parent.glob(".install.*")), "installer left staging files"

        invoke(True)
        ca = root / "company CA.pem"
        ca.write_text("synthetic fixture for the curl argument check")
        env.update(HTTPS_PROXY=PROXY, NO_PROXY="intranet.invalid", SHOOWCASE_CA_FILE=str(ca),
                   FIXTURE_WANT_PROXY=PROXY, FIXTURE_PROXY_PASSWORD=PROXY_PASSWORD)
        invoke(True)  # Both release downloads and connect inherit the selected network settings.
        # Update an older install atomically at the same path.
        installed.write_bytes(b"old local version")
        invoke(True)
        # An unverified download cannot run connect or replace the existing install.
        good_bytes = archive.read_bytes()
        archive.write_bytes(b"corrupted in transit")
        invoke(False)
        assert hashlib.sha256(installed.read_bytes()).digest() == hashlib.sha256(shim.read_bytes()).digest()
        archive.write_bytes(good_bytes)
        sums.write_text(f"{sha}  {asset}\n{sha}  {asset}\n")
        invoke(False)
        assert f"$Version = '{TAG}'" in (ASSETS / "uninstall.ps1").read_text()
        assert f"    version={TAG}\n" in (ASSETS / "uninstall.sh").read_text()
        # The uninstaller must run a verified helper outside the old install
        # path; on Windows the running executable cannot delete itself.
        env.update(TEMP=str(root), TMP=str(root), TMPDIR=str(root))
        if windows:
            env["FIXTURE_SCRIPT"] = str(ASSETS / "uninstall.ps1")
            remove_args = ["powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
                           "& ([scriptblock]::Create([IO.File]::ReadAllText($env:FIXTURE_SCRIPT))); exit $LASTEXITCODE"]
            remove_data = None
        else:
            remove_args = ["sh", "-s"]
            remove_data = (ASSETS / "uninstall.sh").read_bytes()
        for good in (False, True):
            if good:
                sums.write_text(f"{sha}  {asset}\n")
            receipt.unlink(missing_ok=True)
            res = subprocess.run(remove_args, input=remove_data, env=env, capture_output=True, timeout=90)
            msg = (res.stdout + res.stderr).decode(errors="replace")
            assert CODE not in msg and PROXY_PASSWORD not in msg, "uninstaller printed a credential"
            assert (res.returncode == 0) == good, msg
            assert receipt.exists() == good, "uninstaller did not respect checksum gate"
            if good:
                helper = Path(receipt.read_text())
                assert helper != installed and not helper.exists()
                assert not helper.parent.exists(), "uninstaller left temporary files"
        print(f"PASS: {system}/{arch}: install, reuse, update and uninstall bootstrap; bad checksum and ambiguous manifest; stable path, symmetric proxy/CA settings, cleanup and no credentials in output.")


if __name__ == "__main__":
    main()
