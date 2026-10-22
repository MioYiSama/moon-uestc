package moonuestc_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	moonuestc "moon-uestc/go-sdk"
)

func newTestClient(t *testing.T, handler http.HandlerFunc, options moonuestc.Options) *moonuestc.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	if options.BaseURL == "" {
		options.BaseURL = server.URL
	}
	if options.HTTPClient == nil {
		options.HTTPClient = server.Client()
	}
	client, err := moonuestc.NewClient(options)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(client.CloseIdleConnections)
	return client
}

func staticJSON(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func validCheckin() moonuestc.CheckinRequest {
	return moonuestc.CheckinRequest{
		LocationTelemetry: moonuestc.LocationTelemetry{
			ClientTime: moonuestc.NewDateTime(2026, 10, 22, 21, 30, 0),
		},
		Latitude:       30.674521,
		Longitude:      104.096295,
		ReverseGeocode: "四川省成都市成华区一环路东一段",
	}
}

func TestRecordDecodesServerRepresentations(t *testing.T) {
	body := `{"code":0,"data":{
		"id":"record-1",
		"businessDate":"2026-10-22",
		"studentSn":"student-1",
		"studentName":"Example",
		"recordStatusCode":"CHECKED_IN",
		"instructorList":"[{\"sn\":\"teacher-1\",\"name\":\"Teacher\",\"phone\":\"10000\"}]",
		"checkinTime":"2026-10-22 21:30:00",
		"studentLocationLog":{"checkinRecordId":"record-1","clientServerSkewMs":"1524","userAgent":"example"},
		"studentTags":[],
		"reportedLocationTypeCode":null,
		"newServerField":{"version":2}
	}}`
	client := newTestClient(t, staticJSON(200, body), moonuestc.Options{Session: "test-session"})
	response, err := client.Student.Today(context.Background())
	if err != nil {
		t.Fatalf("Today: %v", err)
	}
	if response.Data == nil {
		t.Fatal("expected a record")
	}
	record := response.Data
	if record.BusinessDate.Year() != 2026 || record.BusinessDate.Month() != time.October || record.BusinessDate.Day() != 22 {
		t.Fatalf("unexpected business date: %v", record.BusinessDate)
	}
	if record.CheckinTime == nil || record.CheckinTime.Hour() != 21 || record.CheckinTime.Minute() != 30 {
		t.Fatalf("unexpected check-in time: %v", record.CheckinTime)
	}
	if len(record.InstructorList) != 1 || record.InstructorList[0].SN != "teacher-1" {
		t.Fatalf("unexpected contacts: %#v", record.InstructorList)
	}
	if record.StudentLocationLog == nil || record.StudentLocationLog.ClientServerSkewMs == nil ||
		*record.StudentLocationLog.ClientServerSkewMs != 1524 {
		t.Fatalf("string integer was not decoded: %#v", record.StudentLocationLog)
	}
	if record.ReportedLocationTypeCode != nil {
		t.Fatalf("nullable location type should be nil, got %v", record.ReportedLocationTypeCode)
	}
}

func TestNullTodayIsNotAMalformedRecord(t *testing.T) {
	client := newTestClient(t, staticJSON(200, `{"code":0,"data":null}`), moonuestc.Options{})
	response, err := client.Student.Today(context.Background())
	if err != nil {
		t.Fatalf("Today: %v", err)
	}
	if response.Data != nil {
		t.Fatalf("expected nil record, got %#v", response.Data)
	}
}

func TestPaginationKeepsLargeStringCountsExact(t *testing.T) {
	body := `{"code":0,"data":[],"page":{
		"pageIndex":2,"pageSize":10,"totalCount":"9007199254740993","totalPage":900719925474100
	}}`
	client := newTestClient(t, staticJSON(200, body), moonuestc.Options{})
	response, err := client.Student.History(context.Background(), moonuestc.HistoryQuery{Year: 2026, Month: 10, PageIndex: 2})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if response.Page == nil {
		t.Fatal("expected page metadata")
	}
	if response.Page.TotalCount != 9007199254740993 {
		t.Fatalf("lost integer precision: %d", response.Page.TotalCount)
	}
	if response.Page.PageIndex != 2 {
		t.Fatalf("unexpected page index: %d", response.Page.PageIndex)
	}
}

func TestBusinessErrorsAreTyped(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		check  func(*testing.T, error)
	}{
		{
			name: "expired session", status: 200,
			body: `{"code":5000,"msg":"系统异常: 未登录或会话已失效"}`,
			check: func(t *testing.T, err error) {
				var target *moonuestc.AuthenticationError
				if !errors.As(err, &target) {
					t.Fatalf("want AuthenticationError, got %T: %v", err, err)
				}
				if target.Code != 5000 {
					t.Fatalf("unexpected code: %d", target.Code)
				}
			},
		},
		{
			name: "forbidden", status: 200,
			body: `{"code":4003,"msg":"无权执行该操作"}`,
			check: func(t *testing.T, err error) {
				var target *moonuestc.AuthorizationError
				if !errors.As(err, &target) {
					t.Fatalf("want AuthorizationError, got %T: %v", err, err)
				}
				if target.Code != 4003 {
					t.Fatalf("unexpected code: %d", target.Code)
				}
			},
		},
		{
			name: "ticket code", status: 200,
			body: `{"code":46001,"msg":"授权无效"}`,
			check: func(t *testing.T, err error) {
				var target *moonuestc.CheckinTicketError
				if !errors.As(err, &target) {
					t.Fatalf("want CheckinTicketError, got %T: %v", err, err)
				}
				if target.Code != 46001 {
					t.Fatalf("unexpected code: %d", target.Code)
				}
				var apiErr *moonuestc.APIError
				if !errors.As(err, &apiErr) {
					t.Fatal("ticket error should unwrap to APIError")
				}
			},
		},
		{
			name: "other business error", status: 200,
			body: `{"code":5000,"msg":"其它系统错误"}`,
			check: func(t *testing.T, err error) {
				var target *moonuestc.APIError
				if !errors.As(err, &target) {
					t.Fatalf("want APIError, got %T: %v", err, err)
				}
				if target.Code != 5000 {
					t.Fatalf("unexpected code: %d", target.Code)
				}
			},
		},
		{
			name: "http 401 wins over body code", status: 401,
			body: `{"code":4003,"msg":"无权执行该操作"}`,
			check: func(t *testing.T, err error) {
				var target *moonuestc.AuthenticationError
				if !errors.As(err, &target) {
					t.Fatalf("want AuthenticationError, got %T: %v", err, err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newTestClient(t, staticJSON(test.status, test.body), moonuestc.Options{})
			_, err := client.Student.Today(context.Background())
			if err == nil {
				t.Fatal("expected an error")
			}
			test.check(t, err)
		})
	}
}

func TestNonSuccessTicketCodeKeepsOperationError(t *testing.T) {
	client := newTestClient(t, staticJSON(403, `{"code":46002,"msg":"授权失效"}`), moonuestc.Options{})
	_, err := client.Student.Today(context.Background())
	var target *moonuestc.CheckinTicketError
	if !errors.As(err, &target) {
		t.Fatalf("want CheckinTicketError, got %T: %v", err, err)
	}
	if target.Code != 46002 || target.StatusCode != 403 {
		t.Fatalf("unexpected ticket error: %#v", target.APIError)
	}
}

func TestBooleanCodeIsNotNumericSuccess(t *testing.T) {
	client := newTestClient(t, staticJSON(200, `{"code":false,"data":null}`), moonuestc.Options{})
	_, err := client.Student.Today(context.Background())
	var target *moonuestc.ProtocolError
	if !errors.As(err, &target) {
		t.Fatalf("want ProtocolError, got %T: %v", err, err)
	}
}

func TestMissingBusinessStatusIsProtocolError(t *testing.T) {
	client := newTestClient(t, staticJSON(200, `{"data":null}`), moonuestc.Options{})
	_, err := client.Student.Today(context.Background())
	var target *moonuestc.ProtocolError
	if !errors.As(err, &target) {
		t.Fatalf("want ProtocolError, got %T: %v", err, err)
	}
}

func TestRedirectIsNotFollowedWithCredentials(t *testing.T) {
	var calls atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "https://other.invalid/login", http.StatusFound)
	}, moonuestc.Options{Session: "private-session"})
	_, err := client.Auth.UserInfo(context.Background(), "")
	var target *moonuestc.ProtocolError
	if !errors.As(err, &target) {
		t.Fatalf("want ProtocolError, got %T: %v", err, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("redirect was followed: %d calls", calls.Load())
	}
}

func TestLoginHTMLIsNotData(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html>login</html>")
	}, moonuestc.Options{})
	_, err := client.Student.Today(context.Background())
	var target *moonuestc.ProtocolError
	if !errors.As(err, &target) {
		t.Fatalf("want ProtocolError, got %T: %v", err, err)
	}
}

func TestAvatarBytesAndJSONError(t *testing.T) {
	image := []byte{0xff, 0xd8, 0xff, 0xe1, 'i', 'm', 'a', 'g', 'e'}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fastdfs-util/api/fastdfs/download/image" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("filePath") != "group/example.jpg" {
			t.Errorf("unexpected filePath: %q", r.URL.Query().Get("filePath"))
		}
		w.Header()["Content-Type"] = nil
		_, _ = w.Write(image)
	}))
	t.Cleanup(server.Close)
	imageClient, err := moonuestc.NewClient(moonuestc.Options{
		BaseURL:    server.URL + "/daily-checkin",
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(imageClient.CloseIdleConnections)
	got, err := imageClient.Student.DownloadAvatar(context.Background(), "group/example.jpg")
	if err != nil {
		t.Fatalf("DownloadAvatar: %v", err)
	}
	if string(got) != string(image) {
		t.Fatalf("unexpected image bytes: %v", got)
	}

	errorClient := newTestClient(t, staticJSON(403, `{"code":4003,"msg":"禁止访问"}`), moonuestc.Options{})
	_, err = errorClient.Student.DownloadAvatar(context.Background(), "group/example.jpg")
	var authErr *moonuestc.AuthorizationError
	if !errors.As(err, &authErr) {
		t.Fatalf("want AuthorizationError, got %T: %v", err, err)
	}
}

func TestReadEndpointPathsAndSchemas(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/auth/user-info":
			if r.URL.RawQuery != "" {
				t.Errorf("unexpected user-info query: %s", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"code":0,"data":{"userData":{
				"id":"user-1","fullName":"Example","username":"user-1",
				"role":"001","roleName":"学生","invalid":false,
				"ability":[],"roles":[],"collegeSns":[]
			},"accessToken":"NOT_IN_USE","refreshToken":"NOT_IN_USE"}}`)
		case "/api/user/service-account-subscribed":
			_, _ = io.WriteString(w, `{"code":0,"data":true}`)
		case "/api/checkin-schedule/effective":
			_, _ = io.WriteString(w, `{"code":0,"data":{
				"id":"1","scheduleName":"默认签到时段",
				"studentCheckinStartOffsetMinute":1260,
				"studentCheckinEndOffsetMinute":1410,
				"effective":true
			}}`)
		case "/api/checkin-location/available-locations":
			_, _ = io.WriteString(w, `{"code":0,"data":[{
				"id":"10","locationName":"欣苑组团","address":"",
				"longitude":104.096295,"latitude":30.674521,"radiusMeter":140
			}]}`)
		case "/api/campus-fence/current-student":
			_, _ = io.WriteString(w, `{"code":0,"data":[{
				"id":"2","campusCode":"610054","campusName":"沙河校区",
				"coordinateSystemCode":"GCJ02","fenceGeojson":"{}","statusCode":"ACTIVE"
			}]}`)
		case "/api/case-keyword/active":
			if r.URL.Query().Get("locationTypeCode") != "CAMPUS" {
				t.Errorf("unexpected keyword query: %s", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"code":0,"data":[{
				"id":"keyword-1","situationTypeCode":"CAMPUS",
				"keyword":"学习","remarkRequired":true,"statusCode":"ACTIVE",
				"situationTypeValue":"校内"
			}]}`)
		case "/api/common/attachMore":
			if r.Method != http.MethodPost {
				t.Errorf("attachMore method = %s", r.Method)
			}
			raw, _ := io.ReadAll(r.Body)
			var body []map[string]string
			if err := json.Unmarshal(raw, &body); err != nil || len(body) != 1 || body[0]["target"] != "SITUATION_TYPE" {
				t.Errorf("unexpected attachMore body: %s (%v)", raw, err)
			}
			_, _ = io.WriteString(w, `{"code":0,"data":{"situationTypeOptions":[
				{"label":"校内","value":"CAMPUS","ext":null,"children":null},
				{"label":"校外","value":"OUTSIDE","ext":null,"children":null}
			]}}`)
		default:
			http.NotFound(w, r)
		}
	}, moonuestc.Options{})
	ctx := context.Background()

	user, err := client.Auth.UserInfo(ctx, "")
	if err != nil || user.Data.UserData.ID != "user-1" || user.Data.UserData.Role != "001" {
		t.Fatalf("UserInfo: %#v, %v", user, err)
	}
	subscribed, err := client.Auth.ServiceAccountSubscribed(ctx)
	if err != nil || subscribed.Data == nil || !*subscribed.Data {
		t.Fatalf("ServiceAccountSubscribed: %#v, %v", subscribed, err)
	}
	schedule, err := client.Student.EffectiveSchedule(ctx)
	if err != nil || schedule.Data == nil || schedule.Data.StudentCheckinStartOffsetMinute != 1260 {
		t.Fatalf("EffectiveSchedule: %#v, %v", schedule, err)
	}
	locations, err := client.Student.AvailableLocations(ctx)
	if err != nil || len(locations.Data) != 1 || locations.Data[0].RadiusMeter != 140 {
		t.Fatalf("AvailableLocations: %#v, %v", locations, err)
	}
	fences, err := client.Student.CampusFences(ctx)
	if err != nil || len(fences.Data) != 1 || fences.Data[0].CampusCode != "610054" {
		t.Fatalf("CampusFences: %#v, %v", fences, err)
	}
	keywords, err := client.Student.CaseKeywords(ctx, moonuestc.LocationTypeCampus)
	if err != nil || len(keywords.Data) != 1 || keywords.Data[0].SituationTypeCode != moonuestc.LocationTypeCampus {
		t.Fatalf("CaseKeywords: %#v, %v", keywords, err)
	}
	types, err := client.Student.SituationTypes(ctx)
	if err != nil || types.Data == nil || len(types.Data.SituationTypeOptions) != 2 ||
		types.Data.SituationTypeOptions[1].Value != moonuestc.LocationTypeOutside {
		t.Fatalf("SituationTypes: %#v, %v", types, err)
	}
}

func TestScalarMissingDataIsProtocolError(t *testing.T) {
	client := newTestClient(t, staticJSON(200, `{"code":0}`), moonuestc.Options{})
	ctx := context.Background()
	calls := []struct {
		name string
		call func() error
	}{
		{name: "user info", call: func() error { _, err := client.Auth.UserInfo(ctx, ""); return err }},
		{name: "subscription", call: func() error { _, err := client.Auth.ServiceAccountSubscribed(ctx); return err }},
		{name: "reverse geocode", call: func() error { _, err := client.Student.ReverseGeocode(ctx, 30, 104); return err }},
		{name: "situation types", call: func() error { _, err := client.Student.SituationTypes(ctx); return err }},
		{name: "class count", call: func() error {
			_, err := client.ClassCadre.CheckinCount(ctx, moonuestc.NewDate(2026, 10, 22))
			return err
		}},
	}
	for _, call := range calls {
		t.Run(call.name, func(t *testing.T) {
			var protocolErr *moonuestc.ProtocolError
			if err := call.call(); !errors.As(err, &protocolErr) {
				t.Fatalf("want ProtocolError, got %T: %v", err, err)
			}
		})
	}
}

func TestSessionCookieUpdatesFromServer(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("SESSION")
		if err != nil {
			t.Errorf("missing SESSION cookie: %v", err)
			return
		}
		mu.Lock()
		seen = append(seen, cookie.Value)
		count := len(seen)
		mu.Unlock()
		if count == 1 {
			http.SetCookie(w, &http.Cookie{Name: "SESSION", Value: "rotated", Path: "/", HttpOnly: true})
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"data":true}`)
	}, moonuestc.Options{Session: "initial"})
	for range 2 {
		if _, err := client.Auth.ServiceAccountSubscribed(context.Background()); err != nil {
			t.Fatalf("ServiceAccountSubscribed: %v", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0] != "initial" || seen[1] != "rotated" {
		t.Fatalf("cookie values = %#v, want [initial rotated]", seen)
	}
}

func TestMissingTicketPreventsAnyNetworkRequest(t *testing.T) {
	var calls atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0}`)
	}, moonuestc.Options{Session: "login-only"})
	_, err := client.Student.Checkin(context.Background(), validCheckin())
	var ticketErr *moonuestc.CheckinTicketError
	if !errors.As(err, &ticketErr) {
		t.Fatalf("want CheckinTicketError, got %T: %v", err, err)
	}
	var authzErr *moonuestc.AuthorizationError
	if !errors.As(err, &authzErr) {
		t.Fatal("ticket error should unwrap to AuthorizationError")
	}
	_, err = client.Student.Report(context.Background(), moonuestc.ReportRequest{
		ReportedLocationTypeCode: moonuestc.LocationTypeCampus,
		CaseKeywordID:            "keyword-1",
		Latitude:                 30,
		Longitude:                104,
		ReverseGeocode:           "test",
	})
	if !errors.As(err, &ticketErr) {
		t.Fatalf("want CheckinTicketError, got %T: %v", err, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("request escaped without a ticket: %d", calls.Load())
	}
}

func TestWriteProtocolAndNoRetry(t *testing.T) {
	var checkinCalls atomic.Int32
	var totalCalls atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		totalCalls.Add(1)
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Errorf("decode body: %v", err)
		}
		cookie, err := r.Cookie("SESSION")
		if err != nil || cookie.Value != "official-session" {
			t.Errorf("unexpected session cookie: %#v, %v", cookie, err)
		}
		switch r.URL.Path {
		case "/api/student-checkin-record/checkin":
			checkinCalls.Add(1)
			if r.Header.Get("X-Checkin-Ticket") != "official-ticket" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"code":46002,"msg":"授权失效"}`)
				return
			}
			if payload["clientTime"] != "2026-10-22 21:30:00" ||
				payload["coordinateSystemCode"] != "gcj02" ||
				payload["latitude"] != 30.674521 ||
				payload["reverseGeocode"] != "四川省成都市成华区一环路东一段" {
				t.Errorf("unexpected check-in body: %#v", payload)
			}
			if _, exists := payload["client_time"]; exists {
				t.Error("snake_case field leaked into request")
			}
		case "/api/student-checkin-record/report":
			if r.Header.Get("X-Checkin-Ticket") != "official-ticket" {
				t.Error("report was not sent with the operation ticket")
			}
			if payload["reportedLocationTypeCode"] != "CAMPUS" ||
				payload["caseKeywordId"] != "keyword-1" ||
				payload["coordinateSystemCode"] != "gcj02" ||
				payload["caseRemark"] != "test remark" {
				t.Errorf("unexpected report body: %#v", payload)
			}
		case "/api/student-checkin-record/location-log":
			if r.Header.Get("X-Checkin-Ticket") != "" {
				t.Error("diagnostics must not carry the operation ticket")
			}
			if payload["errorMessage"] != "local diagnostic" {
				t.Errorf("unexpected diagnostics body: %#v", payload)
			}
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0}`)
	}, moonuestc.Options{Session: "official-session", CheckinTicket: "official-ticket"})

	checkinResponse, err := client.Student.Checkin(context.Background(), validCheckin())
	if err != nil {
		t.Fatalf("Checkin: %v", err)
	}
	if checkinResponse.Data != nil {
		t.Fatalf("write without data should be nil, got %s", checkinResponse.Data)
	}
	_, err = client.Student.Report(context.Background(), moonuestc.ReportRequest{
		ReportedLocationTypeCode: moonuestc.LocationTypeCampus,
		CaseKeywordID:            "keyword-1",
		Latitude:                 30.674521,
		Longitude:                104.096295,
		ReverseGeocode:           "四川省成都市成华区一环路东一段",
		CaseRemark:               "test remark",
	})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	_, err = client.Student.LogLocation(context.Background(), moonuestc.LocationLogRequest{
		LocationTelemetry: moonuestc.LocationTelemetry{
			ClientTime:   moonuestc.NewDateTime(2026, 10, 22, 21, 30, 0),
			ErrorMessage: "local diagnostic",
		},
	})
	if err != nil {
		t.Fatalf("LogLocation: %v", err)
	}

	if err := client.SetCheckinTicket("expired-ticket"); err != nil {
		t.Fatalf("SetCheckinTicket: %v", err)
	}
	_, err = client.Student.Checkin(context.Background(), validCheckin())
	var ticketErr *moonuestc.CheckinTicketError
	if !errors.As(err, &ticketErr) {
		t.Fatalf("want CheckinTicketError, got %T: %v", err, err)
	}
	if checkinCalls.Load() != 2 {
		t.Fatalf("expected exactly two check-in attempts, got %d", checkinCalls.Load())
	}
	if totalCalls.Load() != 4 {
		t.Fatalf("expected no automatic retry, got %d total requests", totalCalls.Load())
	}
}

func TestHistoryFilterDistinguishesFalseAndAll(t *testing.T) {
	var values []string
	var present []bool
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		clock, ok := query["clockIn"]
		if ok {
			values = append(values, clock[0])
		} else {
			values = append(values, "<absent>")
		}
		present = append(present, ok)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"data":[]}`)
	}, moonuestc.Options{})
	ctx := context.Background()
	notCheckedIn := false
	checkedIn := true
	if _, err := client.Student.History(ctx, moonuestc.HistoryQuery{Year: 2026, Month: 10, ClockIn: &notCheckedIn}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Student.History(ctx, moonuestc.HistoryQuery{Year: 2026, Month: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Student.History(ctx, moonuestc.HistoryQuery{Year: 2026, Month: 10, ClockIn: &checkedIn}); err != nil {
		t.Fatal(err)
	}
	want := []string{"false", "", "true"}
	for i := range want {
		if values[i] != want[i] || !present[i] {
			t.Fatalf("clockIn filters = %#v (present %#v), want %#v", values, present, want)
		}
	}
}

func TestLocalValidationPreventsNetwork(t *testing.T) {
	var calls atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0}`)
	}, moonuestc.Options{})
	ctx := context.Background()

	for _, latitude := range []moonuestc.Latitude{91, moonuestc.Latitude(math.NaN()), moonuestc.Latitude(math.Inf(1))} {
		request := validCheckin()
		request.Latitude = latitude
		var validationErr *moonuestc.ValidationError
		if _, err := client.Student.Checkin(ctx, request); !errors.As(err, &validationErr) {
			t.Fatalf("latitude %v: want ValidationError, got %T: %v", latitude, err, err)
		}
	}
	if _, err := client.Student.ReverseGeocode(ctx, 30, 181); err == nil {
		t.Fatal("expected longitude validation error")
	}
	if _, err := client.Student.History(ctx, moonuestc.HistoryQuery{Year: 2026, Month: 13}); err == nil {
		t.Fatal("expected month validation error")
	}
	if _, err := client.Student.CaseKeywords(ctx, moonuestc.LocationType("UNKNOWN")); err == nil {
		t.Fatal("expected location type validation error")
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid requests reached the network: %d", calls.Load())
	}
}

func TestMalformedRecordsAreProtocolErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing id", body: `{"code":0,"data":{"businessDate":"2026-10-22","studentSn":"s","studentName":"n","recordStatusCode":"PENDING"}}`},
		{name: "invalid date", body: `{"code":0,"data":{"id":"r","businessDate":"not-a-date","studentSn":"s","studentName":"n","recordStatusCode":"PENDING"}}`},
		{name: "invalid embedded contacts", body: `{"code":0,"data":{"id":"r","businessDate":"2026-10-22","studentSn":"s","studentName":"n","recordStatusCode":"PENDING","instructorList":"not-json"}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newTestClient(t, staticJSON(200, test.body), moonuestc.Options{})
			_, err := client.Student.Today(context.Background())
			var protocolErr *moonuestc.ProtocolError
			if !errors.As(err, &protocolErr) {
				t.Fatalf("want ProtocolError, got %T: %v", err, err)
			}
		})
	}
}

func TestReverseGeocodeQueryFormatting(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/map/reverse" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("lat") != "30.674521" || r.URL.Query().Get("lng") != "104.096295" {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"data":"四川省成都市成华区一环路东一段"}`)
	}, moonuestc.Options{})
	response, err := client.Student.ReverseGeocode(context.Background(), 30.674521, 104.096295)
	if err != nil {
		t.Fatalf("ReverseGeocode: %v", err)
	}
	if response.Data == nil || *response.Data != "四川省成都市成华区一环路东一段" {
		t.Fatalf("unexpected address: %v", response.Data)
	}
}

func TestClassCadreProtocol(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/class-cadre/my-classes":
			_, _ = io.WriteString(w, `{"code":0,"data":[{"classKey":"class-1","manageClass":"Class 1"}]}`)
		case "/api/class-cadre/checkin-list":
			query := r.URL.Query()
			if query.Get("classKey") != "class-1" || query.Get("businessDate") != "2026-10-22" ||
				query.Get("pageIndex") != "1" || query.Get("pageSize") != "10" ||
				query.Get("keyword") != "" {
				t.Errorf("unexpected class list query: %s", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"code":0,"data":[],"page":{"pageIndex":1,"pageSize":10,"totalCount":"0","totalPage":0}}`)
		case "/api/class-cadre/checkin-count":
			if r.URL.Query().Get("businessDate") != "2026-10-22" {
				t.Errorf("unexpected count query: %s", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"code":0,"data":"7"}`)
		case "/api/class-cadre/situation/report":
			if r.Header.Get("X-Checkin-Ticket") != "" {
				t.Error("class report must not send the student operation ticket")
			}
			raw, _ := io.ReadAll(r.Body)
			var reports []map[string]any
			if err := json.Unmarshal(raw, &reports); err != nil {
				t.Errorf("report body is not an array: %v", err)
			}
			if len(reports) != 1 || reports[0]["classKey"] != "class-1" ||
				reports[0]["recordId"] != "record-1" ||
				reports[0]["locationTypeCode"] != "CAMPUS" ||
				reports[0]["caseKeywordId"] != "keyword-1" {
				t.Errorf("unexpected reports: %#v", reports)
			}
			_, _ = io.WriteString(w, `{"code":0}`)
		default:
			http.NotFound(w, r)
		}
	}, moonuestc.Options{})
	ctx := context.Background()

	classes, err := client.ClassCadre.MyClasses(ctx)
	if err != nil || len(classes.Data) != 1 || classes.Data[0].ClassKey != "class-1" {
		t.Fatalf("MyClasses: %#v, %v", classes, err)
	}
	list, err := client.ClassCadre.CheckinList(ctx, moonuestc.ClassCheckinQuery{
		BusinessDate: moonuestc.NewDate(2026, 10, 22),
		ClassKey:     "class-1",
	})
	if err != nil || list.Page == nil {
		t.Fatalf("CheckinList: %#v, %v", list, err)
	}
	count, err := client.ClassCadre.CheckinCount(ctx, moonuestc.NewDate(2026, 10, 22))
	if err != nil || count.Data == nil || *count.Data != 7 {
		t.Fatalf("CheckinCount: %#v, %v", count, err)
	}
	_, err = client.ClassCadre.ReportSituations(ctx, []moonuestc.ClassSituationReport{{
		ClassKey:         "class-1",
		RecordID:         "record-1",
		LocationTypeCode: moonuestc.LocationTypeCampus,
		CaseKeywordID:    "keyword-1",
	}})
	if err != nil {
		t.Fatalf("ReportSituations: %v", err)
	}
}

func TestClientOptionValidation(t *testing.T) {
	tests := []moonuestc.Options{
		{BaseURL: "https://user:pass@example.com"},
		{BaseURL: "https://example.com?query=1"},
		{BaseURL: "ftp://example.com"},
		{Timeout: -time.Second},
		{Session: "bad\nsession"},
		{CheckinTicket: "bad\rticket"},
	}
	for _, options := range tests {
		if _, err := moonuestc.NewClient(options); err == nil {
			t.Fatalf("expected options to be rejected: %#v", options)
		}
	}
}

func TestErrorMessagesAndUnwrapping(t *testing.T) {
	auth := &moonuestc.AuthenticationError{APIError: moonuestc.APIError{
		Message: "expired", Code: 5000, HasCode: true, StatusCode: 200,
	}}
	var apiErr *moonuestc.APIError
	if !errors.As(auth, &apiErr) {
		t.Fatal("authentication error should unwrap to APIError")
	}
	if got := auth.Error(); !strings.Contains(got, "code 5000") || !strings.Contains(got, "HTTP 200") {
		t.Fatalf("unexpected authentication error text: %s", got)
	}

	authz := &moonuestc.AuthorizationError{APIError: moonuestc.APIError{
		Message: "forbidden", Code: 4003, HasCode: true,
	}}
	if got := authz.Error(); !strings.Contains(got, "code 4003") || strings.Contains(got, "HTTP") {
		t.Fatalf("unexpected authorization error text: %s", got)
	}

	ticket := &moonuestc.CheckinTicketError{AuthorizationError: moonuestc.AuthorizationError{
		APIError: moonuestc.APIError{Message: "ticket required"},
	}}
	if got := ticket.Error(); strings.Contains(got, "HTTP 0") {
		t.Fatalf("ticket error leaked a zero status: %s", got)
	}
	var authzTarget *moonuestc.AuthorizationError
	if !errors.As(ticket, &authzTarget) {
		t.Fatal("ticket error should unwrap to AuthorizationError")
	}
	if !errors.As(ticket, &apiErr) {
		t.Fatal("ticket error should unwrap to APIError")
	}

	if got := (&moonuestc.HTTPError{StatusCode: 502}).Error(); got != "moonuestc: HTTP 502" {
		t.Fatalf("unexpected HTTP error text: %s", got)
	}
	if got := (&moonuestc.HTTPError{StatusCode: 502, Message: "bad gateway"}).Error(); got != "moonuestc: HTTP 502: bad gateway" {
		t.Fatalf("unexpected HTTP error text: %s", got)
	}
	if got := (&moonuestc.ValidationError{Field: "month", Message: "out of range"}).Error(); got != "moonuestc: invalid month: out of range" {
		t.Fatalf("unexpected validation error text: %s", got)
	}
	if got := (&moonuestc.ValidationError{Message: "bad request"}).Error(); got != "moonuestc: validation error: bad request" {
		t.Fatalf("unexpected validation error text: %s", got)
	}
	if got := (&moonuestc.ProtocolError{Message: "bad shape"}).Error(); got != "moonuestc: protocol error: bad shape" {
		t.Fatalf("unexpected protocol error text: %s", got)
	}
}

func TestTimeAndJSONBoundaryHelpers(t *testing.T) {
	date := moonuestc.NewDate(2026, time.October, 22)
	if date.String() != "2026-10-22" {
		t.Fatalf("unexpected date string: %s", date)
	}
	raw, err := json.Marshal(date)
	if err != nil || string(raw) != `"2026-10-22"` {
		t.Fatalf("unexpected date JSON: %s, %v", raw, err)
	}
	var decodedDate moonuestc.Date
	if err := json.Unmarshal([]byte(`"2026-10-22"`), &decodedDate); err != nil || decodedDate.String() != "2026-10-22" {
		t.Fatalf("decode date: %v, %s", err, decodedDate)
	}
	var zeroDate moonuestc.Date
	if _, err := json.Marshal(zeroDate); err == nil {
		t.Fatal("zero Date should not be serializable")
	}
	var nullDate moonuestc.Date
	if err := json.Unmarshal([]byte("null"), &nullDate); err != nil || !nullDate.IsZero() {
		t.Fatalf("null date: %v, %v", err, nullDate)
	}
	if _, err := moonuestc.ParseDate("2026-13-01"); err == nil {
		t.Fatal("invalid date should be rejected")
	}

	dateTime := moonuestc.NewDateTime(2026, time.October, 22, 21, 30, 0)
	raw, err = json.Marshal(dateTime)
	if err != nil || string(raw) != `"2026-10-22 21:30:00"` {
		t.Fatalf("unexpected datetime JSON: %s, %v", raw, err)
	}
	wall := moonuestc.DateTimeFromTime(time.Date(2026, time.October, 22, 21, 30, 0, 0, time.FixedZone("X", -5*3600)))
	if wall.String() != "2026-10-22 21:30:00" {
		t.Fatalf("wall clock components changed: %s", wall)
	}
	if moonuestc.DateTimeNow().IsZero() {
		t.Fatal("DateTimeNow returned the zero value")
	}
	parsedDateTime, err := moonuestc.ParseDateTime("2026-10-22T21:30:00+08:00")
	if err != nil || parsedDateTime.String() != "2026-10-22 21:30:00" {
		t.Fatalf("parse RFC3339 datetime: %v, %s", err, parsedDateTime)
	}
	var zeroDateTime moonuestc.DateTime
	if _, err := json.Marshal(zeroDateTime); err == nil {
		t.Fatal("zero DateTime should not be serializable")
	}
	if _, err := moonuestc.ParseDateTime("nope"); err == nil {
		t.Fatal("invalid datetime should be rejected")
	}

	intCases := []struct {
		input string
		want  moonuestc.Int64
		ok    bool
	}{
		{`7`, 7, true},
		{`"7"`, 7, true},
		{`null`, 0, true},
		{`1.5`, 0, false},
		{`true`, 0, false},
	}
	for _, test := range intCases {
		var number moonuestc.Int64
		err := json.Unmarshal([]byte(test.input), &number)
		if (err == nil) != test.ok {
			t.Fatalf("Int64 %s: err=%v, want ok=%v", test.input, err, test.ok)
		}
		if test.ok && number != test.want {
			t.Fatalf("Int64 %s = %d, want %d", test.input, number, test.want)
		}
	}
	raw, _ = json.Marshal(moonuestc.Int64(7))
	if string(raw) != "7" {
		t.Fatalf("unexpected Int64 JSON: %s", raw)
	}

	var contacts moonuestc.InstructorList
	if err := json.Unmarshal([]byte(`"[{\"name\":\"A\"}]"`), &contacts); err != nil || len(contacts) != 1 || contacts[0].Name != "A" {
		t.Fatalf("decode embedded contacts: %v, %#v", err, contacts)
	}
	if err := json.Unmarshal([]byte(`null`), &contacts); err != nil || contacts != nil {
		t.Fatalf("decode null contacts: %v, %#v", err, contacts)
	}
	if err := json.Unmarshal([]byte(`""`), &contacts); err != nil || contacts != nil {
		t.Fatalf("decode empty contacts: %v, %#v", err, contacts)
	}
	raw, _ = json.Marshal(moonuestc.InstructorList(nil))
	if string(raw) != "[]" {
		t.Fatalf("nil contacts should marshal as [], got %s", raw)
	}
}

func TestPublicValidationMethods(t *testing.T) {
	if err := validCheckin().Validate(); err != nil {
		t.Fatalf("valid check-in rejected: %v", err)
	}
	negative := -1.0
	bad := validCheckin()
	bad.Accuracy = &negative
	if err := bad.Validate(); err == nil {
		t.Fatal("negative accuracy should be rejected")
	}
	bad = validCheckin()
	bad.CoordinateSystemCode = "wgs84"
	if err := bad.Validate(); err == nil {
		t.Fatal("non-GCJ02 coordinate system should be rejected")
	}
	bad = validCheckin()
	bad.Longitude = 181
	if err := bad.Validate(); err == nil {
		t.Fatal("out-of-range longitude should be rejected")
	}

	if err := (moonuestc.LocationLogRequest{}).Validate(); err == nil {
		t.Fatal("diagnostics without clientTime should be rejected")
	}

	report := moonuestc.ReportRequest{
		ReportedLocationTypeCode: "UNKNOWN",
		Latitude:                 30,
		Longitude:                104,
	}
	if err := report.Validate(); err == nil {
		t.Fatal("unknown report location type should be rejected")
	}
	report.ReportedLocationTypeCode = moonuestc.LocationTypeCampus
	report.GPSAccuracyMeter = &negative
	if err := report.Validate(); err == nil {
		t.Fatal("negative GPS accuracy should be rejected")
	}

	if err := (moonuestc.HistoryQuery{Year: 2026, Month: 10, PageIndex: -1}).Validate(); err == nil {
		t.Fatal("negative page index should be rejected")
	}
	if err := (moonuestc.HistoryQuery{Year: 2026, Month: 10}).Validate(); err != nil {
		t.Fatalf("page defaults should be accepted: %v", err)
	}
	if err := (moonuestc.ClassCheckinQuery{}).Validate(); err == nil {
		t.Fatal("zero class business date should be rejected")
	}
	if err := (moonuestc.ClassSituationReport{LocationTypeCode: "UNKNOWN"}).Validate(); err == nil {
		t.Fatal("unknown class report location type should be rejected")
	}
}

func TestLiveAnonymousSessionRejected(t *testing.T) {
	if os.Getenv("MOON_LIVE") != "1" {
		t.Skip("set MOON_LIVE=1 to run the real-server anonymous check")
	}
	client, err := moonuestc.NewClient(moonuestc.Options{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.CloseIdleConnections()
	_, err = client.Auth.UserInfo(context.Background(), "")
	var authErr *moonuestc.AuthenticationError
	if !errors.As(err, &authErr) {
		t.Fatalf("want AuthenticationError, got %T: %v", err, err)
	}
	if authErr.Code != 5000 || !strings.Contains(authErr.Message, "未登录") {
		t.Fatalf("unexpected live error: %#v", authErr.APIError)
	}
}
