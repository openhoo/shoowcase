// Package agentsetup installs a user-scoped Shoowcase MCP connection.
package agentsetup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openhoo/shoowcase/internal/agentclients"
	"github.com/openhoo/shoowcase/internal/sites"
)

const connectUsage = "usage: shoowcase connect <client> --url https://CONTROL/mcp --setup-code CODE"

func validClient(c string) bool {
	return agentclients.Name(c) != ""
}

func endpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err == nil && u.Hostname() != "" && u.User == nil && u.EscapedPath() == "/mcp" && !strings.ContainsAny(raw, "?#") {
		if u.Scheme == "https" || (u.Scheme == "http" && (net.ParseIP(u.Hostname()).IsLoopback() || u.Hostname() == "localhost")) {
			return u, nil
		}
	}
	return nil, errors.New("expected an HTTPS control URL ending in /mcp (HTTP is allowed only on loopback)")
}

type credential struct {
	URL     string        `json:"url"`
	Token   string        `json:"token"`
	Network *relayNetwork `json:"network,omitempty"`
}

func credentialDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil || !filepath.IsAbs(base) {
		return "", errors.New("cannot locate your user configuration directory")
	}
	return filepath.Join(base, "shoowcase", "agents"), nil
}

func safeStoreDir(dir string) bool {
	return privatePath(filepath.Dir(dir), true) && privatePath(dir, true)
}

func httpClient() *http.Client {
	return &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func redeem(ctx context.Context, cli *http.Client, ep url.URL, name, code string) (string, error) {
	ep.Path = "/quickstart/redeem"
	body, _ := json.Marshal(map[string]string{"code": code, "client": name})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.String(), bytes.NewReader(body))
	if err != nil {
		return "", errors.New("invalid Shoowcase endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := cli.Do(req)
	if err != nil {
		return "", errors.New("could not reach Shoowcase; check connectivity, proxy settings and certificate trust")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", errors.New("Shoowcase did not accept this setup code; create a new command in Agent setup")
	}
	var result struct {
		Token string `json:"token"`
	}
	b, err := readBounded(res.Body, 4096)
	if err != nil || json.Unmarshal(b, &result) != nil || sites.KeyID(result.Token) == "" {
		return "", errors.New("invalid setup response from the control host")
	}
	return result.Token, nil
}

func readBounded(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil || int64(len(b)) > max {
		return nil, errors.New("input exceeds the supported size or could not be read")
	}
	return b, nil
}

func Connect(ctx context.Context, args []string, out io.Writer) error {
	if len(args) < 1 || !validClient(args[0]) {
		return errors.New(connectUsage)
	}
	name := args[0]
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // Flag errors may echo a credential accidentally passed as an argument.
	raw := fs.String("url", "", "")
	code := fs.String("setup-code", "", "")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || len(*code) < 1 || len(*code) > 512 || strings.TrimSpace(*code) != *code {
		return errors.New(connectUsage)
	}
	ep, err := endpoint(*raw)
	if err != nil {
		return err
	}
	network, err := networkForSetup(ep)
	if err != nil {
		return err
	}
	cli, err := relayHTTPClient(network)
	if err != nil {
		return err
	}
	defer cli.CloseIdleConnections()
	dir, err := credentialDir()
	if err != nil {
		return err
	}
	if os.MkdirAll(dir, 0700) != nil || !safeStoreDir(dir) {
		return errors.New("Shoowcase needs a private user credential directory")
	}
	// Serialize clients updating shared agent configuration and credentials.
	lock, err := os.OpenFile(filepath.Join(dir, ".connect-lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return errors.New("cannot open the Shoowcase setup lock")
	}
	defer lock.Close() // The OS releases the lock on exit, including after a killed installer.
	if !privatePath(lock.Name(), false) || lockStore(lock) != nil {
		return errors.New("another Shoowcase setup is running or the credential directory is locked")
	}
	localID := agentclients.LocalID(name)
	slot := localID
	var connectionID string
	if name == "stdio" {
		// Each custom client gets its own credential; adding a second one must
		// never change the access granted to the first.
		var id [16]byte
		rand.Read(id[:])
		connectionID = hex.EncodeToString(id[:])
		slot += "-" + connectionID
	}
	path := filepath.Join(dir, slot+".json")
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) && !privatePath(path, false) {
		return errors.New("existing Shoowcase credentials are not a private regular file")
	}
	// Prove we can write the private store before consuming a one-use code.
	tmp, err := os.CreateTemp(dir, ".new-*")
	if err != nil {
		return errors.New("cannot write the private credential file")
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	binary, err := os.Executable()
	if err != nil || !filepath.IsAbs(binary) {
		return errors.New("cannot locate the installed Shoowcase executable")
	}
	// Registration contains no secret. If a client cannot accept the configuration,
	// the code is still usable. An unsuccessful renewal leaves prior credentials intact.
	if err := register(ctx, localID, binary); err != nil {
		return err
	}
	token, err := redeem(ctx, cli, *ep, name, *code)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(credential{URL: ep.String(), Token: token, Network: network})
	b, err = seal(b)
	if err != nil {
		return errors.New("cannot protect credentials; revoke the new key in Shoowcase")
	}
	if _, err = tmp.Write(b); err != nil || tmp.Sync() != nil || tmp.Close() != nil || os.Rename(tmp.Name(), path) != nil {
		return errors.New("could not save the new credential; revoke the new key in Shoowcase")
	}
	if name == "stdio" {
		if _, err = fmt.Fprintln(out, "Add a local stdio server named shoowcase in your MCP client. Use the command and arguments below:"); err != nil {
			return err
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(entry{Command: binary, Args: []string{"mcp-stdio", "stdio", connectionID}})
	}
	_, err = fmt.Fprintf(out, "Shoowcase connected for %s. Restart the agent or open a new session.\n", agentclients.Name(name))
	return err
}

func Stdio(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	slot, ok := stdioSlot(args)
	if !ok {
		return errors.New("usage: shoowcase mcp-stdio <client> (a custom stdio connection also requires its connection ID)")
	}
	dir, err := credentialDir()
	if err != nil {
		return err
	}
	p := filepath.Join(dir, slot+".json")
	if !safeStoreDir(dir) || !privatePath(p, false) {
		return errors.New("no private credential available; connect this agent in Shoowcase")
	}
	f, err := os.Open(p)
	if err != nil {
		return errors.New("cannot open the Shoowcase credential")
	}
	defer f.Close()
	b, err := readBounded(f, 64<<10)
	if err == nil {
		b, err = unseal(b)
	}
	var c credential
	if err != nil || json.Unmarshal(b, &c) != nil || sites.KeyID(c.Token) == "" {
		return errors.New("invalid private Shoowcase credential; reconnect this agent")
	}
	ep, err := endpoint(c.URL)
	if err != nil {
		return err
	}
	if c.Network == nil {
		// Credentials from older releases follow the current environment.
		c.Network, err = networkForSetup(ep)
		if err != nil {
			return err
		}
	}
	cli, err := relayHTTPClient(c.Network)
	if err != nil {
		return err
	}
	defer cli.CloseIdleConnections()
	return bridge(ctx, cli, c, in, out)
}

// stdioSlot accepts only names generated during registration. Never treat
// client-supplied arguments as arbitrary file paths.
func stdioSlot(args []string) (string, bool) {
	if len(args) == 1 && validClient(args[0]) && args[0] != "stdio" {
		return agentclients.LocalID(args[0]), true
	}
	if len(args) == 2 && args[0] == "stdio" && len(args[1]) == 32 {
		if _, err := hex.DecodeString(args[1]); err == nil && args[1] == strings.ToLower(args[1]) {
			return "stdio-" + args[1], true
		}
	}
	return "", false
}
