package agentsetup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func clearRelayEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"https_proxy", "HTTPS_PROXY", "http_proxy", "HTTP_PROXY", "all_proxy", "ALL_PROXY", "no_proxy", "NO_PROXY", "REQUEST_METHOD", "SHOOWCASE_CA_FILE", "CURL_CA_BUNDLE", "SSL_CERT_FILE"} {
		t.Setenv(k, "")
	}
}

func relayTestURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestRelaySelectsOnlyNetworkSettingsForItsOrigin(t *testing.T) {
	for _, tc := range []struct {
		name, target, https, http, all, no, want string
	}{
		{"ambient default", "https://control.example/mcp", "", "", "", "", "ambient"},
		{"HTTP-only is irrelevant", "https://control.example/mcp", "", "http://other.invalid", "", "", "ambient"},
		{"HTTPS chooses HTTP CONNECT", "https://control.example/mcp", "edge:8080", "http://other.invalid", "http://third.invalid", "", "http://edge:8080"},
		{"ALL_PROXY fallback", "https://control.example/mcp", "", "", "https://edge:8443", "", "https://edge:8443"},
		{"suffix exclusion", "https://control.example/mcp", "http://secret@irrelevant", "", "", ".example", "direct"},
		{"only relevant NO_PROXY", "https://control.example/mcp", "", "", "", "other.test", "ambient"},
		{"NO_PROXY alone pins direct", "https://control.example/mcp", "", "", "", "control.example", "direct"},
		{"subnet exclusion", "https://192.0.2.5/mcp", "http://edge", "", "", "192.0.2.0/24", "direct"},
		{"port exclusion", "https://control.example:8443/mcp", "http://edge", "", "", "control.example:8443", "direct"},
		{"port does not match", "https://control.example:7443/mcp", "http://edge", "", "", "control.example:8443", "http://edge"},
		{"invalid but excluded proxy", "https://control.example/mcp", "http://bad host", "", "", "*", "direct"},
		{"HTTP loopback stays local", "http://127.0.0.1:4321/mcp", "", "http://irrelevant", "", "", "ambient"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearRelayEnv(t)
			t.Setenv("HTTPS_PROXY", tc.https)
			t.Setenv("HTTP_PROXY", tc.http)
			t.Setenv("ALL_PROXY", tc.all)
			t.Setenv("NO_PROXY", tc.no)
			u := relayTestURL(t, tc.target)
			n, err := networkForSetup(u)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "ambient" {
				if n != nil {
					t.Fatal("captured unrelated settings")
				}
				return
			}
			if n == nil || n.Origin != u.Scheme+"://"+u.Host || n.CAFile != "" || tc.want == "direct" && n.ProxyURL != "" || tc.want != "direct" && n.ProxyURL != tc.want {
				t.Fatal("incorrect endpoint route")
			}
			if tc.want == "direct" {
				t.Setenv("HTTPS_PROXY", "http://other.invalid")
				t.Setenv("NO_PROXY", "")
				c, err := relayHTTPClient(n)
				if err != nil {
					t.Fatal(err)
				}
				req, _ := http.NewRequest("POST", tc.target, nil)
				var proxy *url.URL
				proxy, err = c.Transport.(*relayTransport).tr.Proxy(req)
				if err != nil || proxy != nil {
					t.Fatal("GUI environment overrode saved NO_PROXY")
				}
			}
		})
	}
}

func TestRelayPrecedenceAndValidation(t *testing.T) {
	clearRelayEnv(t)
	u := relayTestURL(t, "https://control.example/mcp")
	t.Setenv("HTTPS_PROXY", "http://upper")
	t.Setenv("https_proxy", "https://lower:8443")
	t.Setenv("SSL_CERT_FILE", filepath.Join(t.TempDir(), "ssl"))
	t.Setenv("CURL_CA_BUNDLE", filepath.Join(t.TempDir(), "curl"))
	ca := filepath.Join(t.TempDir(), "company.pem")
	t.Setenv("SHOOWCASE_CA_FILE", ca)
	n, err := networkForSetup(u)
	if err != nil || n.ProxyURL != "https://lower:8443" || n.CAFile != ca {
		t.Fatalf("incorrect environment priority: %v", err)
	}
	if runtime.GOOS != "windows" {
		t.Setenv("https_proxy", "")
		n, err = networkForSetup(u)
		if err != nil || n.ProxyURL != "http://upper" {
			t.Fatalf("expected uppercase fallback: %v", err)
		}
	}
	for _, s := range []string{"socks5://edge", "http://", "https://edge:70000", "http://edge:", "./edge", "http://edge/path", "http://invalid user:p@ss@edge", "http://edge?test", "http://edge#fragment", "http://user%0A:pass@edge"} {
		t.Setenv("https_proxy", s)
		if _, err := networkForSetup(u); err == nil || strings.Contains(err.Error(), s) {
			t.Errorf("invalid proxy accepted or disclosed in error")
		}
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, "secret-ca-filename.pem")
	if err := os.WriteFile(bad, []byte("sensitive garbage"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"relative", bad, dir, filepath.Join(dir, "missing")} {
		if _, err := relayHTTPClient(&relayNetwork{Origin: "https://control.example", CAFile: path}); err == nil || strings.Contains(err.Error(), "secret-ca") || strings.Contains(err.Error(), "sensitive") {
			t.Fatal("invalid CA accepted or disclosed in error")
		}
	}
	t.Setenv("REQUEST_METHOD", "GET")
	t.Setenv("HTTP_PROXY", "http://untrusted")
	if _, err := networkForSetup(relayTestURL(t, "http://localhost:1234/mcp")); err == nil {
		t.Fatal("accepted a CGI HTTP proxy")
	}
}

// All fixture listeners bind to loopback. The proxy permits exactly one
// CONNECT authority and forwards only to the test TLS listener, never DNS.
func TestRelayAuthenticatedConnectAndCorporateCA(t *testing.T) {
	for _, useTLSProxy := range []bool{false, true} {
		t.Run(fmt.Sprintf("proxyTLS=%t", useTLSProxy), func(t *testing.T) {
			clearRelayEnv(t)
			const authority = "relay.example.com:8443"
			const password = "fixture:p@ss"
			const proxyUser = "fixture-user"
			const bearer = "Bearer dummy-origin-key"
			var origins, accepted, denied atomic.Int32
			origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("Authorization") != bearer || r.Host != authority {
					t.Error("origin received the wrong credentials or host")
				}
				origins.Add(1)
				if r.URL.Path == "/redirect" {
					http.Redirect(w, r, "https://outside.invalid", http.StatusFound)
					return
				}
				_, _ = io.WriteString(w, "ok")
			}))
			defer origin.Close()
			expected := "Basic " + base64.StdEncoding.EncodeToString([]byte(proxyUser+":"+password))
			var refuse atomic.Bool
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "CONNECT" || r.Host != authority || r.Header.Get("Authorization") != "" {
					t.Error("unexpected proxy request")
					http.Error(w, "forbidden", http.StatusForbidden)
					return
				}
				if r.Header.Get("Proxy-Authorization") != expected || refuse.Load() {
					denied.Add(1)
					// Deliberately reflect sensitive text in a CONNECT status line.
					c, b, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					defer c.Close()
					_, _ = fmt.Fprintf(b, "HTTP/1.1 407 %s %s\r\nContent-Length: 0\r\n\r\n", password, expected)
					_ = b.Flush()
					return
				}
				up, err := net.Dial("tcp", origin.Listener.Addr().String())
				if err != nil {
					t.Error(err)
					return
				}
				defer up.Close()
				down, b, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				defer down.Close()
				accepted.Add(1)
				_, _ = b.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
				_ = b.Flush()
				go func() { _, _ = io.Copy(up, b); _ = up.Close() }()
				_, _ = io.Copy(down, up)
			})
			var proxy *httptest.Server
			if useTLSProxy {
				proxy = httptest.NewTLSServer(handler)
			} else {
				proxy = httptest.NewServer(handler)
			}
			defer proxy.Close()
			p := relayTestURL(t, proxy.URL)
			p.User = url.UserPassword(proxyUser, password)
			t.Setenv("HTTPS_PROXY", p.String())
			ca := filepath.Join(t.TempDir(), "corporate.pem")
			if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: origin.Certificate().Raw}), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("SHOOWCASE_CA_FILE", ca)
			u := relayTestURL(t, "https://"+authority+"/mcp")
			settings, err := networkForSetup(u)
			if err != nil || settings == nil {
				t.Fatalf("no proxy settings: %v", err)
			}
			data, err := json.Marshal(settings)
			if err != nil {
				t.Fatal(err)
			}
			clearRelayEnv(t) // Simulate a GUI process launched without the shell.
			var saved relayNetwork
			if err := json.Unmarshal(data, &saved); err != nil {
				t.Fatal(err)
			}
			client, err := relayHTTPClient(&saved)
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()
			var directAttempts atomic.Int32
			limitDial := func(cli *http.Client) {
				tr := cli.Transport.(*relayTransport).tr
				dial := tr.DialContext
				tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
					if addr != proxy.Listener.Addr().String() {
						directAttempts.Add(1)
						return nil, errors.New("test only permits dialing the local proxy")
					}
					return dial(ctx, network, addr)
				}
			}
			limitDial(client)
			request := func(cli *http.Client, path string) (*http.Response, error) {
				req, _ := http.NewRequestWithContext(t.Context(), "GET", "https://"+authority+path, nil)
				req.Header.Set("Authorization", bearer)
				return cli.Do(req)
			}
			res, err := request(client, "/mcp")
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil || string(body) != "ok" || accepted.Load() != 1 || origins.Load() != 1 {
				t.Fatal("TLS origin was not reached through authenticated CONNECT")
			}
			res, err = request(client, "/redirect")
			if err != nil || res.StatusCode != http.StatusFound {
				t.Fatalf("redirect followed or failed: %v", err)
			}
			res.Body.Close()
			if res, err := client.Get("https://outside.invalid"); err == nil {
				res.Body.Close()
				t.Fatal("transport allowed a different origin")
			}
			// A live TLS tunnel has already passed CONNECT authentication. Use a
			// new transport here so this case always tests a fresh negotiation,
			// independent of when the previous response returns to the idle pool.
			refuse.Store(true)
			rejected, err := relayHTTPClient(&saved)
			if err != nil {
				t.Fatal(err)
			}
			defer rejected.CloseIdleConnections()
			limitDial(rejected)
			res, err = request(rejected, "/mcp")
			if err == nil {
				res.Body.Close()
				t.Fatal("fresh relay transport forwarded a refused CONNECT")
			}
			if strings.Contains(err.Error(), password) || strings.Contains(err.Error(), expected) || strings.Contains(err.Error(), proxyUser) {
				t.Fatal("refused CONNECT exposed a proxy credential")
			}
			if denied.Load() != 1 || origins.Load() != 2 {
				t.Fatal("a rejected CONNECT was retried or forwarded")
			}
			refuse.Store(false)
			saved.CAFile = ""
			untrusted, err := relayHTTPClient(&saved)
			if err != nil {
				t.Fatal(err)
			}
			defer untrusted.CloseIdleConnections()
			limitDial(untrusted)
			if res, err := request(untrusted, "/mcp"); err == nil {
				res.Body.Close()
				t.Fatal("an untrusted TLS certificate was accepted without the enterprise bundle")
			}
			if directAttempts.Load() != 0 {
				t.Fatal("proxy failure triggered a direct connection attempt")
			}
			if err := os.Remove(ca); err != nil {
				t.Fatal(err)
			}
			if _, err := relayHTTPClient(settings); err == nil {
				t.Fatal("silently ignored an inaccessible configured bundle")
			}
		})
	}
}
