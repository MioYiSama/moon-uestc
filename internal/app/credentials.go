package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/MioYiSama/moonuestc/internal/store"
	auth "yyb_go/moonuestcauth"
)

var errNeedsRescan = errors.New("credentials need rescan")

// prepare requires the openid lock. Rotation is committed before fetching a
// login-buffer or code, including when that subsequent operation fails.
func (s *Service) prepare(ctx context.Context, a store.Account) (store.Account, error) {
	if a.CredentialStatus == "needs_rescan" {
		return a, errNeedsRescan
	}
	if a.Credentials.ExpiresAt > s.now().Add(time.Minute).Unix() {
		return a, nil
	}
	if a.Credentials.RefreshToken == "" {
		if err := s.db.CredentialError(ctx, a.ID, "needs_rescan", "missing_refresh_token", 0); err != nil {
			return a, err
		}
		return a, errNeedsRescan
	}
	credentials, err := s.auth.Refresh(ctx, a.Credentials)
	if err != nil {
		status, code := "error", "refresh_unavailable"
		var rejected *auth.RefreshRejectedError
		if errors.As(err, &rejected) || errors.Is(err, auth.ErrMissingRefreshToken) {
			status, code = "needs_rescan", "refresh_rejected"
		}
		if saveErr := s.db.CredentialError(ctx, a.ID, status, code, s.now().Add(time.Minute).Unix()); saveErr != nil {
			return a, saveErr
		}
		if status == "needs_rescan" {
			return a, errNeedsRescan
		}
		return a, errors.New("credential refresh unavailable")
	}
	if credentials.OpenID != a.OpenID || credentials.AccessToken == "" {
		return a, errors.New("invalid credential rotation")
	}
	credentials.RefreshTokenObservedAt = a.Credentials.RefreshTokenObservedAt
	if err := s.db.SaveCredentials(ctx, a.ID, credentials, s.now().Unix()); err != nil {
		return a, err
	}
	a.Credentials, a.CredentialStatus = credentials, "healthy"
	return a, nil
}

// Keepalive reloads every due account after taking its lock. One slow account
// doesn't prevent maintenance of independent accounts.
func (s *Service) Keepalive(ctx context.Context) error {
	accounts, err := s.db.Accounts(ctx)
	if err != nil {
		return err
	}
	jobs := make(chan store.Account)
	var workers sync.WaitGroup
	for range min(4, len(accounts)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for snapshot := range jobs {
				unlock, err := s.locks.acquire(ctx, snapshot.OpenID)
				if err != nil {
					continue
				}
				account, err := s.db.Account(ctx, snapshot.ID)
				if err == nil && account.CredentialStatus != "needs_rescan" && account.NextRefreshAt <= s.now().Unix() {
					_, _ = s.prepare(ctx, account)
				}
				unlock()
			}
		}()
	}
	for _, account := range accounts {
		if account.CredentialStatus == "needs_rescan" || account.NextRefreshAt > s.now().Unix() {
			continue
		}
		select {
		case jobs <- account:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return ctx.Err()
		}
	}
	close(jobs)
	workers.Wait()
	return nil
}

func (s *Service) keepaliveLoop() {
	_ = s.Keepalive(s.ctx)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			_ = s.Keepalive(s.ctx)
		}
	}
}
