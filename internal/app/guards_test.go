package app

import (
	"context"
	"testing"
	"time"
)

func TestCrossDayRunAndNeedsRescanNeverWrite(t *testing.T) {
	t.Run("cross day", func(t *testing.T) {
		service, db, remote, _ := setup(t, &fakeAuth{})
		bound(t, db, "a")
		if err := service.RunDaily(context.Background(), testNow.AddDate(0, 0, -1)); err != nil {
			t.Fatal(err)
		}
		runs, err := db.Runs(context.Background(), 0, 100)
		if err != nil || len(runs) != 0 || remote.posts != 0 {
			t.Fatalf("cross-day delayed task ran: runs=%v error=%v", runs, err)
		}
	})
	t.Run("rescan required", func(t *testing.T) {
		authentication := &fakeAuth{}
		service, db, remote, _ := setup(t, authentication)
		credentials := credentials("a")
		credentials.RefreshToken = ""
		credentials.ExpiresAt = testNow.Add(-time.Hour).Unix()
		account, err := db.SaveBound(context.Background(), credentials, "school-a", "2026-a", "集成场景 a")
		if err != nil {
			t.Fatal(err)
		}
		if err := service.RunDaily(context.Background(), testNow); err != nil {
			t.Fatal(err)
		}
		runs, err := db.Runs(context.Background(), account.ID, 30)
		if err != nil || len(runs) != 1 || runs[0].Status != "skipped" || runs[0].Stage != "credential" || authentication.codes != 0 || remote.posts != 0 {
			t.Fatalf("needs-rescan account reached authentication or write: runs=%v error=%v", runs, err)
		}
	})
}
