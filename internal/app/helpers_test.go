package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MioYiSama/moonuestc/internal/school"
	"github.com/MioYiSama/moonuestc/internal/store"
	auth "yyb_go/moonuestcauth"
)

var testNow = time.Date(2026, 10, 24, 21, 30, 0, 0, shanghai)

type fakeAuth struct {
	mu                                     sync.Mutex
	next                                   auth.Credentials
	scans                                  map[string]auth.Credentials
	n, codes, refreshes, active, maxActive int
	codeFailure                            bool
	refreshErr                             error
}

func (f *fakeAuth) StartQR(context.Context) (*auth.QR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	id := fmt.Sprint(f.n)
	if f.scans == nil {
		f.scans = map[string]auth.Credentials{}
	}
	f.scans[id] = f.next
	return &auth.QR{ID: id, Image: []byte("local QR"), CreatedAt: testNow}, nil
}
func (f *fakeAuth) PollQR(context.Context, *auth.QR) (string, error) { return "authorized", nil }
func (f *fakeAuth) ExchangeQR(_ context.Context, q *auth.QR) (auth.Credentials, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.scans[q.ID], nil
}
func (f *fakeAuth) Forget(context.Context, string) error { return nil }
func (f *fakeAuth) Code(_ context.Context, c auth.Credentials) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.codes++
	if f.codeFailure {
		return "", errors.New("login buffer unavailable")
	}
	return fmt.Sprintf("%s:%d", c.OpenID, f.codes), nil
}
func (f *fakeAuth) Refresh(ctx context.Context, c auth.Credentials) (auth.Credentials, error) {
	f.mu.Lock()
	f.refreshes++
	f.active++
	f.maxActive = max(f.maxActive, f.active)
	e := f.refreshErr
	f.mu.Unlock()
	select {
	case <-ctx.Done():
	case <-time.After(30 * time.Millisecond):
	}
	f.mu.Lock()
	f.active--
	f.mu.Unlock()
	if e != nil {
		return auth.Credentials{}, e
	}
	c.AccessToken = "rotated-access"
	c.RefreshToken = "rotated-refresh"
	c.ExpiresAt = testNow.Add(2 * time.Hour).Unix()
	return c, nil
}
func credentials(openid string) auth.Credentials {
	return auth.Credentials{OpenID: openid, AccessToken: "local-access", RefreshToken: "local-refresh", ExpiresAt: testNow.Add(time.Hour).Unix(), RefreshTokenObservedAt: testNow.Add(-time.Hour).Unix()}
}

type upstream struct {
	mu                                              sync.Mutex
	unbound, missingSession, bindReject, identities map[string]string
	posts                                           int
	ambiguous, businessReject                       bool
	checkinReject                                   bool
	todayStatus, todayDate                          string
	payload                                         map[string]any
	codes                                           []string
}

func newUpstream() *upstream {
	return &upstream{unbound: map[string]string{}, missingSession: map[string]string{}, bindReject: map[string]string{}, identities: map[string]string{}}
}
func (u *upstream) handler(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	defer u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	cookie, _ := r.Cookie("SESSION")
	openid := ""
	if cookie != nil {
		openid = strings.TrimPrefix(cookie.Value, "session-")
	}
	userID := func(id string) string {
		if v := u.identities[id]; v != "" {
			return v
		}
		return "school-" + id
	}
	user := func(id string) map[string]any {
		return map[string]any{"userData": map[string]any{"id": userID(id), "username": "2026-" + id, "fullName": "集成场景 " + id}}
	}
	switch r.URL.Path {
	case "/platform-app/api/auth/check-bind":
		var body struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		id := strings.Split(body.Code, ":")[0]
		u.codes = append(u.codes, body.Code)
		if u.bindReject[id] != "" {
			write(map[string]any{"ok": false, "code": 4003, "message": "绑定校验未通过"})
			return
		}
		if u.unbound[id] != "" {
			write(map[string]any{"ok": true, "message": "尚未绑定学校账号", "data": map[string]any{"bind": false}})
			return
		}
		data := map[string]any{"bind": true}
		if u.missingSession[id] == "" {
			data["sessionId"] = "session-" + id
		}
		write(map[string]any{"ok": true, "data": data})
	case "/platform-app/api/auth/user-info":
		write(map[string]any{"ok": true, "data": user(openid)})
	case "/daily-checkin/api/auth/user-info":
		if u.businessReject {
			write(map[string]any{"code": 4003, "message": "业务会话校验未通过"})
			return
		}
		write(map[string]any{"code": 0, "data": user(openid)})
	case "/daily-checkin/api/student-checkin-record/today":
		var data any
		if u.todayStatus != "" {
			date := u.todayDate
			if date == "" {
				date = testNow.Format("2006-01-02")
			}
			data = map[string]any{"businessDate": date, "recordStatusCode": u.todayStatus, "studentSn": "2026-" + openid}
		}
		write(map[string]any{"code": 0, "message": "当天状态已查询", "data": data})
	case "/platform-app/api/auth/checkin-ticket":
		var body struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		u.codes = append(u.codes, body.Code)
		write(map[string]any{"ok": true, "data": map[string]any{"ticket": strings.Repeat("A", 43)}})
	case "/daily-checkin/api/map/reverse":
		write(map[string]any{"code": 0, "data": "本地上游实际地址"})
	case "/daily-checkin/api/student-checkin-record/checkin":
		u.posts++
		_ = json.NewDecoder(r.Body).Decode(&u.payload)
		if u.ambiguous {
			h := w.(http.Hijacker)
			conn, _, _ := h.Hijack()
			conn.Close()
			return
		}
		if u.checkinReject {
			write(map[string]any{"code": 4001, "message": "未到签到时间"})
			return
		}
		write(map[string]any{"code": 0, "message": "签到成功"})
	default:
		http.NotFound(w, r)
	}
}
func setup(t *testing.T, f *fakeAuth) (*Service, *store.Store, *upstream, string) {
	t.Helper()
	dir := t.TempDir()
	db, e := store.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	u := newUpstream()
	server := httptest.NewServer(http.HandlerFunc(u.handler))
	s := New(db, f, school.New(server.URL, server.Client()), Options{Now: func() time.Time { return testNow }})
	t.Cleanup(func() { s.Close(); db.Close(); server.Close() })
	return s, db, u, dir
}
func bound(t *testing.T, db *store.Store, id string) store.Account {
	t.Helper()
	a, e := db.SaveBound(context.Background(), credentials(id), "school-"+id, "2026-"+id, "集成场景 "+id)
	if e != nil {
		t.Fatal(e)
	}
	return a
}
func scanComplete(t *testing.T, s *Service, f *fakeAuth, c auth.Credentials, target int64) ScanView {
	t.Helper()
	f.mu.Lock()
	f.next = c
	f.mu.Unlock()
	v, e := s.NewScan(context.Background(), "admin", target)
	if e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		v, e = s.Scan("admin", v.ID)
		if e != nil {
			t.Fatal(e)
		}
		if v.Terminal {
			return v
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("scan did not finish")
	return v
}
