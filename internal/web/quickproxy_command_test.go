package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/pem"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestQuickProxyBootstrap(t *testing.T) {
	const setupCode = "synthetic-setup-code"
	const password = "fixture-proxy-password"
	platform, shell, curl := "unix", "sh", "curl"
	if runtime.GOOS == "windows" {
		platform, shell, curl = "windows", "powershell.exe", "curl.exe"
	}
	if _, err := exec.LookPath(curl); err != nil {
		t.Fatal("bootstrap requires native curl")
	}
	var requests, tunnels atomic.Int32
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Proxy-Authorization") != "" {
			t.Error("origin received proxy credentials")
		}
		switch r.URL.Path {
		case "/quickstart/install.sh":
			io.WriteString(w, `[ "$1" = codex ] && [ "$3" = synthetic-setup-code ] && [ "$https_proxy" = "$HTTPS_PROXY" ] && [ -n "$SHOOWCASE_CA_FILE" ] && echo INSTALLER-RAN`)
		case "/quickstart/install.ps1":
			io.WriteString(w, `param($Client,$Endpoint,$Code)
if ($Client -ne 'codex' -or $Code -ne 'synthetic-setup-code' -or !$env:HTTPS_PROXY -or !$env:SHOOWCASE_CA_FILE) { throw 'incorrect invocation' }
Write-Output 'INSTALLER-RAN'`)
		default:
			http.NotFound(w, r)
		}
	}))
	origin.Config.ErrorLog = log.New(io.Discard, "", 0) // Expected TLS failure below.
	origin.StartTLS()
	defer origin.Close()
	ca := filepath.Join(t.TempDir(), "company test CA.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: origin.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	address := origin.Listener.Addr().String()
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte("fixture:"+password))
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tunnels.Add(1)
		if r.Method != "CONNECT" || r.Host != address || r.Header.Get("Proxy-Authorization") != auth {
			http.Error(w, "proxy authentication failed", http.StatusProxyAuthRequired)
			return
		}
		up, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			t.Error(err)
			return
		}
		defer up.Close()
		down, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer down.Close()
		io.WriteString(down, "HTTP/1.1 200 Connection Established\r\n\r\n")
		done := make(chan struct{})
		go func() { _, _ = io.Copy(up, down); close(done) }()
		_, _ = io.Copy(down, up)
		down.Close()
		<-done
	}))
	defer proxy.Close()
	route := strings.Replace(proxy.URL, "://", "://fixture:"+password+"@", 1)
	all := "ALL_PROXY"
	if platform != "windows" {
		all = "all_proxy"
	}
	for _, tc := range []struct {
		name, variable, input, bypass, bundle string
		success, tunneled                     bool
	}{
		{"proxy from environment", "HTTPS_PROXY", route, "unused.invalid", ca, true, true},
		{"fallback proxy", all, route, "unused.invalid", ca, true, true},
		{"bypass selected origin", all, route, "127.0.0.1", ca, true, false},
		{"untrusted certificate", "HTTPS_PROXY", route, "", "", false, true},
		{"relative proxy", "HTTPS_PROXY", "fixture:" + password + "@relative:80", "", ca, false, false},
		{"invalid port", "HTTPS_PROXY", "https://fixture:" + password + "@company.invalid:99999/", "", ca, false, false},
		{"relative CA", "HTTPS_PROXY", route, "", "relative.pem", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests.Store(0)
			tunnels.Store(0)
			command := quickProxyCommand(origin.URL, "codex", platform, setupCode)
			if strings.Contains(command, password) {
				t.Fatal("copied command contains proxy credentials")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			var cmd *exec.Cmd
			if platform == "windows" {
				cmd = exec.CommandContext(ctx, shell, "-NoProfile", "-NonInteractive", "-Command",
					"$saved = $env:HTTPS_PROXY\n"+command+"\nif ($saved -cne $env:HTTPS_PROXY) { exit 29 }; exit $LASTEXITCODE")
			} else {
				cmd = exec.CommandContext(ctx, shell, "-c", command)
			}
			for _, e := range os.Environ() {
				key, _, _ := strings.Cut(e, "=")
				switch strings.ToUpper(key) {
				case "HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "NO_PROXY", "SHOOWCASE_CA_FILE", "CURL_CA_BUNDLE", "SSL_CERT_FILE", "SSL_CERT_DIR":
				default:
					cmd.Env = append(cmd.Env, e)
				}
			}
			cmd.Env = append(cmd.Env, tc.variable+"="+tc.input, "NO_PROXY="+tc.bypass, "SHOOWCASE_CA_FILE="+tc.bundle)
			out, err := cmd.CombinedOutput()
			if bytes.Contains(out, []byte(password)) || bytes.Contains(out, []byte(setupCode)) {
				t.Fatal("bootstrap leaked a secret")
			}
			if (err == nil) != tc.success || bytes.Contains(out, []byte("INSTALLER-RAN")) != tc.success || (requests.Load() != 0) != tc.success || (tunnels.Load() != 0) != tc.tunneled {
				t.Fatalf("installer or routing mismatch: err=%v requests=%d tunnels=%d output=%s", err, requests.Load(), tunnels.Load(), out)
			}
		})
	}
}

// A PTY is needed to distinguish hidden entry from an ordinary read on stdin,
// and to check the terminal flags after a real interactive Ctrl-C.
func TestQuickProxyHiddenPrompt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX terminal echo; Windows uses Read-Host -AsSecureString")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required for the PTY test")
	}
	const ptyTest = `
import fcntl, os, pty, select, subprocess, sys, termios, time
command = sys.stdin.read()
for action in ("invalid", "interrupt"):
    master, slave = pty.openpty()
    original = termios.tcgetattr(slave)
    def terminal():
        os.setsid()
        fcntl.ioctl(0, termios.TIOCSCTTY, 0)
        os.tcsetpgrp(0, os.getpgrp())
    env = {k:v for k,v in os.environ.items() if k.lower() not in {"https_proxy","all_proxy"}}
    proc = subprocess.Popen(["sh","-c",command], stdin=slave, stdout=slave, stderr=slave, env=env, preexec_fn=terminal)
    seen = bytearray()
    try:
        deadline = time.monotonic() + 5
        while b"input hidden): " not in seen:
            if time.monotonic() >= deadline: raise AssertionError("no hidden prompt")
            if select.select([master],[],[],0.1)[0]: seen.extend(os.read(master,8192))
        assert not termios.tcgetattr(slave)[3] & termios.ECHO, "proxy entry was visible"
        secret = b"fixture-password-must-never-be-echoed"
        os.write(master, secret + (b"\n" if action == "invalid" else b"\x03"))
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            if select.select([master],[],[],0.05)[0]:
                chunk = os.read(master,8192)
                seen.extend(chunk)
            if termios.tcgetattr(slave) == original and (action == "interrupt" or b"Invalid proxy" in seen): break
        assert secret not in seen, "proxy password appeared in terminal output"
        assert termios.tcgetattr(slave) == original, "terminal state was not restored"
        if action == "invalid": assert b"Invalid proxy" in seen, "missing validation failure"
    finally:
        os.close(slave); os.close(master)
        try: result = proc.wait(timeout=2)
        except subprocess.TimeoutExpired: proc.kill(); proc.wait(); raise
    assert result != 0, "invalid or interrupted setup succeeded"
`
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "-c", ptyTest)
	cmd.Stdin = strings.NewReader(quickProxyCommand("https://control.invalid", "codex", "unix", "synthetic"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hidden proxy prompt or interrupt: %v: %s", err, out)
	}
}
