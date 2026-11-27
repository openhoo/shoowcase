package agentsetup

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/http/httpproxy"
)

// This is stored only inside the user's protected credential. An empty proxy
// in a saved route means direct for this origin, even in a different GUI
// environment. No settings for unrelated destinations are copied.
type relayNetwork struct {
	Origin   string `json:"origin"`
	ProxyURL string `json:"proxy_url,omitempty"`
	CAFile   string `json:"ca_file,omitempty"`
}

func networkForSetup(ep *url.URL) (*relayNetwork, error) {
	if ep == nil {
		return nil, errors.New("missing Shoowcase control URL")
	}
	if _, err := endpoint(ep.String()); err != nil {
		return nil, err
	}
	cfg := httpproxy.FromEnvironment()
	candidate := cfg.HTTPSProxy
	if ep.Scheme == "http" {
		candidate = cfg.HTTPProxy
	}
	if candidate == "" {
		candidate = firstNetworkEnv("all_proxy", "ALL_PROXY")
	}
	ca := firstNetworkEnv("SHOOWCASE_CA_FILE", "CURL_CA_BUNDLE", "SSL_CERT_FILE")
	if ep.Scheme == "http" && cfg.CGI && candidate != "" {
		return nil, errors.New("refusing an environment HTTP proxy in CGI")
	}
	// Evaluate NO_PROXY with Go's actual host/port/CIDR rules before parsing
	// a possibly irrelevant proxy credential. An invalid proxy that applies
	// to this origin must fail closed: httpproxy otherwise ignores parse errors.
	const probe = "http://proxy.invalid"
	matcher := httpproxy.Config{HTTPProxy: probe, HTTPSProxy: probe, NoProxy: cfg.NoProxy}
	use, err := matcher.ProxyFunc()(ep)
	if err != nil {
		return nil, errors.New("cannot determine the proxy route")
	}
	var proxy string
	if use != nil && candidate != "" {
		u, err := parseRelayProxy(candidate)
		if err != nil {
			return nil, err
		}
		proxy = u.String()
	}
	loopback := strings.EqualFold(ep.Hostname(), "localhost") || net.ParseIP(ep.Hostname()).IsLoopback()
	bypass := use == nil && cfg.NoProxy != "" && !loopback
	if proxy == "" && ca == "" && !bypass {
		return nil, nil
	}
	return &relayNetwork{Origin: ep.Scheme + "://" + ep.Host, ProxyURL: proxy, CAFile: ca}, nil
}

func firstNetworkEnv(names ...string) string {
	for _, name := range names {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}

// Never return a URL parse error. It may contain the entire proxy password.
func parseRelayProxy(raw string) (*url.URL, error) {
	invalid := errors.New("invalid proxy: use an HTTP or HTTPS proxy address, with an optional port and URL-encoded credentials")
	if raw == "" || len(raw) > 4096 || strings.ContainsAny(raw, "?#") || strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
		return nil, invalid
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw // The standard proxy variables also accept host:port.
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		(u.EscapedPath() != "" && u.EscapedPath() != "/") || strings.HasSuffix(u.Host, ":") ||
		(!strings.HasPrefix(u.Host, "[") && strings.Count(u.Host, ":") > 1) {
		return nil, invalid
	}
	if strings.HasPrefix(u.Hostname(), ".") {
		return nil, invalid
	}
	if strings.HasPrefix(u.Host, "[") {
		addr, err := netip.ParseAddr(u.Hostname())
		if err != nil || !addr.Is6() {
			return nil, invalid
		}
	}
	if port := u.Port(); port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return nil, invalid
		}
	}
	if u.User != nil {
		user := u.User.Username()
		password, _ := u.User.Password()
		if strings.Contains(user, ":") || strings.IndexFunc(user+password, unicode.IsControl) >= 0 {
			return nil, invalid
		}
	}
	u.Path, u.RawPath = "", ""
	return u, nil
}

func relayHTTPClient(saved *relayNetwork) (*http.Client, error) {
	client := httpClient() // Uses the existing timeout and never follows redirects.
	if saved == nil {
		return client, nil
	}
	ep, err := endpoint(saved.Origin + "/mcp")
	if err != nil || ep.Scheme+"://"+ep.Host != saved.Origin {
		return nil, errors.New("invalid origin in private network settings; reconnect this agent")
	}
	var proxy *url.URL
	if saved.ProxyURL != "" {
		proxy, err = parseRelayProxy(saved.ProxyURL)
		if err != nil {
			return nil, err
		}
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("cannot initialize the standard HTTP transport")
	}
	tr := base.Clone()
	tr.Proxy = http.ProxyURL(proxy) // A nil proxy pins direct mode, not ProxyFromEnvironment.
	if saved.CAFile != "" {
		roots, err := relayCertPool(saved.CAFile)
		if err != nil {
			return nil, err
		}
		if tr.TLSClientConfig == nil {
			tr.TLSClientConfig = new(tls.Config)
		}
		tr.TLSClientConfig.RootCAs = roots
	}
	client.Transport = &relayTransport{origin: saved.Origin, tr: tr}
	return client, nil
}

func relayCertPool(path string) (*x509.CertPool, error) {
	if len(path) > 4096 || !filepath.IsAbs(path) {
		return nil, errors.New("set an absolute path to your PEM CA bundle")
	}
	const max = 16 << 20
	st, err := os.Stat(path) // Trust bundles are often OS-managed symlinks.
	if err != nil || !st.Mode().IsRegular() || st.Size() > max {
		return nil, errors.New("cannot read the configured PEM CA bundle")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open the configured PEM CA bundle")
	}
	defer f.Close()
	if opened, err := f.Stat(); err != nil || !opened.Mode().IsRegular() || !os.SameFile(st, opened) {
		return nil, errors.New("the configured PEM CA bundle changed during setup")
	}
	data, err := readBounded(f, max)
	if err != nil {
		return nil, errors.New("cannot read the configured PEM CA bundle")
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, errors.New("cannot load the system certificate authorities")
	}
	if !roots.AppendCertsFromPEM(data) {
		return nil, errors.New("the configured PEM CA bundle contains no usable certificates")
	}
	return roots, nil
}

type relayTransport struct {
	origin string
	tr     *http.Transport
}

func (t *relayTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.EqualFold(req.URL.Scheme+"://"+req.URL.Host, t.origin) || req.URL.User != nil {
		return nil, errors.New("the saved network settings apply only to this Shoowcase control origin")
	}
	res, err := t.tr.RoundTrip(req)
	if err != nil {
		if canceled := req.Context().Err(); canceled != nil {
			return nil, canceled
		}
		// An untrusted CONNECT response may reflect Proxy-Authorization or
		// other credentials in its status text. No raw transport errors escape.
		return nil, errors.New("cannot reach the Shoowcase control host; check the proxy and certificate trust settings")
	}
	return res, nil
}

func (t *relayTransport) CloseIdleConnections() { t.tr.CloseIdleConnections() }
