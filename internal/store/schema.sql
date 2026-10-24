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
 message TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX checkin_runs_active_account_date
 ON checkin_runs(account_id,business_date)
 WHERE status IN ('running','success','already_done');
PRAGMA user_version=2;
