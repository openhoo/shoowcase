package web

import (
	"context"

	"github.com/openhoo/shoowcase/internal/sites"
)

// Both the MCP adapter and typed console queries must act as the same
// principal, share the same rate limit, and audit permission denials.
func withToolAccess[T any](ctx context.Context, a *App, tool string, fn func(context.Context, sites.Actor) (T, error)) (out T, err error) {
	if !a.config.Public {
		ctx = sites.WithPrivateDelivery(ctx)
	}
	who, _ := ctx.Value(whoKey{}).(sites.Actor)
	if who.Subject == "" {
		return out, problem("unauthorized", "Sign in to Shoowcase.")
	}
	defer func() {
		if err != nil && errorCode(err) == "forbidden" {
			a.auditDenied(ctx, who, tool)
		}
	}()
	if !a.rate.Allow("agent:"+who.Workspace+"\x00"+who.Subject, 300) {
		return out, problem("rate_limited", "Wait a minute before retrying.")
	}
	return fn(ctx, who)
}

func (a *App) getSite(ctx context.Context, q sites.GetSiteQuery) (*sites.SiteDetails, error) {
	return withToolAccess(ctx, a, "get_site", func(ctx context.Context, who sites.Actor) (*sites.SiteDetails, error) {
		return a.store.GetSite(ctx, who, q)
	})
}

func (a *App) siteLinks(site, draft, selected string) (string, string) {
	revision := selected
	if revision == "" {
		revision = draft
	}
	if revision == "" {
		return a.siteURL(site), ""
	}
	return a.siteURL(site), a.siteURL("pv-" + revision)
}
