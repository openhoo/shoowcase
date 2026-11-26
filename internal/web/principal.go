package web

import (
	"context"
	"github.com/openhoo/shoowcase/internal/auth"
	"github.com/openhoo/shoowcase/internal/sites"
	"net/http"
	"strings"
)

type sourceKeyID struct{}

func sourceKey(ctx context.Context) string { id, _ := ctx.Value(sourceKeyID{}).(string); return id }
func keyContext(ctx context.Context, id string) context.Context {
	return sites.WithKeyID(context.WithValue(ctx, sourceKeyID{}, id), id)
}
func (a *App) agent(r *http.Request) (sites.Actor, context.Context, error) {
	ctx := r.Context()
	if h := r.Header.Values("Authorization"); len(h) == 1 {
		f := strings.Fields(h[0])
		if len(f) == 2 && strings.EqualFold(f[0], "Bearer") && strings.HasPrefix(f[1], sites.MCPKeyPrefix) {
			who, err := a.store.ResolveKey(ctx, f[1])
			return who, keyContext(ctx, sites.KeyID(f[1])), err
		}
	}
	i, err := a.auth.Bearer(r)
	if err != nil {
		return sites.Actor{}, ctx, problem("unauthorized", "Sign in to Shoowcase.")
	}
	ctx = verified(ctx, i)
	who, err := a.store.LimitActor(ctx, actor(i))
	return who, ctx, err
}
func (a *App) agentError(w http.ResponseWriter, err error) {
	if errorCode(err) == "unauthorized" {
		a.auth.Challenge(w)
		http.Error(w, "Authentication required", 401)
		return
	}
	writeError(w, err)
}
func (a *App) ticketActor(ctx context.Context, t ticket, browser *auth.Identity) (context.Context, sites.Actor, error) {
	who := t.Who
	if browser != nil {
		b := actor(*browser)
		if who.Subject != b.Subject || who.Workspace != b.Workspace {
			return ctx, sites.Actor{}, problem("forbidden", "Use the account that requested this action.")
		}
		effective, err := a.store.LimitActor(verified(ctx, *browser), b)
		if err != nil {
			return ctx, sites.Actor{}, err
		}
		level := func(role string) int {
			return map[string]int{"reader": 1, "editor": 2, "publisher": 3, "admin": 4}[role]
		}
		if level(effective.Role) < level(who.Role) {
			who.Role = effective.Role
		}
	}
	if t.KeyID != "" {
		who, err := a.store.ResolveKeyID(ctx, who, t.KeyID)
		return keyContext(ctx, t.KeyID), who, err
	}
	who, err := a.store.LimitActor(ctx, who)
	return ctx, who, err
}
