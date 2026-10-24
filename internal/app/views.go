package app

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("not found")
var ErrExpired = errors.New("scan expired")

type AccountView struct {
	ID                                                         int64
	SchoolUserID, SchoolUsername, SchoolName, CredentialStatus string
	LastRefreshAt                                              *int64
	ExpiresAt                                                  int64
	LastErrorStage, LastErrorCode, BusinessStatus              string
	LastBusinessCheckAt                                        *int64
	BusinessErrorCode, TodayStatus                             string
}
type RunView struct {
	ID, AccountID                                                      int64
	SchoolName, SchoolUsername, BusinessDate, Status, Stage, ErrorCode string
	Message                                                            string
	StartedAt                                                          int64
	FinishedAt                                                         *int64
}
type ScanView struct {
	ID, Status, Message string
	TargetID, AccountID int64
	TargetName          string
	CreatedAt           time.Time
	Terminal            bool
}

// Backend is the web boundary: it never exposes credentials or authentication artifacts.
type Backend interface {
	Accounts(context.Context) ([]AccountView, error)
	Account(context.Context, int64) (AccountView, error)
	Runs(context.Context, int64, int) ([]RunView, error)
	RunCheckin(context.Context, int64) error
	NewScan(context.Context, string, int64) (ScanView, error)
	Scan(string, string) (ScanView, error)
	ScanImage(string, string) ([]byte, error)
	CancelScan(string, string) error
	Delete(context.Context, int64) error
}
