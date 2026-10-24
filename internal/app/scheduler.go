package app

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"
	_ "time/tzdata"

	"github.com/MioYiSama/moonuestc/internal/school"
)

var shanghai = func() *time.Location {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		panic(err)
	}
	return location
}()

// NextCheckin uses the Shanghai calendar, including the exact 21:30 boundary.
func NextCheckin(now time.Time) time.Time {
	local := now.In(shanghai)
	target := time.Date(local.Year(), local.Month(), local.Day(), 21, 30, 0, 0, shanghai)
	if local.After(target) {
		target = time.Date(local.Year(), local.Month(), local.Day()+1, 21, 30, 0, 0, shanghai)
	}
	return target
}

func (s *Service) scheduleLoop() {
	target := NextCheckin(s.now())
	for {
		timer := time.NewTimer(max(0, target.Sub(s.now())))
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		now := s.now()
		if now.Before(target) {
			continue
		}
		if now.In(shanghai).Format("2006-01-02") == target.In(shanghai).Format("2006-01-02") {
			_ = s.RunDaily(s.ctx, target)
		}
		// Calendar advancement prevents a clock rollback from executing the same
		// timer target twice. Clock jumps never backfill missed business dates.
		base := s.now()
		if base.Before(target) {
			base = target
		}
		target = NextCheckin(base.Add(time.Nanosecond))
	}
}

func (s *Service) RunDaily(ctx context.Context, at time.Time) error {
	s.life.Lock()
	if s.stopped {
		s.life.Unlock()
		return context.Canceled
	}
	s.wg.Add(1)
	s.life.Unlock()
	defer s.wg.Done()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	date := at.In(shanghai).Format("2006-01-02")
	if s.now().In(shanghai).Format("2006-01-02") != date {
		return nil
	}
	accounts, err := s.db.Accounts(ctx)
	if err != nil {
		return err
	}
	jobs := make(chan int64)
	var workers sync.WaitGroup
	var mu sync.Mutex
	var failures []error
	for range min(4, len(accounts)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for id := range jobs {
				if err := s.runAccount(ctx, id, date); err != nil {
					mu.Lock()
					failures = append(failures, err)
					mu.Unlock()
				}
			}
		}()
	}
	for _, account := range accounts {
		select {
		case jobs <- account.ID:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return ctx.Err()
		}
	}
	close(jobs)
	workers.Wait()
	return errors.Join(failures...)
}

// RunCheckin executes one account for today's Shanghai date, sharing the daily
// scheduler's lock, durable claim and upstream safety checks.
func (s *Service) RunCheckin(ctx context.Context, id int64) error {
	s.life.Lock()
	if s.stopped {
		s.life.Unlock()
		return context.Canceled
	}
	s.wg.Add(1)
	s.life.Unlock()
	defer s.wg.Done()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	if _, err := s.Account(ctx, id); err != nil {
		return err
	}
	return s.runAccount(ctx, id, s.now().In(shanghai).Format("2006-01-02"))
}

func (s *Service) runAccount(parent context.Context, id int64, date string) error {
	ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
	defer cancel()
	snapshot, err := s.db.Account(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
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
		return nil
	}
	if err != nil {
		return err
	}
	runID, claimed, err := s.db.Claim(ctx, id, date)
	if err != nil || !claimed {
		return err
	}
	message := ""
	finish := func(status, stage, code string) error {
		save, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return s.db.Finish(save, runID, status, stage, code, message)
	}
	failure := func(stage string, err error) error {
		_, code := safeError(err)
		message = ""
		var responseError *school.Error
		if errors.As(err, &responseError) {
			message = responseError.Message
		}
		return finish("failed", stage, code)
	}
	account, err = s.prepare(ctx, account)
	if err != nil {
		if errors.Is(err, errNeedsRescan) {
			return finish("skipped", "credential", "needs_rescan")
		}
		return failure("credential", err)
	}
	code, err := s.auth.Code(ctx, account.Credentials)
	if err != nil {
		return failure("code", err)
	}
	mini, bound, message, err := s.school.CheckBind(ctx, code)
	if err != nil {
		return failure("check_bind", err)
	}
	if !bound {
		return finish("failed", "check_bind", "unbound")
	}
	message = ""
	user, err := mini.MiniUserInfo(ctx)
	if err != nil {
		return failure("mini_identity", err)
	}
	if user.ID != account.SchoolUserID {
		return finish("failed", "mini_identity", "identity_mismatch")
	}
	business, err := s.school.OpenBusinessSession(ctx, mini, account.SchoolUserID)
	if err != nil {
		_, code := safeError(err)
		if saveErr := s.db.Business(ctx, id, "rejected", code); saveErr != nil {
			return failure("business_session", saveErr)
		}
		return failure("business_session", err)
	}
	today, err := business.Today(ctx)
	if err != nil {
		_, code := safeError(err)
		if saveErr := s.db.Business(ctx, id, "rejected", code); saveErr != nil {
			return failure("today", saveErr)
		}
		return failure("today", err)
	}
	if today != nil {
		message = today.Message
		invalid := ""
		if today.BusinessDate != date {
			invalid = "date_mismatch"
		} else {
			switch today.RecordStatusCode {
			case "CHECKED_IN", "STUDENT_REPORTED", "INSTRUCTOR_REPORTED", "EXEMPT", "PENDING":
			default:
				invalid = "unknown_record_status"
			}
		}
		if invalid != "" {
			if err := s.db.Business(ctx, id, "rejected", invalid); err != nil {
				return failure("today", err)
			}
			return finish("failed", "today", invalid)
		}
	}
	if err := s.db.Business(ctx, id, "ready", ""); err != nil {
		return failure("today", err)
	}
	if today != nil {
		switch today.RecordStatusCode {
		case "CHECKED_IN":
			return finish("already_done", "today", "")
		case "STUDENT_REPORTED", "INSTRUCTOR_REPORTED", "EXEMPT":
			return finish("skipped", "today", today.RecordStatusCode)
		}
	}
	message = ""
	code, err = s.auth.Code(ctx, account.Credentials)
	if err != nil {
		return failure("ticket_code", err)
	}
	ticket, err := mini.Ticket(ctx, code)
	if err != nil {
		return failure("ticket", err)
	}
	address, err := business.Reverse(ctx)
	if err != nil {
		return failure("reverse", err)
	}
	if s.now().In(shanghai).Format("2006-01-02") != date {
		return finish("skipped", "checkin", "date_changed")
	}
	message, err = business.Checkin(ctx, ticket, address, date, s.now())
	if err != nil {
		var schoolError *school.Error
		if errors.As(err, &schoolError) {
			message = schoolError.Message
			if schoolError.Category == "date_changed" {
				return finish("skipped", "checkin", schoolError.Code)
			}
			if schoolError.Ambiguous {
				return finish("unknown", "checkin", schoolError.Code)
			}
		}
		return failure("checkin", err)
	}
	return finish("success", "checkin", "")
}
