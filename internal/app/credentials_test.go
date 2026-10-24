package app

import (
	"context"
	"errors"
	"github.com/MioYiSama/moonuestc/internal/store"
	"sync"
	"testing"
	"time"
	auth "yyb_go/moonuestcauth"
)

func TestCredentialRotationSurvivesCodeFailure(t *testing.T) {
	f := &fakeAuth{codeFailure: true}
	s, db, _, dir := setup(t, f)
	c := credentials("a")
	c.ExpiresAt = testNow.Add(-time.Minute).Unix()
	a, e := db.SaveBound(context.Background(), c, "school-a", "2026-a", "集成场景 a")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.RunDaily(context.Background(), testNow); e != nil {
		t.Fatal(e)
	}
	s.Close()
	db.Close()
	reopened, e := store.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	got, e := reopened.Account(context.Background(), a.ID)
	if e != nil || got.Credentials.AccessToken != "rotated-access" || got.Credentials.RefreshToken != "rotated-refresh" || got.Credentials.RefreshTokenObservedAt != c.RefreshTokenObservedAt {
		t.Fatalf("rotation lost after code failure: %v", e)
	}
}
func TestRefreshSerialization(t *testing.T) {
	f := &fakeAuth{}
	s, db, _, _ := setup(t, f)
	c := credentials("a")
	c.ExpiresAt = testNow.Add(-time.Minute).Unix()
	a, e := db.SaveBound(context.Background(), c, "school-a", "2026-a", "集成场景 a")
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = s.Keepalive(context.Background()) }()
	go func() { defer wg.Done(); _ = s.RunDaily(context.Background(), testNow) }()
	wg.Wait()
	got, _ := db.Account(context.Background(), a.ID)
	if f.maxActive != 1 || f.refreshes != 1 || got.Credentials.AccessToken != "rotated-access" {
		t.Fatalf("refresh race: active=%d calls=%d", f.maxActive, f.refreshes)
	}
}
func TestRefreshFailuresPreserveCredentials(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status string
	}{{"network", errors.New("network failure"), "error"}, {"revoked", &auth.RefreshRejectedError{Code: 40001}, "needs_rescan"}} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAuth{refreshErr: tc.err}
			s, db, _, _ := setup(t, f)
			c := credentials("a")
			c.ExpiresAt = testNow.Add(-time.Minute).Unix()
			a, e := db.SaveBound(context.Background(), c, "school-a", "2026-a", "集成场景 a")
			if e != nil {
				t.Fatal(e)
			}
			_ = s.Keepalive(context.Background())
			got, _ := db.Account(context.Background(), a.ID)
			if got.CredentialStatus != tc.status || got.Credentials.AccessToken != c.AccessToken || got.Credentials.RefreshToken != c.RefreshToken {
				t.Fatal("refresh failure destroyed credentials")
			}
			if tc.status == "error" && got.NextRefreshAt != testNow.Add(time.Minute).Unix() {
				t.Fatal("transient failure did not defer maintenance")
			}
		})
	}
}
