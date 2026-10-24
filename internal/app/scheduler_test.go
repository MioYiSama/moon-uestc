package app

import (
	"context"
	"errors"
	"github.com/MioYiSama/moonuestc/internal/store"
	"sync"
	"testing"
	"time"
)

func TestNextCheckinShanghai(t *testing.T) {
	t.Setenv("TZ", "UTC")
	for _, tc := range []struct{ in, want string }{{"2026-10-24T13:29:59Z", "2026-10-24T13:30:00Z"}, {"2026-10-24T13:30:00Z", "2026-10-24T13:30:00Z"}, {"2026-10-24T13:30:01Z", "2026-10-25T13:30:00Z"}, {"2026-12-31T13:30:01Z", "2027-01-01T13:30:00Z"}} {
		in, _ := time.Parse(time.RFC3339, tc.in)
		if got := NextCheckin(in).UTC().Format(time.RFC3339); got != tc.want {
			t.Fatalf("%s -> %s want %s", tc.in, got, tc.want)
		}
	}
}
func TestDailyRunIsolationAndDeduplication(t *testing.T) {
	f := &fakeAuth{}
	s, db, u, _ := setup(t, f)
	a := bound(t, db, "a")
	b := bound(t, db, "b")
	u.bindReject["a"] = "yes"
	if e := s.RunDaily(context.Background(), testNow); e != nil {
		t.Fatal(e)
	}
	rs, _ := db.Runs(context.Background(), 0, 100)
	states := map[int64]string{}
	for _, r := range rs {
		states[r.AccountID] = r.Status
	}
	if states[a.ID] != "failed" || states[b.ID] != "success" || u.posts != 1 {
		t.Fatalf("isolation %#v posts=%d", states, u.posts)
	}
	if len(u.codes) != 3 || u.codes[1] == u.codes[2] {
		t.Fatal("login code reused for ticket")
	}
	u.mu.Lock()
	delete(u.bindReject, "a")
	u.mu.Unlock()
	if e := s.RunDaily(context.Background(), testNow); e != nil {
		t.Fatal(e)
	}
	if u.posts != 2 {
		t.Fatal("failed account did not retry or successful account replayed")
	}
	if u.payload["reverseGeocode"] != "本地上游实际地址" || u.payload["latitude"] != 30.67449967 || u.payload["longitude"] != 104.09629583 || u.payload["clientTime"] != "2026-10-24 21:30:00" || u.payload["coordinateSystemCode"] != "gcj02" {
		t.Fatalf("wrong checkin payload %#v", u.payload)
	}
	rs, err := db.Runs(context.Background(), a.ID, 30)
	if err != nil || len(rs) != 2 || rs[0].Status != "success" || rs[1].Status != "failed" || rs[1].Message != "绑定校验未通过" {
		t.Fatalf("retry history: %#v, %v", rs, err)
	}
}
func TestAmbiguousWriteRecovery(t *testing.T) {
	for _, observed := range []string{"", "CHECKED_IN"} {
		t.Run("today_"+observed, func(t *testing.T) {
			f := &fakeAuth{}
			s, db, u, dir := setup(t, f)
			a := bound(t, db, "a")
			u.ambiguous = true
			if err := s.RunCheckin(context.Background(), a.ID); err != nil {
				t.Fatal(err)
			}
			s.Close()
			db.Close()
			reopened, err := store.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			s2 := New(reopened, f, s.school, Options{Now: func() time.Time { return testNow }})
			defer s2.Close()
			u.mu.Lock()
			u.ambiguous = false
			u.todayStatus = observed
			u.mu.Unlock()
			if err := s2.RunDaily(context.Background(), testNow); err != nil {
				t.Fatal(err)
			}
			if err := s2.RunCheckin(context.Background(), a.ID); err != nil {
				t.Fatal(err)
			}
			runs, err := reopened.Runs(context.Background(), a.ID, 30)
			wantStatus, wantPosts, wantMessage := "success", 2, "签到成功"
			if observed == "CHECKED_IN" {
				wantStatus, wantPosts, wantMessage = "already_done", 1, "当天状态已查询"
			}
			u.mu.Lock()
			posts := u.posts
			u.mu.Unlock()
			if err != nil || len(runs) != 2 || runs[0].Status != wantStatus || runs[0].Message != wantMessage || runs[1].Status != "unknown" || posts != wantPosts {
				t.Fatalf("recovery: %#v, posts=%d, err=%v", runs, posts, err)
			}
		})
	}
}
func TestTodayStatesStopWrites(t *testing.T) {
	for _, tc := range []struct{ status, date, want string }{{"CHECKED_IN", "", "already_done"}, {"STUDENT_REPORTED", "", "skipped"}, {"INSTRUCTOR_REPORTED", "", "skipped"}, {"EXEMPT", "", "skipped"}, {"UNKNOWN", "", "failed"}, {"PENDING", "2026-10-23", "failed"}} {
		t.Run(tc.status+tc.date, func(t *testing.T) {
			f := &fakeAuth{}
			s, db, u, _ := setup(t, f)
			a := bound(t, db, "a")
			u.todayStatus = tc.status
			u.todayDate = tc.date
			if e := s.RunDaily(context.Background(), testNow); e != nil {
				t.Fatal(e)
			}
			rs, _ := db.Runs(context.Background(), a.ID, 30)
			if len(rs) != 1 || rs[0].Status != tc.want || u.posts != 0 {
				t.Fatalf("Today guard: %#v posts=%d", rs, u.posts)
			}
		})
	}
}

func TestManualCheckinSharesDailyClaim(t *testing.T) {
	s, db, u, _ := setup(t, &fakeAuth{})
	// UTC is still the previous date; manual runs use today's Shanghai date
	// and do not wait for 21:30.
	now := time.Date(2026, 10, 23, 17, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	a := bound(t, db, "a")
	b := bound(t, db, "b")
	if err := s.RunCheckin(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	runs, err := db.Runs(context.Background(), 0, 100)
	if err != nil || len(runs) != 1 || runs[0].AccountID != a.ID || runs[0].BusinessDate != "2026-10-24" || runs[0].Status != "success" {
		t.Fatalf("manual account/date/result: %#v, %v", runs, err)
	}
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			if err := s.RunCheckin(context.Background(), a.ID); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Go(func() {
		if err := s.RunDaily(context.Background(), now); err != nil {
			t.Error(err)
		}
	})
	workers.Wait()
	runs, err = db.Runs(context.Background(), b.ID, 100)
	u.mu.Lock()
	posts := u.posts
	u.mu.Unlock()
	if err != nil || len(runs) != 1 || runs[0].Status != "success" || posts != 2 {
		t.Fatalf("daily/manual dedup: %#v, posts=%d, err=%v", runs, posts, err)
	}
	if err := s.RunCheckin(context.Background(), b.ID+1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing account: %v", err)
	}
	s.Stop()
	if err := s.RunCheckin(context.Background(), a.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped service: %v", err)
	}
}

func TestUnsuccessfulManualCheckinRetriesAtNight(t *testing.T) {
	for _, initial := range []string{"failed", "skipped"} {
		t.Run(initial, func(t *testing.T) {
			s, db, u, _ := setup(t, &fakeAuth{})
			a := bound(t, db, "a")
			now := testNow.Add(-12 * time.Hour)
			s.now = func() time.Time { return now }
			u.checkinReject = initial == "failed"
			if initial == "skipped" {
				u.todayStatus = "EXEMPT"
			}
			if err := s.RunCheckin(context.Background(), a.ID); err != nil {
				t.Fatal(err)
			}
			now = testNow
			u.mu.Lock()
			u.checkinReject, u.todayStatus = false, ""
			u.mu.Unlock()
			if err := s.RunDaily(context.Background(), now); err != nil {
				t.Fatal(err)
			}
			if err := s.RunCheckin(context.Background(), a.ID); err != nil {
				t.Fatal(err)
			}
			runs, err := db.Runs(context.Background(), a.ID, 30)
			firstMessage := "未到签到时间"
			if initial == "skipped" {
				firstMessage = "当天状态已查询"
			}
			if err != nil || len(runs) != 2 || runs[0].Status != "success" || runs[0].Message != "签到成功" || runs[1].Status != initial || runs[1].Message != firstMessage {
				t.Fatalf("manual failure/night retry history: %#v, %v", runs, err)
			}
		})
	}
}
