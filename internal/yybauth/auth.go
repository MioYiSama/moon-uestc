// Package moonuestcauth exposes only YYB's WeChat authentication boundary.
package moonuestcauth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"yyb_go/internal/protocol"
	"yyb_go/internal/qr"
	"yyb_go/internal/store"
)

type Credentials = protocol.LoginBufferCredentials
type RefreshRejectedError = protocol.RefreshRejectedError

var ErrMissingRefreshToken = protocol.ErrMissingRefreshToken

type Client struct {
	qr   *qr.Client
	db   *store.DB
	pool *protocol.Pool
}
type QR struct {
	ID        string
	Image     []byte
	CreatedAt time.Time
	session   *qr.Session
}

func Open() (*Client, error) {
	q, err := qr.NewClientWithProxy(30*time.Second, "", false)
	if err != nil {
		return nil, err
	}
	db, err := store.Open(":memory:")
	if err != nil {
		return nil, err
	}
	return &Client{q, db, protocol.NewPool(protocol.DefaultConfig(), db)}, nil
}
func (c *Client) Close() error { return c.db.Close() }
func (c *Client) StartQR(ctx context.Context) (*QR, error) {
	r, err := c.qr.GetQRCodeImage(ctx)
	if err != nil {
		return nil, err
	}
	return &QR{r.Session.ID, r.ImageBytes, r.Session.CreatedAt, r.Session}, nil
}
func (c *Client) PollQR(ctx context.Context, q *QR) (string, error) {
	r, err := c.qr.PollQRCode(ctx, q.session)
	return r.Status, err
}
func (c *Client) ExchangeQR(ctx context.Context, q *QR) (Credentials, error) {
	r, err := c.qr.GetLoginBuffer(ctx, q.session)
	return r.Credentials, err
}
func (c *Client) Refresh(ctx context.Context, creds Credentials) (Credentials, error) {
	return c.qr.LoginBuffers().RefreshCredentials(ctx, creds)
}
func (c *Client) Code(ctx context.Context, creds Credentials) (string, error) {
	lb, err := c.qr.LoginBuffers().FetchLoginBuffer(ctx, creds)
	if err != nil {
		return "", err
	}
	a, err := c.db.UpsertAccount(ctx, creds.OpenID, lb, nil, nil, nil, nil, nil, nil)
	if err != nil {
		return "", err
	}
	r, err := c.pool.GetCode(ctx, lb, "wxaa78bf2905f01cee", a.ID, "", false)
	if err != nil {
		return "", err
	}
	code, ok := r["code"].(string)
	if !ok || strings.TrimSpace(code) == "" {
		return "", errors.New("empty login code")
	}
	return code, nil
}
func (c *Client) Forget(ctx context.Context, openID string) error {
	a, err := c.db.GetAccountByOpenID(ctx, openID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = c.db.InvalidateAccountSessions(ctx, a.ID); err != nil {
		return err
	}
	return c.db.DeleteAccount(ctx, a.ID)
}
