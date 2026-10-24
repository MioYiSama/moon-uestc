package school

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSchoolAuthorizationBoundaries(t *testing.T) {
	ctx := context.Background()
	const ticketValue = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ"
	if len(ticketValue) != 43 {
		t.Fatal("test ticket must satisfy the API's 43-character format")
	}

	var mu sync.Mutex
	checkinBodies := make([]map[string]any, 0, 1)
	checkinCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/platform-app/api/auth/check-bind":
			if r.Method != http.MethodPost || r.Header.Get("platform") != "mp-weixin" || r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("check-bind request headers or method incorrect: %s %v", r.Method, r.Header)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode check-bind body: %v", err)
			}
			code, _ := body["code"].(string)
			if body["sn"] != "" {
				t.Errorf("check-bind sn = %#v, want empty string", body["sn"])
			}
			sessionID := map[string]string{"bind-1": "mini-1", "bind-2": "mini-2"}[code]
			if sessionID == "" {
				t.Errorf("unexpected bind code %q", code)
				http.Error(w, "unexpected", http.StatusBadRequest)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "SESSION", Value: sessionID, Path: "/"})
			writeJSON(t, w, map[string]any{"ok": true, "message": "绑定成功", "msg": "备用绑定消息", "data": map[string]any{"bind": true, "sessionId": sessionID}})
		case "/platform-app/api/auth/user-info":
			if r.Method != http.MethodGet || r.Header.Get("platform") != "mp-weixin" {
				t.Errorf("mini user-info request was not mp-weixin GET")
			}
			cookie, _ := r.Cookie("SESSION")
			if cookie == nil {
				t.Errorf("mini user-info request omitted SESSION")
				http.Error(w, "missing", http.StatusUnauthorized)
				return
			}
			id := ""
			switch cookie.Value {
			case "mini-1":
				id = "school-1"
				http.SetCookie(w, &http.Cookie{Name: "SESSION", Value: "mini-1-rotated", Path: "/"})
			case "mini-1-rotated":
				id = "school-1"
			case "mini-2":
				id = "school-2"
				http.SetCookie(w, &http.Cookie{Name: "SESSION", Value: "mini-2-rotated", Path: "/"})
			case "mini-2-rotated":
				id = "school-2"
			default:
				t.Errorf("mini jar leaked or failed to rotate: SESSION=%q", cookie.Value)
				http.Error(w, "wrong session", http.StatusUnauthorized)
				return
			}
			envelope := userInfoEnvelope(id)
			envelope["ok"] = true
			writeJSON(t, w, envelope)
		case "/platform-app/api/auth/checkin-ticket":
			if r.Method != http.MethodPost || r.Header.Get("platform") != "mp-weixin" {
				t.Errorf("ticket request was not mp-weixin POST")
			}
			cookie, _ := r.Cookie("SESSION")
			if cookie == nil || cookie.Value != "mini-1-rotated" {
				t.Errorf("ticket request used stale/wrong mini SESSION: %#v", cookie)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["code"] != "fresh-ticket-code" {
				t.Errorf("ticket request code must be a separately supplied fresh code: %#v (%v)", body, err)
			}
			writeJSON(t, w, map[string]any{"ok": true, "data": map[string]any{"ticket": ticketValue}})
		case "/daily-checkin/api/auth/user-info":
			if r.Method != http.MethodGet || r.Header.Get("platform") != "h5" {
				t.Errorf("business user-info request was not h5 GET")
			}
			miniID := r.URL.Query().Get("sessionId")
			cookie, _ := r.Cookie("SESSION")
			if cookie == nil || cookie.Value != miniID {
				t.Errorf("business bootstrap must copy current mini SESSION: query=%q cookie=%#v", miniID, cookie)
			}
			businessID := ""
			switch miniID {
			case "mini-1-rotated":
				businessID = "school-1"
				http.SetCookie(w, &http.Cookie{Name: "SESSION", Value: "business-1-rotated", Path: "/"})
			case "mini-2-rotated":
				businessID = "school-2"
				http.SetCookie(w, &http.Cookie{Name: "SESSION", Value: "business-2-rotated", Path: "/"})
			default:
				t.Errorf("business bootstrap received unexpected mini session %q", miniID)
				http.Error(w, "wrong session", http.StatusUnauthorized)
				return
			}
			writeJSON(t, w, userInfoEnvelope(businessID))
		case "/daily-checkin/api/student-checkin-record/today":
			if r.Method != http.MethodGet || r.Header.Get("platform") != "h5" {
				t.Errorf("today request was not h5 GET")
			}
			cookie, _ := r.Cookie("SESSION")
			if cookie == nil {
				t.Errorf("today omitted business SESSION")
				http.Error(w, "missing", http.StatusUnauthorized)
				return
			}
			student := ""
			switch cookie.Value {
			case "business-1-rotated":
				student = "SN-1"
			case "business-2-rotated":
				student = "SN-2"
			default:
				t.Errorf("business jars were not isolated/rotated: SESSION=%q", cookie.Value)
				http.Error(w, "wrong session", http.StatusUnauthorized)
				return
			}
			status := "CHECKED_IN"
			if student == "SN-2" {
				status = "EXEMPT"
			}
			writeJSON(t, w, map[string]any{"code": 0, "message": "今日记录已确认", "data": map[string]any{"businessDate": "2026-10-24", "recordStatusCode": status, "studentSn": student}})
		case "/daily-checkin/api/map/reverse":
			if r.Method != http.MethodGet || r.Header.Get("platform") != "h5" || r.URL.Query().Get("lat") != "30.67449967" || r.URL.Query().Get("lng") != "104.09629583" {
				t.Errorf("reverse-geocode query/header incorrect: %s %v", r.URL.String(), r.Header)
			}
			writeJSON(t, w, map[string]any{"code": 0, "data": "成都固定地址"})
		case "/daily-checkin/api/student-checkin-record/checkin":
			if r.Method != http.MethodPost || r.Header.Get("platform") != "h5" || r.Header.Get("X-Checkin-Ticket") != ticketValue {
				t.Errorf("checkin method/platform/ticket incorrect: %s %v", r.Method, r.Header)
			}
			cookie, _ := r.Cookie("SESSION")
			if cookie == nil || cookie.Value != "business-1-rotated" {
				t.Errorf("checkin used wrong business jar: %#v", cookie)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode checkin body: %v", err)
			}
			mu.Lock()
			checkinCount++
			checkinBodies = append(checkinBodies, body)
			mu.Unlock()
			// The production contract permits a successful write response without data.
			writeJSON(t, w, map[string]any{"code": 0, "status": true, "message": "签到成功"})
		default:
			t.Errorf("unexpected school request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New(server.URL, server.Client())
	mini1, bound, message, err := client.CheckBind(ctx, "bind-1")
	if err != nil || !bound || mini1 == nil || message != "绑定成功" {
		t.Fatalf("CheckBind() = (%v, %t, %q, %v)", mini1, bound, message, err)
	}
	user1, err := mini1.MiniUserInfo(ctx)
	if err != nil || user1.ID != "school-1" || user1.Username != "user-1" || user1.FullName != "Student 1" {
		t.Fatalf("MiniUserInfo() = (%+v, %v)", user1, err)
	}
	ticket, err := mini1.Ticket(ctx, "fresh-ticket-code")
	if err != nil || ticket != ticketValue {
		t.Fatalf("Ticket() = (%q, %v)", ticket, err)
	}

	mini2, bound, message, err := client.CheckBind(ctx, "bind-2")
	if err != nil || !bound || mini2 == nil || message != "绑定成功" {
		t.Fatalf("second CheckBind() = (%v, %t, %q, %v)", mini2, bound, message, err)
	}
	user2, err := mini2.MiniUserInfo(ctx)
	if err != nil || user2.ID != "school-2" {
		t.Fatalf("second MiniUserInfo() = (%+v, %v)", user2, err)
	}
	business1, err := client.OpenBusinessSession(ctx, mini1, "school-1")
	if err != nil {
		t.Fatalf("OpenBusinessSession(first) = %v", err)
	}
	business2, err := client.OpenBusinessSession(ctx, mini2, "school-2")
	if err != nil {
		t.Fatalf("OpenBusinessSession(second) = %v", err)
	}
	today1, err := business1.Today(ctx)
	if err != nil || today1 == nil || today1.StudentSn != "SN-1" || today1.RecordStatusCode != "CHECKED_IN" || today1.BusinessDate != "2026-10-24" || today1.Message != "今日记录已确认" {
		t.Fatalf("Today(first) = (%+v, %v)", today1, err)
	}
	today2, err := business2.Today(ctx)
	if err != nil || today2 == nil || today2.StudentSn != "SN-2" || today2.RecordStatusCode != "EXEMPT" || today2.Message != "今日记录已确认" {
		t.Fatalf("Today(second) = (%+v, %v)", today2, err)
	}
	address, err := business1.Reverse(ctx)
	if err != nil || address != "成都固定地址" {
		t.Fatalf("Reverse() = (%q, %v)", address, err)
	}

	now := time.Date(2026, time.October, 24, 13, 30, 0, 0, time.UTC)
	_, badTicketErr := business1.Checkin(ctx, "bad-ticket", address, "2026-10-24", now)
	assertSchoolError(t, badTicketErr, "checkin", "ticket", "invalid_ticket", false)
	_, dateErr := business1.Checkin(ctx, ticketValue, address, "2026-10-25", now)
	assertSchoolError(t, dateErr, "checkin", "date_changed", "date_changed", false)
	mu.Lock()
	writesBefore := checkinCount
	mu.Unlock()
	if writesBefore != 0 {
		t.Fatalf("invalid ticket/date unexpectedly submitted %d checkin writes", writesBefore)
	}
	checkinMessage, err := business1.Checkin(ctx, ticket, address, "2026-10-24", now)
	if err != nil {
		t.Fatalf("Checkin() = %v", err)
	}
	if checkinMessage != "签到成功" {
		t.Fatalf("Checkin() message = %q, want %q", checkinMessage, "签到成功")
	}
	mu.Lock()
	writes := checkinCount
	payloads := append([]map[string]any(nil), checkinBodies...)
	mu.Unlock()
	if writes != 1 || len(payloads) != 1 {
		t.Fatalf("checkin writes = %d, want exactly one", writes)
	}
	body := payloads[0]
	if len(body) != 5 || body["clientTime"] != "2026-10-24 21:30:00" || body["coordinateSystemCode"] != "gcj02" || body["latitude"] != Latitude || body["longitude"] != Longitude || body["reverseGeocode"] != address {
		t.Fatalf("checkin payload differs from required exact fields: %#v", body)
	}

	t.Run("explicit mini rejection and empty envelopes", func(t *testing.T) {
		for _, tc := range []struct {
			name        string
			body        string
			want        string
			wantMessage string
			ambiguous   bool
		}{
			{name: "ok_false_wins_over_code_zero", body: `{"ok":false,"code":0,"message":"private school explanation","data":{"bind":true,"sessionId":"ignored"}}`, want: "api", wantMessage: "private school explanation"},
			{name: "empty_mini_envelope", body: `{}`, want: "protocol", ambiguous: true},
			{name: "null_bind_is_not_false", body: `{"ok":true,"msg":"bind data issue","data":{"bind":null}}`, want: "protocol", wantMessage: "bind data issue", ambiguous: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				badServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, tc.body)
				}))
				defer badServer.Close()
				_, _, _, err := New(badServer.URL, badServer.Client()).CheckBind(ctx, "test-code")
				var schoolErr *Error
				if !errors.As(err, &schoolErr) || schoolErr.Stage != "check_bind" || schoolErr.Category != tc.want || schoolErr.Ambiguous != tc.ambiguous || schoolErr.Code == "" {
					t.Fatalf("CheckBind error = %#v, want POST %s ambiguous=%t", schoolErr, tc.want, tc.ambiguous)
				}
				if schoolErr.Message != tc.wantMessage {
					t.Fatalf("CheckBind error message = %q, want %q", schoolErr.Message, tc.wantMessage)
				}
				if (tc.wantMessage != "" && strings.Contains(schoolErr.Error(), tc.wantMessage)) || strings.Contains(schoolErr.Error(), "ignored") || strings.Contains(schoolErr.Error(), "test-code") {
					t.Fatalf("sanitized error included response/request content: %q", schoolErr.Error())
				}
			})
		}
	})

	t.Run("unbound and identity mismatch", func(t *testing.T) {
		unboundServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			writeJSON(t, w, map[string]any{"ok": true, "message": "", "msg": "未绑定", "data": map[string]any{"bind": false}})
		}))
		defer unboundServer.Close()
		mini, bound, message, err := New(unboundServer.URL, unboundServer.Client()).CheckBind(ctx, "code")
		if err != nil || bound || mini != nil || message != "未绑定" {
			t.Fatalf("unbound CheckBind() = (%v, %t, %q, %v)", mini, bound, message, err)
		}

		mismatchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/platform-app/api/auth/check-bind":
				writeJSON(t, w, map[string]any{"ok": true, "data": map[string]any{"bind": true, "sessionId": "mini-session"}})
			case "/daily-checkin/api/auth/user-info":
				response := userInfoEnvelope("different-school-id")
				response["message"] = "identity response"
				writeJSON(t, w, response)
			default:
				t.Errorf("identity mismatch test unexpectedly called %s", r.URL.Path)
				http.NotFound(w, r)
			}
		}))
		defer mismatchServer.Close()
		client := New(mismatchServer.URL, mismatchServer.Client())
		mini, _, _, err = client.CheckBind(ctx, "code")
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.OpenBusinessSession(ctx, mini, "expected-school-id")
		assertSchoolError(t, err, "business_session", "identity_mismatch", "identity_mismatch", false)
		var identityErr *Error
		if !errors.As(err, &identityErr) || identityErr.Message != "identity response" {
			t.Fatalf("identity mismatch error message = %#v", identityErr)
		}
	})

	t.Run("empty h5 envelope and invalid ticket", func(t *testing.T) {
		emptyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/platform-app/api/auth/check-bind":
				writeJSON(t, w, map[string]any{"ok": true, "data": map[string]any{"bind": true, "sessionId": "mini-session"}})
			case "/daily-checkin/api/auth/user-info":
				writeJSON(t, w, userInfoEnvelope("school-id"))
			case "/daily-checkin/api/student-checkin-record/today":
				_, _ = fmt.Fprint(w, `{"message":"today response message"}`)
			default:
				t.Errorf("empty envelope test unexpectedly called %s", r.URL.Path)
				http.NotFound(w, r)
			}
		}))
		defer emptyServer.Close()
		client := New(emptyServer.URL, emptyServer.Client())
		mini, _, _, err := client.CheckBind(ctx, "code")
		if err != nil {
			t.Fatal(err)
		}
		business, err := client.OpenBusinessSession(ctx, mini, "school-id")
		if err != nil {
			t.Fatal(err)
		}
		_, err = business.Today(ctx)
		assertSchoolError(t, err, "today", "protocol", "missing_business_status", false)
		var todayErr *Error
		if !errors.As(err, &todayErr) || todayErr.Message != "today response message" {
			t.Fatalf("Today error message = %#v", todayErr)
		}
	})

	t.Run("invalid ticket response", func(t *testing.T) {
		invalidTicketServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/platform-app/api/auth/check-bind":
				writeJSON(t, w, map[string]any{"ok": true, "data": map[string]any{"bind": true, "sessionId": "mini-session"}})
			case "/platform-app/api/auth/checkin-ticket":
				writeJSON(t, w, map[string]any{"ok": true, "message": "ticket validation response", "data": map[string]any{"ticket": "not-a-valid-ticket"}})
			default:
				t.Errorf("invalid ticket test unexpectedly called %s", r.URL.Path)
				http.NotFound(w, r)
			}
		}))
		defer invalidTicketServer.Close()
		mini, _, _, err := New(invalidTicketServer.URL, invalidTicketServer.Client()).CheckBind(ctx, "code")
		if err != nil {
			t.Fatal(err)
		}
		_, err = mini.Ticket(ctx, "fresh-code")
		assertSchoolError(t, err, "ticket", "protocol", "invalid_data", true)
		var ticketErr *Error
		if !errors.As(err, &ticketErr) || ticketErr.Message != "ticket validation response" {
			t.Fatalf("invalid ticket error message = %#v", ticketErr)
		}
	})

	t.Run("write ambiguity follows request dispatch", func(t *testing.T) {
		var writes atomic.Int32
		dropServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/platform-app/api/auth/check-bind":
				writeJSON(t, w, map[string]any{"ok": true, "data": map[string]any{"bind": true, "sessionId": "mini-session"}})
			case "/daily-checkin/api/auth/user-info":
				writeJSON(t, w, userInfoEnvelope("school-id"))
			case "/daily-checkin/api/student-checkin-record/checkin":
				writes.Add(1)
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = conn.Close()
				}
			default:
				t.Errorf("write ambiguity test unexpectedly called %s", r.URL.Path)
				http.NotFound(w, r)
			}
		}))
		defer dropServer.Close()
		client := New(dropServer.URL, dropServer.Client())
		mini, _, _, err := client.CheckBind(ctx, "code")
		if err != nil {
			t.Fatal(err)
		}
		business, err := client.OpenBusinessSession(ctx, mini, "school-id")
		if err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, time.October, 24, 13, 30, 0, 0, time.UTC)
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		_, err = business.Checkin(cancelled, ticketValue, "address", "2026-10-24", now)
		assertSchoolError(t, err, "checkin", "network", "request_failed", false)
		if writes.Load() != 0 {
			t.Fatalf("pre-cancelled request reached server: writes=%d", writes.Load())
		}
		_, err = business.Checkin(ctx, ticketValue, "address", "2026-10-24", now)
		assertSchoolError(t, err, "checkin", "network", "request_failed", true)
		if writes.Load() != 1 {
			t.Fatalf("dispatched write count = %d, want 1", writes.Load())
		}
	})
}

func TestCheckinResponseMessages(t *testing.T) {
	const ticketValue = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ"
	now := time.Date(2026, time.October, 24, 13, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		name          string
		status        int
		body          string
		wantMessage   string
		wantCategory  string
		wantCode      string
		wantAmbiguous bool
	}{
		{name: "message_preferred", status: http.StatusOK, body: `{"code":0,"message":"primary","msg":"fallback"}`, wantMessage: "primary"},
		{name: "msg_fallback_non_string_message", status: http.StatusOK, body: `{"code":0,"message":17,"msg":"fallback"}`, wantMessage: "fallback"},
		{name: "msg_fallback_empty_message", status: http.StatusOK, body: `{"code":0,"message":"","msg":"fallback"}`, wantMessage: "fallback"},
		{name: "missing_message", status: http.StatusOK, body: `{"code":0}`},
		{name: "non_string_messages", status: http.StatusOK, body: `{"code":0,"message":{},"msg":false}`},
		{name: "api_rejection_message", status: http.StatusOK, body: `{"code":4101,"message":"学校拒绝签到"}`, wantMessage: "学校拒绝签到", wantCategory: "api", wantCode: "4101"},
		{name: "api_rejection_msg_fallback", status: http.StatusOK, body: `{"code":4102,"message":false,"msg":"学校拒绝签到"}`, wantMessage: "学校拒绝签到", wantCategory: "api", wantCode: "4102"},
		{name: "api_rejection_missing_message", status: http.StatusOK, body: `{"code":4103}`, wantCategory: "api", wantCode: "4103"},
		{name: "api_rejection_non_string_messages", status: http.StatusOK, body: `{"code":4104,"message":false,"msg":{}}`, wantCategory: "api", wantCode: "4104"},
		{name: "http_failure_message", status: http.StatusServiceUnavailable, body: `{"code":0,"message":"学校服务不可用"}`, wantMessage: "学校服务不可用", wantCategory: "protocol", wantCode: "http_503", wantAmbiguous: true},
		{name: "http_client_error_message", status: http.StatusNotFound, body: `{"code":0,"msg":"学校接口不存在"}`, wantMessage: "学校接口不存在", wantCategory: "http", wantCode: "http_404"},
		{name: "parsed_protocol_failure_message", status: http.StatusOK, body: `{"code":"invalid","message":"协议解析失败"}`, wantMessage: "协议解析失败", wantCategory: "protocol", wantCode: "invalid_json", wantAmbiguous: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()

			business := &BusinessSession{client: New(server.URL, nil), hc: server.Client()}
			message, err := business.Checkin(context.Background(), ticketValue, "test address", "2026-10-24", now)
			if tc.wantCategory == "" {
				if err != nil || message != tc.wantMessage {
					t.Fatalf("Checkin() = (%q, %v), want (%q, nil)", message, err, tc.wantMessage)
				}
				return
			}
			if message != "" {
				t.Fatalf("Checkin() returned message %q with error %v", message, err)
			}
			assertSchoolError(t, err, "checkin", tc.wantCategory, tc.wantCode, tc.wantAmbiguous)
			var schoolErr *Error
			if !errors.As(err, &schoolErr) || schoolErr.Message != tc.wantMessage {
				t.Fatalf("Checkin() error message = %#v, want %q", schoolErr, tc.wantMessage)
			}
			if tc.wantMessage != "" && strings.Contains(schoolErr.Error(), tc.wantMessage) {
				t.Fatalf("sanitized Error() leaked response message: %q", schoolErr.Error())
			}
		})
	}
}

func userInfoEnvelope(id string) map[string]any {
	return map[string]any{"code": 0, "data": map[string]any{"userData": map[string]any{"id": id, "username": "user-" + strings.TrimPrefix(id, "school-"), "fullName": "Student " + strings.TrimPrefix(id, "school-")}}}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode test response: %v", err)
	}
}

func assertSchoolError(t *testing.T, err error, stage, category, code string, ambiguous bool) {
	t.Helper()
	var schoolErr *Error
	if !errors.As(err, &schoolErr) {
		t.Fatalf("error = %v, want *school.Error", err)
	}
	if schoolErr.Stage != stage || schoolErr.Category != category || schoolErr.Code != code || schoolErr.Ambiguous != ambiguous {
		t.Fatalf("school error = %+v, want stage=%q category=%q code=%q ambiguous=%t", schoolErr, stage, category, code, ambiguous)
	}
}
