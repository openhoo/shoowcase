package sites

import (
	"bytes"
	"strings"
	"testing"
)

func TestLocalAccountLifecycle(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			dsn := testDSN(t, backend)
			s, peer := openTest(t, backend, dsn), openTest(t, backend, dsn)
			ctx := t.Context()
			admin := Actor{"admin-sub", "team", "admin"}
			oidc := Actor{"same-name", "team", "admin"}
			for _, a := range []Actor{admin, oidc} {
				if _, err := s.EnrollAccount(ctx, a, a.Subject); err != nil {
					t.Fatal(err)
				}
			}
			const old = "  a long π pass phrase  "
			const next = "a different lengthy phrase"
			m, err := s.CreateLocalAccount(ctx, admin, "Same-Name", old, "editor")
			if err != nil || m.Role != "editor" || !strings.HasPrefix(m.Subject, LocalSubjectPrefix) || m.Subject == oidc.Subject || m.DisplayName != "same-name" {
				t.Fatal(m, err)
			}
			other, err := peer.RegisterLocalAccount(ctx, "other-team", "Same-Name", next)
			if err != nil || other.IdentityRole != "reader" || other.Subject == m.Subject {
				t.Fatal(other, err)
			}
			_, err = peer.RegisterLocalAccount(ctx, "team", " SAME-NAME ", next)
			wantErr(t, err, "conflict")
			_, err = peer.CreateLocalAccount(ctx, admin, "third", old, "admin")
			wantErr(t, err, "invalid_argument")
			_, err = peer.CreateLocalAccount(ctx, admin, "third", "short", "reader")
			wantErr(t, err, "invalid_argument")
			if err := peer.ProvisionMember(ctx, admin, LocalSubjectPrefix+"new", "Injected"); err == nil {
				t.Fatal("reserved OIDC subject")
			}
			auth, generation, err := peer.AuthenticateLocal(ctx, "team", "SAME-name", old)
			if err != nil || auth.Subject != m.Subject || auth.Role != "editor" || generation != 1 {
				t.Fatal(auth, generation, err)
			}
			_, _, e := peer.AuthenticateLocal(ctx, "team", "same-name", strings.TrimSpace(old))
			wantErr(t, e, "unauthorized")
			_, _, e = peer.AuthenticateLocal(ctx, "team", "unregistered", old)
			wantErr(t, e, "unauthorized")
			_, _, e = peer.AuthenticateLocal(ctx, "other-team", "same-name", old)
			wantErr(t, e, "unauthorized")
			active := WithLocalSession(ctx, generation)
			_, err = peer.CreateLocalAccount(active, auth, "fourth", next, "reader")
			wantErr(t, err, "forbidden")
			err = s.ChangeLocalPassword(ctx, oidc, old, next)
			wantErr(t, err, "forbidden")
			err = s.ChangeLocalPassword(WithKeyID(ctx, "delegated-key"), auth, old, next)
			wantErr(t, err, "forbidden")
			err = s.ChangeLocalPassword(active, auth, "wrong secret", next)
			wantErr(t, err, "unauthorized")
			var oldSalt, oldHash []byte
			if err = s.db.QueryRow("SELECT salt,digest FROM sc_local_credentials WHERE workspace=$1 AND username=$2", auth.Workspace, m.DisplayName).Scan(&oldSalt, &oldHash); err != nil || len(oldSalt) != 16 || len(oldHash) != 32 || bytes.Contains(oldHash, []byte(old)) {
				t.Fatal("password storage", err)
			}
			if err = s.ChangeLocalPassword(active, auth, old, next); err != nil {
				t.Fatal(err)
			}
			var newSalt, newHash []byte
			if err = s.db.QueryRow("SELECT salt,digest FROM sc_local_credentials WHERE workspace=$1 AND username=$2", auth.Workspace, m.DisplayName).Scan(&newSalt, &newHash); err != nil || bytes.Equal(oldSalt, newSalt) || bytes.Equal(oldHash, newHash) {
				t.Fatal("credential unchanged", err)
			}
			if err = peer.ValidateLocalSession(ctx, auth, generation); err == nil {
				t.Fatal("old session survived rotation")
			}
			_, _, e = peer.CreateKey(active, auth, "old session", "reader", 1)
			wantErr(t, e, "unauthorized")
			_, _, e = peer.AuthenticateLocal(ctx, "team", "same-name", old)
			wantErr(t, e, "unauthorized")
			auth2, gen2, err := peer.AuthenticateLocal(ctx, "team", "same-name", next)
			if err != nil || gen2 != generation+1 || auth != auth2 {
				t.Fatal(auth2, gen2, err)
			}
			// An OIDC account with the same display name cannot observe or change this password.
			if a, err := peer.LimitActor(ctx, oidc); err != nil || a.Role != "admin" {
				t.Fatal(a, err)
			}
			groups, err := s.WorkspaceSettings(ctx, admin)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.SetWorkspaceSettings(ctx, admin, "Team", "managed", groups.Version); err != nil {
				t.Fatal(err)
			}
			_, err = peer.RegisterLocalAccount(ctx, "team", "outsider", old)
			wantErr(t, err, "forbidden")
			managed, err := peer.CreateLocalAccount(ctx, admin, "invited", next, "publisher")
			if err != nil || managed.Role != "reader" || managed.IdentityRole != "publisher" {
				t.Fatal(managed, err)
			}
			if err = s.SetMember(ctx, admin, auth.Subject, "", true, "", false); err != nil {
				t.Fatal(err)
			}
			_, _, e = peer.AuthenticateLocal(ctx, "team", "same-name", next)
			wantErr(t, e, "unauthorized")
			if err = s.SetMember(ctx, admin, auth.Subject, "", false, "", true); err != nil {
				t.Fatal(err)
			}
			if err = peer.ValidateLocalSession(ctx, auth, gen2); err == nil {
				t.Fatal("pre-suspension session revived")
			}
			events, err := peer.AccountActivity(ctx, admin)
			if err != nil {
				t.Fatal(err)
			}
			created, changed := 0, 0
			for _, e := range events {
				if e.Action == "local_account_created" && e.Subject == admin.Subject && e.Target == auth.Subject {
					created++
				}
				if e.Action == "local_password_changed" && e.Subject == auth.Subject && e.Target == auth.Subject {
					changed++
				}
			}
			if created != 1 || changed != 1 {
				t.Fatal("unexpected audit entries", created, changed)
			}
		})
	}
}

func TestLocalUsername(t *testing.T) {
	for _, input := range []string{"ab", "karl@example.com", "Kelvin", "-leading", "local:nobody", strings.Repeat("a", 65)} {
		if canonicalLogin(input) != "" {
			t.Errorf("accepted %q", input)
		}
	}
}
