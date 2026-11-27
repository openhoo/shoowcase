package agentsetup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// Target only the stable editor's default local profile. Portable mode and
// VSCODE_APPDATA use the same precedence as VS Code's getUserDataPath.
func registerVSCode(ctx context.Context, home, binary string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := vsCodePath(home)
	if err != nil {
		return err
	}
	const id = "vscode-local"
	want := entry{Type: "stdio", Command: binary, Args: []string{"mcp-stdio", id}}
	return mergeClientJSONC(path, "servers", "shoowcase", want, func(raw json.RawMessage) bool {
		var fields map[string]json.RawMessage
		if !ours(raw, binary, id) || json.Unmarshal(raw, &fields) != nil {
			return false
		}
		for key := range fields {
			if key != "command" && key != "args" && key != "type" {
				return false
			}
		}
		return true
	})
}

func vsCodePath(home string) (string, error) {
	var dir string
	switch {
	case os.Getenv("VSCODE_PORTABLE") != "":
		dir = filepath.Join(os.Getenv("VSCODE_PORTABLE"), "user-data")
	case os.Getenv("VSCODE_APPDATA") != "":
		dir = filepath.Join(os.Getenv("VSCODE_APPDATA"), "Code")
	default:
		base, err := os.UserConfigDir()
		if err != nil {
			if runtime.GOOS != "windows" || !filepath.IsAbs(home) {
				return "", errors.New("cannot locate the VS Code user configuration directory")
			}
			base = filepath.Join(home, "AppData", "Roaming")
		}
		dir = filepath.Join(base, "Code")
	}
	return filepath.Join(dir, "User", "mcp.json"), nil
}
