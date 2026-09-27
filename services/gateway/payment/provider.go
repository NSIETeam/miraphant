// Package payment contains real, fail-closed payment protocol adapters. It
// deliberately has no mock-success provider; tests inject local HTTP servers
// only from package tests.
package payment

import (
	"context"
	"errors"
	"net/http"
	"time"
)

var (
	ErrDisabled      = errors.New("payment channel is disabled")
	ErrUnknownStatus = errors.New("provider payment status is unknown")
	ErrAlreadyPaid   = errors.New("provider confirms order is already paid")
	ErrNotPaid       = errors.New("provider confirms order is not paid")
	ErrInvalidNotice = errors.New("invalid payment notification")
	ErrOrderMismatch = errors.New("provider payment does not match the order")
	ErrNotCloseable  = errors.New("provider order is not confirmed unpaid")
)

type Order struct {
	OrderKey    string
	AmountFen   int64
	Currency    string
	Description string
	ExpiresAt   time.Time
}

type Checkout struct {
	Kind       string            `json:"kind"` // qr or form
	CodeURL    string            `json:"code_url,omitempty"`
	GatewayURL string            `json:"gateway_url,omitempty"`
	Fields     map[string]string `json:"fields,omitempty"`
}

type Trade struct {
	Provider           string
	OrderKey           string
	TransactionID      string
	MerchantID         string
	AppID              string
	AmountFen          int64
	Currency           string
	Status             string
	ProviderEventID    string
	ProviderOccurredAt time.Time
	EvidenceSource     string
}

type VerifiedNotification = Trade

type Provider interface {
	Create(context.Context, Order) (Checkout, error)
	VerifyNotification(http.Header, []byte) (VerifiedNotification, error)
	Query(context.Context, string) (Trade, error)
	Close(context.Context, string) error
}

// RefundOutcome describes what the verified protocol evidence proves. In
// particular, "accepted" is not a completed refund and "unknown" must never
// release a ledger hold.
type RefundOutcome string

const (
	RefundAccepted       RefundOutcome = "accepted"
	RefundSucceeded      RefundOutcome = "succeeded"
	RefundDefiniteFailed RefundOutcome = "definite_failed"
	RefundUnknown        RefundOutcome = "unknown"
	RefundAbnormal       RefundOutcome = "abnormal"
)

// RefundRequest is built from the immutable, server-side refund and paid-order
// snapshots. Adapters verify every field they receive against their own
// configured merchant identity before making a request.
type RefundRequest struct {
	RefundKey         string
	ProviderRefundKey string // WeChat out_refund_no or Alipay out_request_no
	OrderKey          string
	TransactionID     string
	MerchantID        string
	AppID             string
	AmountFen         int64
	TotalFen          int64
	PriorRefundedFen  int64 // trusted local amount completed before this refund
	Currency          string
	Reason            string
}

// RefundResult contains normalized, identity-bound protocol evidence. The
// returned AmountFen is always this refund amount; it is never a provider's
// cumulative refunded amount.
type RefundResult struct {
	Provider           string
	Outcome            RefundOutcome
	Status             string
	RefundKey          string
	ProviderRefundKey  string
	ProviderRefundID   string
	OrderKey           string
	TransactionID      string
	MerchantID         string
	AppID              string
	AmountFen          int64
	TotalFen           int64
	Currency           string
	ProviderEventID    string
	ProviderOccurredAt time.Time
	EvidenceSource     string
	// RetrySameKey permits an exact idempotent resubmission after this result is
	// durably recorded. It never means the provider proved non-acceptance.
	RetrySameKey bool
}

// RefundProvider is a protocol-only interface. Implementations do not persist
// state or decide retry policy; callers must keep funds frozen for unknown or
// merely accepted evidence.
type RefundProvider interface {
	ApplyRefund(context.Context, RefundRequest) (RefundResult, error)
	QueryRefund(context.Context, RefundRequest) (RefundResult, error)
}

// RefundNotificationVerifier is implemented only by channels with a refund
// notification protocol supported in this integration stage.
type RefundNotificationVerifier interface {
	VerifyRefundNotification(http.Header, []byte) (RefundResult, error)
}
