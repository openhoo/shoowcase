package sites

import (
	"bytes"
	"context"
	"database/sql"
	"net/url"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

// Verify site-local hyperlink targets and the MIME class expected by each
// image, script and stylesheet reference. Only HTML bytes are read. Query strings and
// fragments do not change the file selected by the delivery host.
func checkReleaseLinks(ctx context.Context, tx *sql.Tx, revision string) error {
	rows, e := tx.QueryContext(ctx, "SELECT f.path,f.content_type,CASE WHEN f.content_type='text/html; charset=utf-8' THEN b.body ELSE NULL END FROM sc_files f JOIN sc_blobs b ON b.digest=f.digest WHERE f.revision=$1", revision)
	if e != nil {
		return e
	}
	defer rows.Close()
	types := map[string]string{}
	docs := map[string][]byte{}
	var paths []string
	for rows.Next() {
		var path, ct string
		var b []byte
		if e = rows.Scan(&path, &ct, &b); e != nil {
			return e
		}
		types[path] = ct
		if strings.HasPrefix(ct, "text/html") {
			docs[path] = b
			paths = append(paths, path)
		}
	}
	if e = rows.Err(); e != nil {
		return e
	}
	sort.Strings(paths)
	for _, path := range paths {
		if e = checkPageLinks(ctx, path, docs[path], types); e != nil {
			return e
		}
	}
	return nil
}
func checkPageLinks(ctx context.Context, path string, data []byte, types map[string]string) error {
	base := url.URL{Scheme: "https", Host: "site.invalid", Path: "/" + path}
	doc, e := html.Parse(bytes.NewReader(data))
	if e != nil {
		return bad("Invalid HTML: " + path)
	}
	todo := []*html.Node{doc}
	for len(todo) > 0 {
		if e := ctx.Err(); e != nil {
			return e
		}
		n := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		for c := n.LastChild; c != nil; c = c.PrevSibling {
			todo = append(todo, c)
		}
		if n.Type != html.ElementNode {
			continue
		}
		attr := ""
		expected := ""
		switch n.Data {
		case "script":
			target := scriptPath(path, n)
			if target == "" || types[target] != "text/javascript; charset=utf-8" {
				return bad("The local script in " + path + " is missing or has an incompatible type: " + target)
			}
			continue
		case "a":
			attr = "href"
		case "link":
			attr = "href"
			expected = "text/css"
		case "img":
			attr = "src"
			expected = "image/"
		default:
			continue
		}
		for _, a := range n.Attr {
			if a.Key != attr {
				continue
			}
			ref, e := url.Parse(strings.TrimSpace(a.Val))
			if e != nil {
				return bad("Invalid local link in " + path)
			}
			if ref.IsAbs() {
				continue
			}
			target := strings.TrimPrefix(base.ResolveReference(ref).Path, "/")
			if target == "" || strings.HasSuffix(target, "/") {
				target += "index.html"
			}
			ct, ok := types[target]
			if !ok || !strings.HasPrefix(ct, expected) {
				return bad("Local reference in " + path + " does not resolve to a compatible file: " + target)
			}
		}
	}
	return nil
}
