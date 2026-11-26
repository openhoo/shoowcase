// Guest programs are data. Only the host's nonce-bearing bridge can execute
// natively. The browser worker evaluates the data in its own QuickJS VM.
package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/openhoo/shoowcase/internal/sites"
	"golang.org/x/net/html"
)

//go:embed runtimeassets/bridge.js runtimeassets/worker.js
var runtimeFS embed.FS

type runtimeAsset struct {
	data []byte
	etag string
}

func loadRuntime(name string) runtimeAsset {
	b, err := runtimeFS.ReadFile("runtimeassets/" + name)
	if err != nil {
		panic(err)
	}
	return runtimeAsset{b, fmt.Sprintf("%q", fmt.Sprintf("%x", sha256.Sum256(b)))}
}

var trustedAssets = map[string]runtimeAsset{
	"/auth/runtime/bridge.js": loadRuntime("bridge.js"),
	"/auth/runtime/worker.js": loadRuntime("worker.js"),
}

func serveRuntime(w http.ResponseWriter, r *http.Request) {
	f, ok := trustedAssets[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !readMethod(w, r) {
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/javascript; charset=utf-8")
	// WASM compilation is enabled here only. The engine is bundled; the
	// worker cannot connect, import network scripts or start child workers.
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'wasm-unsafe-eval'; connect-src 'none'; worker-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Cache-Control", "public, no-cache")
	h.Set("ETag", f.etag)
	if r.Header.Get("If-None-Match") == f.etag {
		w.WriteHeader(304)
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(f.data)))
	if r.Method == "GET" {
		_, _ = w.Write(f.data)
	}
}

// No tenant data is cached here. A known-passive digest lets HEAD and
// conditional GET retain the metadata-only path after the first inspection.
type passiveDigests struct {
	mu    sync.Mutex
	items map[string]bool
}

func (s *passiveDigests) contains(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.items[key]
}
func (s *passiveDigests) remember(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil || len(s.items) >= 2048 {
		s.items = make(map[string]bool)
	}
	s.items[key] = true
}

const sitePolicy = "sandbox allow-same-origin; default-src 'none'; style-src 'self' 'unsafe-inline'; img-src 'self'; font-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors 'none'"

func (a *App) representation(ctx context.Context, method, slug, rev, path string, who sites.Actor, digest string) (sites.File, string, error) {
	fetch := a.store.FetchConditional
	if method == http.MethodHead {
		fetch = a.store.FetchMetadata
	}
	f, err := fetch(ctx, slug, rev, path, who, digest)
	if err != nil {
		return sites.File{}, "", err
	}
	if strings.HasPrefix(f.ContentType, "text/javascript") {
		// Browsing a source file displays text. It is never a native script.
		f.ContentType = "text/plain; charset=utf-8"
		return f, sitePolicy, nil
	}
	if a.config.DisableApps || !strings.HasPrefix(f.ContentType, "text/html") || a.passive.contains(f.Digest) {
		return f, sitePolicy, nil
	}
	if method == http.MethodHead || f.NotModified {
		f, err = a.store.Fetch(ctx, slug, rev, path, who)
		if err != nil {
			return sites.File{}, "", err
		}
	}
	programPath := sites.GuestScript(path, f.Data)
	if programPath == "" {
		a.passive.remember(f.Digest)
		f.NotModified = f.Digest == digest
		return f, sitePolicy, nil
	}
	program, err := a.store.FetchCompanion(ctx, slug, rev, f.Revision, programPath, who)
	if err != nil {
		return sites.File{}, "", err
	}
	if program.ContentType != "text/javascript; charset=utf-8" {
		return sites.File{}, "", problem("not_found", "Script not available.")
	}
	doc, err := html.Parse(bytes.NewReader(f.Data))
	if err != nil {
		return sites.File{}, "", err
	}
	nonce := rand.Text()
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && n.Data == "script" {
			n.Attr = []html.Attribute{{Key: "nonce", Val: nonce}, {Key: "src", Val: "/auth/runtime/bridge.js"}, {Key: "data-program", Val: base64.StdEncoding.EncodeToString(program.Data)}, {Key: "defer"}}
		}
	}
	var page bytes.Buffer
	if err = html.Render(&page, doc); err != nil {
		return sites.File{}, "", err
	}
	f.Data = page.Bytes()
	f.Size = page.Len()
	f.Digest = ""
	f.NotModified = false
	policy := "sandbox allow-scripts allow-same-origin; default-src 'none'; script-src 'nonce-" + nonce + "'; worker-src 'self'; connect-src 'none'; style-src 'self' 'unsafe-inline'; img-src 'self'; font-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors 'none'"
	return f, policy, nil
}
