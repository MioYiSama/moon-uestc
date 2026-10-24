package web

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MioYiSama/moonuestc/internal/app"
)

const (
	sessionCookieName = "moonuestc_session"
	adminSessionTTL   = 12 * time.Hour
	preauthSessionTTL = 15 * time.Minute
	formBodyLimit     = 16 << 10
)

var beijing = time.FixedZone("北京时间", 8*60*60)

//go:embed templates/*.html static/*
var embedded embed.FS

type webSession struct {
	id         string
	csrf       string
	expiresAt  time.Time
	preauth    bool
	scanMu     sync.Mutex
	activeScan string
}

type server struct {
	backend     app.Backend
	password    [sha256.Size]byte
	passwordSet bool
	secure      bool
	templates   *template.Template
	static      http.Handler

	sessionsMu sync.Mutex
	sessions   map[string]*webSession
}

// New returns the single-administrator web interface. Sessions are in-memory,
// opaque, and expire twelve hours after creation without sliding renewal.
func New(backend app.Backend, password string, secure bool) http.Handler {
	templates := template.Must(template.ParseFS(embedded, "templates/*.html"))
	staticFiles, err := fs.Sub(embedded, "static")
	if err != nil {
		panic("web static assets unavailable: " + err.Error())
	}
	s := &server{
		backend:     backend,
		password:    sha256.Sum256([]byte(password)),
		passwordSet: password != "",
		secure:      secure,
		templates:   templates,
		static:      http.StripPrefix("/static/", http.FileServer(http.FS(staticFiles))),
		sessions:    make(map[string]*webSession),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /static/", func(w http.ResponseWriter, r *http.Request) {
		s.static.ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.login)

	mux.HandleFunc("GET /", s.private(s.home))
	mux.HandleFunc("GET /accounts", s.private(s.accountsPage))
	mux.HandleFunc("POST /accounts/scans", s.private(s.createScan))
	mux.HandleFunc("GET /accounts/scans/{scanID}", s.private(s.scanPage))
	mux.HandleFunc("GET /accounts/scans/{scanID}/status", s.private(s.scanStatus))
	mux.HandleFunc("GET /accounts/scans/{scanID}/image", s.private(s.scanImage))
	mux.HandleFunc("POST /accounts/scans/{scanID}/cancel", s.private(s.cancelScan))
	mux.HandleFunc("GET /accounts/{accountID}", s.private(s.accountPage))
	mux.HandleFunc("POST /accounts/{accountID}/checkin", s.private(s.checkin))
	mux.HandleFunc("POST /accounts/{accountID}/rescan", s.private(s.rescan))
	mux.HandleFunc("POST /accounts/{accountID}/delete", s.private(s.deleteAccount))
	mux.HandleFunc("GET /runs", s.private(s.runsPage))
	mux.HandleFunc("POST /logout", s.private(s.logout))

	return s.secureHeaders(mux)
}

func (s *server) secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self'; style-src 'self'; script-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func (s *server) private(next func(http.ResponseWriter, *http.Request, *webSession)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := s.sessionFromRequest(r)
		if sess == nil || sess.preauth {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r, sess)
	}
}

func (s *server) sessionFromRequest(r *http.Request) *webSession {
	s.pruneSessions()
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil
	}
	s.sessionsMu.Lock()
	sess := s.sessions[cookie.Value]
	if sess != nil && time.Now().After(sess.expiresAt) {
		delete(s.sessions, cookie.Value)
		sess = nil
	}
	s.sessionsMu.Unlock()
	return sess
}

func (s *server) pruneSessions() {
	now := time.Now()
	var expired []*webSession
	s.sessionsMu.Lock()
	for id, sess := range s.sessions {
		if !now.Before(sess.expiresAt) {
			delete(s.sessions, id)
			expired = append(expired, sess)
		}
	}
	s.sessionsMu.Unlock()
	for _, sess := range expired {
		s.cancelActiveScan(sess)
	}
}

func (s *server) newSession(preauth bool) (*webSession, error) {
	s.pruneSessions()
	id, err := randomToken()
	if err != nil {
		return nil, err
	}
	csrf, err := randomToken()
	if err != nil {
		return nil, err
	}
	ttl := adminSessionTTL
	if preauth {
		ttl = preauthSessionTTL
	}
	sess := &webSession{id: id, csrf: csrf, expiresAt: time.Now().Add(ttl), preauth: preauth}
	s.sessionsMu.Lock()
	s.sessions[id] = sess
	s.sessionsMu.Unlock()
	return sess, nil
}

func randomToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

func (s *server) setSessionCookie(w http.ResponseWriter, sess *webSession) {
	maxAge := int(time.Until(sess.expiresAt).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sess.id,
		Path:     "/",
		Expires:  sess.expiresAt,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(1, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *server) checkCSRF(w http.ResponseWriter, r *http.Request, sess *webSession) bool {
	r.Body = http.MaxBytesReader(w, r.Body, formBodyLimit)
	if err := r.ParseForm(); err != nil {
		return false
	}
	provided := []byte(r.PostForm.Get("csrf"))
	want := []byte(sess.csrf)
	return len(provided) == len(want) && subtle.ConstantTimeCompare(provided, want) == 1
}

func (s *server) loginPage(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionFromRequest(r)
	if sess != nil && !sess.preauth {
		http.Redirect(w, r, "/accounts", http.StatusSeeOther)
		return
	}
	if sess == nil {
		var err error
		sess, err = s.newSession(true)
		if err != nil {
			s.renderBareError(w, http.StatusInternalServerError, "登录暂不可用", "无法建立登录会话，请稍后重试。")
			return
		}
		s.setSessionCookie(w, sess)
	}
	s.render(w, http.StatusOK, pageData{Template: "login", Title: "管理员登录", CSRF: sess.csrf})
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionFromRequest(r)
	if sess != nil && !sess.preauth {
		http.Redirect(w, r, "/accounts", http.StatusSeeOther)
		return
	}
	if sess == nil {
		s.clearSessionCookie(w)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !s.checkCSRF(w, r, sess) {
		s.renderLoginError(w, http.StatusForbidden, sess, "登录页面已失效，请重新打开后再试。")
		return
	}
	provided := sha256.Sum256([]byte(r.PostForm.Get("password")))
	valid := subtle.ConstantTimeCompare(s.password[:], provided[:])
	if valid != 1 || !s.passwordSet {
		s.renderLoginError(w, http.StatusUnauthorized, sess, "口令不正确，请重试。")
		return
	}
	admin, err := s.newSession(false)
	if err != nil {
		s.renderLoginError(w, http.StatusInternalServerError, sess, "无法建立管理员会话，请稍后重试。")
		return
	}
	s.deleteSession(sess.id)
	s.setSessionCookie(w, admin)
	http.Redirect(w, r, "/accounts", http.StatusSeeOther)
}

func (s *server) renderLoginError(w http.ResponseWriter, code int, sess *webSession, message string) {
	s.render(w, code, pageData{Template: "login", Title: "管理员登录", CSRF: sess.csrf, Error: message})
}

func (s *server) deleteSession(id string) {
	s.sessionsMu.Lock()
	sess := s.sessions[id]
	delete(s.sessions, id)
	s.sessionsMu.Unlock()
	if sess != nil {
		s.cancelActiveScan(sess)
	}
}
func (s *server) sessionCurrent(sess *webSession) bool {
	s.sessionsMu.Lock()
	current := s.sessions[sess.id] == sess && time.Now().Before(sess.expiresAt)
	s.sessionsMu.Unlock()
	return current
}

func (s *server) cancelActiveScan(sess *webSession) {
	sess.scanMu.Lock()
	defer sess.scanMu.Unlock()
	if sess.activeScan == "" {
		return
	}
	_ = s.backend.CancelScan(sess.id, sess.activeScan)
	sess.activeScan = ""
}

func (s *server) home(w http.ResponseWriter, r *http.Request, sess *webSession) {
	if r.URL.Path != "/" {
		s.notFound(w)
		return
	}
	http.Redirect(w, r, "/accounts", http.StatusSeeOther)
}

func (s *server) logout(w http.ResponseWriter, r *http.Request, sess *webSession) {
	if !s.checkCSRF(w, r, sess) {
		s.renderBareError(w, http.StatusForbidden, "请求已失效", "请刷新页面后重试。")
		return
	}
	s.deleteSession(sess.id)
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *server) createScan(w http.ResponseWriter, r *http.Request, sess *webSession) {
	if !s.checkCSRF(w, r, sess) {
		s.renderBareError(w, http.StatusForbidden, "请求已失效", "请刷新页面后重试。")
		return
	}
	s.createScanFor(w, r, sess, 0)
}

func (s *server) rescan(w http.ResponseWriter, r *http.Request, sess *webSession) {
	if !s.checkCSRF(w, r, sess) {
		s.renderBareError(w, http.StatusForbidden, "请求已失效", "请刷新页面后重试。")
		return
	}
	id, ok := parseAccountID(r.PathValue("accountID"))
	if !ok {
		s.notFound(w)
		return
	}
	if _, err := s.backend.Account(r.Context(), id); err != nil {
		if errors.Is(err, app.ErrNotFound) {
			s.notFound(w)
		} else {
			s.renderBareError(w, http.StatusServiceUnavailable, "账号暂不可用", "无法读取账号，请稍后重试。")
		}
		return
	}
	s.createScanFor(w, r, sess, id)
}

func (s *server) checkin(w http.ResponseWriter, r *http.Request, sess *webSession) {
	if !s.checkCSRF(w, r, sess) {
		s.renderBareError(w, http.StatusForbidden, "请求已失效", "请刷新页面后重试。")
		return
	}
	id, ok := parseAccountID(r.PathValue("accountID"))
	if !ok {
		s.notFound(w)
		return
	}
	// A check-in may take three minutes; retain the normal server deadline on
	// every other route.
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(3*time.Minute + 10*time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		s.renderBareError(w, http.StatusServiceUnavailable, "签到暂不可用", "无法开始签到，请稍后重试。")
		return
	}
	if err := s.backend.RunCheckin(r.Context(), id); err != nil {
		if errors.Is(err, app.ErrNotFound) {
			s.notFound(w)
		} else {
			s.renderBareError(w, http.StatusServiceUnavailable, "签到暂不可用", "无法完成签到流程，请先查看该账号的签到记录，确认结果后再操作。")
		}
		return
	}
	http.Redirect(w, r, "/accounts/"+strconv.FormatInt(id, 10)+"#account-runs-heading", http.StatusSeeOther)
}

func (s *server) createScanFor(w http.ResponseWriter, r *http.Request, sess *webSession, targetID int64) {
	sess.scanMu.Lock()
	defer sess.scanMu.Unlock()
	if !s.sessionCurrent(sess) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if sess.activeScan != "" {
		if err := s.backend.CancelScan(sess.id, sess.activeScan); err != nil && !errors.Is(err, app.ErrNotFound) && !errors.Is(err, app.ErrExpired) {
			s.renderBareError(w, http.StatusServiceUnavailable, "二维码暂不可用", "无法结束上一次扫码，请稍后重试。")
			return
		}
		sess.activeScan = ""
	}
	scan, err := s.backend.NewScan(r.Context(), sess.id, targetID)
	if err != nil {
		if errors.Is(err, app.ErrNotFound) {
			s.notFound(w)
		} else {
			s.renderBareError(w, http.StatusServiceUnavailable, "二维码暂不可用", "无法创建扫码会话，请稍后重试。")
		}
		return
	}
	sess.activeScan = scan.ID
	http.Redirect(w, r, "/accounts/scans/"+scan.ID, http.StatusSeeOther)
}

func (s *server) cancelScan(w http.ResponseWriter, r *http.Request, sess *webSession) {
	if !s.checkCSRF(w, r, sess) {
		s.renderBareError(w, http.StatusForbidden, "请求已失效", "请刷新页面后重试。")
		return
	}
	id := r.PathValue("scanID")
	sess.scanMu.Lock()
	err := s.backend.CancelScan(sess.id, id)
	if err == nil || errors.Is(err, app.ErrNotFound) || errors.Is(err, app.ErrExpired) {
		if sess.activeScan == id {
			sess.activeScan = ""
		}
	}
	sess.scanMu.Unlock()
	if errors.Is(err, app.ErrNotFound) {
		s.notFound(w)
		return
	}
	if err != nil && !errors.Is(err, app.ErrExpired) {
		s.renderBareError(w, http.StatusServiceUnavailable, "扫码暂不可用", "无法取消扫码，请稍后重试。")
		return
	}
	http.Redirect(w, r, "/accounts/scans/"+id, http.StatusSeeOther)
}

func (s *server) accountsPage(w http.ResponseWriter, r *http.Request, sess *webSession) {
	accounts, err := s.backend.Accounts(r.Context())
	if err != nil {
		s.renderBareError(w, http.StatusServiceUnavailable, "账号暂不可用", "无法读取账号列表，请稍后重试。")
		return
	}
	rows := make([]accountRow, 0, len(accounts))
	for _, account := range accounts {
		rows = append(rows, makeAccountRow(account))
	}
	s.render(w, http.StatusOK, pageData{
		Template:     "accounts",
		Title:        "账号",
		Nav:          "accounts",
		CSRF:         sess.csrf,
		Accounts:     rows,
		AccountCount: len(rows),
		NextCheckin:  nextCheckinLabel(),
		Coordinates:  "30.67449967, 104.09629583",
	})
}

func (s *server) accountPage(w http.ResponseWriter, r *http.Request, sess *webSession) {
	id, ok := parseAccountID(r.PathValue("accountID"))
	if !ok {
		s.notFound(w)
		return
	}
	account, err := s.backend.Account(r.Context(), id)
	if err != nil {
		if errors.Is(err, app.ErrNotFound) {
			s.notFound(w)
		} else {
			s.renderBareError(w, http.StatusServiceUnavailable, "账号暂不可用", "无法读取账号，请稍后重试。")
		}
		return
	}
	runs, err := s.backend.Runs(r.Context(), id, 30)
	if err != nil {
		s.renderBareError(w, http.StatusServiceUnavailable, "签到记录暂不可用", "无法读取最近的签到记录，请稍后重试。")
		return
	}
	rows := make([]runRow, 0, len(runs))
	for _, run := range runs {
		rows = append(rows, makeRunRow(run))
	}
	s.render(w, http.StatusOK, pageData{
		Template:    "account",
		Title:       account.SchoolName,
		Nav:         "accounts",
		CSRF:        sess.csrf,
		Account:     makeAccountRow(account),
		Runs:        rows,
		NextCheckin: nextCheckinLabel(),
		Coordinates: "30.67449967, 104.09629583",
	})
}

func (s *server) runsPage(w http.ResponseWriter, r *http.Request, sess *webSession) {
	runs, err := s.backend.Runs(r.Context(), 0, 100)
	if err != nil {
		s.renderBareError(w, http.StatusServiceUnavailable, "签到记录暂不可用", "无法读取签到记录，请稍后重试。")
		return
	}
	rows := make([]runRow, 0, len(runs))
	for _, run := range runs {
		rows = append(rows, makeRunRow(run))
	}
	s.render(w, http.StatusOK, pageData{
		Template:    "runs",
		Title:       "签到记录",
		Nav:         "runs",
		Runs:        rows,
		RunCount:    len(rows),
		CSRF:        sess.csrf,
		NextCheckin: nextCheckinLabel(),
		Coordinates: "30.67449967, 104.09629583",
	})
}

func (s *server) scanPage(w http.ResponseWriter, r *http.Request, sess *webSession) {
	id := r.PathValue("scanID")
	view, err := s.backend.Scan(sess.id, id)
	code := http.StatusOK
	if errors.Is(err, app.ErrNotFound) {
		s.notFound(w)
		return
	}
	if errors.Is(err, app.ErrExpired) {
		view = app.ScanView{ID: id, Status: "expired", Terminal: true}
		code = http.StatusGone
	} else if err != nil {
		s.renderBareError(w, http.StatusServiceUnavailable, "扫码状态暂不可用", "无法读取扫码状态，请稍后重试。")
		return
	}
	if view.ID == "" {
		view.ID = id
	}
	model := s.scanModel(view)
	if model.Terminal {
		s.forgetScan(sess, id)
	}
	s.render(w, code, pageData{Template: "scan", Title: scanTitle(model), Nav: "accounts", CSRF: sess.csrf, Scan: model})
}
func (s *server) scanStatus(w http.ResponseWriter, r *http.Request, sess *webSession) {
	id := r.PathValue("scanID")
	view, err := s.backend.Scan(sess.id, id)
	if errors.Is(err, app.ErrNotFound) {
		s.writeJSON(w, http.StatusNotFound, scanSnapshot{Status: "failed", Message: "扫码会话不存在。", Terminal: true})
		return
	}
	if errors.Is(err, app.ErrExpired) {
		s.forgetScan(sess, id)
		s.writeJSON(w, http.StatusGone, scanSnapshot{Status: "expired", Message: statusMessage("expired"), Terminal: true})
		return
	}
	if err != nil {
		s.writeJSON(w, http.StatusServiceUnavailable, scanSnapshot{Status: "pending", Message: "暂时无法读取扫码状态，请稍后重试。"})
		return
	}
	if view.ID == "" {
		view.ID = id
	}
	model := s.scanModel(view)
	if model.Terminal {
		s.forgetScan(sess, id)
	}
	accountURL := ""
	if model.AccountID > 0 {
		accountURL = "/accounts/" + strconv.FormatInt(model.AccountID, 10)
	}
	s.writeJSON(w, http.StatusOK, scanSnapshot{Status: model.Status, Message: model.Message, Terminal: model.Terminal, AccountURL: accountURL})
}

func (s *server) scanImage(w http.ResponseWriter, r *http.Request, sess *webSession) {
	id := r.PathValue("scanID")
	image, err := s.backend.ScanImage(sess.id, id)
	if errors.Is(err, app.ErrNotFound) {
		s.renderBareError(w, http.StatusNotFound, "二维码不存在", "此扫码会话不存在或已结束。")
		return
	}
	if errors.Is(err, app.ErrExpired) {
		s.renderBareError(w, http.StatusGone, "二维码已过期", "请返回账号列表重新创建二维码。")
		return
	}
	if err != nil {
		s.renderBareError(w, http.StatusServiceUnavailable, "二维码暂不可用", "无法读取二维码，请稍后重试。")
		return
	}
	contentType := http.DetectContentType(image)
	switch contentType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(image)
	default:
		s.renderBareError(w, http.StatusBadGateway, "二维码暂不可用", "二维码格式无效，请重新创建扫码会话。")
	}
}

func (s *server) forgetScan(sess *webSession, id string) {
	sess.scanMu.Lock()
	if sess.activeScan == id {
		sess.activeScan = ""
	}
	sess.scanMu.Unlock()
}

type scanSnapshot struct {
	Status     string `json:"status"`
	Message    string `json:"message"`
	Terminal   bool   `json:"terminal"`
	AccountURL string `json:"accountUrl,omitempty"`
}

func (s *server) scanModel(view app.ScanView) scanPageModel {
	status := scanStatus(view.Status)
	terminal := view.Terminal || isTerminalScan(status)
	message := statusMessage(status)
	if trusted := strings.TrimSpace(view.Message); trusted != "" {
		message = trusted
	}
	return scanPageModel{
		ID:          view.ID,
		Status:      status,
		StatusLabel: scanStatusLabel(status),
		Message:     message,
		TargetID:    view.TargetID,
		TargetName:  view.TargetName,
		AccountID:   view.AccountID,
		Terminal:    terminal,
	}
}

func scanStatus(status string) string {
	switch strings.ToLower(status) {
	case "pending", "scanned", "authorized", "checking_bind", "completed", "unbound", "cancelled", "expired", "failed":
		return strings.ToLower(status)
	default:
		return "failed"
	}
}

func isTerminalScan(status string) bool {
	switch status {
	case "completed", "unbound", "cancelled", "expired", "failed":
		return true
	default:
		return false
	}
}

func statusMessage(status string) string {
	switch status {
	case "pending":
		return "请使用微信扫描二维码。"
	case "scanned":
		return "已扫码，正在等待微信授权。"
	case "authorized":
		return "微信已授权，正在检查账号绑定。"
	case "checking_bind":
		return "正在校验小程序绑定与学校身份。"
	case "completed":
		return "账号已添加。"
	case "unbound":
		return "请先在官方小程序绑定学校账号后重新扫码。"
	case "cancelled":
		return "此次扫码已取消。"
	case "expired":
		return "二维码已过期，请重新创建扫码会话。"
	default:
		return "扫码流程未完成，请重试。"
	}
}

func scanStatusLabel(status string) string {
	switch status {
	case "pending":
		return "等待扫码"
	case "scanned":
		return "已扫码"
	case "authorized":
		return "已授权"
	case "checking_bind":
		return "检查绑定"
	case "completed":
		return "已完成"
	case "unbound":
		return "未绑定"
	case "cancelled":
		return "已取消"
	case "expired":
		return "已过期"
	default:
		return "未完成"
	}
}

func (s *server) deleteAccount(w http.ResponseWriter, r *http.Request, sess *webSession) {
	if !s.checkCSRF(w, r, sess) {
		s.renderBareError(w, http.StatusForbidden, "请求已失效", "请刷新页面后重试。")
		return
	}
	id, ok := parseAccountID(r.PathValue("accountID"))
	if !ok || r.PostForm.Get("confirm") != strconv.FormatInt(id, 10) {
		s.renderBareError(w, http.StatusBadRequest, "需要确认删除", "请勾选与账号编号相符的确认项后再提交。")
		return
	}
	if err := s.backend.Delete(r.Context(), id); err != nil {
		if errors.Is(err, app.ErrNotFound) {
			s.notFound(w)
		} else {
			s.renderBareError(w, http.StatusServiceUnavailable, "删除未完成", "账号未能删除，请稍后重试。")
		}
		return
	}
	http.Redirect(w, r, "/accounts", http.StatusSeeOther)
}

func parseAccountID(raw string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	return id, err == nil && id > 0
}

func (s *server) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *server) render(w http.ResponseWriter, status int, data pageData) {
	var body bytes.Buffer
	if err := s.templates.ExecuteTemplate(&body, "layout", data); err != nil {
		http.Error(w, "Page unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = body.WriteTo(w)
}

func (s *server) renderBareError(w http.ResponseWriter, status int, title, message string) {
	s.render(w, status, pageData{Template: "error", Title: title, ErrorTitle: title, Error: message})
}

func (s *server) notFound(w http.ResponseWriter) {
	s.renderBareError(w, http.StatusNotFound, "页面不存在", "请求的账号、扫码会话或记录不存在。")
}
