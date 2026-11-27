package agentsetup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/tailscale/hujson"
)

// registerOther stores exactly the stdio registration written by Gemini CLI
// and OpenCode 1.x. A client may be installed after the connector; neither
// the registration nor its conflict checks need to launch an unrelated MCP server.
func registerOther(ctx context.Context, name, binary string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return errors.New("cannot find your home directory")
	}
	switch name {
	case "gemini":
		path := geminiPath(home)
		doc, err := readClientDocument(path)
		if err != nil {
			return err
		}
		// Gemini strips comments, then calls JSON.parse; unlike OpenCode it
		// rejects trailing commas in any object or array of its settings.
		for v := range doc.root.All() {
			switch c := v.Value.(type) {
			case *hujson.Array:
				if n := len(c.Elements); n > 0 && c.Elements[n-1].AfterExtra != nil {
					return errors.New("Gemini CLI settings contain an unsupported trailing comma; repair them before setup")
				}
			case *hujson.Object:
				if n := len(c.Members); n > 0 && c.Members[n-1].Value.AfterExtra != nil {
					return errors.New("Gemini CLI settings contain an unsupported trailing comma; repair them before setup")
				}
			}
		}
		type geminiServer struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}
		want := geminiServer{Command: binary, Args: []string{"mcp-stdio", name}}
		return mergeClientJSONC(path, "mcpServers", "shoowcase", want, func(raw json.RawMessage) bool {
			var got geminiServer
			return decodeManagedServer(raw, &got) && got.Command == want.Command && slices.Equal(got.Args, want.Args)
		})
	case "opencode":
		return registerOpenCode(home, binary)
	default:
		return errors.New("unsupported agent for this registration")
	}
}

func geminiPath(home string) string {
	base := os.Getenv("GEMINI_CLI_HOME")
	if base == "" {
		base = home
	}
	return filepath.Join(base, ".gemini", "settings.json")
}

var openCodeFiles = []string{"config.json", "opencode.json", "opencode.jsonc"}

func openCodeDir(home string) string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "opencode")
}

type openCodeServer struct {
	Type    string   `json:"type"`
	Command []string `json:"command"`
	Enabled *bool    `json:"enabled,omitempty"`
}

func registerOpenCode(home, binary string) error {
	// Config overrides can be a project file, raw JSON, or a directory shared
	// with other people. Do not consume a code after modifying an inactive file.
	for _, key := range []string{"OPENCODE_CONFIG", "OPENCODE_CONFIG_CONTENT", "OPENCODE_CONFIG_DIR"} {
		if os.Getenv(key) != "" {
			return errors.New("OpenCode has a configuration override; use its default global profile for automatic setup or use manual MCP setup")
		}
	}
	dir := openCodeDir(home)
	if !filepath.IsAbs(dir) {
		return errors.New("XDG_CONFIG_HOME must be absolute for OpenCode user configuration")
	}
	// OpenCode 1.x merges all three in order. Check every layer because
	// adding our entry in a higher layer could shadow an unrelated server.
	target := filepath.Join(dir, "opencode.json")
	want := openCodeServer{Type: "local", Command: []string{binary, "mcp-stdio", "opencode"}}
	matches := func(raw json.RawMessage) bool {
		var got openCodeServer
		return decodeManagedServer(raw, &got) && got.Type == "local" && slices.Equal(got.Command, want.Command) && (got.Enabled == nil || *got.Enabled)
	}
	found := false
	for _, file := range openCodeFiles {
		path := filepath.Join(dir, file)
		raw, ok, err := readClientJSONCEntry(path, "mcp", "shoowcase")
		if err != nil {
			return err
		}
		if ok && !matches(raw) {
			return conflict
		}
		found = found || ok
		if file == "opencode.jsonc" {
			if _, err = os.Lstat(path); err == nil {
				target = path
			}
		}
	}
	if found {
		return nil
	}
	return mergeClientJSONC(target, "mcp", "shoowcase", want, matches)
}

// Additional fields (environment, cwd, disabled, tool filters) may change
// which executable runs or which credential it loads. Leave such entries alone.
func decodeManagedServer(raw json.RawMessage, dst any) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(dst) == nil
}
