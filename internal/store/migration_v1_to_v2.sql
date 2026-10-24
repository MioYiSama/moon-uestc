CREATE TABLE checkin_runs_v2 (
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
INSERT INTO checkin_runs_v2(id,account_id,business_date,status,stage,error_code,started_at,finished_at,message)
 SELECT id,account_id,business_date,status,stage,error_code,started_at,finished_at,'' FROM checkin_runs;
DROP TABLE checkin_runs;
ALTER TABLE checkin_runs_v2 RENAME TO checkin_runs;
CREATE UNIQUE INDEX checkin_runs_active_account_date
 ON checkin_runs(account_id,business_date)
 WHERE status IN ('running','success','already_done');
PRAGMA user_version=2;
