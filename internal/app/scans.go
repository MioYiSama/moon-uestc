package app

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/MioYiSama/moonuestc/internal/store"
	auth "yyb_go/moonuestcauth"
)

const scanTTL = 5 * time.Minute

type scanState struct {
	view   ScanView
	owner  string
	qr     *auth.QR
	cancel context.CancelFunc
}

func (s *Service) NewScan(ctx context.Context, owner string, target int64) (ScanView, error) {
	s.scanCreate.Lock()
	defer s.scanCreate.Unlock()
	var targetName string
	if target != 0 {
		a, e := s.db.Account(ctx, target)
		if errors.Is(e, sql.ErrNoRows) {
			return ScanView{}, ErrNotFound
		}
		if e != nil {
			return ScanView{}, e
		}
		targetName = a.SchoolName + " · " + a.SchoolUsername
	}
	s.scansMu.Lock()
	if old := s.scans[s.owners[owner]]; old != nil && !old.view.Terminal {
		old.cancel()
		old.view.Status = "cancelled"
		old.view.Message = "扫码已取消"
		old.view.Terminal = true
	}
	s.scansMu.Unlock()
	request, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	q, e := s.auth.StartQR(request)
	if e != nil {
		return ScanView{}, errors.New("二维码获取失败")
	}
	scanCtx, scanCancel := context.WithTimeout(s.ctx, scanTTL)
	state := &scanState{view: ScanView{ID: q.ID, Status: "pending", Message: "请使用微信扫码", TargetID: target, TargetName: targetName, CreatedAt: s.now()}, owner: owner, qr: q, cancel: scanCancel}
	s.scansMu.Lock()
	s.scans[q.ID] = state
	s.owners[owner] = q.ID
	s.scansMu.Unlock()
	if !s.launch(func() { s.pollScan(scanCtx, state) }) {
		scanCancel()
		s.setScan(state, "cancelled", "服务正在关闭", 0)
		return ScanView{}, errors.New("service closed")
	}
	return s.Scan(owner, q.ID)
}
func (s *Service) Scan(owner, id string) (ScanView, error) {
	s.scansMu.Lock()
	defer s.scansMu.Unlock()
	state := s.scans[id]
	if state == nil || state.owner != owner {
		return ScanView{}, ErrNotFound
	}
	if s.now().Sub(state.view.CreatedAt) >= scanTTL {
		return ScanView{}, ErrExpired
	}
	return state.view, nil
}
func (s *Service) ScanImage(owner, id string) ([]byte, error) {
	s.scansMu.Lock()
	defer s.scansMu.Unlock()
	state := s.scans[id]
	if state == nil || state.owner != owner {
		return nil, ErrNotFound
	}
	if s.now().Sub(state.view.CreatedAt) >= scanTTL {
		return nil, ErrExpired
	}
	if state.qr == nil || state.view.Terminal {
		return nil, ErrNotFound
	}
	return append([]byte(nil), state.qr.Image...), nil
}
func (s *Service) CancelScan(owner, id string) error {
	s.scansMu.Lock()
	defer s.scansMu.Unlock()
	state := s.scans[id]
	if state == nil || state.owner != owner {
		return ErrNotFound
	}
	if !state.view.Terminal {
		state.view.Status = "cancelled"
		state.view.Message = "扫码已取消"
		state.view.Terminal = true
		state.cancel()
	}
	return nil
}
func (s *Service) setScan(state *scanState, status, message string, accountID int64) {
	s.scansMu.Lock()
	defer s.scansMu.Unlock()
	if state.view.Terminal {
		return
	}
	state.view.Status = status
	state.view.Message = message
	state.view.AccountID = accountID
	switch status {
	case "completed", "unbound", "cancelled", "expired", "failed":
		state.view.Terminal = true
		state.qr = nil
	}
}
func (s *Service) pollScan(ctx context.Context, state *scanState) {
	// Only this worker touches the upstream QR object. Browser polling reads a
	// public snapshot, never starts another WeChat long-poll.
	s.scansMu.Lock()
	q := state.qr
	s.scansMu.Unlock()
	defer func() {
		contextErr := ctx.Err()
		state.cancel()
		s.scansMu.Lock()
		defer s.scansMu.Unlock()
		state.qr = nil
		if contextErr != nil && state.view.Status != "completed" && state.view.Status != "cancelled" {
			state.view.Status = "cancelled"
			state.view.Message = "扫码已取消"
			if errors.Is(contextErr, context.DeadlineExceeded) {
				state.view.Status = "expired"
				state.view.Message = "二维码已过期，请重新扫码"
			}
			state.view.Terminal = true
		}
	}()
	for {
		status, e := s.auth.PollQR(ctx, q)
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				s.setScan(state, "expired", "二维码已过期，请重新扫码", 0)
			} else {
				s.setScan(state, "cancelled", "扫码已取消", 0)
			}
			return
		}
		if e != nil {
			s.setScan(state, "failed", "微信扫码请求失败，请重新扫码", 0)
			return
		}
		switch status {
		case "pending", "scanned":
			s.setScan(state, status, map[string]string{"pending": "请使用微信扫码", "scanned": "已扫码，请在微信确认"}[status], 0)
		case "authorized", "confirmed":
			s.setScan(state, "authorized", "微信已授权，正在校验绑定", 0)
			s.authorizeScan(ctx, state, q)
			return
		case "cancelled", "expired":
			s.setScan(state, status, "二维码已取消或过期，请重新扫码", 0)
			return
		default:
			s.setScan(state, "failed", "微信返回未知扫码状态", 0)
			return
		}
		// The upstream long-poll normally waits. A short guard prevents a hot loop
		// when the remote endpoint immediately repeats a state.
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
}
func (s *Service) authorizeScan(ctx context.Context, state *scanState, q *auth.QR) {
	c, e := s.auth.ExchangeQR(ctx, q)
	if e != nil {
		s.setScan(state, "failed", "微信授权交换失败，请重新扫码", 0)
		return
	}
	c.RefreshTokenObservedAt = s.now().Unix()
	if c.OpenID == "" || c.AccessToken == "" {
		s.setScan(state, "failed", "微信凭据不完整", 0)
		return
	}
	// Even an account that doesn't yet exist takes the openid lock, closing the
	// race between concurrent addition, replacement and deletion.
	unlock, e := s.locks.acquire(ctx, c.OpenID)
	if e != nil {
		return
	}
	defer unlock()
	existing, lookup := s.db.ByOpenID(ctx, c.OpenID)
	if lookup != nil && !errors.Is(lookup, sql.ErrNoRows) {
		s.setScan(state, "failed", "读取账号失败", 0)
		return
	}
	if state.view.TargetID != 0 {
		target, e := s.db.Account(ctx, state.view.TargetID)
		if e != nil || target.OpenID != c.OpenID {
			s.setScan(state, "failed", "扫码微信与目标账号不一致，未修改原账号", 0)
			return
		}
	}
	persisted := lookup == nil
	defer func() {
		if !persisted {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = s.auth.Forget(cleanup, c.OpenID)
		}
	}()
	if e = s.auth.Forget(ctx, c.OpenID); e != nil {
		s.setScan(state, "failed", "清理旧协议会话失败", 0)
		return
	}
	if c.ExpiresAt <= s.now().Add(time.Minute).Unix() && c.RefreshToken != "" {
		rotated, e := s.auth.Refresh(ctx, c)
		if e != nil {
			s.setScan(state, "failed", "新微信凭据刷新失败", 0)
			return
		}
		rotated.RefreshTokenObservedAt = c.RefreshTokenObservedAt
		c = rotated
	}
	s.setScan(state, "checking_bind", "正在检查官方小程序绑定", 0)
	code, e := s.auth.Code(ctx, c)
	if e != nil {
		s.setScan(state, "failed", "获取微信登录码失败", 0)
		return
	}
	mini, bound, _, e := s.school.CheckBind(ctx, code)
	if e != nil {
		s.setScan(state, "failed", "官方小程序绑定校验失败", 0)
		return
	}
	if !bound {
		s.setScan(state, "unbound", "请先在官方小程序绑定学校账号后重新扫码", 0)
		return
	}
	user, e := mini.MiniUserInfo(ctx)
	if e != nil {
		s.setScan(state, "failed", "读取小程序学校身份失败", 0)
		return
	}
	if lookup == nil && existing.SchoolUserID != user.ID {
		s.setScan(state, "failed", "学校身份不符，未覆盖原账号", 0)
		return
	}
	if ctx.Err() != nil {
		return
	}
	a, e := s.db.SaveBound(ctx, c, user.ID, user.Username, user.FullName)
	if e != nil {
		message := "保存账号失败"
		if errors.Is(e, store.ErrDuplicate) {
			message = "该学校账号已添加"
		}
		if errors.Is(e, store.ErrIdentity) {
			message = "学校身份不符，未覆盖原账号"
		}
		s.setScan(state, "failed", message, 0)
		return
	}
	persisted = true
	business, e := s.school.OpenBusinessSession(ctx, mini, user.ID)
	if e == nil {
		_, e = business.Today(ctx)
	}
	status, message, businessCode := "ready", "已绑定，可访问签到业务", ""
	if e != nil {
		status = "rejected"
		message = "微信已绑定，签到业务会话未通过校验"
		_, businessCode = safeError(e)
	}
	// Persistence remains valid when read-only business validation fails. Never
	// roll back a newly authorized credential set because H5 rejects its session.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if e = s.db.Business(saveCtx, a.ID, status, businessCode); e != nil {
		s.setScan(state, "failed", "账号已保存，但业务校验状态保存失败", a.ID)
		return
	}
	if c.RefreshToken == "" {
		message += "；缺少刷新凭据，长期保活不可用，请重新扫码"
	}
	s.setScan(state, "completed", message, a.ID)
}
func (s *Service) scanCleanupLoop() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			s.scansMu.Lock()
			for id, state := range s.scans {
				if s.now().Sub(state.view.CreatedAt) >= scanTTL {
					state.cancel()
					delete(s.scans, id)
					if s.owners[state.owner] == id {
						delete(s.owners, state.owner)
					}
				}
			}
			s.scansMu.Unlock()
		}
	}
}
