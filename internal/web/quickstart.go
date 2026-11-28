package web

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/openhoo/shoowcase/internal/agentclients"
	"github.com/openhoo/shoowcase/internal/sites"
)

//go:embed quickstartassets/install.* quickstartassets/uninstall.*
var installers embed.FS

const (
	quickAnyClient  = "*"
	quickSetupTTL   = 10 * time.Minute
	quickDefaultTTL = "30"
)

type quickPage struct {
	memberPage
	Client, Platform, ClientName, Command, Prompt string
	Clients                                       []agentclients.Client
	Manual, CompanyProxy                          bool
	UninstallUnix, UninstallWindows               string
}

// The browser receives an encrypted setup key, not the persistent agent key.
// Manual setup binds it to the chosen client. A prompt lets the receiving agent
// identify one supported local client. Both have one redemption across replicas.
// No recoverable credential or browser cookie is stored in the database.
func (a *App) setupAEAD() cipher.AEAD {
	k := hmac.New(sha256.New, a.config.Key)
	k.Write([]byte("shoowcase/quickstart/key/v1"))
	block, _ := aes.NewCipher(k.Sum(nil)) // SHA-256 is exactly one AES-256 key.
	aead, _ := cipher.NewGCMWithRandomNonce(block)
	return aead
}

func (a *App) setupAAD(client string) []byte {
	return []byte("shoowcase/quickstart/v1\x00" + a.config.ControlURL + "\x00" + client)
}

func (a *App) setupCode(secret, client string, expiry time.Time) string {
	plain := binary.BigEndian.AppendUint64(nil, uint64(expiry.Unix()))
	plain = append(plain, secret...)
	return base64.RawURLEncoding.EncodeToString(a.setupAEAD().Seal(nil, nil, plain, a.setupAAD(client)))
}

func (a *App) openSetupCode(code, client string) (string, time.Time, bool) {
	if agentclients.Name(client) == "" || len(code) > 512 {
		return "", time.Time{}, false
	}
	buf, err := base64.RawURLEncoding.Strict().DecodeString(code)
	box := a.setupAEAD()
	if err != nil || len(buf) < box.Overhead()+9 {
		return "", time.Time{}, false
	}
	raw, err := box.Open(nil, nil, buf, a.setupAAD(client))
	if err != nil {
		raw, err = box.Open(nil, nil, buf, a.setupAAD(quickAnyClient))
	}
	if err != nil || len(raw) < 9 {
		return "", time.Time{}, false
	}
	expiry := time.Unix(int64(binary.BigEndian.Uint64(raw[:8])), 0)
	secret := string(raw[8:])
	return secret, expiry, expiry.After(time.Now()) && sites.KeyID(secret) != ""
}

func (a *App) quickRedeem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "Use POST.", 405)
		return
	}
	if r.Header.Get("Origin") != "" || r.URL.RawQuery != "" {
		http.Error(w, "Use the terminal command.", 403)
		return
	}
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" {
		http.Error(w, "Expected JSON.", 415)
		return
	}
	var input struct {
		Code   string `json:"code"`
		Client string `json:"client"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
	dec.DisallowUnknownFields()
	if dec.Decode(&input) != nil || dec.Decode(&struct{}{}) != io.EOF {
		http.Error(w, "Invalid setup request.", 400)
		return
	}
	secret, expiry, ok := a.openSetupCode(input.Code, input.Client)
	invalid := func() { http.Error(w, "This setup key is no longer valid. Open Quickstart for a new one.", 401) }
	if !ok {
		invalid()
		return
	}
	// Validate and spend this key atomically with account deletion.
	used, err := a.store.RedeemSetupKey(r.Context(), secret, expiry)
	if err != nil {
		if errorCode(err) == "unauthorized" || errorCode(err) == "forbidden" {
			invalid()
		} else {
			http.Error(w, "Setup unavailable. Open Quickstart to reconnect.", 503)
		}
		return
	}
	if !used {
		invalid()
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"token": secret})
}

func quickCommand(base, client, platform, code string) string {
	// base is the validated canonical origin, client is from the fixed list,
	// and code contains only unpadded base64url characters.
	if platform == "windows" {
		return fmt.Sprintf("& ([scriptblock]::Create((Invoke-RestMethod '%s/quickstart/install.ps1'))) '%s' '%s/mcp' '%s'", base, client, base, code)
	}
	return fmt.Sprintf("curl -fsSL '%s/quickstart/install.sh' | sh -s -- '%s' '%s/mcp' '%s'", base, client, base, code)
}

func quickAgentPrompt(base, code string) string {
	clients := make([]string, 0, len(agentclients.Clients))
	for _, c := range agentclients.Clients {
		clients = append(clients, "- "+c.Name+": "+c.ID)
	}
	return fmt.Sprintf(`Connect this AI agent to Shoowcase on the computer where it runs.

Shoowcase MCP URL: %s/mcp
One-time setup key (valid for 10 minutes): %s

1. Identify the current agent and its local operating system. Choose its client ID:
%s
Use stdio for a custom local profile or another local stdio client. If you cannot access the computer or change this agent's MCP settings, tell me to use manual setup at %s/quickstart?setup=manual. For a cloud or HTTP-only client, direct me to %s/keys instead.

2. Review the official Shoowcase installer. In a local terminal, run just the command for your OS as the current user. Replace CLIENT_ID and SETUP_KEY with the client ID and the key above. Keep the setup key out of your reply, files and logs. The installer verifies the release, saves its own credential privately and configures Shoowcase. If you use stdio, add the Shoowcase server using the command and arguments it prints. Leave other agent settings intact.

macOS or Linux:
%s

Windows PowerShell:
%s

3. Reload the MCP connection if possible, then call list_sites to verify it. Tell me if I need to open a new session or approve the Shoowcase server first. Report whether it connected. Do not create, edit or publish any site as part of setup.`,
		base, code, strings.Join(clients, "\n"), base, base,
		quickCommand(base, "CLIENT_ID", "unix", "SETUP_KEY"),
		quickCommand(base, "CLIENT_ID", "windows", "SETUP_KEY"))
}

func (a *App) quickStart(w http.ResponseWriter, r *http.Request, c consoleContext, m memberPage) {
	p := quickPage{memberPage: m, Clients: agentclients.Clients, Platform: "unix", Manual: r.URL.Query().Get("setup") == "manual"}
	p.UninstallUnix = fmt.Sprintf("script=$(curl -fsSL '%s/quickstart/uninstall.sh') && printf '%%s\\n' \"$script\" | sh", a.config.ControlURL)
	p.UninstallWindows = fmt.Sprintf("& ([scriptblock]::Create((Invoke-RestMethod '%s/quickstart/uninstall.ps1')))", a.config.ControlURL)
	p.Scopes = memberScopes(c.Actor.Role)
	p.Scope, p.Days = defaultKeyScope(p.Scopes), quickDefaultTTL
	if strings.Contains(strings.ToLower(r.UserAgent()), "windows") {
		p.Platform = "windows"
	}
	status := 200
	if r.Method == http.MethodPost {
		// A previously loaded manual form may still be submitted after an upgrade.
		p.Manual = r.PostForm.Get("setup") != "prompt"
		p.ClientName = "Quickstart agent"
		if p.Manual {
			p.Client, p.Platform = r.PostForm.Get("client"), r.PostForm.Get("platform")
			p.CompanyProxy = r.PostForm.Get("company_proxy") == "on"
			p.ClientName = agentclients.Name(p.Client)
			p.Scope, p.Days = r.PostForm.Get("scope"), r.PostForm.Get("days")
			p.NameValue = strings.TrimSpace(r.PostForm.Get("name"))
		}
		days := map[string]int{"7": 7, "30": 30, "90": 90}[p.Days]
		if p.ClientName == "" || (p.Platform != "unix" && p.Platform != "windows") || !slices.Contains(p.Scopes, p.Scope) || days == 0 {
			p.Err = "Choose an agent, a terminal and an access level available to your account."
			status = 400
		} else if p.NameValue != "" && !validMemberLabel(p.NameValue, 80) {
			p.Err = "Choose a connection name of up to 80 characters."
			status = 400
		} else {
			if !a.spendKeyForm(w, r, c) {
				return
			}
			create, name := a.store.CreateKey, p.NameValue
			if name == "" {
				create, name = a.store.CreateKeyWithSuggestedName, p.ClientName
			}
			key, secret, err := create(r.Context(), actor(c.Identity), name, p.Scope, days)
			if err == nil {
				p.NameValue = key.Name
				a.quickReceipt(w, c, p, secret)
				return
			}
			p.NameError = keyNameConflict(err)
			if p.NameError == "" {
				memberError(w, c, err)
				return
			}
			status = 409
		}
	}
	p.Creation = a.signConsole(c, ticket{Kind: "create-key", Nonce: rand.Text(), Expires: time.Now().Add(15 * time.Minute).Unix()})
	consoleView(w, c, status, "Agent quickstart", "quickstart", memberPages.Lookup("quick-start"), p)
}

func (a *App) quickReceipt(w http.ResponseWriter, c consoleContext, p quickPage, secret string) {
	expiry := time.Now().Add(quickSetupTTL)
	if !p.Manual {
		p.Prompt = quickAgentPrompt(a.config.ControlURL, a.setupCode(secret, quickAnyClient, expiry))
		consoleView(w, c, 200, "Your agent setup prompt", "quickstart", memberPages.Lookup("quick-ready"), p)
		return
	}
	command := quickCommand
	if p.CompanyProxy {
		command = quickProxyCommand
	}
	p.Command = command(a.config.ControlURL, p.Client, p.Platform, a.setupCode(secret, p.Client, expiry))
	p.Prompt = "List the sites I can access in Shoowcase."
	if p.Scope == "editor" {
		p.Prompt = "Create a Shoowcase site with a welcome page and give me its private preview."
	}
	if p.Scope == "publisher" {
		p.Prompt = "Create a Shoowcase site with a welcome page. Show me the preview, then ask me before publishing it."
	}
	consoleView(w, c, 200, "Connect "+p.ClientName, "quickstart", memberPages.Lookup("quick-ready"), p)
}

func serveInstaller(w http.ResponseWriter, r *http.Request) {
	if !readMethod(w, r) {
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/quickstart/")
	b, err := installers.ReadFile("quickstartassets/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.Method == http.MethodGet {
		_, _ = w.Write(b)
	}
}
