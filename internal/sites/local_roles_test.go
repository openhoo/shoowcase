package sites

import (
	"context"
	"testing"
)

func TestChangeLocalSignInRole(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			ctx := t.Context()
			dsn := testDSN(t, driver)
			s, peer := openTest(t, driver, dsn), openTest(t, driver, dsn)
			admin := Actor{"ops", "design", "admin"}
			foreign := Actor{"ops", "elsewhere", "admin"}
			sso := Actor{"member", "design", "reader"}
			for _, a := range []Actor{admin, foreign, sso} {
				if _, err := s.EnrollAccount(ctx, a, a.Subject); err != nil {
					t.Fatal(err)
				}
			}
			const password = "synthetic test password"
			m, err := s.CreateLocalAccount(ctx, admin, "staff", password, "editor")
			if err != nil {
				t.Fatal(err)
			}
			signIn := func(want string) (Actor, context.Context) {
				t.Helper()
				a, gen, err := peer.AuthenticateLocal(ctx, admin.Workspace, "staff", password)
				if err != nil || a.Role != want {
					t.Fatalf("sign-in: got %q, %v; want %q", a.Role, err, want)
				}
				return a, WithLocalSession(ctx, gen)
			}
			editor, oldSession := signIn("editor")
			_, key, err := peer.CreateKey(oldSession, editor, "Existing editor key", "editor", 1)
			if err != nil {
				t.Fatal(err)
			}
			checkKey := func(want string) {
				t.Helper()
				if a, err := peer.ResolveKey(ctx, key); err != nil || a.Role != want {
					t.Fatalf("key: got %q, %v; want %q", a.Role, err, want)
				}
			}

			wantErr(t, s.SetLocalRole(ctx, foreign, m.Subject, "admin", "editor", "", false), "not_found")
			wantErr(t, s.SetLocalRole(ctx, admin, sso.Subject, "admin", "reader", "", false), "forbidden")
			wantErr(t, s.SetLocalRole(ctx, admin, m.Subject, "owner", "editor", "", false), "invalid_argument")
			wantErr(t, s.SetLocalRole(ctx, sso, m.Subject, "admin", "editor", "", false), "forbidden")
			wantErr(t, s.SetLocalRole(WithKeyID(ctx, KeyID(key)), admin, m.Subject, "admin", "editor", "", false), "forbidden")

			if err := s.SetLocalRole(ctx, admin, m.Subject, "reader", "editor", "", false); err != nil {
				t.Fatal(err)
			}
			_, err = peer.LimitActor(oldSession, editor)
			wantErr(t, err, "unauthorized")
			checkKey("reader")
			wantErr(t, s.SetLocalRole(ctx, admin, m.Subject, "publisher", "editor", "", false), "conflict")
			if err := s.SetLocalRole(ctx, admin, m.Subject, "publisher", "reader", "", false); err != nil {
				t.Fatal(err)
			}
			pub, pubSession := signIn("publisher")
			checkKey("editor")
			if _, _, err := peer.CreateKey(pubSession, pub, "Publisher key", "publisher", 1); err != nil {
				t.Fatal(err)
			}

			if err := s.SetMember(ctx, admin, m.Subject, "editor", false, "", false); err != nil {
				t.Fatal(err)
			}
			wantErr(t, s.SetLocalRole(ctx, admin, m.Subject, "admin", "publisher", "", false), "conflict")
			if err := s.SetLocalRole(ctx, admin, m.Subject, "admin", "publisher", "editor", false); err != nil {
				t.Fatal(err)
			}
			localAdmin, adminSession := signIn("admin")
			if _, err := peer.WorkspaceSettings(adminSession, localAdmin); err == nil {
				t.Fatal("promotion bypassed the existing personal limit")
			}
			if err := s.SetMember(ctx, admin, m.Subject, "", false, "editor", false); err != nil {
				t.Fatal(err)
			}
			if _, err := peer.WorkspaceSettings(adminSession, localAdmin); err != nil {
				t.Fatalf("local administrator cannot manage the workspace: %v", err)
			}
			_, _, err = peer.CreateKey(adminSession, localAdmin, "Admin key", "admin", 1)
			wantErr(t, err, "invalid_argument")
			wantErr(t, peer.SetLocalRole(adminSession, localAdmin, m.Subject, "publisher", "admin", "", false), "forbidden")
			if err := s.SetLocalRole(ctx, admin, m.Subject, "admin", "admin", "", false); err != nil {
				t.Fatal(err)
			}
			if _, err := peer.WorkspaceSettings(adminSession, localAdmin); err != nil {
				t.Fatalf("unchanged role invalidated the session: %v", err)
			}
			if err := peer.SetWorkspaceSettings(adminSession, localAdmin, "", "managed", 1); err != nil {
				t.Fatal(err)
			}
			if err := s.SetLocalRole(ctx, admin, m.Subject, "publisher", "admin", "", false); err != nil {
				t.Fatal(err)
			}
			wantErr(t, peer.SetWorkspaceSettings(adminSession, localAdmin, "", "identity", 2), "unauthorized")
			pub, pubSession = signIn("publisher")
			actual, err := peer.LimitActor(pubSession, pub)
			if err != nil || actual.Role != "reader" {
				t.Fatalf("managed workspace group limit: got %q, %v", actual.Role, err)
			}
			checkKey("reader")

			if err := s.SetMember(ctx, admin, m.Subject, "", true, "", false); err != nil {
				t.Fatal(err)
			}
			wantErr(t, s.SetLocalRole(ctx, admin, m.Subject, "admin", "publisher", "", false), "conflict")
			if err := s.SetLocalRole(ctx, admin, m.Subject, "admin", "publisher", "", true); err != nil {
				t.Fatal(err)
			}
			_, _, err = peer.AuthenticateLocal(ctx, admin.Workspace, "staff", password)
			wantErr(t, err, "unauthorized")
			_, err = peer.ResolveKey(ctx, key)
			wantErr(t, err, "unauthorized")

			events, _, err := peer.AuditPage(ctx, admin, 0, true, 100)
			if err != nil {
				t.Fatal(err)
			}
			var changes [][2]string
			for _, e := range events {
				if e.Action != "local_role_changed" {
					continue
				}
				if e.Subject != admin.Subject || e.Target != m.Subject {
					t.Fatal("unexpected role change", e)
				}
				changes = append(changes, [2]string{e.Previous, e.Value})
			}
			want := [][2]string{{"editor", "reader"}, {"reader", "publisher"}, {"publisher", "admin"}, {"admin", "publisher"}, {"publisher", "admin"}}
			if len(changes) != len(want) {
				t.Fatalf("audit captured %v, want %v", changes, want)
			}
			for i := range changes {
				if changes[i] != want[i] {
					t.Fatal(changes, want)
				}
			}
		})
	}
}
