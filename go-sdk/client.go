// Package moonuestc is a typed Go client for the student-facing "晚安成电"
// daily check-in APIs. It sends and receives JSON with standard library types,
// validates request boundaries locally, and never retries business writes.
package moonuestc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is the deployed student API prefix.
const DefaultBaseURL = "https://jzsz.uestc.edu.cn/daily-checkin"

var ticketCodes = map[int64]struct{}{46001: {}, 46002: {}, 46003: {}}

// Options configures a Client. Supply only your own SESSION cookie.
type Options struct {
	// Session is the value of the HttpOnly SESSION cookie for jzsz.uestc.edu.cn.
	Session string
	// CheckinTicket comes from the official mini-program entry. It is separate
	// from Session and is required only for check-in and situation-report writes.
	CheckinTicket string
	// BearerToken is optional. The deployed frontend currently receives
	// NOT_IN_USE tokens, so SESSION is the verified authentication mechanism.
	BearerToken string
	// BaseURL defaults to DefaultBaseURL and must not contain credentials,
	// a query or a fragment.
	BaseURL string
	// Timeout defaults to 30 seconds. It never triggers an automatic retry.
	Timeout time.Duration
	// HTTPClient is optional and is copied, not modified. Its redirect policy
	// and cookie jar are replaced: credentials cannot follow a redirect, and
	// unrelated cookies are not attached. Server-set SESSION updates are kept
	// in the SDK's own domain-scoped jar.
	HTTPClient *http.Client
}

// Client is a synchronous student API client. It is safe for concurrent use,
// but must be created with NewClient and must not be copied after first use.
type Client struct {
	baseURL string
	origin  string
	http    *http.Client
	bearer  string

	ticketMu sync.RWMutex
	ticket   string

	// Auth exposes user identity and subscription endpoints.
	Auth *AuthService
	// Student exposes the signed-in student's own endpoints.
	Student *StudentService
	// ClassCadre exposes class-cadre endpoints for a CLASS_CADRE identity.
	ClassCadre *ClassCadreService
}

// NewClient validates options and builds a client. No network request is made.
func NewClient(opts Options) (*Client, error) {
	if opts.Session != "" && !validHeaderValue(opts.Session) {
		return nil, &ValidationError{Field: "session", Message: "contains control characters"}
	}
	if opts.CheckinTicket != "" && !validHeaderValue(opts.CheckinTicket) {
		return nil, &ValidationError{Field: "checkinTicket", Message: "contains control characters"}
	}
	if opts.BearerToken != "" && !validHeaderValue(opts.BearerToken) {
		return nil, &ValidationError{Field: "bearerToken", Message: "contains control characters"}
	}
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") {
		return nil, fmt.Errorf("moonuestc: baseURL must be an HTTP(S) URL without credentials, query or fragment")
	}
	if opts.Timeout < 0 {
		return nil, &ValidationError{Field: "timeout", Message: "must not be negative"}
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	var httpClient *http.Client
	if opts.HTTPClient != nil {
		copied := *opts.HTTPClient
		if opts.Timeout == 0 && copied.Timeout != 0 {
			timeout = copied.Timeout
		}
		httpClient = &copied
	} else {
		httpClient = &http.Client{}
	}
	httpClient.Timeout = timeout
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("moonuestc: create cookie jar: %w", err)
	}
	if opts.Session != "" {
		jar.SetCookies(parsed, []*http.Cookie{{
			Name:     "SESSION",
			Value:    opts.Session,
			Path:     "/",
			Secure:   parsed.Scheme == "https",
			HttpOnly: true,
		}})
	}
	// Replace any caller-supplied jar so unrelated cookies cannot be attached.
	httpClient.Jar = jar

	c := &Client{
		baseURL: base,
		origin:  parsed.Scheme + "://" + parsed.Host,
		http:    httpClient,
		bearer:  opts.BearerToken,
		ticket:  opts.CheckinTicket,
	}
	c.Auth = &AuthService{client: c}
	c.Student = &StudentService{client: c}
	c.ClassCadre = &ClassCadreService{client: c}
	return c, nil
}

func validHeaderValue(value string) bool {
	for i := range len(value) {
		if value[i] < 0x20 || value[i] == 0x7f {
			return false
		}
	}
	return true
}

// SetCheckinTicket replaces or clears the independent operation ticket.
// It is safe to call while requests are in flight.
func (c *Client) SetCheckinTicket(ticket string) error {
	if ticket != "" && !validHeaderValue(ticket) {
		return &ValidationError{Field: "checkinTicket", Message: "contains control characters"}
	}
	c.ticketMu.Lock()
	c.ticket = ticket
	c.ticketMu.Unlock()
	return nil
}

// CloseIdleConnections releases idle keep-alive connections.
func (c *Client) CloseIdleConnections() {
	c.http.CloseIdleConnections()
}

func (c *Client) currentTicket() string {
	c.ticketMu.RLock()
	defer c.ticketMu.RUnlock()
	return c.ticket
}

type wireResponse struct {
	status int
	header http.Header
	body   []byte
}

func (c *Client) send(
	ctx context.Context,
	method, path string,
	query url.Values,
	body any,
	signed bool,
) (*wireResponse, error) {
	return c.sendAt(ctx, c.baseURL+path, method, query, body, signed)
}

func (c *Client) sendFromOrigin(
	ctx context.Context,
	method, path string,
	query url.Values,
	body any,
	signed bool,
) (*wireResponse, error) {
	return c.sendAt(ctx, c.origin+path, method, query, body, signed)
}

func (c *Client) sendAt(
	ctx context.Context,
	endpoint, method string,
	query url.Values,
	body any,
	signed bool,
) (*wireResponse, error) {
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, &ValidationError{Message: "encode request body: " + err.Error()}
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return nil, err
	}
	request.Header.Set("platform", "h5")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.bearer != "" {
		request.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	if signed {
		ticket := c.currentTicket()
		if ticket == "" {
			return nil, &CheckinTicketError{AuthorizationError{APIError{
				Message: "this operation requires a checkinTicket from the official mini-program entry",
			}}}
		}
		request.Header.Set("X-Checkin-Ticket", ticket)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return nil, &ProtocolError{Message: "unexpected redirect; log in through the official entry to renew the session"}
	}
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	return &wireResponse{status: response.StatusCode, header: response.Header, body: responseBody}, nil
}

func (c *Client) doJSON(
	ctx context.Context,
	method, path string,
	query url.Values,
	body any,
	signed bool,
) ([]byte, error) {
	response, err := c.send(ctx, method, path, query, body, signed)
	if err != nil {
		return nil, err
	}
	return c.validateEnvelope(response.status, response.body)
}

func request[T any](
	c *Client,
	ctx context.Context,
	method, path string,
	query url.Values,
	body any,
	signed bool,
) (*Response[T], error) {
	payload, err := c.doJSON(ctx, method, path, query, body, signed)
	if err != nil {
		return nil, err
	}
	var response Response[T]
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, &ProtocolError{Message: "response data does not match the student API contract: " + err.Error()}
	}
	return &response, nil
}

func (c *Client) validateEnvelope(status int, payload []byte) ([]byte, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		if status >= 200 && status < 300 {
			return nil, &ProtocolError{Message: "expected a JSON object; the server may have returned a login page"}
		}
		return nil, &HTTPError{StatusCode: status, Message: http.StatusText(status)}
	}
	code, hasCode := parseEnvelopeCode(raw["code"])
	message := envelopeMessage(raw)
	_, hasStatus := raw["status"]
	statusTrue := rawBool(raw["status"])

	if _, ok := ticketCodes[code]; ok && hasCode {
		return nil, &CheckinTicketError{AuthorizationError{APIError{
			Message: message, Code: code, HasCode: true, StatusCode: status,
		}}}
	}
	if status == http.StatusUnauthorized {
		return nil, &AuthenticationError{APIError{Message: message, Code: code, HasCode: hasCode, StatusCode: status}}
	}
	if status == http.StatusForbidden {
		return nil, &AuthorizationError{APIError{Message: message, Code: code, HasCode: hasCode, StatusCode: status}}
	}
	if hasCode && code == 4003 {
		return nil, &AuthorizationError{APIError{Message: message, Code: code, HasCode: true, StatusCode: status}}
	}
	if hasCode && code == 5000 && (strings.Contains(message, "未登录") || strings.Contains(message, "会话已失效")) {
		return nil, &AuthenticationError{APIError{Message: message, Code: code, HasCode: true, StatusCode: status}}
	}
	if status < 200 || status >= 300 {
		return nil, &HTTPError{StatusCode: status, Message: message}
	}
	if (hasCode && code == 0) || statusTrue {
		return payload, nil
	}
	if !hasCode && !hasStatus {
		return nil, &ProtocolError{Message: "JSON response has no recognized business status"}
	}
	return nil, &APIError{Message: message, Code: code, HasCode: hasCode, StatusCode: status}
}

func parseEnvelopeCode(raw json.RawMessage) (int64, bool) {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" || strings.HasPrefix(text, `"`) {
		return 0, false
	}
	code, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, false
	}
	return code, true
}

func envelopeMessage(raw map[string]json.RawMessage) string {
	for _, key := range []string{"msg", "message"} {
		var message string
		if err := json.Unmarshal(raw[key], &message); err == nil && message != "" {
			return message
		}
	}
	return "API request failed"
}

func rawBool(raw json.RawMessage) bool {
	var value bool
	return json.Unmarshal(raw, &value) == nil && value
}
