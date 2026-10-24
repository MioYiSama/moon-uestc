package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
	auth "yyb_go/moonuestcauth"
)

//go:embed schema.sql
var schema string

//go:embed migration_v1_to_v2.sql
var migrationV1ToV2 string
var ErrIdentity = errors.New("school identity changed")
var ErrDuplicate = errors.New("school account already added")

type Store struct {
	db   *sql.DB
	lock *os.File
}
type Account struct {
	ID                                                         int64
	OpenID                                                     string
	Credentials                                                auth.Credentials
	SchoolUserID, SchoolUsername, SchoolName, CredentialStatus string
	LastRefreshAt                                              *int64
	NextRefreshAt                                              int64
	LastErrorStage, LastErrorCode, BusinessStatus              string
	LastBusinessCheckAt                                        *int64
	BusinessErrorCode                                          string
	CreatedAt, UpdatedAt                                       int64
}
type Run struct {
	ID, AccountID                                                      int64
	SchoolName, SchoolUsername, BusinessDate, Status, Stage, ErrorCode string
	Message                                                            string
	StartedAt                                                          int64
	FinishedAt                                                         *int64
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "moonuestc.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("data directory already locked: %w", err)
	}
	s := &Store{lock: f}
	fail := func(e error) (*Store, error) { s.Close(); return nil, e }
	path := filepath.Join(dir, "moonuestc.db")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fail(err)
	}
	file.Close()
	if err = os.Chmod(path, 0600); err != nil {
		return fail(err)
	}
	s.db, err = sql.Open("sqlite", path)
	if err != nil {
		return fail(err)
	}
	s.db.SetMaxOpenConns(1)
	for _, q := range []string{"PRAGMA foreign_keys=ON", "PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000"} {
		if _, err = s.db.Exec(q); err != nil {
			return fail(err)
		}
	}
	var v int
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return fail(err)
	}
	if v > 2 {
		return fail(errors.New("unsupported database version"))
	}
	if v == 0 || v == 1 {
		migration := migrationV1ToV2
		if v == 0 {
			migration = schema
		}
		tx, e := s.db.Begin()
		if e != nil {
			return fail(e)
		}
		if _, e = tx.Exec(migration); e != nil {
			tx.Rollback()
			return fail(e)
		}
		if e = tx.Commit(); e != nil {
			return fail(e)
		}
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if e := os.Chmod(path+suffix, 0600); e != nil && !os.IsNotExist(e) {
			return fail(e)
		}
	}
	if _, err = s.db.Exec("UPDATE checkin_runs SET status='unknown', finished_at=? WHERE status='running'", time.Now().Unix()); err != nil {
		return fail(err)
	}
	return s, nil
}
func (s *Store) Close() error {
	var err error
	if s.db != nil {
		err = s.db.Close()
	}
	if s.lock != nil {
		unix.Flock(int(s.lock.Fd()), unix.LOCK_UN)
		s.lock.Close()
	}
	return err
}

const cols = `id,openid,credentials_json,school_user_id,school_username,school_name,credential_status,last_refresh_at,next_refresh_at,COALESCE(last_error_stage,''),COALESCE(last_error_code,''),business_status,last_business_check_at,COALESCE(business_error_code,''),created_at,updated_at`

type scanner interface{ Scan(...any) error }

func scanAccount(row scanner) (Account, error) {
	var a Account
	var raw string
	err := row.Scan(&a.ID, &a.OpenID, &raw, &a.SchoolUserID, &a.SchoolUsername, &a.SchoolName, &a.CredentialStatus, &a.LastRefreshAt, &a.NextRefreshAt, &a.LastErrorStage, &a.LastErrorCode, &a.BusinessStatus, &a.LastBusinessCheckAt, &a.BusinessErrorCode, &a.CreatedAt, &a.UpdatedAt)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &a.Credentials)
	}
	return a, err
}
func (s *Store) Account(ctx context.Context, id int64) (Account, error) {
	return scanAccount(s.db.QueryRowContext(ctx, "SELECT "+cols+" FROM accounts WHERE id=?", id))
}
func (s *Store) ByOpenID(ctx context.Context, id string) (Account, error) {
	return scanAccount(s.db.QueryRowContext(ctx, "SELECT "+cols+" FROM accounts WHERE openid=?", id))
}
func (s *Store) Accounts(ctx context.Context) ([]Account, error) {
	rows, e := s.db.QueryContext(ctx, "SELECT "+cols+" FROM accounts ORDER BY id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		a, e := scanAccount(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SaveBound uses a short transaction; no network operation belongs inside it.
func (s *Store) SaveBound(ctx context.Context, c auth.Credentials, userID, username, name string) (Account, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return Account{}, e
	}
	defer tx.Rollback()
	existing, e := scanAccount(tx.QueryRowContext(ctx, "SELECT "+cols+" FROM accounts WHERE openid=?", c.OpenID))
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return Account{}, e
	}
	if e == nil && existing.SchoolUserID != userID {
		return Account{}, ErrIdentity
	}
	var other int64
	e = tx.QueryRowContext(ctx, "SELECT id FROM accounts WHERE school_user_id=? AND openid<>?", userID, c.OpenID).Scan(&other)
	if e == nil {
		return Account{}, ErrDuplicate
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return Account{}, e
	}
	raw, e := json.Marshal(c)
	if e != nil {
		return Account{}, e
	}
	status := "healthy"
	if c.RefreshToken == "" {
		status = "needs_rescan"
	}
	now := time.Now().Unix()
	_, e = tx.ExecContext(ctx, `INSERT INTO accounts(openid,credentials_json,school_user_id,school_username,school_name,credential_status,next_refresh_at,business_status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,'unchecked',?,?) ON CONFLICT(openid) DO UPDATE SET credentials_json=excluded.credentials_json,school_username=excluded.school_username,school_name=excluded.school_name,credential_status=excluded.credential_status,next_refresh_at=excluded.next_refresh_at,last_error_stage=NULL,last_error_code=NULL,updated_at=excluded.updated_at`, c.OpenID, string(raw), userID, username, name, status, c.ExpiresAt-60, now, now)
	if e != nil {
		return Account{}, e
	}
	if e = tx.Commit(); e != nil {
		return Account{}, e
	}
	return s.ByOpenID(ctx, c.OpenID)
}
func (s *Store) SaveCredentials(ctx context.Context, id int64, c auth.Credentials, now int64) error {
	raw, e := json.Marshal(c)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `UPDATE accounts SET credentials_json=?,credential_status='healthy',last_refresh_at=?,next_refresh_at=?,last_error_stage=NULL,last_error_code=NULL,updated_at=? WHERE id=?`, string(raw), now, c.ExpiresAt-60, now, id)
	return e
}
func (s *Store) CredentialError(ctx context.Context, id int64, status, code string, next int64) error {
	_, e := s.db.ExecContext(ctx, `UPDATE accounts SET credential_status=?,last_error_stage='credential',last_error_code=?,next_refresh_at=?,updated_at=? WHERE id=?`, status, code, next, time.Now().Unix(), id)
	return e
}
func (s *Store) Business(ctx context.Context, id int64, status, code string) error {
	_, e := s.db.ExecContext(ctx, `UPDATE accounts SET business_status=?,business_error_code=?,last_business_check_at=?,updated_at=? WHERE id=?`, status, code, time.Now().Unix(), time.Now().Unix(), id)
	return e
}
func (s *Store) Delete(ctx context.Context, id int64) error {
	_, e := s.db.ExecContext(ctx, "DELETE FROM accounts WHERE id=?", id)
	return e
}
func (s *Store) Claim(ctx context.Context, id int64, date string) (int64, bool, error) {
	r, e := s.db.ExecContext(ctx, `INSERT INTO checkin_runs(account_id,business_date,status,stage,started_at) VALUES(?,?,'running','credential',?) ON CONFLICT(account_id,business_date) WHERE status IN ('running','success','already_done') DO NOTHING`, id, date, time.Now().Unix())
	if e != nil {
		return 0, false, e
	}
	n, e := r.RowsAffected()
	if e != nil || n == 0 {
		return 0, false, e
	}
	rid, e := r.LastInsertId()
	return rid, true, e
}
func (s *Store) Finish(ctx context.Context, id int64, status, stage, code, message string) error {
	_, e := s.db.ExecContext(ctx, "UPDATE checkin_runs SET status=?,stage=?,error_code=?,message=?,finished_at=? WHERE id=?", status, stage, code, message, time.Now().Unix(), id)
	return e
}
func (s *Store) Runs(ctx context.Context, accountID int64, limit int) ([]Run, error) {
	q := `SELECT r.id,r.account_id,a.school_name,a.school_username,r.business_date,r.status,r.stage,COALESCE(r.error_code,''),COALESCE(r.message,''),r.started_at,r.finished_at FROM checkin_runs r JOIN accounts a ON a.id=r.account_id`
	args := []any{}
	if accountID != 0 {
		q += " WHERE r.account_id=?"
		args = append(args, accountID)
	}
	q += " ORDER BY r.id DESC LIMIT ?"
	args = append(args, limit)
	rows, e := s.db.QueryContext(ctx, q, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var r Run
		if e = rows.Scan(&r.ID, &r.AccountID, &r.SchoolName, &r.SchoolUsername, &r.BusinessDate, &r.Status, &r.Stage, &r.ErrorCode, &r.Message, &r.StartedAt, &r.FinishedAt); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) DateStatuses(ctx context.Context, date string) (map[int64]string, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT r.account_id,r.status
 FROM checkin_runs r
 JOIN (SELECT account_id,MAX(id) AS id FROM checkin_runs WHERE business_date=? GROUP BY account_id) latest
 ON latest.id=r.id`, date)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var status string
		if e = rows.Scan(&id, &status); e != nil {
			return nil, e
		}
		out[id] = status
	}
	return out, rows.Err()
}
