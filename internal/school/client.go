package school

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptrace"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	Latitude         = 30.67449967
	Longitude        = 104.09629583
	productionOrigin = "https://jzsz.uestc.edu.cn"
	maxResponseBytes = 1 << 20
)

var ticketPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// Error is a sanitized school API failure. Code is a stable business/transport
// code and Message is the selected school response message. Error() deliberately
// excludes Message, response bodies, credentials, URLs, and underlying network
// errors.
type Error struct {
	Stage     string
	Category  string
	Code      string
	Message   string
	Ambiguous bool
}

func (e *Error) Error() string {
	if e == nil {
		return "school operation failed"
	}
	return "school " + safeLabel(e.Stage) + " " + safeLabel(e.Category) + " (" + safeLabel(e.Code) + ")"
}

func safeLabel(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "operation_failed"
	}
	return b.String()
}

func withResponseMessage(err *Error, message string) *Error {
	err.Message = message
	return err
}

func schoolError(stage, category, code string, ambiguous bool) *Error {
	return &Error{Stage: stage, Category: category, Code: code, Ambiguous: ambiguous}
}

// Client owns no account state. Each call to CheckBind creates a new mini
// session and each OpenBusinessSession creates a separate business cookie jar.
type Client struct {
	origin    string
	originErr bool
	hc        *http.Client
}

// New creates a school client. Empty origin selects the production service;
// non-empty origins are intended for controlled local test servers.
func New(origin string, hc *http.Client) *Client {
	if origin == "" {
		origin = productionOrigin
	}
	c := &Client{hc: hc}
	if hc == nil {
		c.hc = &http.Client{}
	}
	u, err := url.Parse(origin)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		c.originErr = true
		return c
	}
	c.origin = u.Scheme + "://" + u.Host
	return c
}

type MiniSession struct {
	client *Client
	hc     *http.Client
}

type BusinessSession struct {
	client *Client
	hc     *http.Client
}

type User struct {
	ID       string
	Username string
	FullName string
}

type TodayRecord struct {
	BusinessDate     string
	RecordStatusCode string
	StudentSn        string
	Message          string
}

type schoolEnvelope struct {
	fields  map[string]json.RawMessage
	data    json.RawMessage
	code    *int64
	status  *bool
	ok      *bool
	message string
}

func (c *Client) sessionClient(stage string) (*http.Client, error) {
	if c.originErr || c.origin == "" {
		return nil, schoolError(stage, "configuration", "invalid_origin", false)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, schoolError(stage, "internal", "cookie_jar_failed", false)
	}
	clone := *c.hc
	clone.Jar = jar
	clone.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	clone.Timeout = 30 * time.Second
	return &clone, nil
}

func (c *Client) serviceURL(path string, query url.Values) string {
	u := c.origin + path
	if len(query) != 0 {
		return u + "?" + query.Encode()
	}
	return u
}

func (c *Client) request(ctx context.Context, hc *http.Client, stage, method, path string, query url.Values, platform string, body any, checkinTicket ...string) (schoolEnvelope, error) {
	if c.originErr || c.origin == "" {
		return schoolEnvelope{}, schoolError(stage, "configuration", "invalid_origin", false)
	}
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return schoolEnvelope{}, schoolError(stage, "internal", "request_encode_failed", false)
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.serviceURL(path, query), payload)
	if err != nil {
		return schoolEnvelope{}, schoolError(stage, "internal", "request_build_failed", false)
	}
	req.Header.Set("platform", platform)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if len(checkinTicket) != 0 {
		req.Header.Set("X-Checkin-Ticket", checkinTicket[0])
	}
	var wrote atomic.Bool
	trace := &httptrace.ClientTrace{
		WroteHeaders: func() { wrote.Store(true) },
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				wrote.Store(true)
			}
		},
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	resp, err := hc.Do(req)
	if err != nil {
		ambiguous := method == http.MethodPost && (wrote.Load() || resp != nil)
		return schoolEnvelope{}, schoolError(stage, "network", "request_failed", ambiguous)
	}
	defer resp.Body.Close()
	wasWritten := wrote.Load() || resp != nil
	ambiguous := method == http.MethodPost && wasWritten
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return schoolEnvelope{}, schoolError(stage, "protocol", "unexpected_redirect", ambiguous)
	}
	limited := io.LimitReader(resp.Body, maxResponseBytes+1)
	responseBody, readErr := io.ReadAll(limited)
	if readErr != nil || len(responseBody) > maxResponseBytes {
		return schoolEnvelope{}, schoolError(stage, "protocol", "invalid_response_body", ambiguous)
	}
	env, parseErr := parseEnvelope(responseBody)
	if parseErr != nil {
		if resp.StatusCode == http.StatusUnauthorized {
			return schoolEnvelope{}, withResponseMessage(schoolError(stage, "authentication", "http_401", false), env.message)
		}
		if resp.StatusCode == http.StatusForbidden {
			return schoolEnvelope{}, withResponseMessage(schoolError(stage, "authorization", "http_403", false), env.message)
		}
		return schoolEnvelope{}, withResponseMessage(schoolError(stage, "protocol", "invalid_json", ambiguous), env.message)
	}
	if err := envelopeFailure(env, resp.StatusCode, platform == "mp-weixin", stage); err != nil {
		if ambiguous && err.Category == "protocol" {
			err.Ambiguous = true
		}
		return schoolEnvelope{}, withResponseMessage(err, env.message)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode >= 500 && method == http.MethodPost {
			return schoolEnvelope{}, withResponseMessage(schoolError(stage, "protocol", "http_"+strconv.Itoa(resp.StatusCode), ambiguous), env.message)
		}
		return schoolEnvelope{}, withResponseMessage(schoolError(stage, "http", "http_"+strconv.Itoa(resp.StatusCode), false), env.message)
	}
	return env, nil
}

func parseEnvelope(body []byte) (schoolEnvelope, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return schoolEnvelope{}, errors.New("invalid envelope")
	}
	e := schoolEnvelope{fields: fields, message: envelopeResponseMessage(fields)}
	if raw, ok := fields["data"]; ok {
		e.data = raw
	}
	if raw, ok := fields["code"]; ok && string(raw) != "null" {
		var n json.Number
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		var v any
		if err := d.Decode(&v); err != nil {
			return e, errors.New("invalid code")
		}
		var ok bool
		n, ok = v.(json.Number)
		if !ok {
			return e, errors.New("invalid code")
		}
		i, err := n.Int64()
		if err != nil {
			return e, errors.New("invalid code")
		}
		e.code = &i
	}
	if raw, ok := fields["status"]; ok && string(raw) != "null" {
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return e, errors.New("invalid status")
		}
		e.status = &b
	}
	if raw, ok := fields["ok"]; ok && string(raw) != "null" {
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return e, errors.New("invalid ok")
		}
		e.ok = &b
	}
	return e, nil
}

func envelopeResponseMessage(fields map[string]json.RawMessage) string {
	var message string
	if raw, ok := fields["message"]; ok && json.Unmarshal(raw, &message) == nil && message != "" {
		return message
	}
	message = ""
	if raw, ok := fields["msg"]; ok && json.Unmarshal(raw, &message) == nil && message != "" {
		return message
	}
	return ""
}

func envelopeFailure(e schoolEnvelope, httpStatus int, mini bool, stage string) *Error {
	// Ticket errors intentionally take precedence over all other status signals.
	if e.code != nil && (*e.code == 46001 || *e.code == 46002 || *e.code == 46003) {
		return schoolError(stage, "ticket", strconv.FormatInt(*e.code, 10), false)
	}
	if httpStatus == http.StatusUnauthorized {
		return schoolError(stage, "authentication", "http_401", false)
	}
	if httpStatus == http.StatusForbidden {
		return schoolError(stage, "authorization", "http_403", false)
	}
	if e.code != nil && *e.code == 4003 {
		return schoolError(stage, "authorization", "4003", false)
	}
	if e.code != nil && *e.code == 5000 && envelopeMessageIndicatesExpiredSession(e.fields) {
		return schoolError(stage, "authentication", "5000", false)
	}
	if mini {
		if e.ok == nil {
			return schoolError(stage, "protocol", "missing_ok", false)
		}
		if *e.ok {
			return nil
		}
		return schoolError(stage, "api", businessCode(e.code, "api_rejected"), false)
	}
	if (e.code != nil && *e.code == 0) || (e.status != nil && *e.status) {
		return nil
	}
	if e.code == nil && e.status == nil {
		return schoolError(stage, "protocol", "missing_business_status", false)
	}
	return schoolError(stage, "api", businessCode(e.code, "api_rejected"), false)
}

func businessCode(code *int64, fallback string) string {
	if code == nil || *code == 0 {
		return fallback
	}
	return strconv.FormatInt(*code, 10)
}

func envelopeMessageIndicatesExpiredSession(fields map[string]json.RawMessage) bool {
	for _, key := range []string{"msg", "message"} {
		raw, ok := fields[key]
		if !ok {
			continue
		}
		var message string
		if json.Unmarshal(raw, &message) == nil && (strings.Contains(message, "未登录") || strings.Contains(message, "会话已失效")) {
			return true
		}
	}
	return false
}

func malformedData(stage string, ambiguous bool) *Error {
	return schoolError(stage, "protocol", "invalid_data", ambiguous)
}

func decodeObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var object map[string]json.RawMessage
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, false
	}
	return object, true
}

func requiredString(fields map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := fields[key]
	if !ok {
		return "", false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) == "" {
		return "", false
	}
	return value, true
}

func validSessionCookie(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r <= 0x20 || r >= 0x7f || r == '"' || r == ',' || r == ';' || r == '\\' {
			return false
		}
	}
	return true
}

func (c *Client) cookieURL(path string) (*url.URL, error) {
	if c.originErr || c.origin == "" {
		return nil, errors.New("invalid origin")
	}
	return url.Parse(c.origin + path)
}

func setSessionCookie(hc *http.Client, target *url.URL, value string) {
	hc.Jar.SetCookies(target, []*http.Cookie{{Name: "SESSION", Value: value, Path: "/"}})
}

func sessionCookie(hc *http.Client, target *url.URL) string {
	for _, cookie := range hc.Jar.Cookies(target) {
		if cookie.Name == "SESSION" && validSessionCookie(cookie.Value) {
			return cookie.Value
		}
	}
	return ""
}

// CheckBind exchanges the first fresh mini-program code. Unbound accounts are a
// successful result with bound=false and no retained MiniSession; the third
// result preserves the school's response message in either successful case.
func (c *Client) CheckBind(ctx context.Context, code string) (*MiniSession, bool, string, error) {
	if strings.TrimSpace(code) == "" {
		return nil, false, "", schoolError("check_bind", "validation", "invalid_code", false)
	}
	hc, err := c.sessionClient("check_bind")
	if err != nil {
		return nil, false, "", err
	}
	response, err := c.request(ctx, hc, "check_bind", http.MethodPost, "/platform-app/api/auth/check-bind", nil, "mp-weixin", struct {
		Code string `json:"code"`
		Sn   string `json:"sn"`
	}{Code: code, Sn: ""})
	if err != nil {
		return nil, false, "", err
	}
	fields, ok := decodeObject(response.data)
	if !ok {
		return nil, false, "", withResponseMessage(malformedData("check_bind", true), response.message)
	}
	bindRaw, ok := fields["bind"]
	if !ok {
		return nil, false, "", withResponseMessage(malformedData("check_bind", true), response.message)
	}
	var bound *bool
	if json.Unmarshal(bindRaw, &bound) != nil || bound == nil {
		return nil, false, "", withResponseMessage(malformedData("check_bind", true), response.message)
	}
	if !*bound {
		return nil, false, response.message, nil
	}
	sessionID, ok := requiredString(fields, "sessionId")
	if !ok || !validSessionCookie(sessionID) {
		return nil, false, "", withResponseMessage(malformedData("check_bind", true), response.message)
	}
	mini := &MiniSession{client: c, hc: hc}
	target, err := c.cookieURL("/platform-app/api/auth/user-info")
	if err != nil {
		return nil, false, "", withResponseMessage(schoolError("check_bind", "configuration", "invalid_origin", false), response.message)
	}
	if current := sessionCookie(hc, target); current == "" {
		setSessionCookie(hc, target, sessionID)
	}
	return mini, true, response.message, nil
}

func decodeUser(data json.RawMessage) (User, bool) {
	fields, ok := decodeObject(data)
	if !ok {
		return User{}, false
	}
	userData, ok := decodeObject(fields["userData"])
	if !ok {
		return User{}, false
	}
	id, idOK := requiredString(userData, "id")
	username, usernameOK := requiredString(userData, "username")
	fullName, fullNameOK := requiredString(userData, "fullName")
	if !idOK || !usernameOK || !fullNameOK {
		return User{}, false
	}
	return User{ID: id, Username: username, FullName: fullName}, true
}

// MiniUserInfo validates the active mini-program SESSION and returns the
// school's public identity fields.
func (m *MiniSession) MiniUserInfo(ctx context.Context) (User, error) {
	if m == nil || m.client == nil || m.hc == nil {
		return User{}, schoolError("mini_user_info", "configuration", "missing_session", false)
	}
	response, err := m.client.request(ctx, m.hc, "mini_user_info", http.MethodGet, "/platform-app/api/auth/user-info", nil, "mp-weixin", nil)
	if err != nil {
		return User{}, err
	}
	user, ok := decodeUser(response.data)
	if !ok {
		return User{}, withResponseMessage(malformedData("mini_user_info", false), response.message)
	}
	return user, nil
}

// OpenBusinessSession bootstraps an isolated H5 business jar only from the
// current SESSION in mini's jar, then checks that it resolves to expectedID.
func (c *Client) OpenBusinessSession(ctx context.Context, mini *MiniSession, expectedID string) (*BusinessSession, error) {
	if mini == nil || mini.client != c || mini.hc == nil || strings.TrimSpace(expectedID) == "" {
		return nil, schoolError("business_session", "configuration", "missing_session", false)
	}
	miniURL, err := c.cookieURL("/platform-app/api/auth/user-info")
	if err != nil {
		return nil, schoolError("business_session", "configuration", "invalid_origin", false)
	}
	miniSessionID := sessionCookie(mini.hc, miniURL)
	if miniSessionID == "" {
		return nil, schoolError("business_session", "authentication", "missing_session", false)
	}
	hc, err := c.sessionClient("business_session")
	if err != nil {
		return nil, err
	}
	businessURL, err := c.cookieURL("/daily-checkin/api/auth/user-info")
	if err != nil {
		return nil, schoolError("business_session", "configuration", "invalid_origin", false)
	}
	setSessionCookie(hc, businessURL, miniSessionID)
	query := url.Values{"sessionId": []string{miniSessionID}}
	response, err := c.request(ctx, hc, "business_session", http.MethodGet, "/daily-checkin/api/auth/user-info", query, "h5", nil)
	if err != nil {
		return nil, err
	}
	user, ok := decodeUser(response.data)
	if !ok {
		return nil, withResponseMessage(malformedData("business_session", false), response.message)
	}
	if user.ID != expectedID {
		return nil, withResponseMessage(schoolError("business_session", "identity_mismatch", "identity_mismatch", false), response.message)
	}
	return &BusinessSession{client: c, hc: hc}, nil
}

// Ticket exchanges a second, fresh mini-program code for a checkin ticket.
func (m *MiniSession) Ticket(ctx context.Context, code string) (string, error) {
	if strings.TrimSpace(code) == "" {
		return "", schoolError("ticket", "validation", "invalid_code", false)
	}
	response, err := m.client.request(ctx, m.hc, "ticket", http.MethodPost, "/platform-app/api/auth/checkin-ticket", nil, "mp-weixin", struct {
		Code string `json:"code"`
	}{Code: code})
	if err != nil {
		return "", err
	}
	fields, ok := decodeObject(response.data)
	if !ok {
		return "", withResponseMessage(malformedData("ticket", true), response.message)
	}
	ticket, ok := requiredString(fields, "ticket")
	if !ok || !ticketPattern.MatchString(ticket) {
		return "", withResponseMessage(malformedData("ticket", true), response.message)
	}
	return ticket, nil
}

// Today reads the current business day's record. A JSON null data field means
// no record yet; callers decide what record state warrants a check-in.
func (b *BusinessSession) Today(ctx context.Context) (*TodayRecord, error) {
	if b == nil || b.client == nil || b.hc == nil {
		return nil, schoolError("today", "configuration", "missing_session", false)
	}
	response, err := b.client.request(ctx, b.hc, "today", http.MethodGet, "/daily-checkin/api/student-checkin-record/today", nil, "h5", nil)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(bytes.TrimSpace(response.data), []byte("null")) {
		return nil, nil
	}
	fields, ok := decodeObject(response.data)
	if !ok {
		return nil, withResponseMessage(malformedData("today", false), response.message)
	}
	businessDate, dateOK := requiredString(fields, "businessDate")
	status, statusOK := requiredString(fields, "recordStatusCode")
	studentSn, snOK := requiredString(fields, "studentSn")
	if !dateOK || !statusOK || !snOK {
		return nil, withResponseMessage(malformedData("today", false), response.message)
	}
	return &TodayRecord{BusinessDate: businessDate, RecordStatusCode: status, StudentSn: studentSn, Message: response.message}, nil
}

// Reverse obtains the address used in the check-in payload for the fixed PoC
// coordinates.
func (b *BusinessSession) Reverse(ctx context.Context) (string, error) {
	if b == nil || b.client == nil || b.hc == nil {
		return "", schoolError("reverse_geocode", "configuration", "missing_session", false)
	}
	query := url.Values{
		"lat": {strconv.FormatFloat(Latitude, 'f', -1, 64)},
		"lng": {strconv.FormatFloat(Longitude, 'f', -1, 64)},
	}
	response, err := b.client.request(ctx, b.hc, "reverse_geocode", http.MethodGet, "/daily-checkin/api/map/reverse", query, "h5", nil)
	if err != nil {
		return "", err
	}
	var address string
	if json.Unmarshal(response.data, &address) != nil || strings.TrimSpace(address) == "" {
		return "", withResponseMessage(malformedData("reverse_geocode", false), response.message)
	}
	return address, nil
}

// Checkin submits exactly one write and never retries. The caller's timestamp
// must still be on businessDate in Beijing at the instant this method starts.
// It returns the school's response message on success.
func (b *BusinessSession) Checkin(ctx context.Context, ticket, address, businessDate string, now time.Time) (string, error) {
	if b == nil || b.client == nil || b.hc == nil {
		return "", schoolError("checkin", "configuration", "missing_session", false)
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return "", schoolError("checkin", "internal", "timezone_unavailable", false)
	}
	if now.In(location).Format("2006-01-02") != businessDate {
		return "", schoolError("checkin", "date_changed", "date_changed", false)
	}
	if !ticketPattern.MatchString(ticket) {
		return "", schoolError("checkin", "ticket", "invalid_ticket", false)
	}
	if strings.TrimSpace(address) == "" {
		return "", schoolError("checkin", "validation", "invalid_address", false)
	}
	body := struct {
		ClientTime       string  `json:"clientTime"`
		CoordinateSystem string  `json:"coordinateSystemCode"`
		Latitude         float64 `json:"latitude"`
		Longitude        float64 `json:"longitude"`
		ReverseGeocode   string  `json:"reverseGeocode"`
	}{
		ClientTime:       now.In(location).Format("2006-01-02 15:04:05"),
		CoordinateSystem: "gcj02",
		Latitude:         Latitude,
		Longitude:        Longitude,
		ReverseGeocode:   address,
	}
	// Only the check-in write receives the one-time operation ticket.
	response, err := b.client.request(ctx, b.hc, "checkin", http.MethodPost, "/daily-checkin/api/student-checkin-record/checkin", nil, "h5", body, ticket)
	if err != nil {
		return "", err
	}
	return response.message, nil
}
