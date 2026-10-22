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
