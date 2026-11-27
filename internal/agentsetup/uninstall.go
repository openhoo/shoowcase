package agentsetup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/tailscale/hujson"
)

var automaticClients = []string{"claude", "codex", "copilot", "cursor", "gemini", "opencode", "vscode-local"}

// Uninstall removes the current user's Quickstart installation. It is run
// from a separately verified temporary executable so it also works on Windows
// and can clean up installations that predate this command.
func Uninstall(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 0 {
		return errors.New("usage: shoowcase uninstall")
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return errors.New("cannot locate your home directory")
	}
	base, root, file := filepath.Join(home, ".local", "share"), "shoowcase", "shoowcase"
	if runtime.GOOS == "windows" {
		base, root, file = os.Getenv("LOCALAPPDATA"), "Shoowcase", "shoowcase.exe"
	}
	if !filepath.IsAbs(base) {
		return errors.New("cannot locate your local application data directory")
	}
	install := filepath.Join(base, root)
	binary := filepath.Join(install, "bin", file)
	if runtime.GOOS != "windows" {
		if err := regularIfPresent(filepath.Join(home, ".local"), true); err != nil {
			return err
		}
	}
	for _, path := range []string{base, install, filepath.Dir(binary)} {
		if err := regularIfPresent(path, true); err != nil {
			return err
		}
	}
	if err := regularIfPresent(binary, false); err != nil {
		return err
	}
	dir, err := credentialDir()
	if err != nil {
		return err
	}
	for _, path := range []string{filepath.Dir(dir), dir} {
		if err := regularIfPresent(path, true); err != nil {
			return err
		}
	}

	var slots []string
	if _, err := os.Lstat(dir); err == nil {
		if !safeStoreDir(dir) {
			return errors.New("Shoowcase credentials are not in a private user directory")
		}
		lockPath := filepath.Join(dir, ".connect-lock")
		if err := regularIfPresent(lockPath, false); err != nil {
			return err
		}
		lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return errors.New("cannot open the Shoowcase setup lock")
		}
		defer lock.Close()
		if !privatePath(lockPath, false) || lockStore(lock) != nil {
			return errors.New("another Shoowcase setup or uninstall is running; retry when it finishes")
		}
		files, err := os.ReadDir(dir)
		if err != nil {
			return errors.New("cannot read the Shoowcase credential directory")
		}
		for _, f := range files {
			if !managedCredentialName(f.Name()) {
				continue
			}
			path := filepath.Join(dir, f.Name())
			if !privatePath(path, false) {
				return errors.New("refusing to remove a credential that is not a private regular file")
			}
			slots = append(slots, path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect the Shoowcase credential directory")
	}

	for _, name := range automaticClients {
		if err := removeRegistration(ctx, home, binary, name, out); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	for _, path := range slots {
		if err := removeFile(path); err != nil {
			return errors.New("could not remove a local Shoowcase credential")
		}
	}
	if err := removeFile(binary); err != nil {
		return errors.New("could not remove the Shoowcase executable; close any agent still running it and retry")
	}
	// Remove only empty install directories. Keep the credential lock inode
	// for concurrent setup attempts, as well as anything we do not own.
	_ = os.Remove(filepath.Dir(binary))
	_ = os.Remove(install)
	fmt.Fprintln(out, "Shoowcase Quickstart has been removed for this user.")
	fmt.Fprintln(out, "Remove any manually configured local Shoowcase MCP entries in your agents.")
	fmt.Fprintln(out, "To revoke server access, sign in to Shoowcase and revoke the corresponding Agent keys.")
	return nil
}

func managedCredentialName(name string) bool {
	// Connect can be interrupted after writing a credential but before rename.
	if temp, ok := strings.CutPrefix(name, ".new-"); ok {
		return temp != "" && strings.Trim(temp, "0123456789") == ""
	}
	slot, ok := strings.CutSuffix(name, ".json")
	if !ok {
		return false
	}
	if id, ok := strings.CutPrefix(slot, "stdio-"); ok {
		_, valid := stdioSlot([]string{"stdio", id})
		return valid
	}
	return slices.Contains(automaticClients, slot)
}

func regularIfPresent(path string, dir bool) error {
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || dir && !st.IsDir() || !dir && !st.Mode().IsRegular() {
		return errors.New("refusing to use a linked or unexpected Shoowcase path")
	}
	return nil
}

func removeFile(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func removeRegistration(ctx context.Context, home, binary, name string, out io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	remove := func(path, collection string, matches func(json.RawMessage) bool) error {
		doc, err := readClientDocument(path)
		if err != nil {
			return err
		}
		obj, raw, err := doc.forEntry(collection, "shoowcase")
		if err != nil || raw == nil {
			return err
		}
		if !matches(raw) {
			fmt.Fprintf(out, "Kept the custom Shoowcase MCP entry for %s. Review it in the agent if needed.\n", name)
			return nil
		}
		for i, m := range obj.Members {
			if m.Name.Value.(hujson.Literal).String() == "shoowcase" {
				// Preserve a preceding comment and the separator style of
				// a last member; strict JSON must stay strict JSON.
				if i+1 < len(obj.Members) {
					next := &obj.Members[i+1]
					next.Name.BeforeExtra = append(bytes.Clone(m.Name.BeforeExtra), next.Name.BeforeExtra...)
				} else {
					obj.AfterExtra = append(bytes.Clone(m.Name.BeforeExtra), obj.AfterExtra...)
					if i > 0 && m.Value.AfterExtra == nil {
						obj.AfterExtra = append(bytes.Clone(obj.Members[i-1].Value.AfterExtra), obj.AfterExtra...)
						obj.Members[i-1].Value.AfterExtra = nil
					}
				}
				obj.Members = slices.Delete(obj.Members, i, i+1)
				return doc.install(path, doc.root.Pack())
			}
		}
		return nil
	}
	matches := func(raw json.RawMessage) bool {
		var e entry
		return decodeManagedServer(raw, &e) && ours(raw, binary, name) &&
			(len(e.Tools) == 0 || name == "copilot" && slices.Equal(e.Tools, []string{"*"}))
	}
	switch name {
	case "codex":
		return removeCodex(ctx, home, binary, out)
	case "claude", "copilot", "cursor":
		return remove(standardClientPath(home, name), "mcpServers", matches)
	case "gemini":
		return remove(geminiPath(home), "mcpServers", matches)
	case "vscode-local":
		path, err := vsCodePath(home)
		if err != nil {
			return err
		}
		return remove(path, "servers", matches)
	case "opencode":
		for _, file := range openCodeFiles {
			if err := remove(filepath.Join(openCodeDir(home), file), "mcp", func(raw json.RawMessage) bool {
				var e openCodeServer
				return decodeManagedServer(raw, &e) && e.Type == "local" &&
					slices.Equal(e.Command, []string{binary, "mcp-stdio", name})
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func removeCodex(ctx context.Context, home, binary string, out io.Writer) error {
	base := os.Getenv("CODEX_HOME")
	if base == "" {
		base = filepath.Join(home, ".codex")
	}
	path := filepath.Join(base, "config.toml")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if !filepath.IsAbs(path) {
		return errors.New("CODEX_HOME must be an absolute path")
	}
	if err := regularIfPresent(path, false); err != nil {
		return err
	}
	if _, err := exec.LookPath("codex"); err != nil {
		fmt.Fprintln(out, "Codex CLI was not found. Review its user MCP settings and remove the Quickstart Shoowcase entry if present.")
		return nil
	}
	b, err := runClient(ctx, home, "codex", "mcp", "list", "--json")
	if err != nil {
		return err
	}
	var servers []struct {
		Name      string          `json:"name"`
		Transport json.RawMessage `json:"transport"`
	}
	if json.Unmarshal(b, &servers) != nil || servers == nil {
		return errors.New("cannot read Codex MCP settings: expected a JSON list")
	}
	for _, s := range servers {
		if s.Name != "shoowcase" {
			continue
		}
		var extra struct {
			Env     map[string]string `json:"env"`
			EnvVars []string          `json:"env_vars"`
			Cwd     string            `json:"cwd"`
		}
		if !ours(s.Transport, binary, "codex") || json.Unmarshal(s.Transport, &extra) != nil ||
			len(extra.Env) != 0 || len(extra.EnvVars) != 0 || extra.Cwd != "" {
			fmt.Fprintln(out, "Kept the custom Shoowcase MCP entry for Codex. Review it in the agent if needed.")
			return nil
		}
		_, err := runClient(ctx, home, "codex", "mcp", "remove", "shoowcase")
		return err
	}
	return nil
}
