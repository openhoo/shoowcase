package sites

import "testing"

func TestSelectedKeyRevocationIsScopedAndAtomic(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			s := openTest(t, backend, testDSN(t, backend))
			ctx := t.Context()
			admin := Actor{Subject: "admin", Workspace: "desk", Role: "admin"}
			member := Actor{Subject: "reader", Workspace: "desk", Role: "reader"}
			outside := Actor{Subject: "elsewhere", Workspace: "other", Role: "reader"}
			var ids, secrets []string
			for _, a := range []Actor{admin, member, outside} {
				if _, err := s.EnrollAccount(ctx, a, a.Subject); err != nil {
					t.Fatal(err)
				}
				k, secret, err := s.CreateKey(ctx, a, "Agent", "reader", 1)
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, k.ID)
				secrets = append(secrets, secret)
			}
			for _, c := range []struct {
				actor Actor
				wide  bool
				ids   []string
				code  string
			}{
				{admin, false, ids[:2], "not_found"},
				{member, true, ids[:2], "forbidden"},
				{admin, true, ids, "not_found"},
				{admin, true, []string{ids[0], ids[0]}, "invalid_argument"},
				{admin, false, nil, "invalid_argument"},
			} {
				_, e := s.KeysForRevocation(ctx, c.actor, c.wide, c.ids)
				wantErr(t, e, c.code)
				e = s.RevokeKeys(ctx, c.actor, c.wide, c.ids)
				wantErr(t, e, c.code)
				for _, sec := range secrets {
					if _, err := s.ResolveKey(ctx, sec); err != nil {
						t.Fatal("rejected selection revoked a key", err)
					}
				}
			}
			for i := 0; i < 2; i++ {
				if err := s.RevokeKeys(ctx, admin, true, ids[:2]); err != nil {
					t.Fatal(err)
				}
			}
			for _, sec := range secrets[:2] {
				_, err := s.ResolveKey(ctx, sec)
				wantErr(t, err, "unauthorized")
			}
			if _, err := s.ResolveKey(ctx, secrets[2]); err != nil {
				t.Fatal("affected another workspace", err)
			}
			audit, _, err := s.AuditPage(ctx, admin, 0, false, 25)
			if err != nil {
				t.Fatal(err)
			}
			revoked := map[string]int{}
			for _, e := range audit {
				if e.Action == "key_revoked" && e.Subject == admin.Subject {
					revoked[e.Target]++
				}
			}
			if len(revoked) != 2 || revoked[ids[0]] != 1 || revoked[ids[1]] != 1 {
				t.Fatal("incorrect audit on retry", revoked)
			}
		})
	}
}
