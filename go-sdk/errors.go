package moonuestc

import "fmt"

// APIError is a business failure returned inside an HTTP 2xx JSON envelope.
type APIError struct {
	Message    string
	Code       int64
	HasCode    bool
	StatusCode int
}

func (e APIError) Error() string {
	switch {
	case e.HasCode && e.StatusCode != 0:
		return fmt.Sprintf("moonuestc: %s (code %d, HTTP %d)", e.Message, e.Code, e.StatusCode)
	case e.HasCode:
		return fmt.Sprintf("moonuestc: %s (code %d)", e.Message, e.Code)
	case e.StatusCode != 0:
		return fmt.Sprintf("moonuestc: %s (HTTP %d)", e.Message, e.StatusCode)
	default:
		return "moonuestc: " + e.Message
	}
}

// AuthenticationError means the SESSION cookie is missing, rejected or expired.
type AuthenticationError struct{ APIError }

func (e *AuthenticationError) Unwrap() error { return &e.APIError }

// AuthorizationError means the authenticated identity may not perform the operation.
type AuthorizationError struct{ APIError }

func (e *AuthorizationError) Unwrap() error { return &e.APIError }

// CheckinTicketError means the independent mini-program operation ticket is
// missing, expired or bound to another identity. It also unwraps to
// AuthorizationError and APIError.
type CheckinTicketError struct{ AuthorizationError }

func (e *CheckinTicketError) Unwrap() error { return &e.AuthorizationError }

// ProtocolError means the response shape, redirect or media type violates the
// extracted student API contract.
type ProtocolError struct{ Message string }

func (e *ProtocolError) Error() string { return "moonuestc: protocol error: " + e.Message }

// HTTPError is an unsuccessful HTTP status without a recognized auth or ticket code.
type HTTPError struct {
	StatusCode int
	Message    string
}

func (e *HTTPError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("moonuestc: HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("moonuestc: HTTP %d: %s", e.StatusCode, e.Message)
}

// ValidationError is raised locally before any network request.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return "moonuestc: validation error: " + e.Message
	}
	return fmt.Sprintf("moonuestc: invalid %s: %s", e.Field, e.Message)
}
