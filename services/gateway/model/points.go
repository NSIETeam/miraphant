package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/config"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const PointMicroPerPoint int64 = 1_000_000

func checkedAdd(a, b int64) (int64, error) {
	maxInt64 := int64(^uint64(0) >> 1)
	if b > 0 && a > maxInt64-b || b < 0 && a < -maxInt64-1-b {
		return 0, errors.New("integer amount overflow")
	}
	return a + b, nil
}

var ErrPointsDisabled = errors.New("points billing is disabled")
var ErrPointsConflict = errors.New("points idempotency key conflict")
var ErrPointsInsufficient = errors.New("insufficient available points")
var ErrPointsPending = errors.New("point hold requires usage review")
var ErrPointsLegacyQuotaDisabled = errors.New("legacy quota changes are disabled while points billing is enabled")
var ErrPointsUserDeletionDisabled = errors.New("user deletion is disabled while points billing is enabled; disable the account instead")
var ErrPointsInvalidTokenSettings = errors.New("invalid points token settings")

type PointAccount struct {
	UserID         int   `gorm:"primaryKey"`
	AvailableMicro int64 `gorm:"not null;default:0"`
	HeldMicro      int64 `gorm:"not null;default:0"`
	SpentMicro     int64 `gorm:"not null;default:0"`
	UpdatedAt      time.Time
}

type PointLot struct {
	ID             uint   `gorm:"primaryKey"`
	UserID         int    `gorm:"not null;index:idx_point_lot_user_expiry"`
	BusinessKey    string `gorm:"size:160;not null;uniqueIndex"`
	Kind           string `gorm:"size:24;not null;index"` // purchase, bonus, grant, migration
	SourceRef      string `gorm:"size:160;not null;default:''"`
	AmountFen      int64  `gorm:"not null;default:0"`
	InitialMicro   int64  `gorm:"not null"`
	AvailableMicro int64  `gorm:"not null"`
	HeldMicro      int64  `gorm:"not null;default:0"`
	ConsumedMicro  int64  `gorm:"not null;default:0"`
	RefundedMicro  int64  `gorm:"not null;default:0"`
	ExpiresAt      *int64 `gorm:"index:idx_point_lot_user_expiry"` // UTC Unix seconds
	CreatedAt      time.Time
}

type PointLedger struct {
	ID             uint   `gorm:"primaryKey"`
	UserID         int    `gorm:"not null;index"`
	BusinessKey    string `gorm:"size:180;not null;uniqueIndex"`
	Kind           string `gorm:"size:32;not null"`
	LotID          *uint  `gorm:"index"`
	HoldID         *uint  `gorm:"index"`
	OrderID        *uint  `gorm:"index"`
	AvailableDelta int64  `gorm:"not null"`
	HeldDelta      int64  `gorm:"not null"`
	SpentDelta     int64  `gorm:"not null"`
	AvailableAfter int64  `gorm:"not null"`
	HeldAfter      int64  `gorm:"not null"`
	SpentAfter     int64  `gorm:"not null"`
	Reason         string `gorm:"size:512;not null;default:''"`
	CreatedAt      time.Time
}

func (*PointLedger) BeforeUpdate(*gorm.DB) error { return errors.New("point ledger is append-only") }
func (*PointLedger) BeforeDelete(*gorm.DB) error { return errors.New("point ledger is append-only") }

type PointPriceVersion struct {
	ID                    uint   `gorm:"primaryKey"`
	Version               string `gorm:"size:80;not null;uniqueIndex"`
	ModelID               string `gorm:"size:160;not null"`
	Source                string `gorm:"size:512;not null;default:''"`
	InputMicroPer1K       int64  `gorm:"not null"`
	CachedInputMicroPer1K int64  `gorm:"not null"`
	OutputMicroPer1K      int64  `gorm:"not null"`
	ExtraMicro            int64  `gorm:"not null;default:0"`
	CreatedAt             time.Time
}

type PointActivePrice struct {
	ModelID   string `gorm:"primaryKey;size:160"`
	Version   string `gorm:"size:80;not null"`
	UpdatedAt time.Time
}

type PointHold struct {
	ID                uint   `gorm:"primaryKey"`
	UserID            int    `gorm:"not null;index"`
	TokenID           int    `gorm:"not null;index"`
	LogicalRequestKey string `gorm:"size:180;not null;uniqueIndex"`
	BudgetMicro       int64  `gorm:"not null"`
	PriceVersion      string `gorm:"size:80;not null;default:''"`
	PriceSnapshot     string `gorm:"type:text;not null;default:''"`
	State             string `gorm:"size:24;not null;index"` // held, pending, settled, released, needs_review
	UsageMicro        int64  `gorm:"not null;default:0"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type PointHoldAllocation struct {
	ID            uint  `gorm:"primaryKey"`
	HoldID        uint  `gorm:"not null;uniqueIndex:idx_point_hold_lot"`
	LotID         uint  `gorm:"not null;uniqueIndex:idx_point_hold_lot;index"`
	ReservedMicro int64 `gorm:"not null"`
	SettledMicro  int64 `gorm:"not null;default:0"`
}

type PointHoldAttempt struct {
	ID                 uint   `gorm:"primaryKey"`
	HoldID             uint   `gorm:"not null;uniqueIndex:idx_point_attempt_key"`
	AttemptKey         string `gorm:"size:180;not null;uniqueIndex:idx_point_attempt_key"`
	LocalRequestID     string `gorm:"size:180;not null;default:''"`
	RequestFingerprint string `gorm:"size:128;not null;default:''"`
	State              string `gorm:"size:24;not null;index"` // started, succeeded, failed, unknown
	UsageMicro         int64  `gorm:"not null;default:0"`
	UsageSource        string `gorm:"size:48;not null;default:''"`
	UsageAuthoritative bool   `gorm:"not null;default:false"`
	UsageFingerprint   string `gorm:"size:128;not null;default:''"`
	Billable           bool   `gorm:"not null;default:false"`
	PromptTokens       int64  `gorm:"not null;default:0"`
	CachedPromptTokens int64  `gorm:"not null;default:0"`
	CompletionTokens   int64  `gorm:"not null;default:0"`
	ReasoningTokens    int64  `gorm:"not null;default:0"`
	ProviderRequestID  string `gorm:"size:180;not null;default:''"`
	ProviderResponseID string `gorm:"size:180;not null;default:''"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type PointHoldDecision struct {
	ID          uint   `gorm:"primaryKey"`
	HoldID      uint   `gorm:"not null;index"`
	DecisionKey string `gorm:"size:180;not null;uniqueIndex"`
	Action      string `gorm:"size:16;not null"`
	UsageMicro  int64  `gorm:"not null;default:0"`
	Reason      string `gorm:"size:512;not null"`
	CreatedAt   time.Time
}

type PointAdminAudit struct {
	ID           uint   `gorm:"primaryKey"`
	ActorUserID  int    `gorm:"not null;index"`
	Action       string `gorm:"size:48;not null;index"`
	BusinessKey  string `gorm:"size:180;not null;uniqueIndex"`
	TargetUserID int    `gorm:"not null;default:0;index"`
	Details      string `gorm:"type:text;not null;default:''"`
	CreatedAt    time.Time
}

func (*PointAdminAudit) BeforeUpdate(*gorm.DB) error {
	return errors.New("point admin audit is append-only")
}
func (*PointAdminAudit) BeforeDelete(*gorm.DB) error {
	return errors.New("point admin audit is append-only")
}

func (*PointHoldDecision) BeforeUpdate(*gorm.DB) error {
	return errors.New("point hold decisions are append-only")
}
func (*PointHoldDecision) BeforeDelete(*gorm.DB) error {
	return errors.New("point hold decisions are append-only")
}

type PointTokenBudget struct {
	TokenID    int   `gorm:"primaryKey"`
	UserID     int   `gorm:"not null;index"`
	LimitMicro int64 `gorm:"not null;default:0"`
	HeldMicro  int64 `gorm:"not null;default:0"`
	SpentMicro int64 `gorm:"not null;default:0"`
	Unlimited  bool  `gorm:"not null;default:false"`
	UpdatedAt  time.Time
}

type PointPurchaseOrder struct {
	ID                    uint    `gorm:"primaryKey"`
	OrderKey              string  `gorm:"size:160;not null;uniqueIndex"`
	UserID                int     `gorm:"not null;index;uniqueIndex:idx_point_order_idempotency"`
	IdempotencyKey        *string `gorm:"size:180;uniqueIndex:idx_point_order_idempotency"`
	Channel               string  `gorm:"size:16;not null;default:'';index"`
	PackageID             string  `gorm:"size:80;not null;default:'';index"`
	PackageVersion        string  `gorm:"size:80;not null;default:''"`
	PackageSnapshot       string  `gorm:"type:text;not null;default:''"`
	Currency              string  `gorm:"size:3;not null;default:'CNY'"`
	AmountFen             int64   `gorm:"not null"`
	PurchaseMicro         int64   `gorm:"not null"`
	BonusMicro            int64   `gorm:"not null;default:0"`
	BonusValiditySecs     int64   `gorm:"not null;default:0"`
	BonusExpiresAt        *int64  // UTC Unix seconds
	ExpiresAt             *int64  // UTC Unix seconds
	ProviderTransactionID string  `gorm:"size:180;not null;default:'';index"`
	ProviderMerchantID    string  `gorm:"size:80;not null;default:''"`
	State                 string  `gorm:"size:24;not null;index"` // pending, paid, credited, closed, paid_review, refunded
	PaidEventKey          *string `gorm:"size:180;uniqueIndex"`
	ClosedReason          string  `gorm:"size:128;not null;default:''"`
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// PointPackage is an immutable price/credit snapshot. Package changes create
// a new version; existing orders retain the exact snapshot they were created
// from, including bonus expiry.
type PointPackage struct {
	ID                uint   `gorm:"primaryKey"`
	PackageID         string `gorm:"size:80;not null;uniqueIndex:idx_point_package_version"`
	Version           string `gorm:"size:80;not null;uniqueIndex:idx_point_package_version"`
	Name              string `gorm:"size:160;not null"`
	AmountFen         int64  `gorm:"not null"`
	PurchaseMicro     int64  `gorm:"not null"`
	BonusMicro        int64  `gorm:"not null;default:0"`
	BonusValiditySecs int64  `gorm:"not null;default:0"`
	Currency          string `gorm:"size:3;not null;default:'CNY'"`
	Published         bool   `gorm:"not null;default:false;index"`
	CreatedBy         int    `gorm:"not null;default:0"`
	CreatedAt         time.Time
}

func (*PointPackage) BeforeUpdate(*gorm.DB) error {
	return errors.New("point packages are immutable; publish a new version")
}
func (*PointPackage) BeforeDelete(*gorm.DB) error {
	return errors.New("point packages are immutable")
}

type PointActivePackage struct {
	PackageID string `gorm:"primaryKey;size:80"`
	Version   string `gorm:"size:80;not null"`
	UpdatedBy int    `gorm:"not null;default:0"`
	UpdatedAt time.Time
}

// PaymentEvent is a durable notification inbox. Raw callback bodies and
// credentials are intentionally never stored; Digest is SHA-256 of the exact
// callback body and Payload is the normalized, non-secret provider result.
type PaymentEvent struct {
	ID                    uint   `gorm:"primaryKey"`
	Provider              string `gorm:"size:16;not null;uniqueIndex:idx_payment_event_provider_key"`
	ProviderEventID       string `gorm:"size:180;not null;uniqueIndex:idx_payment_event_provider_key"`
	OrderKey              string `gorm:"size:160;not null;index"`
	ProviderTransactionID string `gorm:"size:180;not null;default:'';index"`
	Digest                string `gorm:"size:64;not null"`
	Payload               string `gorm:"type:text;not null"`
	Verification          string `gorm:"size:24;not null"`       // verified, rejected, mismatch
	State                 string `gorm:"size:24;not null;index"` // received, processed, quarantined
	ErrorCode             string `gorm:"size:64;not null;default:''"`
	CreatedAt             time.Time
	ProcessedAt           *time.Time
}

// PaymentTransaction is the durable, unique owner of a provider transaction.
// Quarantined inbox notifications never claim ownership. A claim is inserted
// in the same transaction as the paid order transition and point credit.
type PaymentTransaction struct {
	ID                    uint   `gorm:"primaryKey"`
	Provider              string `gorm:"size:16;not null;uniqueIndex:idx_payment_transaction_owner"`
	MerchantID            string `gorm:"size:80;not null;uniqueIndex:idx_payment_transaction_owner"`
	AppID                 string `gorm:"size:80;not null"`
	ProviderTransactionID string `gorm:"size:180;not null;uniqueIndex:idx_payment_transaction_owner"`
	OrderKey              string `gorm:"size:160;not null;index"`
	EventID               uint   `gorm:"not null;index"`
	CreatedAt             time.Time
}

func (*PaymentTransaction) BeforeUpdate(*gorm.DB) error {
	return errors.New("payment transaction ownership is append-only")
}
func (*PaymentTransaction) BeforeDelete(*gorm.DB) error {
	return errors.New("payment transaction ownership is append-only")
}

func (*PaymentEvent) BeforeUpdate(tx *gorm.DB) error {
	updates, ok := tx.Statement.Dest.(map[string]interface{})
	if !ok || len(updates) == 0 {
		return errors.New("payment event updates must use the transition-field allowlist")
	}
	for key := range updates {
		switch strings.ToLower(key) {
		case "state", "processed_at", "error_code":
		default:
			return errors.New("payment event evidence is immutable")
		}
	}
	return nil
}
func (*PaymentEvent) BeforeDelete(*gorm.DB) error {
	return errors.New("payment event evidence is append-only")
}

func (*PointPriceVersion) BeforeUpdate(*gorm.DB) error {
	return errors.New("point price versions are immutable")
}
func (*PointPriceVersion) BeforeDelete(*gorm.DB) error {
	return errors.New("point price versions are immutable")
}

func CreatePointPriceVersion(price *PointPriceVersion) error {
	if price == nil || price.Version == "" || price.ModelID == "" || price.InputMicroPer1K < 0 || price.CachedInputMicroPer1K < 0 || price.CachedInputMicroPer1K > price.InputMicroPer1K || price.OutputMicroPer1K < 0 || price.ExtraMicro < 0 {
		return errors.New("invalid price version")
	}
	return DB.Create(price).Error
}

func ActivatePointPriceVersion(modelID, version string) error {
	if modelID == "" || version == "" {
		return errors.New("model and price version are required")
	}
	return pointsTransaction(func(tx *gorm.DB) error {
		var price PointPriceVersion
		if err := tx.Where("version = ? AND model_id = ?", version, modelID).First(&price).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "model_id"}}, DoUpdates: clause.Assignments(map[string]interface{}{"version": version, "updated_at": now})}).Create(&PointActivePrice{ModelID: modelID, Version: version, UpdatedAt: now}).Error
	})
}

func PublishPointPriceVersion(actorID int, price *PointPriceVersion) error {
	if actorID <= 0 || price == nil || price.ModelID == "" || price.Version == "" || price.Source == "" || price.InputMicroPer1K < 0 || price.CachedInputMicroPer1K < 0 || price.CachedInputMicroPer1K > price.InputMicroPer1K || price.OutputMicroPer1K < 0 || price.ExtraMicro < 0 {
		return errors.New("invalid price publication")
	}
	return pointsTransaction(func(tx *gorm.DB) error {
		var existing PointPriceVersion
		lookup := tx.Where("version = ?", price.Version).First(&existing)
		if lookup.Error == nil {
			if existing.ModelID == price.ModelID && existing.Source == price.Source && existing.InputMicroPer1K == price.InputMicroPer1K && existing.CachedInputMicroPer1K == price.CachedInputMicroPer1K && existing.OutputMicroPer1K == price.OutputMicroPer1K && existing.ExtraMicro == price.ExtraMicro {
				var audit PointAdminAudit
				if tx.Where("business_key = ? AND actor_user_id = ?", "price:"+price.Version, actorID).First(&audit).Error == nil {
					return nil
				}
			}
			return ErrPointsConflict
		}
		if !errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
			return lookup.Error
		}
		if err := tx.Create(price).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "model_id"}}, DoUpdates: clause.Assignments(map[string]interface{}{"version": price.Version, "updated_at": now})}).Create(&PointActivePrice{ModelID: price.ModelID, Version: price.Version, UpdatedAt: now}).Error; err != nil {
			return err
		}
		details, _ := json.Marshal(map[string]interface{}{"version": price.Version, "model_id": price.ModelID, "input_micro_per_1k": price.InputMicroPer1K, "cached_input_micro_per_1k": price.CachedInputMicroPer1K, "output_micro_per_1k": price.OutputMicroPer1K, "extra_micro": price.ExtraMicro, "source": price.Source})
		return tx.Create(&PointAdminAudit{ActorUserID: actorID, Action: "price_publish", BusinessKey: "price:" + price.Version, Details: string(details)}).Error
	})
}

func GetActivePointPrices() ([]PointPriceVersion, error) {
	var prices []PointPriceVersion
	err := DB.Table("point_price_versions AS p").Select("p.*").Joins("JOIN point_active_prices AS a ON a.model_id = p.model_id AND a.version = p.version").Order("p.model_id ASC").Find(&prices).Error
	return prices, err
}

func GetActivePointPrice(modelID string) (*PointPriceVersion, error) {
	var price PointPriceVersion
	err := DB.Table("point_price_versions AS p").Select("p.*").Joins("JOIN point_active_prices AS a ON a.model_id = p.model_id AND a.version = p.version").Where("p.model_id = ?", modelID).First(&price).Error
	return &price, err
}

type PointWalletView struct {
	AvailableMicro          int64 `json:"available_micro"`
	HeldMicro               int64 `json:"held_micro"`
	SpentMicro              int64 `json:"spent_micro"`
	PurchasedAvailableMicro int64 `json:"purchased_available_micro"`
	GiftedAvailableMicro    int64 `json:"gifted_available_micro"`
	MigratedAvailableMicro  int64 `json:"migrated_available_micro"`
	PurchasedTotalMicro     int64 `json:"purchased_total_micro"`
	GiftedTotalMicro        int64 `json:"gifted_total_micro"`
	MigratedTotalMicro      int64 `json:"migrated_total_micro"`
}

func GetPointWallet(userID int) (*PointWalletView, error) {
	view := &PointWalletView{}
	err := pointsTransaction(func(tx *gorm.DB) error {
		var account PointAccount
		if err := tx.First(&account, "user_id = ?", userID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if err := expireAvailablePointLotsTx(tx, userID, time.Now().UTC()); err != nil {
			return err
		}
		if err := tx.First(&account, "user_id = ?", userID).Error; err != nil {
			return err
		}
		view.AvailableMicro, view.HeldMicro, view.SpentMicro = account.AvailableMicro, account.HeldMicro, account.SpentMicro
		var lots []PointLot
		if err := tx.Select("kind", "initial_micro", "refunded_micro", "available_micro").Where("user_id = ?", userID).Find(&lots).Error; err != nil {
			return err
		}
		for _, lot := range lots {
			credited := lot.InitialMicro - lot.RefundedMicro
			if credited < 0 {
				return errors.New("point lot refund exceeds original credit")
			}
			var err error
			switch lot.Kind {
			case "purchase":
				view.PurchasedTotalMicro, err = checkedAdd(view.PurchasedTotalMicro, credited)
				if err == nil {
					view.PurchasedAvailableMicro, err = checkedAdd(view.PurchasedAvailableMicro, lot.AvailableMicro)
				}
			case "bonus", "grant":
				view.GiftedTotalMicro, err = checkedAdd(view.GiftedTotalMicro, credited)
				if err == nil {
					view.GiftedAvailableMicro, err = checkedAdd(view.GiftedAvailableMicro, lot.AvailableMicro)
				}
			case "migration":
				view.MigratedTotalMicro, err = checkedAdd(view.MigratedTotalMicro, credited)
				if err == nil {
					view.MigratedAvailableMicro, err = checkedAdd(view.MigratedAvailableMicro, lot.AvailableMicro)
				}
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

type PointUsageView struct {
	LogicalRequestKey string             `json:"request_id"`
	UserID            int                `json:"user_id"`
	TokenID           int                `json:"token_id"`
	ModelID           string             `json:"model_id"`
	PriceVersion      string             `json:"price_version"`
	State             string             `json:"state"`
	BudgetMicro       int64              `json:"budget_micro"`
	UsageMicro        int64              `json:"usage_micro"`
	CreatedAt         time.Time          `json:"created_at"`
	Attempts          []PointHoldAttempt `json:"attempts"`
}

// PointTokenView contains only session-safe token metadata. The API key itself
// must never be returned by the points console after its one-time creation.
type PointTokenView struct {
	ID          int     `json:"id"`
	Name        string  `json:"name"`
	Status      int     `json:"status"`
	ExpiredTime int64   `json:"expired_time"`
	CreatedTime int64   `json:"created_time"`
	Models      *string `json:"models"`
	HasBudget   bool    `json:"has_budget"`
	Unlimited   bool    `json:"unlimited"`
	LimitMicro  int64   `json:"limit_micro"`
	HeldMicro   int64   `json:"held_micro"`
	SpentMicro  int64   `json:"spent_micro"`
}

func ListPointTokens(userID int) ([]PointTokenView, error) {
	if userID <= 0 {
		return nil, errors.New("valid user is required")
	}
	var tokens []Token
	if err := DB.Select("id", "user_id", "name", "status", "expired_time", "created_time", "models").Where("user_id = ?", userID).Order("id DESC").Limit(100).Find(&tokens).Error; err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return []PointTokenView{}, nil
	}
	ids := make([]int, 0, len(tokens))
	for _, token := range tokens {
		ids = append(ids, token.Id)
	}
	var budgets []PointTokenBudget
	if err := DB.Where("user_id = ? AND token_id IN ?", userID, ids).Find(&budgets).Error; err != nil {
		return nil, err
	}
	byID := make(map[int]PointTokenBudget, len(budgets))
	for _, budget := range budgets {
		byID[budget.TokenID] = budget
	}
	result := make([]PointTokenView, 0, len(tokens))
	for _, token := range tokens {
		budget, ok := byID[token.Id]
		view := PointTokenView{ID: token.Id, Name: token.Name, Status: token.Status, ExpiredTime: token.ExpiredTime, CreatedTime: token.CreatedTime, Models: token.Models, HasBudget: ok}
		if ok {
			view.Unlimited, view.LimitMicro, view.HeldMicro, view.SpentMicro = budget.Unlimited, budget.LimitMicro, budget.HeldMicro, budget.SpentMicro
		}
		result = append(result, view)
	}
	return result, nil
}

type PointAttemptEvidence struct {
	AttemptKey         string    `json:"attempt_key"`
	LocalRequestID     string    `json:"local_request_id"`
	State              string    `json:"state"`
	UsageMicro         int64     `json:"usage_micro"`
	UsageSource        string    `json:"usage_source"`
	UsageAuthoritative bool      `json:"usage_authoritative"`
	PromptTokens       int64     `json:"prompt_tokens"`
	CachedPromptTokens int64     `json:"cached_prompt_tokens"`
	CompletionTokens   int64     `json:"completion_tokens"`
	ReasoningTokens    int64     `json:"reasoning_tokens"`
	ProviderRequestID  string    `json:"provider_request_id"`
	ProviderResponseID string    `json:"provider_response_id"`
	CreatedAt          time.Time `json:"created_at"`
}

type PointRecoveryHoldView struct {
	ID                uint                   `json:"id"`
	LogicalRequestKey string                 `json:"logical_request_key"`
	UserID            int                    `json:"user_id"`
	TokenID           int                    `json:"token_id"`
	BudgetMicro       int64                  `json:"budget_micro"`
	PriceVersion      string                 `json:"price_version"`
	State             string                 `json:"state"`
	UsageMicro        int64                  `json:"usage_micro"`
	CreatedAt         time.Time              `json:"created_at"`
	Attempts          []PointAttemptEvidence `json:"attempts"`
}

func ListPointRecoveryHoldsWithAttempts(limit int) ([]PointRecoveryHoldView, error) {
	holds, err := ListPointRecoveryHolds(limit)
	if err != nil {
		return nil, err
	}
	result := make([]PointRecoveryHoldView, 0, len(holds))
	for _, hold := range holds {
		var attempts []PointHoldAttempt
		if err := DB.Where("hold_id = ?", hold.ID).Order("id ASC").Find(&attempts).Error; err != nil {
			return nil, err
		}
		view := PointRecoveryHoldView{ID: hold.ID, LogicalRequestKey: hold.LogicalRequestKey, UserID: hold.UserID, TokenID: hold.TokenID, BudgetMicro: hold.BudgetMicro, PriceVersion: hold.PriceVersion, State: hold.State, UsageMicro: hold.UsageMicro, CreatedAt: hold.CreatedAt, Attempts: make([]PointAttemptEvidence, 0, len(attempts))}
		for _, attempt := range attempts {
			view.Attempts = append(view.Attempts, PointAttemptEvidence{AttemptKey: attempt.AttemptKey, LocalRequestID: attempt.LocalRequestID, State: attempt.State, UsageMicro: attempt.UsageMicro, UsageSource: attempt.UsageSource, UsageAuthoritative: attempt.UsageAuthoritative, PromptTokens: attempt.PromptTokens, CachedPromptTokens: attempt.CachedPromptTokens, CompletionTokens: attempt.CompletionTokens, ReasoningTokens: attempt.ReasoningTokens, ProviderRequestID: attempt.ProviderRequestID, ProviderResponseID: attempt.ProviderResponseID, CreatedAt: attempt.CreatedAt})
		}
		result = append(result, view)
	}
	return result, nil
}

func ListPointUsage(userID int, limit int) ([]PointUsageView, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	var holds []PointHold
	if err := DB.Where("user_id = ? AND state IN ?", userID, []string{"settled", "pending", "needs_review", "released"}).Order("id DESC").Limit(limit).Find(&holds).Error; err != nil {
		return nil, err
	}
	result := make([]PointUsageView, 0, len(holds))
	for _, hold := range holds {
		var attempts []PointHoldAttempt
		if err := DB.Where("hold_id = ?", hold.ID).Order("id ASC").Find(&attempts).Error; err != nil {
			return nil, err
		}
		modelID := ""
		var snapshot PointPriceVersion
		if json.Unmarshal([]byte(hold.PriceSnapshot), &snapshot) == nil {
			modelID = snapshot.ModelID
		}
		result = append(result, PointUsageView{LogicalRequestKey: hold.LogicalRequestKey, UserID: hold.UserID, TokenID: hold.TokenID, ModelID: modelID, PriceVersion: hold.PriceVersion, State: hold.State, BudgetMicro: hold.BudgetMicro, UsageMicro: hold.UsageMicro, CreatedAt: hold.CreatedAt, Attempts: attempts})
	}
	return result, nil
}

func SetPointTokenBudget(userID, tokenID int, limitMicro int64) error {
	if userID <= 0 || tokenID <= 0 || limitMicro <= 0 {
		return errors.New("a positive points budget is required")
	}
	return pointsTransaction(func(tx *gorm.DB) error {
		var token Token
		if err := tx.Select("id", "user_id").First(&token, "id = ? AND user_id = ?", tokenID, userID).Error; err != nil {
			return err
		}
		var current PointTokenBudget
		err := tx.Where("token_id = ?", tokenID).First(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(&PointTokenBudget{TokenID: tokenID, UserID: userID, LimitMicro: limitMicro, Unlimited: false}).Error
		}
		if err != nil {
			return err
		}
		if current.UserID != userID {
			return gorm.ErrRecordNotFound
		}
		if current.HeldMicro > limitMicro || current.SpentMicro > limitMicro-current.HeldMicro {
			return errors.New("budget is below already held or spent points")
		}
		updated := tx.Model(&PointTokenBudget{}).Where("token_id = ? AND user_id = ? AND held_micro = ? AND spent_micro = ?", tokenID, userID, current.HeldMicro, current.SpentMicro).Updates(map[string]interface{}{"limit_micro": limitMicro, "unlimited": false})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrPointsConflict
		}
		return nil
	})
}

func SetPointTokenSettings(userID, tokenID int, expiredAt int64, status int) error {
	if userID <= 0 || tokenID <= 0 || (status != TokenStatusEnabled && status != TokenStatusDisabled) {
		return ErrPointsInvalidTokenSettings
	}
	if status == TokenStatusEnabled && expiredAt != -1 && expiredAt <= time.Now().Unix() {
		return ErrPointsInvalidTokenSettings
	}
	result := DB.Model(&Token{}).Where("id = ? AND user_id = ?", tokenID, userID).Updates(map[string]interface{}{"expired_time": expiredAt, "status": status})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		var current Token
		if err := DB.Select("status", "expired_time").First(&current, "id = ? AND user_id = ?", tokenID, userID).Error; err != nil {
			return err
		}
		if current.Status == status && current.ExpiredTime == expiredAt {
			return nil
		}
		return ErrPointsConflict
	}
	return nil
}

func AdjustPoints(actorID, userID int, amountMicro int64, businessKey, reason string) error {
	if actorID <= 0 || userID <= 0 || amountMicro <= 0 || businessKey == "" || reason == "" {
		return errors.New("positive amount, business key, and reason are required")
	}
	err := pointsTransaction(func(tx *gorm.DB) error {
		var prior PointLot
		lookup := tx.Where("business_key = ?", "adjustment:"+businessKey).First(&prior)
		if lookup.Error == nil {
			var audit PointAdminAudit
			var ledger PointLedger
			if err := tx.Where("business_key = ?", "adjustment:"+businessKey).First(&audit).Error; err != nil {
				return ErrPointsConflict
			}
			if err := tx.Where("business_key = ?", "admin_adjustment:"+businessKey).First(&ledger).Error; err != nil {
				return ErrPointsConflict
			}
			if prior.UserID == userID && prior.Kind == "grant" && prior.SourceRef == businessKey && prior.InitialMicro == amountMicro && audit.ActorUserID == actorID && audit.TargetUserID == userID && ledger.Reason == reason {
				return nil
			}
			return ErrPointsConflict
		}
		if !errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
			return lookup.Error
		}
		var user User
		if err := tx.Select("id").First(&user, "id = ?", userID).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&PointAccount{UserID: userID}).Error; err != nil {
			return err
		}
		lot := PointLot{UserID: userID, BusinessKey: "adjustment:" + businessKey, Kind: "grant", SourceRef: businessKey, InitialMicro: amountMicro, AvailableMicro: amountMicro}
		if err := tx.Create(&lot).Error; err != nil {
			return err
		}
		upd := tx.Model(&PointAccount{}).Where("user_id = ? AND available_micro <= ?", userID, int64(^uint64(0)>>1)-amountMicro).Update("available_micro", gorm.Expr("available_micro + ?", amountMicro))
		if upd.Error != nil {
			return upd.Error
		}
		if upd.RowsAffected != 1 {
			return errors.New("point account credit overflow or missing account")
		}
		var account PointAccount
		if err := tx.First(&account, "user_id = ?", userID).Error; err != nil {
			return err
		}
		if err := tx.Create(&PointLedger{UserID: userID, BusinessKey: "admin_adjustment:" + businessKey, Kind: "adjustment", LotID: &lot.ID, AvailableDelta: amountMicro, AvailableAfter: account.AvailableMicro, HeldAfter: account.HeldMicro, SpentAfter: account.SpentMicro, Reason: reason}).Error; err != nil {
			return err
		}
		details, _ := json.Marshal(map[string]interface{}{"amount_micro": amountMicro, "reason": reason, "lot_id": lot.ID})
		return tx.Create(&PointAdminAudit{ActorUserID: actorID, Action: "points_grant", BusinessKey: "adjustment:" + businessKey, TargetUserID: userID, Details: string(details)}).Error
	})
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		var lot PointLot
		var audit PointAdminAudit
		var ledger PointLedger
		if DB.Where("business_key = ?", "adjustment:"+businessKey).First(&lot).Error == nil && DB.Where("business_key = ?", "adjustment:"+businessKey).First(&audit).Error == nil && DB.Where("business_key = ?", "admin_adjustment:"+businessKey).First(&ledger).Error == nil && lot.UserID == userID && lot.Kind == "grant" && lot.SourceRef == businessKey && lot.InitialMicro == amountMicro && audit.ActorUserID == actorID && audit.TargetUserID == userID && ledger.Reason == reason {
			return nil
		}
		return ErrPointsConflict
	}
	return err
}

func ListPointRecoveryHolds(limit int) ([]PointHold, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	var holds []PointHold
	err := DB.Where("state IN ?", []string{"held", "pending", "needs_review"}).Order("id ASC").Limit(limit).Find(&holds).Error
	return holds, err
}

// RecoverOrphanedPointHolds marks currently held requests pending and audits
// the offline recovery batch. It intentionally does not release or settle funds.
func RecoverOrphanedPointHolds(batchKey, reason string) (int64, error) {
	batchKey = strings.TrimSpace(batchKey)
	reason = strings.TrimSpace(reason)
	if batchKey == "" || len(batchKey) > 80 || reason == "" || len(reason) > 512 {
		return 0, errors.New("a recovery batch key (1-80 chars) and reason (1-512 chars) are required")
	}
	if !common.UsingSQLite {
		return 0, errors.New("offline held-request recovery currently supports the single-instance SQLite deployment only")
	}
	var recovered int64
	err := pointsTransaction(func(tx *gorm.DB) error {
		recovered = 0 // A rolled-back SQLite attempt must not inflate the result.
		batchDetails, _ := json.Marshal(map[string]string{"reason": reason})
		batchAuditKey := "offline-recovery-batch:" + batchKey
		var batchAudit PointAdminAudit
		err := tx.Where("business_key = ?", batchAuditKey).First(&batchAudit).Error
		if err == nil {
			if batchAudit.Action != "offline_hold_recovery" || batchAudit.ActorUserID != 0 || batchAudit.Details != string(batchDetails) {
				return ErrPointsConflict
			}
			return nil // A completed batch never takes ownership of later requests.
		} else if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Create(&PointAdminAudit{ActorUserID: 0, Action: "offline_hold_recovery", BusinessKey: batchAuditKey, Details: string(batchDetails)}).Error; err != nil {
				return err
			}
		} else {
			return err
		}

		var holds []PointHold
		if err := tx.Where("state = ?", "held").Order("id ASC").Find(&holds).Error; err != nil {
			return err
		}
		for _, hold := range holds {
			details, _ := json.Marshal(map[string]string{"batch_key": batchKey, "reason": reason, "logical_request_key": hold.LogicalRequestKey})
			auditKey := fmt.Sprintf("offline-recovery-hold:%s:%d", batchKey, hold.ID)
			result := tx.Model(&PointHold{}).Where("id = ? AND state = ?", hold.ID, "held").Update("state", "pending")
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				continue
			}
			if err := tx.Create(&PointAdminAudit{ActorUserID: 0, Action: "offline_hold_recovery", BusinessKey: auditKey, TargetUserID: hold.UserID, Details: string(details)}).Error; err != nil {
				return err
			}
			recovered++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return recovered, nil
}

type PointsSchemaMigration struct {
	Version   int `gorm:"primaryKey"`
	AppliedAt time.Time
}

const pointsSchemaVersion = 6

// MigratePointsSchema is deliberately separate from the normal startup migration.
func MigratePointsSchema() error {
	if DB == nil {
		return errors.New("database is not initialized")
	}
	if err := DB.AutoMigrate(&PointsSchemaMigration{}); err != nil {
		return err
	}
	var applied PointsSchemaMigration
	err := DB.First(&applied, "version = ?", pointsSchemaVersion).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	// Each additive AutoMigrate is safe to rerun if a backend commits DDL implicitly.
	models := []interface{}{&PointAccount{}, &PointLot{}, &PointLedger{}, &PointPriceVersion{}, &PointActivePrice{}, &PointHold{}, &PointHoldAllocation{}, &PointHoldAttempt{}, &PointHoldDecision{}, &PointAdminAudit{}, &PointTokenBudget{}, &PointPurchaseOrder{}, &PointPackage{}, &PointActivePackage{}, &PaymentEvent{}, &PaymentTransaction{}}
	for _, item := range models {
		if err := DB.AutoMigrate(item); err != nil {
			return err
		}
	}
	return DB.Create(&PointsSchemaMigration{Version: pointsSchemaVersion, AppliedAt: time.Now().UTC()}).Error
}

func RequirePointsSchema() error {
	var applied PointsSchemaMigration
	if err := DB.First(&applied, "version = ?", pointsSchemaVersion).Error; err != nil {
		return fmt.Errorf("points billing schema version %d is required: %w", pointsSchemaVersion, err)
	}
	return nil
}

func pointsTransaction(fn func(*gorm.DB) error) error {
	return pointsTransactionWithContext(context.Background(), DB, fn)
}

func pointsTransactionOn(db *gorm.DB, fn func(*gorm.DB) error) error {
	return pointsTransactionWithContext(context.Background(), db, fn)
}

func isSQLiteBusy(err error) bool {
	var sqliteErr sqlite3.Error
	return errors.As(err, &sqliteErr) && (sqliteErr.Code == sqlite3.ErrBusy || sqliteErr.Code == sqlite3.ErrLocked)
}

func pointsTransactionWithContext(ctx context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err = db.WithContext(ctx).Transaction(fn)
		if err == nil || !common.UsingSQLite || !isSQLiteBusy(err) {
			return err
		}
		// The DB-only closure resolves its unique business key before every retry.
		wait := time.NewTimer(time.Duration(25*(attempt+1)) * time.Millisecond)
		select {
		case <-ctx.Done():
			wait.Stop()
			return ctx.Err()
		case <-wait.C:
		}
	}
	return err
}

func expireAvailablePointLotsTx(tx *gorm.DB, userID int, now time.Time) error {
	var lots []PointLot
	if err := tx.Where("user_id = ? AND available_micro > 0 AND expires_at IS NOT NULL AND expires_at <= ?", userID, now.Unix()).Order("id ASC").Find(&lots).Error; err != nil {
		return err
	}
	var total int64
	for _, lot := range lots {
		next, err := checkedAdd(total, lot.AvailableMicro)
		if err != nil {
			return err
		}
		total = next
	}
	if total == 0 {
		return nil
	}
	update := tx.Model(&PointLot{}).Where("user_id = ? AND available_micro > 0 AND expires_at IS NOT NULL AND expires_at <= ?", userID, now.Unix()).Update("available_micro", 0)
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected != int64(len(lots)) {
		return errors.New("point lot expiry changed concurrently")
	}
	accountUpdate := tx.Model(&PointAccount{}).Where("user_id = ? AND available_micro >= ?", userID, total).Update("available_micro", gorm.Expr("available_micro - ?", total))
	if accountUpdate.Error != nil {
		return accountUpdate.Error
	}
	if accountUpdate.RowsAffected != 1 {
		return errors.New("point account does not cover expired lots")
	}
	var after PointAccount
	if err := tx.First(&after, "user_id = ?", userID).Error; err != nil {
		return err
	}
	runningAvailable := after.AvailableMicro + total
	for _, lot := range lots {
		runningAvailable -= lot.AvailableMicro
		lotID := lot.ID
		if err := tx.Create(&PointLedger{UserID: userID, BusinessKey: fmt.Sprintf("expire:lot:%d", lot.ID), Kind: "expire", LotID: &lotID, AvailableDelta: -lot.AvailableMicro, AvailableAfter: runningAvailable, HeldAfter: after.HeldMicro, SpentAfter: after.SpentMicro, Reason: "expired bonus lot"}).Error; err != nil {
			return err
		}
	}
	return nil
}

// ParseMicroPoints parses a decimal point amount without floating point rounding.
func ParseMicroPoints(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "-") {
		return 0, errors.New("points must be a non-negative decimal")
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || len(parts) == 2 && len(parts[1]) > 6 {
		return 0, errors.New("points support at most six decimal places")
	}
	for _, part := range parts {
		for _, r := range part {
			if r < '0' || r > '9' {
				return 0, errors.New("points must contain digits only")
			}
		}
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, err
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	for len(fraction) < 6 {
		fraction += "0"
	}
	var fractional int64
	if fraction != "" {
		fractional, err = strconv.ParseInt(fraction, 10, 64)
		if err != nil {
			return 0, err
		}
	}
	if whole > (int64(^uint64(0)>>1)-fractional)/PointMicroPerPoint {
		return 0, errors.New("points amount overflow")
	}
	return whole*PointMicroPerPoint + fractional, nil
}

// CalculatePointUsage rounds once, half-up, after summing all token components.
func CalculatePointUsage(prompt, cachedPrompt, output int64, price PointPriceVersion) (int64, error) {
	if prompt < 0 || cachedPrompt < 0 || output < 0 || cachedPrompt > prompt || price.InputMicroPer1K < 0 || price.CachedInputMicroPer1K < 0 || price.OutputMicroPer1K < 0 || price.ExtraMicro < 0 {
		return 0, errors.New("invalid usage or price snapshot")
	}
	uncached := prompt - cachedPrompt
	total := new(big.Int).Mul(big.NewInt(uncached), big.NewInt(price.InputMicroPer1K))
	total.Add(total, new(big.Int).Mul(big.NewInt(cachedPrompt), big.NewInt(price.CachedInputMicroPer1K)))
	total.Add(total, new(big.Int).Mul(big.NewInt(output), big.NewInt(price.OutputMicroPer1K)))
	total.Add(total, big.NewInt(500))
	total.Div(total, big.NewInt(1000))
	total.Add(total, big.NewInt(price.ExtraMicro))
	if !total.IsInt64() {
		return 0, errors.New("usage calculation overflow")
	}
	return total.Int64(), nil
}

type PointReserveRequest struct {
	UserID             int
	TokenID            int
	LogicalRequestKey  string
	AttemptKey         string
	LocalRequestID     string
	AttemptFingerprint string
	BudgetMicro        int64
	PriceVersion       string
	PriceSnapshot      string
	RejectExisting     bool
}

// ReservePoints holds a single logical-request budget; retry attempts share that hold.
func ReservePoints(req PointReserveRequest) (*PointHold, error) {
	return ReservePointsContext(context.Background(), req)
}

func ReservePointsContext(ctx context.Context, req PointReserveRequest) (*PointHold, error) {
	return reservePointsContextOn(ctx, DB, req)
}

func reservePointsOn(db *gorm.DB, req PointReserveRequest) (*PointHold, error) {
	return reservePointsContextOn(context.Background(), db, req)
}

func reservePointsContextOn(ctx context.Context, db *gorm.DB, req PointReserveRequest) (*PointHold, error) {
	if !config.PointsBillingEnabled {
		return nil, ErrPointsDisabled
	}
	if db == nil || req.UserID <= 0 || req.TokenID <= 0 || req.LogicalRequestKey == "" || req.AttemptKey == "" || req.AttemptFingerprint == "" || req.BudgetMicro <= 0 || req.PriceVersion == "" {
		return nil, errors.New("invalid point reserve request")
	}
	var price PointPriceVersion
	if err := db.Where("version = ?", req.PriceVersion).First(&price).Error; err != nil {
		return nil, err
	}
	priceSnapshot, err := json.Marshal(price)
	if err != nil {
		return nil, err
	}
	if req.PriceSnapshot != "" && req.PriceSnapshot != string(priceSnapshot) {
		return nil, ErrPointsConflict
	}
	req.PriceSnapshot = string(priceSnapshot)
	var result PointHold
	err = pointsTransactionWithContext(ctx, db, func(tx *gorm.DB) error {
		var allocations []PointHoldAllocation
		var existing PointHold
		err := tx.Where("logical_request_key = ?", req.LogicalRequestKey).First(&existing).Error
		if err == nil {
			if req.RejectExisting {
				return ErrPointsConflict
			}
			if existing.UserID != req.UserID || existing.TokenID != req.TokenID || existing.BudgetMicro != req.BudgetMicro || existing.PriceVersion != req.PriceVersion || existing.PriceSnapshot != req.PriceSnapshot {
				return ErrPointsConflict
			}
			if existing.State == "settled" || existing.State == "released" || existing.State == "needs_review" {
				return ErrPointsConflict
			}
			var attempt PointHoldAttempt
			err = tx.Where("hold_id = ? AND attempt_key = ?", existing.ID, req.AttemptKey).First(&attempt).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				attempt = PointHoldAttempt{HoldID: existing.ID, AttemptKey: req.AttemptKey, LocalRequestID: req.LocalRequestID, RequestFingerprint: req.AttemptFingerprint, State: "started"}
				if err = tx.Create(&attempt).Error; err != nil {
					return err
				}
			} else if err != nil {
				return err
			} else if attempt.RequestFingerprint != req.AttemptFingerprint || attempt.LocalRequestID != req.LocalRequestID {
				return ErrPointsConflict
			}
			result = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var active PointActivePrice
		if err := tx.Where("model_id = ? AND version = ?", price.ModelID, req.PriceVersion).First(&active).Error; err != nil {
			return errors.New("price version is not active")
		}
		var token Token
		if err := tx.Select("id", "user_id", "status").First(&token, "id = ?", req.TokenID).Error; err != nil {
			return err
		}
		if token.UserId != req.UserID || token.Status != TokenStatusEnabled {
			return errors.New("point token is not active for user")
		}
		var budget PointTokenBudget
		if err := tx.First(&budget, "token_id = ?", req.TokenID).Error; err != nil {
			return errors.New("explicit point token budget is required")
		}
		if budget.UserID != req.UserID {
			return errors.New("point token budget owner mismatch")
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&PointAccount{UserID: req.UserID}).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		if err := expireAvailablePointLotsTx(tx, req.UserID, now); err != nil {
			return err
		}
		updated := tx.Model(&PointAccount{}).Where("user_id = ? AND available_micro >= ?", req.UserID, req.BudgetMicro).Updates(map[string]interface{}{"available_micro": gorm.Expr("available_micro - ?", req.BudgetMicro), "held_micro": gorm.Expr("held_micro + ?", req.BudgetMicro)})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrPointsInsufficient
		}
		if !budget.Unlimited {
			capUpdate := tx.Model(&PointTokenBudget{}).Where("token_id = ? AND user_id = ? AND limit_micro - spent_micro - held_micro >= ?", req.TokenID, req.UserID, req.BudgetMicro).Updates(map[string]interface{}{"held_micro": gorm.Expr("held_micro + ?", req.BudgetMicro)})
			if capUpdate.Error != nil {
				return capUpdate.Error
			}
			if capUpdate.RowsAffected != 1 {
				return errors.New("point token budget exceeded")
			}
		} else {
			capUpdate := tx.Model(&PointTokenBudget{}).Where("token_id = ? AND user_id = ?", req.TokenID, req.UserID).Update("held_micro", gorm.Expr("held_micro + ?", req.BudgetMicro))
			if capUpdate.Error != nil {
				return capUpdate.Error
			}
			if capUpdate.RowsAffected != 1 {
				return errors.New("point token budget missing")
			}
		}
		var lots []PointLot
		now = time.Now().UTC()
		if err := tx.Where("user_id = ? AND available_micro > 0 AND (expires_at IS NULL OR expires_at > ?)", req.UserID, now.Unix()).Order("CASE WHEN expires_at IS NULL THEN 1 ELSE 0 END ASC").Order("expires_at ASC").Order("created_at ASC").Order("id ASC").Find(&lots).Error; err != nil {
			return err
		}
		remaining := req.BudgetMicro
		for _, lot := range lots {
			if remaining == 0 {
				break
			}
			take := lot.AvailableMicro
			if take > remaining {
				take = remaining
			}
			upd := tx.Model(&PointLot{}).Where("id = ? AND available_micro >= ?", lot.ID, take).Updates(map[string]interface{}{"available_micro": gorm.Expr("available_micro - ?", take), "held_micro": gorm.Expr("held_micro + ?", take)})
			if upd.Error != nil {
				return upd.Error
			}
			if upd.RowsAffected != 1 {
				return ErrPointsInsufficient
			}
			remaining -= take
			allocation := PointHoldAllocation{LotID: lot.ID, ReservedMicro: take}
			// Hold ID is filled after the hold insert below; stage allocations in memory.
			allocations = append(allocations, allocation)
		}
		if remaining != 0 {
			return ErrPointsInsufficient
		}
		result = PointHold{UserID: req.UserID, TokenID: req.TokenID, LogicalRequestKey: req.LogicalRequestKey, BudgetMicro: req.BudgetMicro, PriceVersion: req.PriceVersion, PriceSnapshot: req.PriceSnapshot, State: "held"}
		if err := tx.Create(&result).Error; err != nil {
			return err
		}
		for i := range allocations {
			allocations[i].HoldID = result.ID
			if err := tx.Create(&allocations[i]).Error; err != nil {
				return err
			}
		}
		attempt := PointHoldAttempt{HoldID: result.ID, AttemptKey: req.AttemptKey, LocalRequestID: req.LocalRequestID, RequestFingerprint: req.AttemptFingerprint, State: "started"}
		if err := tx.Create(&attempt).Error; err != nil {
			return err
		}
		var account PointAccount
		if err := tx.First(&account, "user_id = ?", req.UserID).Error; err != nil {
			return err
		}
		return tx.Create(&PointLedger{UserID: req.UserID, BusinessKey: "hold:" + req.LogicalRequestKey, Kind: "hold", HeldDelta: req.BudgetMicro, AvailableDelta: -req.BudgetMicro, AvailableAfter: account.AvailableMicro, HeldAfter: account.HeldMicro, SpentAfter: account.SpentMicro, Reason: "request budget reserved"}).Error
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// ReleaseUnsentPointHold releases only a newly reserved hold whose only attempt
// is still started and has no provider evidence. Callers must use it only before
// starting network I/O.
func ReleaseUnsentPointHold(logicalKey, reason string) error {
	if logicalKey == "" || reason == "" {
		return errors.New("logical request key and release reason are required")
	}
	return pointsTransaction(func(tx *gorm.DB) error {
		var hold PointHold
		if err := tx.Where("logical_request_key = ?", logicalKey).First(&hold).Error; err != nil {
			return err
		}
		if hold.State != "held" {
			return ErrPointsConflict
		}
		var attempts []PointHoldAttempt
		if err := tx.Where("hold_id = ?", hold.ID).Find(&attempts).Error; err != nil {
			return err
		}
		if len(attempts) != 1 || attempts[0].State != "started" || attempts[0].ProviderRequestID != "" || attempts[0].ProviderResponseID != "" {
			return ErrPointsConflict
		}
		key := "local-unsent:" + logicalKey
		var prior PointHoldDecision
		err := tx.Where("decision_key = ?", key).First(&prior).Error
		if err == nil {
			if prior.HoldID == hold.ID && prior.Action == "release" && prior.Reason == reason {
				return nil
			}
			return ErrPointsConflict
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Create(&PointHoldDecision{HoldID: hold.ID, DecisionKey: key, Action: "release", Reason: reason}).Error; err != nil {
			return err
		}
		hold.State = "held"
		if err := releasePointHoldTx(tx, &hold); err != nil {
			return err
		}
		return nil
	})
}

type PointAttemptUsage struct {
	LogicalRequestKey  string
	AttemptKey         string
	State              string
	UsageMicro         int64
	UsageSource        string
	Authoritative      bool
	Fingerprint        string
	Billable           bool
	PromptTokens       int64
	CachedPromptTokens int64
	CompletionTokens   int64
	ReasoningTokens    int64
	ProviderRequestID  string
	ProviderResponseID string
}

// RecordPointAttemptUsage stores evidence. Untrusted or absent usage leaves funds held.
func RecordPointAttemptUsage(report PointAttemptUsage) (*PointHold, error) {
	var hold PointHold
	err := pointsTransaction(func(tx *gorm.DB) error {
		if err := tx.Where("logical_request_key = ?", report.LogicalRequestKey).First(&hold).Error; err != nil {
			return err
		}
		var attempt PointHoldAttempt
		if err := tx.Where("hold_id = ? AND attempt_key = ?", hold.ID, report.AttemptKey).First(&attempt).Error; err != nil {
			return err
		}
		if report.UsageMicro < 0 {
			return errors.New("usage cannot be negative")
		}
		if report.Authoritative && (report.UsageSource == "" || report.Fingerprint == "") {
			return errors.New("authoritative usage requires provenance and fingerprint")
		}
		state := report.State
		if state != "succeeded" && state != "failed" && state != "unknown" {
			return errors.New("invalid attempt state")
		}
		if !report.Authoritative || state == "unknown" {
			state = "unknown"
		}
		if hold.State == "settled" || hold.State == "released" {
			if attempt.State == state && attempt.UsageMicro == report.UsageMicro && attempt.UsageSource == report.UsageSource && attempt.UsageAuthoritative == report.Authoritative && attempt.UsageFingerprint == report.Fingerprint && attempt.Billable == report.Billable {
				return nil
			}
			return ErrPointsConflict
		}
		if attempt.State != "started" && !(attempt.State == "unknown" && report.Authoritative) {
			if attempt.State == state && attempt.UsageMicro == report.UsageMicro && attempt.UsageSource == report.UsageSource && attempt.UsageAuthoritative == report.Authoritative && attempt.UsageFingerprint == report.Fingerprint && attempt.Billable == report.Billable && attempt.PromptTokens == report.PromptTokens && attempt.CachedPromptTokens == report.CachedPromptTokens && attempt.CompletionTokens == report.CompletionTokens && attempt.ReasoningTokens == report.ReasoningTokens && attempt.ProviderRequestID == report.ProviderRequestID && attempt.ProviderResponseID == report.ProviderResponseID {
				return nil
			}
			return ErrPointsConflict
		}
		if !report.Authoritative || state == "unknown" {
			state = "unknown"
			hold.State = "pending"
		}
		if err := tx.Model(&attempt).Updates(map[string]interface{}{"state": state, "usage_micro": report.UsageMicro, "usage_source": report.UsageSource, "usage_authoritative": report.Authoritative, "usage_fingerprint": report.Fingerprint, "billable": report.Billable, "prompt_tokens": report.PromptTokens, "cached_prompt_tokens": report.CachedPromptTokens, "completion_tokens": report.CompletionTokens, "reasoning_tokens": report.ReasoningTokens, "provider_request_id": report.ProviderRequestID, "provider_response_id": report.ProviderResponseID}).Error; err != nil {
			return err
		}
		var attempts []PointHoldAttempt
		if err := tx.Where("hold_id = ?", hold.ID).Find(&attempts).Error; err != nil {
			return err
		}
		total := int64(0)
		for _, item := range attempts {
			if item.ID == attempt.ID {
				item.State, item.UsageMicro, item.UsageAuthoritative, item.Billable = state, report.UsageMicro, report.Authoritative, report.Billable
			}
			if item.State == "started" || item.State == "unknown" || !item.UsageAuthoritative {
				hold.State = "pending"
				return tx.Model(&hold).Update("state", "pending").Error
			}
			if item.Billable {
				var sumErr error
				total, sumErr = checkedAdd(total, item.UsageMicro)
				if sumErr != nil {
					return sumErr
				}
			}
		}
		hold.UsageMicro = total
		if total > hold.BudgetMicro {
			hold.State = "needs_review"
			return tx.Model(&hold).Updates(map[string]interface{}{"state": hold.State, "usage_micro": total}).Error
		}
		hold.State = "held"
		return settlePointHoldTx(tx, &hold, total)
	})
	return &hold, err
}

func settlePointHoldTx(tx *gorm.DB, hold *PointHold, usage int64) error {
	if hold.State == "settled" {
		if hold.UsageMicro == usage {
			return nil
		}
		return ErrPointsConflict
	}
	if hold.State != "held" || usage < 0 || usage > hold.BudgetMicro {
		return ErrPointsPending
	}
	now := time.Now().UTC()
	if err := expireAvailablePointLotsTx(tx, hold.UserID, now); err != nil {
		return err
	}
	var allocations []PointHoldAllocation
	if err := tx.Where("hold_id = ?", hold.ID).Order("id ASC").Find(&allocations).Error; err != nil {
		return err
	}
	remaining := usage
	availableRelease := int64(0)
	for _, allocation := range allocations {
		consume := allocation.ReservedMicro
		if consume > remaining {
			consume = remaining
		}
		release := allocation.ReservedMicro - consume
		var lot PointLot
		if err := tx.First(&lot, allocation.LotID).Error; err != nil {
			return err
		}
		updates := map[string]interface{}{"held_micro": gorm.Expr("held_micro - ?", allocation.ReservedMicro), "consumed_micro": gorm.Expr("consumed_micro + ?", consume)}
		if lot.ExpiresAt == nil || *lot.ExpiresAt > now.Unix() {
			updates["available_micro"] = gorm.Expr("available_micro + ?", release)
			availableRelease += release
		}
		lotUpdate := tx.Model(&PointLot{}).Where("id = ? AND held_micro >= ?", lot.ID, allocation.ReservedMicro).Updates(updates)
		if lotUpdate.Error != nil {
			return lotUpdate.Error
		}
		if lotUpdate.RowsAffected != 1 {
			return errors.New("point lot state changed")
		}
		remaining -= consume
		if err := tx.Model(&allocation).Update("settled_micro", consume).Error; err != nil {
			return err
		}
	}
	if remaining != 0 {
		return errors.New("hold allocation does not cover usage")
	}
	update := tx.Model(&PointAccount{}).Where("user_id = ? AND held_micro >= ?", hold.UserID, hold.BudgetMicro).Updates(map[string]interface{}{"held_micro": gorm.Expr("held_micro - ?", hold.BudgetMicro), "available_micro": gorm.Expr("available_micro + ?", hold.BudgetMicro-usage), "spent_micro": gorm.Expr("spent_micro + ?", usage)})
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected != 1 {
		return errors.New("point account state changed")
	}
	if availableRelease != hold.BudgetMicro-usage {
		update = tx.Model(&PointAccount{}).Where("user_id = ?", hold.UserID).Update("available_micro", gorm.Expr("available_micro - ?", hold.BudgetMicro-usage-availableRelease))
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return errors.New("point account expiry adjustment failed")
		}
	}
	tokenUpdate := tx.Model(&PointTokenBudget{}).Where("token_id = ? AND held_micro >= ?", hold.TokenID, hold.BudgetMicro).Updates(map[string]interface{}{"held_micro": gorm.Expr("held_micro - ?", hold.BudgetMicro), "spent_micro": gorm.Expr("spent_micro + ?", usage)})
	if tokenUpdate.Error != nil {
		return tokenUpdate.Error
	}
	if tokenUpdate.RowsAffected != 1 {
		return errors.New("point token budget state changed")
	}
	var account PointAccount
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&account, "user_id = ?", hold.UserID).Error; err != nil {
		return err
	}
	holdUpdate := tx.Model(hold).Where("state IN ?", []string{"held", "pending", "needs_review"}).Updates(map[string]interface{}{"state": "settled", "usage_micro": usage})
	if holdUpdate.Error != nil {
		return holdUpdate.Error
	}
	if holdUpdate.RowsAffected != 1 {
		return ErrPointsConflict
	}
	return tx.Create(&PointLedger{UserID: hold.UserID, BusinessKey: fmt.Sprintf("settle:%s", hold.LogicalRequestKey), Kind: "settle", HoldID: &hold.ID, AvailableDelta: availableRelease, HeldDelta: -hold.BudgetMicro, SpentDelta: usage, AvailableAfter: account.AvailableMicro, HeldAfter: account.HeldMicro, SpentAfter: account.SpentMicro, Reason: "authoritative usage settlement"}).Error
}

// ResolvePointHold is an auditable, idempotent human/provider decision path; no TTL releases funds.
func ResolvePointHold(logicalKey, decisionKey, action string, usage int64, reason string) error {
	return ResolvePointHoldBy(0, logicalKey, decisionKey, action, usage, reason)
}

func ResolvePointHoldBy(actorID int, logicalKey, decisionKey, action string, usage int64, reason string) error {
	if decisionKey == "" || reason == "" {
		return errors.New("decision key and reason are required")
	}
	return pointsTransaction(func(tx *gorm.DB) error {
		var hold PointHold
		if err := tx.Where("logical_request_key = ?", logicalKey).First(&hold).Error; err != nil {
			return err
		}
		var prior PointHoldDecision
		err := tx.Where("decision_key = ?", decisionKey).First(&prior).Error
		if err == nil {
			if prior.HoldID == hold.ID && prior.Action == action && prior.UsageMicro == usage && prior.Reason == reason {
				return nil
			}
			return ErrPointsConflict
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if hold.State != "pending" && hold.State != "needs_review" {
			return ErrPointsConflict
		}
		decision := PointHoldDecision{HoldID: hold.ID, DecisionKey: decisionKey, Action: action, UsageMicro: usage, Reason: reason}
		if err := tx.Create(&decision).Error; err != nil {
			return err
		}
		if action == "settle" {
			if usage > hold.BudgetMicro {
				if err := extendPointHoldTx(tx, &hold, usage-hold.BudgetMicro, decisionKey); err != nil {
					return err
				}
			}
			hold.State = "held"
			if err := settlePointHoldTx(tx, &hold, usage); err != nil {
				return err
			}
		} else if action == "release" {
			if usage != 0 {
				return errors.New("release decision must have zero usage")
			}
			hold.State = "held"
			if err := releasePointHoldTx(tx, &hold); err != nil {
				return err
			}
		} else {
			return errors.New("decision must be settle or release")
		}
		if actorID > 0 {
			details, _ := json.Marshal(map[string]interface{}{"decision_key": decisionKey, "action": action, "usage_micro": usage, "reason": reason})
			return tx.Create(&PointAdminAudit{ActorUserID: actorID, Action: "hold_resolution", BusinessKey: "hold-decision:" + decisionKey, TargetUserID: hold.UserID, Details: string(details)}).Error
		}
		return nil
	})
}

func extendPointHoldTx(tx *gorm.DB, hold *PointHold, extra int64, decisionKey string) error {
	if extra <= 0 {
		return errors.New("hold extension must be positive")
	}
	now := time.Now().UTC()
	if err := expireAvailablePointLotsTx(tx, hold.UserID, now); err != nil {
		return err
	}
	accountUpdate := tx.Model(&PointAccount{}).Where("user_id = ? AND available_micro >= ?", hold.UserID, extra).Updates(map[string]interface{}{"available_micro": gorm.Expr("available_micro - ?", extra), "held_micro": gorm.Expr("held_micro + ?", extra)})
	if accountUpdate.Error != nil {
		return accountUpdate.Error
	}
	if accountUpdate.RowsAffected != 1 {
		return ErrPointsInsufficient
	}
	var budget PointTokenBudget
	if err := tx.First(&budget, "token_id = ? AND user_id = ?", hold.TokenID, hold.UserID).Error; err != nil {
		return err
	}
	if !budget.Unlimited {
		update := tx.Model(&PointTokenBudget{}).Where("token_id = ? AND user_id = ? AND limit_micro - spent_micro - held_micro >= ?", hold.TokenID, hold.UserID, extra).Update("held_micro", gorm.Expr("held_micro + ?", extra))
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return errors.New("point token budget cannot cover reviewed usage")
		}
	} else {
		update := tx.Model(&PointTokenBudget{}).Where("token_id = ? AND user_id = ?", hold.TokenID, hold.UserID).Update("held_micro", gorm.Expr("held_micro + ?", extra))
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return errors.New("point token budget missing")
		}
	}
	var lots []PointLot
	if err := tx.Where("user_id = ? AND available_micro > 0 AND (expires_at IS NULL OR expires_at > ?)", hold.UserID, now.Unix()).Order("CASE WHEN expires_at IS NULL THEN 1 ELSE 0 END ASC").Order("expires_at ASC").Order("created_at ASC").Order("id ASC").Find(&lots).Error; err != nil {
		return err
	}
	remaining := extra
	for _, lot := range lots {
		if remaining == 0 {
			break
		}
		take := lot.AvailableMicro
		if take > remaining {
			take = remaining
		}
		update := tx.Model(&PointLot{}).Where("id = ? AND available_micro >= ?", lot.ID, take).Updates(map[string]interface{}{"available_micro": gorm.Expr("available_micro - ?", take), "held_micro": gorm.Expr("held_micro + ?", take)})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ErrPointsInsufficient
		}
		remaining -= take
		var allocation PointHoldAllocation
		err := tx.Where("hold_id = ? AND lot_id = ?", hold.ID, lot.ID).First(&allocation).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			allocation = PointHoldAllocation{HoldID: hold.ID, LotID: lot.ID, ReservedMicro: take}
			if err := tx.Create(&allocation).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			updated := tx.Model(&allocation).Where("reserved_micro = ?", allocation.ReservedMicro).Update("reserved_micro", gorm.Expr("reserved_micro + ?", take))
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return errors.New("hold allocation changed")
			}
		}
	}
	if remaining != 0 {
		return ErrPointsInsufficient
	}
	newBudget, err := checkedAdd(hold.BudgetMicro, extra)
	if err != nil {
		return err
	}
	if err := tx.Model(hold).Update("budget_micro", newBudget).Error; err != nil {
		return err
	}
	hold.BudgetMicro = newBudget
	var account PointAccount
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&account, "user_id = ?", hold.UserID).Error; err != nil {
		return err
	}
	return tx.Create(&PointLedger{UserID: hold.UserID, BusinessKey: "review_topup:" + decisionKey, Kind: "review_topup", HoldID: &hold.ID, AvailableDelta: -extra, HeldDelta: extra, AvailableAfter: account.AvailableMicro, HeldAfter: account.HeldMicro, SpentAfter: account.SpentMicro, Reason: "adjudicated usage exceeded initial hold"}).Error
}

func releasePointHoldTx(tx *gorm.DB, hold *PointHold) error {
	now := time.Now().UTC()
	if err := expireAvailablePointLotsTx(tx, hold.UserID, now); err != nil {
		return err
	}
	var allocations []PointHoldAllocation
	if err := tx.Where("hold_id = ?", hold.ID).Find(&allocations).Error; err != nil {
		return err
	}
	var availableRelease int64
	for _, allocation := range allocations {
		var lot PointLot
		if err := tx.First(&lot, allocation.LotID).Error; err != nil {
			return err
		}
		release := allocation.ReservedMicro - allocation.SettledMicro
		updates := map[string]interface{}{"held_micro": gorm.Expr("held_micro - ?", release)}
		if lot.ExpiresAt == nil || *lot.ExpiresAt > now.Unix() {
			updates["available_micro"] = gorm.Expr("available_micro + ?", release)
			availableRelease += release
		}
		upd := tx.Model(&PointLot{}).Where("id = ? AND held_micro >= ?", lot.ID, release).Updates(updates)
		if upd.Error != nil {
			return upd.Error
		}
		if upd.RowsAffected != 1 {
			return errors.New("point lot state changed")
		}
	}
	update := tx.Model(&PointAccount{}).Where("user_id = ? AND held_micro >= ?", hold.UserID, hold.BudgetMicro).Updates(map[string]interface{}{"held_micro": gorm.Expr("held_micro - ?", hold.BudgetMicro), "available_micro": gorm.Expr("available_micro + ?", availableRelease)})
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected != 1 {
		return errors.New("point account state changed")
	}
	tokenUpdate := tx.Model(&PointTokenBudget{}).Where("token_id = ? AND held_micro >= ?", hold.TokenID, hold.BudgetMicro).Update("held_micro", gorm.Expr("held_micro - ?", hold.BudgetMicro))
	if tokenUpdate.Error != nil {
		return tokenUpdate.Error
	}
	if tokenUpdate.RowsAffected != 1 {
		return errors.New("point token budget state changed")
	}
	var account PointAccount
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&account, "user_id = ?", hold.UserID).Error; err != nil {
		return err
	}
	holdUpdate := tx.Model(hold).Where("state IN ?", []string{"held", "pending", "needs_review"}).Update("state", "released")
	if holdUpdate.Error != nil {
		return holdUpdate.Error
	}
	if holdUpdate.RowsAffected != 1 {
		return ErrPointsConflict
	}
	return tx.Create(&PointLedger{UserID: hold.UserID, BusinessKey: "release:" + hold.LogicalRequestKey, Kind: "release", HoldID: &hold.ID, AvailableDelta: availableRelease, HeldDelta: -hold.BudgetMicro, AvailableAfter: account.AvailableMicro, HeldAfter: account.HeldMicro, SpentAfter: account.SpentMicro, Reason: "adjudicated release"}).Error
}

// CreditPaidPointOrder only credits an order already durably marked paid by a future verified adapter.
func CreditPaidPointOrder(orderKey, eventKey string) error {
	if orderKey == "" || eventKey == "" {
		return errors.New("order and verified event keys are required")
	}
	return pointsTransaction(func(tx *gorm.DB) error { return creditPaidPointOrderTx(tx, orderKey, eventKey) })
}

// creditPaidPointOrderTx lets the verified payment inbox processor mark an
// order paid and credit its lots/account/ledger in one transaction.
func creditPaidPointOrderTx(tx *gorm.DB, orderKey, eventKey string) error {
	var order PointPurchaseOrder
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_key = ?", orderKey).First(&order).Error; err != nil {
		return err
	}
	if order.State == "credited" {
		if order.PaidEventKey != nil && *order.PaidEventKey == eventKey {
			return nil
		}
		return ErrPointsConflict
	}
	if order.State != "paid" {
		return errors.New("order is not verified paid")
	}
	if order.PaidEventKey == nil || *order.PaidEventKey != eventKey {
		return ErrPointsConflict
	}
	if order.PurchaseMicro <= 0 || order.AmountFen <= 0 || order.AmountFen > int64(^uint64(0)>>1)/PointMicroPerPoint || order.PurchaseMicro != order.AmountFen*PointMicroPerPoint || order.BonusMicro < 0 {
		return errors.New("invalid paid order snapshot")
	}
	if order.BonusMicro > 0 && (order.BonusExpiresAt == nil || *order.BonusExpiresAt <= 0) {
		return errors.New("paid order bonus must carry an explicit expiry")
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&PointAccount{UserID: order.UserID}).Error; err != nil {
		return err
	}
	lot := PointLot{UserID: order.UserID, BusinessKey: "purchase:" + order.OrderKey, Kind: "purchase", SourceRef: order.OrderKey, AmountFen: order.AmountFen, InitialMicro: order.PurchaseMicro, AvailableMicro: order.PurchaseMicro}
	if err := tx.Create(&lot).Error; err != nil {
		return err
	}
	if order.BonusMicro > 0 {
		bonus := PointLot{UserID: order.UserID, BusinessKey: "bonus:" + order.OrderKey, Kind: "bonus", SourceRef: order.OrderKey, InitialMicro: order.BonusMicro, AvailableMicro: order.BonusMicro, ExpiresAt: order.BonusExpiresAt}
		if err := tx.Create(&bonus).Error; err != nil {
			return err
		}
	}
	total, err := checkedAdd(order.PurchaseMicro, order.BonusMicro)
	if err != nil {
		return err
	}
	upd := tx.Model(&PointAccount{}).Where("user_id = ? AND available_micro <= ?", order.UserID, int64(^uint64(0)>>1)-total).Update("available_micro", gorm.Expr("available_micro + ?", total))
	if upd.Error != nil {
		return upd.Error
	}
	if upd.RowsAffected != 1 {
		return errors.New("point account missing or point balance overflow")
	}
	var account PointAccount
	if err := tx.First(&account, "user_id = ?", order.UserID).Error; err != nil {
		return err
	}
	ledger := PointLedger{UserID: order.UserID, BusinessKey: "credit:" + order.OrderKey, Kind: "purchase_credit", AvailableDelta: total, AvailableAfter: account.AvailableMicro, HeldAfter: account.HeldMicro, SpentAfter: account.SpentMicro, Reason: "verified paid order credit"}
	if err := tx.Create(&ledger).Error; err != nil {
		return err
	}
	orderUpdate := tx.Model(&order).Where("state = ? AND paid_event_key = ?", "paid", eventKey).Update("state", "credited")
	if orderUpdate.Error != nil {
		return orderUpdate.Error
	}
	if orderUpdate.RowsAffected != 1 {
		return ErrPointsConflict
	}
	return nil
}

// CreditPaidPointOrderTx is reserved for the payment inbox transaction, where
// marking the verified order paid and crediting the ledger must commit together.
func CreditPaidPointOrderTx(tx *gorm.DB, orderKey, eventKey string) error {
	if tx == nil || orderKey == "" || eventKey == "" {
		return errors.New("transaction, order, and verified event keys are required")
	}
	return creditPaidPointOrderTx(tx, orderKey, eventKey)
}
