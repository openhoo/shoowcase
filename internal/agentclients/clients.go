// Package agentclients describes the local clients supported by both the web
// quickstart and the connector. IDs are part of the one-time setup protocol.
package agentclients

type Client struct {
	ID, Name string
}

var Clients = []Client{
	{"copilot-app", "GitHub Copilot app"},
	{"vscode", "GitHub Copilot in VS Code"},
	{"copilot", "GitHub Copilot CLI"},
	{"vscode-local", "VS Code (legacy Local agent)"},
	{"cursor", "Cursor"},
	{"codex", "Codex"},
	{"claude", "Claude Code"},
	{"gemini", "Gemini CLI"},
	{"opencode", "OpenCode"},
	{"stdio", "Other local MCP client"},
}

func Name(id string) string {
	for _, c := range Clients {
		if c.ID == id {
			return c.Name
		}
	}
	return ""
}

// LocalID identifies the registration and its credential on this machine.
// The GitHub Copilot app, CLI and default VS Code agent read Copilot Global.
// Keep the v0.5.0 registration and filename so existing connections still work.
func LocalID(id string) string {
	switch id {
	case "copilot-app", "vscode":
		return "copilot"
	}
	return id
}
