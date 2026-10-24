package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	auth "yyb_go/moonuestcauth"
)

func TestIdentityPersistenceAndRunClaim(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	if second, e := Open(dir); e == nil {
		second.Close()
		t.Fatal("second instance accepted")
	}
	c := auth.Credentials{OpenID: "local-test", AccessToken: "secret", RefreshToken: "refresh", ExpiresAt: 9999999999}
	a, e := s.SaveBound(ctx, c, "school-1", "2026001", "集成场景")
	if e != nil {
		t.Fatal(e)
	}
	c.AccessToken = "changed"
	if _, e = s.SaveBound(ctx, c, "school-2", "other", "other"); !errors.Is(e, ErrIdentity) {
		t.Fatalf("identity replacement: %v", e)
	}
	c.OpenID = "another"
	if _, e = s.SaveBound(ctx, c, "school-1", "same", "same"); !errors.Is(e, ErrDuplicate) {
		t.Fatalf("duplicate: %v", e)
	}
	runID, ok, e := s.Claim(ctx, a.ID, "2026-10-24")
	if e != nil || !ok {
		t.Fatalf("claim %v %v", ok, e)
	}
	if _, ok, e := s.Claim(ctx, a.ID, "2026-10-24"); e != nil || ok {
		t.Fatalf("duplicate running claim %v %v", ok, e)
	}
	if e = s.Finish(ctx, runID, "failed", "checkin", "network", "school unavailable"); e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	got, e := s.Account(ctx, a.ID)
	if e != nil || got.Credentials.AccessToken != "secret" {
		t.Fatalf("persisted identity credentials: %v", e)
	}
	runs, e := s.Runs(ctx, a.ID, 30)
	if e != nil || len(runs) != 1 || runs[0].Status != "failed" || runs[0].Message != "school unavailable" {
		t.Fatalf("persisted run: %#v %v", runs, e)
	}
	if _, ok, e := s.Claim(ctx, a.ID, "2026-10-24"); e != nil || !ok {
		t.Fatalf("failed run should allow retry: %v %v", ok, e)
	}
	if e = s.Delete(ctx, a.ID); e != nil {
		t.Fatal(e)
	}
	runs, e = s.Runs(ctx, 0, 100)
	if e != nil || len(runs) != 0 {
		t.Fatal("cascade", e)
	}
}

func TestClaimRetryPolicyAndMessageHistory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	a, err := s.SaveBound(ctx, auth.Credentials{OpenID: "retry-test"}, "retry-user", "retry-name", "Retry School")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		date   string
		status string
		retry  bool
	}{
		{"2026-11-01", "running", false},
		{"2026-11-02", "success", false},
		{"2026-11-03", "already_done", false},
		{"2026-11-04", "failed", true},
		{"2026-11-05", "skipped", true},
		{"2026-11-06", "unknown", true},
	}
	type expectedRun struct {
		date, status, message string
	}
	expectedRuns := make(map[int64]expectedRun)
	expectedDates := make(map[string]string)
	var runningID int64
	for _, tc := range cases {
		firstID, claimed, err := s.Claim(ctx, a.ID, tc.date)
		if err != nil || !claimed {
			t.Fatalf("first claim for %s: %v %v", tc.status, claimed, err)
		}
		firstMessage := ""
		if tc.status != "running" {
			firstMessage = "response-" + tc.status
			if err = s.Finish(ctx, firstID, tc.status, "checkin", "code-"+tc.status, firstMessage); err != nil {
				t.Fatal(err)
			}
		} else {
			runningID = firstID
		}
		expectedRuns[firstID] = expectedRun{tc.date, tc.status, firstMessage}
		expectedDates[tc.date] = tc.status

		retryID, retried, err := s.Claim(ctx, a.ID, tc.date)
		if err != nil || retried != tc.retry {
			t.Fatalf("retry after %s: got %v, want %v (err %v)", tc.status, retried, tc.retry, err)
		}
		if tc.retry {
			if retryID <= firstID {
				t.Fatalf("retry did not create a new history row: %d <= %d", retryID, firstID)
			}
			const retryMessage = "response after retry"
			if err = s.Finish(ctx, retryID, "success", "checkin", "", retryMessage); err != nil {
				t.Fatal(err)
			}
			expectedRuns[retryID] = expectedRun{tc.date, "success", retryMessage}
			expectedDates[tc.date] = "success"
		}
	}

	for _, tc := range cases {
		statuses, err := s.DateStatuses(ctx, tc.date)
		if err != nil || len(statuses) != 1 || statuses[a.ID] != expectedDates[tc.date] {
			t.Fatalf("latest status for %s: %#v, want %q (err %v)", tc.date, statuses, expectedDates[tc.date], err)
		}
	}
	runs, err := s.Runs(ctx, a.ID, 100)
	if err != nil || len(runs) != len(expectedRuns) {
		t.Fatalf("run history: %d rows, want %d (err %v)", len(runs), len(expectedRuns), err)
	}
	for _, run := range runs {
		want, ok := expectedRuns[run.ID]
		if !ok || run.BusinessDate != want.date || run.Status != want.status || run.Message != want.message {
			t.Fatalf("unexpected run history row: %#v, expected %#v", run, want)
		}
	}

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = nil
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	expectedRuns[runningID] = expectedRun{"2026-11-01", "unknown", ""}
	expectedDates["2026-11-01"] = "unknown"
	for _, tc := range cases {
		statuses, err := s.DateStatuses(ctx, tc.date)
		if err != nil || len(statuses) != 1 || statuses[a.ID] != expectedDates[tc.date] {
			t.Fatalf("restarted latest status for %s: %#v, want %q (err %v)", tc.date, statuses, expectedDates[tc.date], err)
		}
	}
	recoveredID, retried, err := s.Claim(ctx, a.ID, "2026-11-01")
	if err != nil || !retried {
		t.Fatalf("recovered running claim should allow retry: %v (err %v)", retried, err)
	}
	const recoveredMessage = "retry after restart"
	if err = s.Finish(ctx, recoveredID, "skipped", "checkin", "", recoveredMessage); err != nil {
		t.Fatal(err)
	}
	expectedRuns[recoveredID] = expectedRun{"2026-11-01", "skipped", recoveredMessage}
	expectedDates["2026-11-01"] = "skipped"
	statuses, err := s.DateStatuses(ctx, "2026-11-01")
	if err != nil || statuses[a.ID] != "skipped" {
		t.Fatalf("latest recovered retry status: %#v (err %v)", statuses, err)
	}
	runs, err = s.Runs(ctx, a.ID, 100)
	if err != nil || len(runs) != len(expectedRuns) {
		t.Fatalf("restarted run history: %d rows, want %d (err %v)", len(runs), len(expectedRuns), err)
	}
	for _, run := range runs {
		want, ok := expectedRuns[run.ID]
		if !ok || run.BusinessDate != want.date || run.Status != want.status || run.Message != want.message {
			t.Fatalf("unexpected restarted run row: %#v, expected %#v", run, want)
		}
	}
}

func TestV1MigrationPreservesHistoryAndRetryPolicy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	createV1Database(t, dir)

	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatalf("database version = %d, want 2 (err %v)", version, err)
	}
	account, err := s.Account(ctx, 7)
	if err != nil || account.OpenID != "legacy-openid" || account.SchoolUserID != "legacy-user" || account.SchoolName != "Legacy School" {
		t.Fatalf("legacy account was not preserved: %#v (err %v)", account, err)
	}
	runs, err := s.Runs(ctx, 7, 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("migrated history: %#v (err %v)", runs, err)
	}
	old := runs[0]
	if old.ID != 41 || old.BusinessDate != "2026-10-24" || old.Status != "failed" || old.Stage != "checkin" || old.ErrorCode != "legacy-code" || old.StartedAt != 123 || old.FinishedAt == nil || *old.FinishedAt != 456 || old.Message != "" {
		t.Fatalf("legacy run was not preserved: %#v", old)
	}
	retryID, claimed, err := s.Claim(ctx, 7, old.BusinessDate)
	if err != nil || !claimed || retryID <= old.ID {
		t.Fatalf("migrated failed run should allow retry: %d %v (err %v)", retryID, claimed, err)
	}
	if err = s.Finish(ctx, retryID, "failed", "checkin", "retry-code", "retry response"); err != nil {
		t.Fatal(err)
	}
	successID, claimed, err := s.Claim(ctx, 7, old.BusinessDate)
	if err != nil || !claimed {
		t.Fatalf("migrated failed retry should allow another try: %v (err %v)", claimed, err)
	}
	if err = s.Finish(ctx, successID, "success", "checkin", "", "success response"); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err = s.Claim(ctx, 7, old.BusinessDate); err != nil || claimed {
		t.Fatalf("successful attempt must block another claim: %v (err %v)", claimed, err)
	}
	statuses, err := s.DateStatuses(ctx, old.BusinessDate)
	if err != nil || statuses[7] != "success" {
		t.Fatalf("latest migrated status: %#v (err %v)", statuses, err)
	}
	runs, err = s.Runs(ctx, 7, 10)
	if err != nil || len(runs) != 3 {
		t.Fatalf("migrated attempt history: %#v (err %v)", runs, err)
	}
	if runs[0].ID != successID || runs[0].Status != "success" || runs[0].Message != "success response" ||
		runs[1].ID != retryID || runs[1].Status != "failed" || runs[1].Message != "retry response" ||
		runs[2].ID != old.ID || runs[2].Status != "failed" || runs[2].Message != "" {
		t.Fatalf("migrated history/message mismatch: %#v", runs)
	}
	if err = s.Delete(ctx, 7); err != nil {
		t.Fatal(err)
	}
	runs, err = s.Runs(ctx, 0, 10)
	if err != nil || len(runs) != 0 {
		t.Fatalf("foreign-key cascade after migration: %#v (err %v)", runs, err)
	}
}

func createV1Database(t *testing.T, dir string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "moonuestc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`PRAGMA foreign_keys=ON;
CREATE TABLE accounts (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 openid TEXT NOT NULL UNIQUE,
 credentials_json TEXT NOT NULL,
 school_user_id TEXT NOT NULL UNIQUE,
 school_username TEXT NOT NULL,
 school_name TEXT NOT NULL,
 credential_status TEXT NOT NULL CHECK(credential_status IN ('healthy','error','needs_rescan')),
 last_refresh_at INTEGER,
 next_refresh_at INTEGER NOT NULL,
 last_error_stage TEXT,
 last_error_code TEXT,
 business_status TEXT NOT NULL CHECK(business_status IN ('unchecked','ready','rejected')),
 last_business_check_at INTEGER,
 business_error_code TEXT,
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
);
CREATE TABLE checkin_runs (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 business_date TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('running','success','already_done','skipped','failed','unknown')),
 stage TEXT NOT NULL,
 error_code TEXT,
 started_at INTEGER NOT NULL,
 finished_at INTEGER,
 UNIQUE(account_id,business_date)
);
PRAGMA user_version=1;
INSERT INTO accounts(id,openid,credentials_json,school_user_id,school_username,school_name,credential_status,next_refresh_at,business_status,created_at,updated_at)
 VALUES(7,'legacy-openid','{}','legacy-user','legacy-name','Legacy School','healthy',0,'unchecked',10,11);
INSERT INTO checkin_runs(id,account_id,business_date,status,stage,error_code,started_at,finished_at)
 VALUES(41,7,'2026-10-24','failed','checkin','legacy-code',123,456);`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO checkin_runs(account_id,business_date,status,stage,started_at)
 VALUES(7,'2026-10-24','failed','checkin',789)`)
	if err == nil {
		t.Fatal("fixture did not enforce the v1 account/date unique constraint")
	}
}
