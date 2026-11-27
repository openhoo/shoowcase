// Package uiassets binds each management asset URL to the bytes embedded in this binary.
package uiassets

import (
	"crypto/sha256"
	_ "embed"
	"fmt"
)

//go:embed app.css
var css string

//go:embed app.js
var js string

type Asset struct {
	Path, ETag, Content string
}

func asset(name, content string) Asset {
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
	return Asset{Path: "/ui/" + digest + "/" + name, ETag: `"` + digest + `"`, Content: content}
}

var (
	Style  = asset("ui.css", css)
	Script = asset("console.js", js)
)
