package payments

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/payment"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MerchantIdentity struct {
	Provider   string
	MerchantID string
	AppID      string
}

type PaymentInboxResult struct {
	EventID   uint   `json:"event_id"`
	State     string `json:"state"`
	Duplicate bool   `json:"duplicate"`
}

type paymentEventPayload struct {
	EvidenceSource     string    `json:"evidence_source,omitempty"`
	Provider           string    `json:"provider"`
	ProviderEventID    string    `json:"provider_event_id"`
	ProviderOccurredAt time.Time `json:"provider_occurred_at"`
	OrderKey           string    `json:"order_key"`
	TransactionID      string    `json:"transaction_id"`
	MerchantID         string    `json:"merchant_id"`
	AppID              string    `json:"app_id"`
	AmountFen          int64     `json:"amount_fen"`
	Currency           string    `json:"currency"`
	Status             string    `json:"status"`
}

func normalizedPayload(trade payment.VerifiedNotification) paymentEventPayload {
	return paymentEventPayload{EvidenceSource: trade.EvidenceSource, Provider: trade.Provider, ProviderEventID: trade.ProviderEventID, ProviderOccurredAt: trade.ProviderOccurredAt, OrderKey: trade.OrderKey, TransactionID: trade.TransactionID, MerchantID: trade.MerchantID, AppID: trade.AppID, AmountFen: trade.AmountFen, Currency: trade.Currency, Status: trade.Status}
}

// PersistVerifiedNotification durably records verified, normalized evidence
// before the callback handler acknowledges receipt. It never stores raw bodies
// or provider credentials. Processing is a separate transaction so a crash
// leaves a recoverable `received` event.
func PersistVerifiedNotification(trade payment.VerifiedNotification, rawBody []byte) (PaymentInboxResult, error) {
	if len(rawBody) == 0 || len(rawBody) > 1<<20 || trade.ProviderEventID == "" || trade.OrderKey == "" || trade.TransactionID == "" || trade.AmountFen <= 0 {
		return PaymentInboxResult{}, payment.ErrInvalidNotice
	}
	if trade.Provider != "wechat" && trade.Provider != "alipay" {
		return PaymentInboxResult{}, payment.ErrInvalidNotice
	}
	payloadBytes, err := json.Marshal(normalizedPayload(trade))
	if err != nil {
		return PaymentInboxResult{}, err
	}
	digestBytes := sha256.Sum256(rawBody)
	digest := hex.EncodeToString(digestBytes[:])
	event := model.PaymentEvent{Provider: trade.Provider, ProviderEventID: trade.ProviderEventID, OrderKey: trade.OrderKey, ProviderTransactionID: trade.TransactionID, Digest: digest, Payload: string(payloadBytes), Verification: "verified", State: "received"}
	baseEvent := event
	duplicate := false
	err = model.WithPaymentTransaction(func(tx *gorm.DB) error {
		event = baseEvent
		duplicate = false
		insert := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&event)
		if insert.Error != nil {
			return insert.Error
		}
		if insert.RowsAffected == 1 {
			return nil
		}
		duplicate = true
		var prior model.PaymentEvent
		if err := tx.Where("provider = ? AND provider_event_id = ?", event.Provider, event.ProviderEventID).First(&prior).Error; err != nil {
			return err
		}
		if prior.Digest != event.Digest || prior.Payload != event.Payload || prior.OrderKey != event.OrderKey || prior.ProviderTransactionID != event.ProviderTransactionID {
			return model.ErrPointsConflict
		}
		event = prior
		return nil
	})
	if err != nil {
		return PaymentInboxResult{}, err
	}
	return PaymentInboxResult{EventID: event.ID, State: event.State, Duplicate: duplicate}, nil
}

func successStatus(provider, status string) bool {
	if provider == "wechat" {
		return status == "SUCCESS"
	}
	return status == "TRADE_SUCCESS" || status == "TRADE_FINISHED"
}

func quarantinePaymentEvent(tx *gorm.DB, event *model.PaymentEvent, reason string, details map[string]any) error {
	eventUpdate := tx.Model(&model.PaymentEvent{}).Where("id = ? AND state = ?", event.ID, "received").Updates(map[string]any{"state": "quarantined", "error_code": reason})
	if eventUpdate.Error != nil {
		return eventUpdate.Error
	}
	if eventUpdate.RowsAffected != 1 {
		return model.ErrPointsConflict
	}
	jsonDetails, err := json.Marshal(details)
	if err != nil {
		return err
	}
	audit := model.PointAdminAudit{ActorUserID: 0, Action: "payment_event_quarantined", BusinessKey: "payment-event-quarantine:" + event.Provider + ":" + event.ProviderEventID, TargetUserID: 0, Details: string(jsonDetails)}
	var prior model.PointAdminAudit
	if err := tx.Where("business_key = ?", audit.BusinessKey).First(&prior).Error; err == nil {
		if prior.Action != audit.Action || prior.Details != audit.Details {
			return model.ErrPointsConflict
		}
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return tx.Create(&audit).Error
}

// ProcessVerifiedPaymentEvent atomically matches an inbox event to its frozen
// order snapshot, marks it paid, credits purchase/bonus lots plus account and
// ledger, and marks the inbox event processed. No external I/O occurs here.
func ProcessVerifiedPaymentEvent(eventID uint, identity MerchantIdentity) (PaymentInboxResult, error) {
	if eventID == 0 || identity.Provider == "" || identity.MerchantID == "" || identity.AppID == "" {
		return PaymentInboxResult{}, errors.New("event and configured provider identity are required")
	}
	var result PaymentInboxResult
	var processingErr error
	err := model.WithPaymentTransaction(func(tx *gorm.DB) error {
		// The transaction wrapper may rerun this closure after a safe database
		// lock failure. Do not leak a previous attempt's result into the retry.
		result = PaymentInboxResult{}
		processingErr = nil
		var event model.PaymentEvent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&event, eventID).Error; err != nil {
			return err
		}
		result = PaymentInboxResult{EventID: event.ID, State: event.State}
		if event.State == "processed" || event.State == "quarantined" {
			return nil
		}
		if event.State != "received" || event.Verification != "verified" {
			return errors.New("payment event is not awaiting verified processing")
		}
		var notice paymentEventPayload
		if err := json.Unmarshal([]byte(event.Payload), &notice); err != nil {
			return err
		}
		if notice.Provider != identity.Provider || event.Provider != identity.Provider {
			processingErr = payment.ErrOrderMismatch
			if err := quarantinePaymentEvent(tx, &event, "provider_mismatch", map[string]any{"order_key": event.OrderKey, "reason": "provider_mismatch"}); err != nil {
				return err
			}
			result.State = "quarantined"
			return nil
		}
		var order model.PointPurchaseOrder
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_key = ?", notice.OrderKey).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				processingErr = payment.ErrOrderMismatch
				if err := quarantinePaymentEvent(tx, &event, "order_not_found", map[string]any{"order_key": notice.OrderKey, "reason": "order_not_found"}); err != nil {
					return err
				}
				result.State = "quarantined"
				return nil
			}
			return err
		}
		reason := ""
		switch {
		case order.ProviderMerchantID == "" || order.ProviderAppID == "":
			reason = "order_provider_snapshot_missing"
		case notice.MerchantID != identity.MerchantID:
			reason = "merchant_mismatch"
		case notice.AppID != identity.AppID:
			reason = "app_mismatch"
		case order.Channel != identity.Provider:
			reason = "channel_mismatch"
		case order.ProviderMerchantID != notice.MerchantID:
			reason = "order_merchant_mismatch"
		case order.ProviderAppID != notice.AppID:
			reason = "order_app_mismatch"
		case notice.OrderKey != order.OrderKey || event.OrderKey != order.OrderKey:
			reason = "order_mismatch"
		case notice.AmountFen != order.AmountFen || notice.Currency != order.Currency:
			reason = "amount_or_currency_mismatch"
		case !successStatus(identity.Provider, notice.Status):
			reason = "payment_not_successful"
		}
		if reason != "" {
			processingErr = payment.ErrOrderMismatch
			if err := quarantinePaymentEvent(tx, &event, reason, map[string]any{"order_key": order.OrderKey, "reason": reason, "expected_amount_fen": order.AmountFen, "reported_amount_fen": notice.AmountFen, "expected_currency": order.Currency, "reported_currency": notice.Currency}); err != nil {
				return err
			}
			result.State = "quarantined"
			return nil
		}
		if order.State != "credited" && order.State != "created" && order.State != "pending" && order.State != "closed" {
			processingErr = payment.ErrOrderMismatch
			if err := quarantinePaymentEvent(tx, &event, "order_state_conflict", map[string]any{"order_key": order.OrderKey, "reason": "order_state_conflict", "state": order.State}); err != nil {
				return err
			}
			result.State = "quarantined"
			return nil
		}
		if order.State == "credited" && (order.ProviderTransactionID != notice.TransactionID || order.ProviderMerchantID != notice.MerchantID) {
			processingErr = payment.ErrOrderMismatch
			if err := quarantinePaymentEvent(tx, &event, "credited_order_transaction_conflict", map[string]any{"order_key": order.OrderKey, "reason": "credited_order_transaction_conflict"}); err != nil {
				return err
			}
			result.State = "quarantined"
			return nil
		}
		var owner model.PaymentTransaction
		ownerErr := tx.Where("provider = ? AND merchant_id = ? AND provider_transaction_id = ?", identity.Provider, identity.MerchantID, notice.TransactionID).First(&owner).Error
		if ownerErr == nil {
			if owner.OrderKey != order.OrderKey || owner.AppID != identity.AppID {
				processingErr = payment.ErrOrderMismatch
				if err := quarantinePaymentEvent(tx, &event, "transaction_reused", map[string]any{"order_key": order.OrderKey, "reason": "transaction_reused"}); err != nil {
					return err
				}
				result.State = "quarantined"
				return nil
			}
		} else if errors.Is(ownerErr, gorm.ErrRecordNotFound) {
			claim := model.PaymentTransaction{Provider: identity.Provider, MerchantID: identity.MerchantID, AppID: identity.AppID, ProviderTransactionID: notice.TransactionID, OrderKey: order.OrderKey, EventID: event.ID}
			insert := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&claim)
			if insert.Error != nil {
				return insert.Error
			}
			if insert.RowsAffected == 0 {
				// Another order may have claimed the provider transaction while
				// this transaction was in flight. Read the durable owner and
				// quarantine only after the unique index arbitrates the race.
				if err := tx.Where("provider = ? AND merchant_id = ? AND provider_transaction_id = ?", identity.Provider, identity.MerchantID, notice.TransactionID).First(&owner).Error; err != nil {
					return err
				}
				if owner.OrderKey != order.OrderKey || owner.AppID != identity.AppID {
					processingErr = payment.ErrOrderMismatch
					if err := quarantinePaymentEvent(tx, &event, "transaction_reused", map[string]any{"order_key": order.OrderKey, "reason": "transaction_reused"}); err != nil {
						return err
					}
					result.State = "quarantined"
					return nil
				}
			}
		} else {
			return ownerErr
		}
		if order.State == "credited" {
			if err := tx.Model(&model.PaymentEvent{}).Where("id = ? AND state = ?", event.ID, "received").Updates(map[string]any{"state": "processed", "processed_at": time.Now().UTC()}).Error; err != nil {
				return err
			}
			result.State = "processed"
			return nil
		}
		originalState := order.State
		eventKey := identity.Provider + ":" + notice.ProviderEventID
		paidEvent := eventKey
		updates := map[string]any{"state": "paid", "paid_event_key": paidEvent, "provider_transaction_id": notice.TransactionID, "provider_merchant_id": notice.MerchantID}
		if order.BonusMicro > 0 {
			if order.BonusValiditySecs <= 0 {
				return errors.New("paid order bonus has no validity period")
			}
			paidAt := notice.ProviderOccurredAt.UTC().Unix()
			if notice.ProviderOccurredAt.IsZero() {
				paidAt = order.CreatedAt.UTC().Unix()
			}
			if paidAt <= 0 || order.BonusValiditySecs > int64(^uint64(0)>>1)-paidAt {
				return errors.New("paid order bonus expiry overflows")
			}
			expires := paidAt + order.BonusValiditySecs
			updates["bonus_expires_at"] = expires
		}
		orderUpdate := tx.Model(&order).Where("state IN ?", []string{"created", "pending", "closed"}).Updates(updates)
		if orderUpdate.Error != nil {
			return orderUpdate.Error
		}
		if orderUpdate.RowsAffected != 1 {
			return model.ErrPointsConflict
		}
		if originalState == "closed" {
			details, _ := json.Marshal(map[string]any{"order_key": order.OrderKey, "reason": "verified_success_after_local_close", "transaction_id": notice.TransactionID})
			audit := model.PointAdminAudit{ActorUserID: 0, Action: "late_payment_after_close", BusinessKey: "late-payment:" + eventKey, TargetUserID: order.UserID, Details: string(details)}
			if err := tx.Create(&audit).Error; err != nil {
				return err
			}
		}
		if err := model.CreditPaidPointOrderTx(tx, order.OrderKey, eventKey); err != nil {
			return err
		}
		processedAt := time.Now().UTC()
		eventUpdate := tx.Model(&model.PaymentEvent{}).Where("id = ? AND state = ?", event.ID, "received").Updates(map[string]any{"state": "processed", "processed_at": processedAt})
		if eventUpdate.Error != nil {
			return eventUpdate.Error
		}
		if eventUpdate.RowsAffected != 1 {
			return model.ErrPointsConflict
		}
		result.State = "processed"
		return nil
	})
	if err != nil {
		return PaymentInboxResult{}, err
	}
	if processingErr != nil {
		return result, processingErr
	}
	return result, nil
}

// RetryReceivedPaymentEvents is a network-free recovery path for a durable
// inbox event left at `received` by a crash between persistence and credit.
type RecoveryReport struct {
	Scanned     int  `json:"scanned"`
	Attempted   int  `json:"attempted"`
	Processed   int  `json:"processed"`
	Quarantined int  `json:"quarantined"`
	Failed      int  `json:"failed"`
	Skipped     int  `json:"skipped"`
	NextAfterID uint `json:"next_after_id"`
	HasMore     bool `json:"has_more"`
}

func RetryReceivedPaymentEventsDetailed(afterID uint, limit int, identities map[string]MerchantIdentity) (RecoveryReport, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var events []model.PaymentEvent
	if err := model.DB.Where("state = ? AND id > ?", "received", afterID).Order("id ASC").Limit(limit + 1).Find(&events).Error; err != nil {
		return RecoveryReport{}, err
	}
	report := RecoveryReport{}
	if len(events) > limit {
		report.HasMore = true
		events = events[:limit]
	}
	for _, event := range events {
		report.Scanned++
		report.NextAfterID = event.ID
		identity, ok := identities[event.Provider]
		if !ok {
			report.Skipped++
			continue
		}
		report.Attempted++
		result, err := ProcessVerifiedPaymentEvent(event.ID, identity)
		if err != nil && !errors.Is(err, payment.ErrOrderMismatch) {
			report.Failed++
			continue
		}
		if result.State == "processed" {
			report.Processed++
		} else if result.State == "quarantined" {
			report.Quarantined++
		} else if err != nil {
			report.Failed++
		}
	}
	return report, nil
}

func RetryReceivedPaymentEvents(limit int, identities map[string]MerchantIdentity) (int, error) {
	report, err := RetryReceivedPaymentEventsDetailed(0, limit, identities)
	if err != nil {
		return report.Processed, err
	}
	if report.Failed > 0 || report.Skipped > 0 || report.HasMore {
		return report.Processed, fmt.Errorf("payment recovery incomplete: scanned=%d processed=%d quarantined=%d failed=%d skipped=%d has_more=%t", report.Scanned, report.Processed, report.Quarantined, report.Failed, report.Skipped, report.HasMore)
	}
	return report.Processed, nil
}

func ReceiptAcknowledgementSafe(result PaymentInboxResult) bool {
	return result.EventID != 0 && (result.State == "received" || result.State == "processed" || result.State == "quarantined")
}

func ParseProviderName(raw string) string { return strings.ToLower(strings.TrimSpace(raw)) }
