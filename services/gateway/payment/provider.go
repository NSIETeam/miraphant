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
