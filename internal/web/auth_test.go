package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MioYiSama/moonuestc/internal/app"
)

type webTestBackend struct {
	mu           sync.Mutex
	accounts     map[int64]app.AccountView
	scans        map[string]app.ScanView
	owners       map[string]string
	newScanCalls int
	cancelCalls  int
	deleteCalls  int
}

func newWebTestBackend() *webTestBackend {
	return &webTestBackend{
		accounts: map[int64]app.AccountView{
			1: {ID: 1, SchoolName: "集成测试身份", SchoolUsername: "test-user", SchoolUserID: "test-id", CredentialStatus: "healthy", BusinessStatus: "ready", ExpiresAt: time.Now().Add(time.Hour).Unix()},
		},
		scans:  make(map[string]app.ScanView),
		owners: make(map[string]string),
	}
}

func (b *webTestBackend) Accounts(context.Context) ([]app.AccountView, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]app.AccountView, 0, len(b.accounts))
	for _, account := range b.accounts {
		out = append(out, account)
	}
	return out, nil
}

func (b *webTestBackend) Account(_ context.Context, id int64) (app.AccountView, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	account, ok := b.accounts[id]
	if !ok {
		return app.AccountView{}, app.ErrNotFound
	}
	return account, nil
}

func (b *webTestBackend) Runs(context.Context, int64, int) ([]app.RunView, error) {
	return nil, nil
}

func (b *webTestBackend) RunCheckin(ctx context.Context, id int64) error {
	_, err := b.Account(ctx, id)
	return err
}

func (b *webTestBackend) NewScan(_ context.Context, owner string, targetID int64) (app.ScanView, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.newScanCalls++
	id := fmt.Sprintf("test-scan-%d", b.newScanCalls)
	view := app.ScanView{ID: id, Status: "pending", TargetID: targetID, CreatedAt: time.Now()}
	if account, ok := b.accounts[targetID]; ok && targetID != 0 {
		view.TargetName = account.SchoolName
	}
	b.scans[id] = view
	b.owners[id] = owner
	return view, nil
}

func (b *webTestBackend) Scan(owner, id string) (app.ScanView, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	view, ok := b.scans[id]
	if !ok || b.owners[id] != owner {
		return app.ScanView{}, app.ErrNotFound
	}
	return view, nil
}

func (b *webTestBackend) ScanImage(owner, id string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.scans[id]; !ok || b.owners[id] != owner {
		return nil, app.ErrNotFound
	}
	return nil, errors.New("no image in web auth test")
}
func (b *webTestBackend) CancelScan(owner, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.scans[id]; !ok || b.owners[id] != owner {
		return app.ErrNotFound
	}
	b.cancelCalls++
	return nil
}

func (b *webTestBackend) Delete(_ context.Context, id int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deleteCalls++
	if _, ok := b.accounts[id]; !ok {
		return app.ErrNotFound
	}
	delete(b.accounts, id)
	return nil
}

var csrfField = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func TestPrivateRoutesAndCSRF(t *testing.T) {
	backend := newWebTestBackend()
	handler := New(backend, "web-test-secret", true)

	for _, path := range []string{
		"/accounts",
		"/accounts/1",
		"/runs",
		"/accounts/scans/private/status",
		"/accounts/scans/private/image",
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/login" {
			t.Fatalf("unauthenticated GET %s: got %d %q", path, recorder.Code, recorder.Header().Get("Location"))
		}
	}

	preauthPage := httptest.NewRecorder()
	handler.ServeHTTP(preauthPage, httptest.NewRequest(http.MethodGet, "/login", nil))
	if preauthPage.Code != http.StatusOK {
		t.Fatalf("GET /login: got %d", preauthPage.Code)
	}
	preauthCookie := cookieNamed(t, preauthPage, sessionCookieName)
	assertCookieFlags(t, preauthCookie, true)
	preauthCSRF := extractCSRF(t, preauthPage.Body.String())

	missingCSRF := url.Values{"password": {"web-test-secret"}}
	missingCSRFRequest := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(missingCSRF.Encode()))
	missingCSRFRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	missingCSRFRequest.AddCookie(preauthCookie)
	missingCSRFResponse := httptest.NewRecorder()
	handler.ServeHTTP(missingCSRFResponse, missingCSRFRequest)
	if missingCSRFResponse.Code != http.StatusForbidden {
		t.Fatalf("POST /login without CSRF: got %d", missingCSRFResponse.Code)
	}
	loginForm := url.Values{"csrf": {preauthCSRF}, "password": {"web-test-secret"}}
	loginRequest := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(loginForm.Encode()))
	loginRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRequest.AddCookie(preauthCookie)
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusSeeOther || loginResponse.Header().Get("Location") != "/accounts" {
		t.Fatalf("valid login: got %d %q", loginResponse.Code, loginResponse.Header().Get("Location"))
	}
	adminCookie := cookieNamed(t, loginResponse, sessionCookieName)
	assertCookieFlags(t, adminCookie, true)
	if adminCookie.Value == preauthCookie.Value {
		t.Fatal("login did not rotate the pre-auth session")
	}

	accountsRequest := httptest.NewRequest(http.MethodGet, "/accounts", nil)
	accountsRequest.AddCookie(adminCookie)
	accountsResponse := httptest.NewRecorder()
	handler.ServeHTTP(accountsResponse, accountsRequest)
	if accountsResponse.Code != http.StatusOK {
		t.Fatalf("authenticated GET /accounts: got %d", accountsResponse.Code)
	}
	csrf := extractCSRF(t, accountsResponse.Body.String())
	if accountsResponse.Header().Get("Cache-Control") != "no-store" || accountsResponse.Header().Get("X-Content-Type-Options") != "nosniff" || accountsResponse.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatal("private response is missing required security headers")
	}

	for _, action := range []struct {
		path string
		body url.Values
	}{
		{path: "/accounts/1/delete", body: url.Values{"confirm": {"1"}}},
		{path: "/accounts/1/rescan", body: url.Values{}},
		{path: "/accounts/1/checkin", body: url.Values{}},
		{path: "/accounts/scans", body: url.Values{}},
		{path: "/logout", body: url.Values{}},
	} {
		request := httptest.NewRequest(http.MethodPost, action.path, strings.NewReader(action.body.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.AddCookie(adminCookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Errorf("POST %s without CSRF: got %d", action.path, response.Code)
		}
	}
	backend.mu.Lock()
	newScansAfterRejectedPosts := backend.newScanCalls
	deletesAfterRejectedPosts := backend.deleteCalls
	backend.mu.Unlock()
	if newScansAfterRejectedPosts != 0 || deletesAfterRejectedPosts != 0 {
		t.Fatalf("CSRF-rejected requests changed backend state: scans=%d deletes=%d", newScansAfterRejectedPosts, deletesAfterRejectedPosts)
	}
	unconfirmedDelete := url.Values{"csrf": {csrf}}
	unconfirmedDeleteRequest := httptest.NewRequest(http.MethodPost, "/accounts/1/delete", strings.NewReader(unconfirmedDelete.Encode()))
	unconfirmedDeleteRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	unconfirmedDeleteRequest.AddCookie(adminCookie)
	unconfirmedDeleteResponse := httptest.NewRecorder()
	handler.ServeHTTP(unconfirmedDeleteResponse, unconfirmedDeleteRequest)
	if unconfirmedDeleteResponse.Code != http.StatusBadRequest {
		t.Fatalf("DELETE without explicit confirmation: got %d", unconfirmedDeleteResponse.Code)
	}

	newScanForm := url.Values{"csrf": {csrf}}
	newScanRequest := httptest.NewRequest(http.MethodPost, "/accounts/scans", strings.NewReader(newScanForm.Encode()))
	newScanRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	newScanRequest.AddCookie(adminCookie)
	newScanResponse := httptest.NewRecorder()
	handler.ServeHTTP(newScanResponse, newScanRequest)
	if newScanResponse.Code != http.StatusSeeOther || newScanResponse.Header().Get("Location") != "/accounts/scans/test-scan-1" {
		t.Fatalf("valid scan creation: got %d %q", newScanResponse.Code, newScanResponse.Header().Get("Location"))
	}

	rescanRequest := httptest.NewRequest(http.MethodPost, "/accounts/1/rescan", strings.NewReader(newScanForm.Encode()))
	rescanRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rescanRequest.AddCookie(adminCookie)
	rescanResponse := httptest.NewRecorder()
	handler.ServeHTTP(rescanResponse, rescanRequest)
	if rescanResponse.Code != http.StatusSeeOther || rescanResponse.Header().Get("Location") != "/accounts/scans/test-scan-2" {
		t.Fatalf("valid targeted rescan: got %d %q", rescanResponse.Code, rescanResponse.Header().Get("Location"))
	}

	statusRequest := httptest.NewRequest(http.MethodGet, "/accounts/scans/test-scan-2/status", nil)
	statusRequest.AddCookie(adminCookie)
	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, statusRequest)
	var snapshot scanSnapshot
	if statusResponse.Code != http.StatusOK || json.Unmarshal(statusResponse.Body.Bytes(), &snapshot) != nil || snapshot.Status != "pending" || snapshot.Terminal {
		t.Fatalf("authenticated scan status response is invalid: %d %s", statusResponse.Code, statusResponse.Body.String())
	}

	logoutForm := url.Values{"csrf": {csrf}}
	logoutRequest := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader(logoutForm.Encode()))
	logoutRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	logoutRequest.AddCookie(adminCookie)
	logoutResponse := httptest.NewRecorder()
	handler.ServeHTTP(logoutResponse, logoutRequest)
	if logoutResponse.Code != http.StatusSeeOther || logoutResponse.Header().Get("Location") != "/login" {
		t.Fatalf("valid logout: got %d %q", logoutResponse.Code, logoutResponse.Header().Get("Location"))
	}
	if deleted := cookieNamed(t, logoutResponse, sessionCookieName); deleted.MaxAge >= 0 {
		t.Fatalf("logout did not expire the cookie: MaxAge=%d", deleted.MaxAge)
	}
	backend.mu.Lock()
	cancelledScans := backend.cancelCalls
	backend.mu.Unlock()
	if cancelledScans != 2 {
		t.Fatalf("replaced and logged-out scans were not both cancelled: got %d cancellations", cancelledScans)
	}

	reuseRequest := httptest.NewRequest(http.MethodGet, "/accounts", nil)
	reuseRequest.AddCookie(adminCookie)
	reuseResponse := httptest.NewRecorder()
	handler.ServeHTTP(reuseResponse, reuseRequest)
	if reuseResponse.Code != http.StatusSeeOther || reuseResponse.Header().Get("Location") != "/login" {
		t.Fatalf("logged-out cookie was reused: got %d %q", reuseResponse.Code, reuseResponse.Header().Get("Location"))
	}
}

func extractCSRF(t *testing.T, body string) string {
	t.Helper()
	match := csrfField.FindStringSubmatch(body)
	if len(match) != 2 || match[1] == "" {
		t.Fatal("rendered form has no CSRF token")
	}
	return match[1]
}

func cookieNamed(t *testing.T, recorder *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("response did not set cookie %q", name)
	return nil
}

func assertCookieFlags(t *testing.T, cookie *http.Cookie, secure bool) {
	t.Helper()
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Secure != secure || cookie.Path != "/" {
		t.Fatalf("unexpected session cookie flags: %+v", cookie)
	}
}
