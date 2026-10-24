package app

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/MioYiSama/moonuestc/internal/school"
	"github.com/MioYiSama/moonuestc/internal/store"
	auth "yyb_go/moonuestcauth"
)

type Auth interface {
	StartQR(context.Context) (*auth.QR, error)
	PollQR(context.Context, *auth.QR) (string, error)
	ExchangeQR(context.Context, *auth.QR) (auth.Credentials, error)
	Refresh(context.Context, auth.Credentials) (auth.Credentials, error)
	Code(context.Context, auth.Credentials) (string, error)
	Forget(context.Context, string) error
}

type Options struct{ Now func() time.Time }

type Service struct {
	db               *store.Store
	auth             Auth
	school           *school.Client
	now              func() time.Time
	locks            accountLocks
	ctx              context.Context
	cancel           context.CancelFunc
	life             sync.Mutex
	stopped, started bool
	wg               sync.WaitGroup
	scanCreate       sync.Mutex
	scansMu          sync.Mutex
	scans            map[string]*scanState
	owners           map[string]string
}

func New(db *store.Store, authentication Auth, client *school.Client, opts Options) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		db: db, auth: authentication, school: client, now: now,
		ctx: ctx, cancel: cancel,
		scans: make(map[string]*scanState), owners: make(map[string]string),
	}
}

func (s *Service) launch(fn func()) bool {
	s.life.Lock()
	defer s.life.Unlock()
	if s.stopped {
		return false
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fn()
	}()
	return true
}

// Start launches maintenance and the daily calendar timer. NewScan also works
// before Start so isolated consumers can exercise the production boundary.
func (s *Service) Start() {
	s.life.Lock()
	if s.started || s.stopped {
		s.life.Unlock()
		return
	}
	s.started = true
	s.life.Unlock()
	s.launch(s.keepaliveLoop)
	s.launch(s.scheduleLoop)
	s.launch(s.scanCleanupLoop)
}

// Stop rejects new background work and cancels external calls without waiting.
func (s *Service) Stop() {
	s.life.Lock()
	s.stopped = true
	s.cancel()
	s.life.Unlock()
}

func (s *Service) Close() {
	s.Stop()
	s.wg.Wait()
	s.scansMu.Lock()
	s.scans = make(map[string]*scanState)
	s.owners = make(map[string]string)
	s.scansMu.Unlock()
}

func publicAccount(a store.Account) AccountView {
	return AccountView{
		ID: a.ID, SchoolUserID: a.SchoolUserID,
		SchoolUsername: a.SchoolUsername, SchoolName: a.SchoolName,
		CredentialStatus: a.CredentialStatus, LastRefreshAt: a.LastRefreshAt,
		ExpiresAt: a.Credentials.ExpiresAt, LastErrorStage: a.LastErrorStage,
		LastErrorCode: a.LastErrorCode, BusinessStatus: a.BusinessStatus,
		LastBusinessCheckAt: a.LastBusinessCheckAt, BusinessErrorCode: a.BusinessErrorCode,
	}
}

func (s *Service) Accounts(ctx context.Context) ([]AccountView, error) {
	accounts, err := s.db.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	statuses, err := s.db.DateStatuses(ctx, s.now().In(shanghai).Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	out := make([]AccountView, 0, len(accounts))
	for _, a := range accounts {
		view := publicAccount(a)
		view.TodayStatus = statuses[a.ID]
		out = append(out, view)
	}
	return out, nil
}

func (s *Service) Account(ctx context.Context, id int64) (AccountView, error) {
	account, err := s.db.Account(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return AccountView{}, ErrNotFound
	}
	return publicAccount(account), err
}

func (s *Service) Runs(ctx context.Context, id int64, limit int) ([]RunView, error) {
	runs, err := s.db.Runs(ctx, id, limit)
	if err != nil {
		return nil, err
	}
	out := make([]RunView, 0, len(runs))
	for _, r := range runs {
		out = append(out, RunView{
			ID: r.ID, AccountID: r.AccountID, SchoolName: r.SchoolName,
			SchoolUsername: r.SchoolUsername, BusinessDate: r.BusinessDate,
			Status: r.Status, Stage: r.Stage, ErrorCode: r.ErrorCode,
			Message:   r.Message,
			StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
		})
	}
	return out, nil
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	snapshot, err := s.db.Account(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	unlock, err := s.locks.acquire(ctx, snapshot.OpenID)
	if err != nil {
		return err
	}
	defer unlock()
	account, err := s.db.Account(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	// If subsequent persistence fails the durable account remains valid and can
	// rebuild this protocol cache on its next operation.
	if err := s.auth.Forget(ctx, account.OpenID); err != nil {
		return errors.New("protocol cache deletion failed")
	}
	return s.db.Delete(ctx, id)
}

func safeError(err error) (stage, code string) {
	var schoolError *school.Error
	if errors.As(err, &schoolError) {
		return schoolError.Stage, schoolError.Code
	}
	return "internal", "operation_failed"
}
