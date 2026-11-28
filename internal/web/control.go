package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/openhoo/shoowcase/internal/routes"
	"github.com/openhoo/shoowcase/internal/web/uiassets"
)

func (a *App) controlRouter() *routes.Control {
	return routes.NewControl(routes.Handlers{
		Health: a.health, Ready: a.ready, MCP: a.serveMCP, Approve: a.reviewApproval, Upload: a.signedUpload,
		CSS: a.controlStyle, Audit: a.exportAudit, Metrics: a.metrics, Redeem: a.quickRedeem, Installer: serveInstaller,
		Script: a.controlScript, Theme: a.controlTheme, Console: a.console, Auth: func(w http.ResponseWriter, r *http.Request) { a.auth.Handle(w, r) },
	})
}

func (a *App) health(w http.ResponseWriter, r *http.Request) {
	if !readMethod(w, r) {
		return
	}
	w.WriteHeader(200)
	_, _ = io.WriteString(w, "ok\n")
}

func (a *App) ready(w http.ResponseWriter, r *http.Request) {
	if !readMethod(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if a.store.Ping(ctx) != nil {
		http.Error(w, "storage unavailable", 503)
		return
	}
	_, _ = io.WriteString(w, "ready\n")
}

func (a *App) serveMCP(w http.ResponseWriter, r *http.Request) {
	// Browser clients must originate on the management origin. Native MCP clients omit Origin.
	if o := r.Header.Get("Origin"); o != "" && o != a.config.ControlURL {
		http.Error(w, "Origin denied", 403)
		return
	}
	who, ctx, e := a.agent(r)
	if e != nil {
		a.denied.Add(1)
		a.agentError(w, e)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 12<<20)
	a.mcp.ServeHTTP(w, r.WithContext(context.WithValue(ctx, whoKey{}, who)))
}

func (a *App) reviewApproval(w http.ResponseWriter, r *http.Request) {
	// Browsers derive the Origin of a form POST from the page referrer policy.
	// Send only the control origin, never the signed query or path.
	w.Header().Set("Referrer-Policy", "strict-origin")
	a.approve(w, r)
}

func (a *App) signedUpload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "strict-origin")
	a.upload(w, r)
}

func serveUIAsset(w http.ResponseWriter, r *http.Request, mime string, asset uiassets.Asset) {
	if !readMethod(w, r) {
		return
	}
	if r.URL.Path == asset.Path {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Header().Set("ETag", asset.ETag)
	w.Header().Set("Content-Type", mime)
	http.ServeContent(w, r, "", time.Time{}, strings.NewReader(asset.Content))
}

func (a *App) controlStyle(w http.ResponseWriter, r *http.Request) {
	serveUIAsset(w, r, "text/css; charset=utf-8", uiassets.Style)
}

func (a *App) controlScript(w http.ResponseWriter, r *http.Request) {
	serveUIAsset(w, r, "text/javascript; charset=utf-8", uiassets.Script)
}

func (a *App) controlTheme(w http.ResponseWriter, r *http.Request) {
	serveUIAsset(w, r, "text/javascript; charset=utf-8", uiassets.Theme)
}

func (a *App) metrics(w http.ResponseWriter, r *http.Request) {
	if a.config.MetricsWorkspace == "" {
		http.NotFound(w, r)
		return
	}
	if !readMethod(w, r) {
		return
	}
	i, e := a.auth.Bearer(r)
	if e != nil {
		a.auth.Challenge(w)
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}
	if i.Workspace != a.config.MetricsWorkspace {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	principal, err := a.store.LimitActor(verified(r.Context(), i), actor(i))
	if err != nil {
		writeError(w, err)
		return
	}
	if principal.Role != "admin" {
		http.Error(w, "Forbidden", 403)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "shoowcase_requests_total %d\nshoowcase_site_responses_total %d\nshoowcase_site_bytes_total %d\nshoowcase_denials_total %d\n", a.requests.Load(), a.visits.Load(), a.bytes.Load(), a.denied.Load())
}
