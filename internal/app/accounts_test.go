package app

import (
	"context"
	"testing"
)

func TestScanBindingAndIdentity(t *testing.T) {
	t.Run("unbound", func(t *testing.T) {
		f := &fakeAuth{}
		s, db, u, _ := setup(t, f)
		u.unbound["a"] = "yes"
		v := scanComplete(t, s, f, credentials("a"), 0)
		as, _ := db.Accounts(context.Background())
		if v.Status != "unbound" || len(as) != 0 || u.posts != 0 || len(u.codes) != 1 {
			t.Fatalf("unbound persisted or continued: %#v", v)
		}
	})
	t.Run("missing session", func(t *testing.T) {
		f := &fakeAuth{}
		s, db, u, _ := setup(t, f)
		u.missingSession["a"] = "yes"
		v := scanComplete(t, s, f, credentials("a"), 0)
		as, _ := db.Accounts(context.Background())
		if v.Status != "failed" || len(as) != 0 || u.posts != 0 || len(u.codes) != 1 {
			t.Fatalf("missing SESSION continued: %#v", v)
		}
	})
	t.Run("duplicate school identity", func(t *testing.T) {
		f := &fakeAuth{}
		s, db, u, _ := setup(t, f)
		bound(t, db, "a")
		u.identities["b"] = "school-a"
		v := scanComplete(t, s, f, credentials("b"), 0)
		as, _ := db.Accounts(context.Background())
		if v.Status != "failed" || len(as) != 1 || as[0].OpenID != "a" || u.posts != 0 {
			t.Fatalf("duplicate accepted: %#v", v)
		}
	})
	t.Run("target openid rejected before code", func(t *testing.T) {
		f := &fakeAuth{}
		s, db, _, _ := setup(t, f)
		a := bound(t, db, "a")
		v := scanComplete(t, s, f, credentials("b"), a.ID)
		got, _ := db.Account(context.Background(), a.ID)
		if v.Status != "failed" || f.codes != 0 || got.Credentials.AccessToken != "local-access" {
			t.Fatal("wrong WeChat replaced existing account")
		}
	})
	t.Run("school identity change preserves credentials", func(t *testing.T) {
		f := &fakeAuth{}
		s, db, u, _ := setup(t, f)
		a := bound(t, db, "a")
		u.identities["a"] = "different-school"
		c := credentials("a")
		c.AccessToken = "new-access"
		v := scanComplete(t, s, f, c, a.ID)
		got, _ := db.Account(context.Background(), a.ID)
		if v.Status != "failed" || got.Credentials.AccessToken != "local-access" || u.posts != 0 {
			t.Fatal("identity change replaced credentials")
		}
	})
	t.Run("bound add is read only", func(t *testing.T) {
		f := &fakeAuth{}
		s, db, u, _ := setup(t, f)
		v := scanComplete(t, s, f, credentials("a"), 0)
		as, _ := db.Accounts(context.Background())
		runs, _ := db.Runs(context.Background(), 0, 100)
		if v.Status != "completed" || len(as) != 1 || as[0].BusinessStatus != "ready" || u.posts != 0 || len(runs) != 0 || len(u.codes) != 1 {
			t.Fatalf("add wrote checkin or failed: %#v %#v", v, as)
		}
	})
	t.Run("missing refresh token is explicit", func(t *testing.T) {
		f := &fakeAuth{}
		s, db, _, _ := setup(t, f)
		c := credentials("a")
		c.RefreshToken = ""
		v := scanComplete(t, s, f, c, 0)
		a, _ := db.ByOpenID(context.Background(), "a")
		if v.Status != "completed" || a.CredentialStatus != "needs_rescan" {
			t.Fatal("missing refresh token claimed healthy")
		}
	})
	t.Run("business rejection keeps new credentials", func(t *testing.T) {
		f := &fakeAuth{}
		s, db, u, _ := setup(t, f)
		a := bound(t, db, "a")
		u.businessReject = true
		c := credentials("a")
		c.AccessToken = "newly-authorized"
		v := scanComplete(t, s, f, c, a.ID)
		got, _ := db.Account(context.Background(), a.ID)
		if v.Status != "completed" || got.Credentials.AccessToken != "newly-authorized" || got.BusinessStatus != "rejected" || u.posts != 0 || len(u.codes) != 1 {
			t.Fatal("business rejection rolled back authorization or continued to ticket")
		}
	})
}
