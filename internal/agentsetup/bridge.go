package agentsetup

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxMessage = 12 << 20 // same as the Shoowcase HTTP MCP handler

// Shoowcase is a stateless, JSON-response-only Streamable HTTP server. Forward
// the client's original RPC envelope, retaining tool schemas, request IDs and
// structured results. A notification receives no stdio response.
func bridge(ctx context.Context, cli *http.Client, c credential, in io.Reader, out io.Writer) error {
	scan := bufio.NewScanner(in)
	scan.Buffer(make([]byte, 64<<10), maxMessage+1)
	version := ""
	for scan.Scan() {
		if len(scan.Bytes()) == 0 {
			continue
		}
		var msg struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  struct {
				Name string                     `json:"name"`
				URI  string                     `json:"uri"`
				Meta map[string]json.RawMessage `json:"_meta"`
			} `json:"params"`
		}
		if json.Unmarshal(scan.Bytes(), &msg) != nil || msg.JSONRPC != "2.0" || msg.Method == "" {
			return errors.New("invalid MCP input")
		}
		v := version
		if pv := msg.Params.Meta[mcp.MetaKeyProtocolVersion]; len(pv) > 0 {
			if json.Unmarshal(pv, &v) != nil {
				return errors.New("invalid MCP protocol version")
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(scan.Bytes()))
		if err != nil {
			return errors.New("invalid Shoowcase endpoint")
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if v != "" {
			req.Header.Set("Mcp-Protocol-Version", v)
		}
		if v >= "2026-07-28" {
			req.Header.Set("Mcp-Method", msg.Method)
			switch msg.Method {
			case "tools/call", "prompts/get":
				req.Header.Set("Mcp-Name", msg.Params.Name)
			case "resources/read":
				req.Header.Set("Mcp-Name", msg.Params.URI)
			}
		}
		result, ok := forward(cli, req, msg.ID)
		if len(msg.ID) == 0 {
			continue
		}
		if !ok {
			result, _ = json.Marshal(struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      json.RawMessage `json:"id"`
				Error   any             `json:"error"`
			}{"2.0", msg.ID, map[string]any{"code": -32000, "message": "Shoowcase request failed. Check connectivity and reconnect the agent if its key has expired or was revoked."}})
		} else if msg.Method == "initialize" {
			var init struct {
				Result struct {
					Version string `json:"protocolVersion"`
				} `json:"result"`
			}
			if json.Unmarshal(result, &init) == nil {
				version = init.Result.Version
			}
		}
		var line bytes.Buffer
		if json.Compact(&line, result) != nil {
			return errors.New("invalid response from Shoowcase")
		}
		line.WriteByte('\n')
		if _, err := line.WriteTo(out); err != nil {
			return errors.New("cannot write to the agent")
		}
	}
	if scan.Err() != nil {
		return errors.New("could not read an MCP message within the Shoowcase size limit")
	}
	return nil
}

func forward(cli *http.Client, req *http.Request, id json.RawMessage) ([]byte, bool) {
	res, err := cli.Do(req)
	if err != nil {
		return nil, false
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 || len(id) == 0 {
		return nil, false
	}
	b, err := readBounded(res.Body, maxMessage)
	var rpc struct {
		Version           string `json:"jsonrpc"`
		ID, Result, Error json.RawMessage
	}
	if err != nil || json.Unmarshal(b, &rpc) != nil || rpc.Version != "2.0" || !bytes.Equal(id, rpc.ID) || (len(rpc.Result) == 0) == (len(rpc.Error) == 0) {
		return nil, false
	}
	// A credential is transport-only, including on an erroneous server response.
	if bytes.Contains(b, []byte(req.Header.Get("Authorization")[len("Bearer "):])) {
		return nil, false
	}
	return b, true
}
