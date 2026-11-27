package agentsetup

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/tailscale/hujson"
)

const maxClientSettings = 16 << 20

type clientDocument struct {
	data []byte
	info os.FileInfo
	root hujson.Value
}

var invalidClientJSONC = errors.New("invalid client settings; repair the existing configuration file first")

// Fail closed without printing settings: other servers may contain secrets.
func readClientDocument(path string) (doc clientDocument, err error) {
	if !filepath.IsAbs(path) {
		return doc, errors.New("the client configuration must have an absolute path")
	}
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		doc.root = hujson.Value{Value: new(hujson.Object)}
		return doc, nil
	}
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxClientSettings {
		return doc, errors.New("cannot safely read the client configuration file")
	}
	f, err := os.Open(path)
	if err != nil {
		return doc, errors.New("cannot open client configuration")
	}
	defer f.Close()
	// Validate the opened descriptor as well as the path checked above.
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(st, info) {
		return doc, errors.New("client configuration changed during setup; retry")
	}
	data, err := readBounded(f, maxClientSettings)
	if err != nil {
		return doc, err
	}
	root, err := hujson.Parse(data)
	if _, ok := root.Value.(*hujson.Object); err != nil || !ok {
		return doc, invalidClientJSONC
	}
	return clientDocument{data: data, info: info, root: root}, nil
}

func findClientMember(obj *hujson.Object, key string) (*hujson.Value, error) {
	var found *hujson.Value
	for i := range obj.Members {
		m := &obj.Members[i]
		if m.Name.Value.(hujson.Literal).String() != key {
			continue
		}
		if found != nil {
			return nil, errors.New("ambiguous duplicate key in client configuration; repair it first")
		}
		found = &m.Value
	}
	return found, nil
}

// forEntry locates only the named collection and entry. Its raw JSON is
// independent of the source, so matching does not alter existing trivia.
func (doc *clientDocument) forEntry(collection, name string) (*hujson.Object, json.RawMessage, error) {
	val, err := findClientMember(doc.root.Value.(*hujson.Object), collection)
	if err != nil || val == nil {
		return nil, nil, err
	}
	obj, ok := val.Value.(*hujson.Object)
	if !ok {
		return nil, nil, invalidClientJSONC
	}
	target, err := findClientMember(obj, name)
	if err != nil || target == nil {
		return obj, nil, err
	}
	for v := range target.All() {
		if o, ok := v.Value.(*hujson.Object); ok {
			seen := make(map[string]bool, len(o.Members))
			for _, m := range o.Members {
				key := m.Name.Value.(hujson.Literal).String()
				if seen[key] {
					return nil, nil, invalidClientJSONC
				}
				seen[key] = true
			}
		}
	}
	v := target.Clone()
	v.Standardize()
	return obj, v.Pack(), nil
}

func readClientJSONCEntry(path, collection, name string) (json.RawMessage, bool, error) {
	doc, err := readClientDocument(path)
	if err != nil {
		return nil, false, err
	}
	_, raw, err := doc.forEntry(collection, name)
	return raw, raw != nil, err
}

func mergeClientJSONC(path, collection, name string, value any, matches func(json.RawMessage) bool) error {
	doc, err := readClientDocument(path)
	if err != nil {
		return err
	}
	obj, existing, err := doc.forEntry(collection, name)
	if err != nil {
		return err
	}
	if existing != nil {
		if !matches(existing) {
			return conflict
		}
		return nil
	}
	if obj == nil {
		obj = doc.root.Value.(*hujson.Object)
		value = map[string]any{name: value}
		name = collection
	}
	var data []byte
	if doc.info == nil {
		data, err = json.MarshalIndent(map[string]any{name: value}, "", "  ")
		data = append(data, '\n')
	} else {
		var b []byte
		b, err = json.Marshal(value)
		if err == nil {
			var member hujson.ObjectMember
			member.Name.Value = hujson.String(name)
			member.Name.BeforeExtra = clientMemberIndent(obj)
			member.Value, err = hujson.Parse(b)
			member.Value.BeforeExtra = hujson.Extra(" ")
			// Insert first so comments, comma style, and whitespace of every
			// existing member (including any final comment) remain untouched.
			obj.Members = append([]hujson.ObjectMember{member}, obj.Members...)
			data = doc.root.Pack()
		}
	}
	if err != nil || len(data) > maxClientSettings {
		return errors.New("cannot prepare client MCP settings")
	}
	return doc.install(path, data)
}

func clientMemberIndent(obj *hujson.Object) hujson.Extra {
	var extra []byte
	if len(obj.Members) > 0 {
		extra = obj.Members[0].Name.BeforeExtra
	} else {
		extra = obj.AfterExtra
	}
	i := bytes.LastIndexByte(extra, '\n')
	if i < 0 {
		return nil
	}
	j := i + 1
	for j < len(extra) && (extra[j] == ' ' || extra[j] == '\t') {
		j++
	}
	if i > 0 && extra[i-1] == '\r' {
		i--
	}
	indent := bytes.Clone(extra[i:j])
	if len(obj.Members) == 0 {
		indent = append(indent, ' ', ' ')
	}
	return indent
}

func (doc *clientDocument) install(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return errors.New("cannot create the client configuration directory")
	}
	tmp, err := os.CreateTemp(dir, ".shoowcase-mcp-*")
	if err != nil {
		return errors.New("cannot create temporary client settings")
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := tmp.Write(data); err != nil || tmp.Sync() != nil {
		return errors.New("cannot write client settings")
	}
	if doc.info != nil && tmp.Chmod(doc.info.Mode().Perm()) != nil {
		return errors.New("cannot preserve client configuration permissions")
	}
	if tmp.Close() != nil {
		return errors.New("cannot close temporary client settings")
	}
	if doc.info == nil {
		// Publish a new file only if the path is still free; rename would
		// silently overwrite another process that just created it.
		if err := os.Link(tmp.Name(), path); err != nil {
			return errors.New("cannot install new client settings; check whether another process created the file")
		}
		return nil
	}
	now, err := readClientDocument(path)
	if err != nil || now.info == nil || !os.SameFile(doc.info, now.info) ||
		doc.info.Mode() != now.info.Mode() || !bytes.Equal(doc.data, now.data) {
		return errors.New("client configuration changed during setup; rerun the command")
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return errors.New("cannot install client settings")
	}
	return nil
}
