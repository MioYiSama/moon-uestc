package moonuestc

import (
	"context"
	"net/http"
	"net/url"
)

// AuthService exposes identity and subscription endpoints.
type AuthService struct{ client *Client }

// UserInfo returns the signed-in identity. sessionID is an optional bootstrap
// parameter used by the H5 entry; an empty string omits it.
func (a *AuthService) UserInfo(ctx context.Context, sessionID string) (*Response[UserInfo], error) {
	var query url.Values
	if sessionID != "" {
		query = url.Values{"sessionId": {sessionID}}
	}
	response, err := request[UserInfo](
		a.client, ctx, http.MethodGet, "/api/auth/user-info", query, nil, false,
	)
	if err != nil {
		return nil, err
	}
	if err := validateUserInfo(&response.Data); err != nil {
		return nil, err
	}
	return response, nil
}

// RequestCheckinTicket exchanges a caller-supplied mini-program login code for
// a check-in operation ticket. It returns the ticket but does not install it.
func (a *AuthService) RequestCheckinTicket(ctx context.Context, code string) (*Response[CheckinTicket], error) {
	if code == "" {
		return nil, &ValidationError{Field: "code", Message: "must not be empty"}
	}
	response, err := requestAt[CheckinTicket](
		a.client, ctx, a.client.origin+"/platform-app/api/auth/checkin-ticket",
		http.MethodPost, nil, struct {
			Code string `json:"code"`
		}{Code: code}, false, "mp-weixin",
	)
	if err != nil {
		return nil, err
	}
	if !validCheckinTicket(response.Data.Ticket) {
		return nil, &ProtocolError{Message: "checkin-ticket response contains an invalid ticket"}
	}
	return response, nil
}

func validCheckinTicket(ticket string) bool {
	if len(ticket) != 43 {
		return false
	}
	for i := range len(ticket) {
		char := ticket[i]
		if (char < 'A' || char > 'Z') && (char < 'a' || char > 'z') &&
			(char < '0' || char > '9') && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

// ServiceAccountSubscribed reports whether the user follows the service account.
// A missing data field is a protocol error; false is a valid value.
func (a *AuthService) ServiceAccountSubscribed(ctx context.Context) (*Response[*bool], error) {
	response, err := request[*bool](
		a.client, ctx, http.MethodGet, "/api/user/service-account-subscribed", nil, nil, false,
	)
	if err != nil {
		return nil, err
	}
	if response.Data == nil {
		return nil, protocolf("service-account-subscribed data is missing")
	}
	return response, nil
}
