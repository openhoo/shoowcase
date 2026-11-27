package agentsetup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"
)

// Never echo child output: it may include settings for other MCP servers.
func runClient(ctx context.Context, dir, exe string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = dir
	b, err := cmd.Output()
	if err != nil {
		return nil, errors.New("client command failed; check its installation and user configuration")
	}
	return b, nil
}

type entry struct {
	Type    string   `json:"type,omitempty"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Tools   []string `json:"tools,omitempty"`
}

var conflict = errors.New("a different MCP server named shoowcase is already configured; rename or remove that entry in your client first")

func ours(raw json.RawMessage, binary, name string) bool {
	var e entry
	return json.Unmarshal(raw, &e) == nil && e.Command == binary && slices.Equal(e.Args, []string{"mcp-stdio", name}) && (e.Type == "" || e.Type == "stdio" || (name == "copilot" && e.Type == "local"))
}

func register(ctx context.Context, name, binary string) error {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return errors.New("cannot find your home directory")
	}
	switch name {
	case "stdio":
		// A generic client has no standard configuration file. Connect prints
		// the local command and arguments only after saving the credential.
		return nil
	case "vscode-local":
		return registerVSCode(ctx, home, binary)
	case "gemini", "opencode":
		return registerOther(ctx, name, binary)
	}
	if name == "codex" {
		b, err := runClient(ctx, home, "codex", "mcp", "list", "--json")
		if err != nil {
			return fmt.Errorf("cannot read Codex MCP settings: %w", err)
		}
		var servers []struct {
			Name      string          `json:"name"`
			Transport json.RawMessage `json:"transport"`
			Enabled   bool            `json:"enabled"`
		}
		if json.Unmarshal(b, &servers) != nil || servers == nil {
			return errors.New("cannot read Codex MCP settings: expected a JSON list")
		}
		for _, s := range servers {
			if s.Name == "shoowcase" {
				if !ours(s.Transport, binary, name) {
					return conflict
				}
				if !s.Enabled {
					return errors.New("the Shoowcase MCP server is disabled in Codex; enable it first")
				}
				return nil
			}
		}
		_, err = runClient(ctx, home, "codex", "mcp", "add", "shoowcase", "--", binary, "mcp-stdio", name)
		return err
	}
	want := entry{Command: binary, Args: []string{"mcp-stdio", name}}
	if name == "claude" {
		if _, err := exec.LookPath("claude"); err != nil {
			return errors.New("Claude Code must be installed and available as claude")
		}
	}
	if name == "copilot" {
		want.Type = "local"
		want.Tools = []string{"*"}
	}
	path := standardClientPath(home, name)
	if !filepath.IsAbs(path) {
		return errors.New("the client configuration directory must be an absolute user path")
	}
	root, servers, original, err := loadClientFile(path)
	if err != nil {
		return err
	}
	if existing, ok := servers["shoowcase"]; ok {
		if !ours(existing, binary, name) {
			return conflict
		}
		return nil
	}
	if name == "claude" {
		_, err := runClient(ctx, home, "claude", "mcp", "add", "--scope", "user", "--transport", "stdio", "shoowcase", "--", binary, "mcp-stdio", name)
		return err
	}
	servers["shoowcase"], _ = json.Marshal(want)
	root["mcpServers"], _ = json.Marshal(servers)
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return errors.New("cannot prepare client MCP settings")
	}
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return errors.New("cannot create the client configuration directory")
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".shoowcase-mcp-*")
	if err != nil {
		return errors.New("cannot create temporary client settings")
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err = tmp.Write(append(data, '\n')); err != nil || tmp.Sync() != nil || tmp.Close() != nil {
		return errors.New("cannot save client settings")
	}
	_, _, current, err := loadClientFile(path)
	if err != nil || !bytes.Equal(original, current) {
		return errors.New("client configuration changed during setup; rerun the command")
	}
	if os.Rename(tmp.Name(), path) != nil {
		return errors.New("cannot install client MCP settings")
	}
	return nil
}

func standardClientPath(home, name string) string {
	switch name {
	case "claude":
		root := os.Getenv("CLAUDE_CONFIG_DIR")
		if root == "" {
			root = home
		}
		return filepath.Join(root, ".claude.json")
	case "copilot":
		root := os.Getenv("COPILOT_HOME")
		if root == "" {
			root = filepath.Join(home, ".copilot")
		}
		return filepath.Join(root, "mcp-config.json")
	case "cursor":
		return filepath.Join(home, ".cursor", "mcp.json")
	}
	return ""
}

func loadClientFile(path string) (root, servers map[string]json.RawMessage, original []byte, err error) {
	root, servers = make(map[string]json.RawMessage), make(map[string]json.RawMessage)
	st, e := os.Lstat(path)
	if errors.Is(e, os.ErrNotExist) {
		return
	}
	if e != nil || !st.Mode().IsRegular() || st.Size() > 16<<20 {
		err = errors.New("cannot safely read the client configuration file")
		return
	}
	original, e = os.ReadFile(path)
	if e != nil || json.Unmarshal(original, &root) != nil || root == nil {
		err = errors.New("invalid client configuration; repair the existing JSON file first")
		return
	}
	if s, ok := root["mcpServers"]; ok && (json.Unmarshal(s, &servers) != nil || servers == nil) {
		err = errors.New("invalid mcpServers object in client settings")
	}
	return
}
