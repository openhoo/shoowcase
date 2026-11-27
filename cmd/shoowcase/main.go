// Shoowcase is a static site publishing service for MCP agents.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/openhoo/shoowcase/internal/agentsetup"
	"github.com/openhoo/shoowcase/internal/auth"
	"github.com/openhoo/shoowcase/internal/sites"
	"github.com/openhoo/shoowcase/internal/telemetry"
	"github.com/openhoo/shoowcase/internal/web"
)

var version = "development"

func env(s, fallback string) string {
	if v := os.Getenv("SHOOWCASE_" + s); v != "" {
		return v
	}
	return fallback
}
func main() {
	if err := run(); err != nil {
		slog.Error("Shoowcase stopped", "reason", err)
		os.Exit(1)
	}
}

func sessionActor(i auth.Identity) sites.Actor {
	role := string(i.Role)
	if i.Role == auth.Viewer {
		role = "reader"
	}
	return sites.Actor{Workspace: i.Workspace, Subject: i.Subject, Role: role}
}

func run() error {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "connect":
			return agentsetup.Connect(context.Background(), os.Args[2:], os.Stdout)
		case "uninstall":
			return agentsetup.Uninstall(context.Background(), os.Args[2:], os.Stdout)
		case "mcp-stdio":
			return agentsetup.Stdio(context.Background(), os.Args[2:], os.Stdin, os.Stdout)
		}
	}
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version)
		return nil
	}
	if len(os.Args) > 1 && os.Args[1] == "keygen" {
		var b [32]byte
		if _, e := rand.Read(b[:]); e != nil {
			return e
		}
		fmt.Println(base64.RawURLEncoding.EncodeToString(b[:]))
		return nil
	}
	dev := flag.Bool("dev", false, "single-user development mode on loopback only")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("usage: shoowcase [-dev] | version | keygen | uninstall | connect <client> --url URL --setup-code CODE")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	control := env("CONTROL_URL", "")
	zone := env("SITES_DOMAIN", "")
	key := env("SESSION_KEY", "")
	driver := "postgres"
	dsn := env("DATABASE_URL", "")
	listen := env("LISTEN", "127.0.0.1:7878")
	token := env("DEV_TOKEN", "")
	if *dev {
		host, _, e := net.SplitHostPort(listen)
		ip := net.ParseIP(host)
		if e != nil || ip == nil || !ip.IsLoopback() {
			return errors.New("development listener must be a numeric loopback address")
		}
		if control == "" {
			_, port, _ := net.SplitHostPort(listen)
			control = "http://localhost:" + port
		}
		if zone == "" {
			zone = "localhost"
		}
		dir := env("STATE_DIR", ".shoowcase")
		if e := os.MkdirAll(dir, 0700); e != nil {
			return errors.New("cannot create development state directory")
		}
		if dsn == "" {
			driver = "sqlite"
			dsn = filepath.Join(dir, "shoowcase.db")
		}
		if key == "" {
			var k [32]byte
			if _, e := rand.Read(k[:]); e != nil {
				return e
			}
			key = base64.RawURLEncoding.EncodeToString(k[:])
		}
		if token == "" {
			p := filepath.Join(dir, "agent-token")
			b, e := os.ReadFile(p)
			if os.IsNotExist(e) {
				var k [32]byte
				if _, e := rand.Read(k[:]); e != nil {
					return e
				}
				token = base64.RawURLEncoding.EncodeToString(k[:])
				f, e := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
				if e != nil {
					return errors.New("cannot create local token")
				}
				_, e = f.WriteString(token + "\n")
				ce := f.Close()
				if e != nil || ce != nil {
					return errors.New("cannot save local token")
				}
			} else if e != nil {
				return errors.New("cannot load local token")
			} else {
				token = strings.TrimSpace(string(b))
			}
			slog.Info("local agent token file", "path", p)
		}
	} else if dsn == "" || control == "" || zone == "" || key == "" {
		return errors.New("production requires DATABASE_URL, CONTROL_URL, SITES_DOMAIN and SESSION_KEY with SHOOWCASE_ prefix")
	} else if err := requireDatabaseTLS(dsn); err != nil {
		return err
	}
	// Refuse ambiguous env values; in particular a typo must never enable public sharing.
	public := env("PUBLIC", "false")
	if public != "true" && public != "false" {
		return errors.New("SHOOWCASE_PUBLIC must be true or false")
	}
	registration := env("PUBLIC_REGISTRATION", "false")
	if registration != "true" && registration != "false" {
		return errors.New("SHOOWCASE_PUBLIC_REGISTRATION must be true or false")
	}
	adminSub, adminWorkspace := env("OIDC_ADMIN_SUBJECT", ""), env("OIDC_ADMIN_WORKSPACE", "")
	apps := env("INTERACTIVE", "true")
	if apps != "true" && apps != "false" {
		return errors.New("SHOOWCASE_INTERACTIVE must be true or false")
	}
	ready, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s, e := sites.Open(ready, driver, dsn)
	if e != nil {
		return errors.New("cannot initialize database (check connection, migration permissions and server logs)")
	}
	defer s.Close()
	issuer := env("ISSUER", "")
	authn, e := auth.New(ready, auth.Config{
		ControlURL: control, SitesDomain: zone, Issuer: issuer,
		ClientID: env("CLIENT_ID", ""), ClientSecret: env("CLIENT_SECRET", ""), ClientAuthMethod: env("CLIENT_AUTH_METHOD", ""),
		Audience: env("AUDIENCE", ""), SessionKey: key,
		WorkspaceClaim: env("WORKSPACE_CLAIM", "workspace"), RolesClaim: env("ROLES_CLAIM", "roles"), RequiredScope: env("REQUIRED_SCOPE", ""),
		Dev: *dev, DevWorkspace: env("DEV_WORKSPACE", "local"), DevSubject: env("DEV_SUBJECT", "local-user"), DevToken: token,
		Consume: s.Consume, IsConsumed: s.IsConsumed,
		LocalAccounts: s, LocalWorkspace: env("LOCAL_WORKSPACE", ""), PublicRegistration: registration == "true",
		OIDCProjectAudience: env("OIDC_PROJECT_AUDIENCE", ""), OIDCAdminSubject: adminSub, OIDCAdminWorkspace: adminWorkspace,
		Audit: func(ctx context.Context, i auth.Identity, action string) error {
			if adminSub != "" && i.Subject == adminSub && i.Workspace == adminWorkspace && i.Role == auth.Admin && i.LocalGeneration == 0 {
				if _, err := s.EnrollAccount(sites.WithIdentityIssuedAt(ctx, i.IssuedAt), sessionActor(i), "Workspace administrator"); err != nil {
					return err
				}
			}
			if i.LocalGeneration > 0 {
				ctx = sites.WithLocalSession(ctx, i.LocalGeneration)
			} else {
				ctx = sites.WithIdentityIssuedAt(ctx, i.IssuedAt)
			}
			return s.RecordSession(ctx, sessionActor(i), action)
		},
		RevokeSession: func(ctx context.Context, key string, expires time.Time, i auth.Identity) error {
			return s.RevokeSessionAndAudit(sites.WithIdentityIssuedAt(ctx, i.IssuedAt), key, expires, sessionActor(i))
		},
	})
	if e != nil {
		return fmt.Errorf("identity configuration: %w", e)
	}
	if err := s.RequireIssuer(ready, authn.IdentityRealm()); err != nil {
		return errors.New("cannot bind identity realm: verify the database, development mode and original issuer before starting")
	}
	metricsWorkspace := env("METRICS_WORKSPACE", "")
	if *dev && metricsWorkspace == "" {
		metricsWorkspace = env("DEV_WORKSPACE", "local")
	}
	app, e := web.New(web.Config{
		ControlURL: strings.TrimSuffix(control, "/"), SitesDomain: zone, Version: version, Key: authn.ActionKey(),
		MetricsWorkspace: metricsWorkspace, DisableApps: apps == "false", Public: public == "true",
		SignupURL: env("SIGNUP_URL", ""), OIDCAccountURL: env("OIDC_ACCOUNT_URL", ""),
	}, s, authn)
	if e != nil {
		return e
	}
	obs, e := telemetry.New(ready, version, control, s.PoolStats)
	if e != nil {
		return e
	}
	defer func() {
		d, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := obs.Shutdown(d); err != nil {
			slog.Warn("telemetry shutdown did not finish cleanly")
		}
	}()
	listener, e := net.Listen("tcp", listen)
	if e != nil {
		return e
	}
	server := &http.Server{Handler: obs.Handler(app), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10}
	drained := make(chan error, 1)
	go func() {
		<-ctx.Done()
		d, c := context.WithTimeout(context.Background(), 12*time.Second)
		defer c()
		drained <- server.Shutdown(d)
	}()
	slog.Info("Shoowcase ready", "version", version, "control", control, "public_sharing", public, "development", *dev)
	if e := server.Serve(listener); !errors.Is(e, http.ErrServerClosed) {
		return e
	}
	// Serve returns as soon as Shutdown closes the listener. Keep the store
	// available until active requests have finished.
	return <-drained
}
