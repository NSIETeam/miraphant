package payments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/payment"
)

const refundOperationLeaseSeconds int64 = 60
const refundProviderTimeout = 20 * time.Second

type RefundDispatchResult struct {
	RefundKey string
	Operation string
	Outcome   payment.RefundOutcome
	InboxID   uint
	State     string
}

type RefundInboxReplayReport struct {
	Scanned   int
	Processed int
	Failed    int
	NextID    uint
	HasMore   bool
	FailedIDs []uint
}

func configuredRefundProvider(refund *model.PointRefund) (payment.RefundProvider, error) {
	registered, ok := ProviderFor(refund.Channel)
	if !ok || registered.Provider == nil || registered.Identity.Provider != refund.Channel || registered.Identity.MerchantID != refund.ProviderMerchantID || registered.Identity.AppID != refund.ProviderAppID {
		return nil, errors.New("frozen refund merchant is not configured")
	}
	provider, ok := registered.Provider.(payment.RefundProvider)
	if !ok {
		return nil, errors.New("configured provider does not support refunds")
	}
	return provider, nil
}

func refundProtocolRequest(refund *model.PointRefund) payment.RefundRequest {
	return payment.RefundRequest{
		RefundKey: refund.RefundKey, ProviderRefundKey: refund.ProviderRefundKey,
		OrderKey: refund.OrderKey, TransactionID: refund.ProviderTransactionID,
		MerchantID: refund.ProviderMerchantID, AppID: refund.ProviderAppID,
		AmountFen: refund.AmountFen, TotalFen: refund.OriginalAmountFen,
		PriorRefundedFen: refund.PriorRefundedFen, Currency: refund.Currency, Reason: refund.Reason,
	}
}

// DispatchPointRefundOperation claims an approved refund durably before doing
// network I/O. A normal approved request is submitted once; any expired claim
// or later recovery is query-only. Unknown responses remain frozen and are
// recorded for operational review.
func DispatchPointRefundOperation(ctx context.Context, refundKey string) (RefundDispatchResult, error) {
	refund, err := model.GetPointRefundByKey(refundKey)
	if err != nil {
		return RefundDispatchResult{}, err
	}
	if err := validateRefundOrderBinding(refund); err != nil {
		return RefundDispatchResult{}, err
	}
	provider, err := configuredRefundProvider(refund)
	if err != nil {
		return RefundDispatchResult{}, err
	}
	token := uuid.NewString()
	claim, err := model.ClaimPointRefundOperation(refundKey, token, time.Now().UTC().Unix(), refundOperationLeaseSeconds)
	if err != nil {
		return RefundDispatchResult{}, err
	}
	request := refundProtocolRequest(&claim.Refund)
	callCtx, cancel := context.WithTimeout(ctx, refundProviderTimeout)
	defer cancel()
	var result payment.RefundResult
	if claim.Kind == "apply" {
		result, err = provider.ApplyRefund(callCtx, request)
	} else {
		result, err = provider.QueryRefund(callCtx, request)
	}
	if err != nil {
		// Never infer definite failure from transport/HTTP errors. Store only a
		// fixed classification and a digest of normalized safe metadata.
		result = refundResultFromSnapshot(&claim.Refund, payment.RefundUnknown, "provider_unconfirmed", claim.Kind)
	}
	if !resultMatchesRefund(result, &claim.Refund) {
		result = refundResultFromSnapshot(&claim.Refund, payment.RefundUnknown, "identity_unconfirmed", claim.Kind)
	}
	inbox, _, err := PersistRefundProviderResult(result, claim.Kind, nil, token)
	if err != nil {
		// A lost database write leaves the durable claim to expire. Recovery then
		// queries the same provider refund key rather than issuing another apply.
		return RefundDispatchResult{RefundKey: refundKey, Operation: claim.Kind, Outcome: payment.RefundUnknown}, err
	}
	processed, err := ProcessPointRefundInbox(inbox.ID)
	if err != nil {
		return RefundDispatchResult{RefundKey: refundKey, Operation: claim.Kind, Outcome: result.Outcome, InboxID: inbox.ID, State: inbox.State}, err
	}
	return RefundDispatchResult{RefundKey: refundKey, Operation: claim.Kind, Outcome: result.Outcome, InboxID: inbox.ID, State: processed.State}, nil
}

func refundResultFromSnapshot(refund *model.PointRefund, outcome payment.RefundOutcome, status, source string) payment.RefundResult {
	return payment.RefundResult{Provider: refund.Channel, Outcome: outcome, Status: status, RefundKey: refund.RefundKey,
		ProviderRefundKey: refund.ProviderRefundKey, OrderKey: refund.OrderKey,
		TransactionID: refund.ProviderTransactionID, MerchantID: refund.ProviderMerchantID,
		AppID: refund.ProviderAppID, AmountFen: refund.AmountFen, TotalFen: refund.OriginalAmountFen,
		Currency: refund.Currency, EvidenceSource: source}
}

func resultMatchesRefund(result payment.RefundResult, refund *model.PointRefund) bool {
	if result.RefundKey != "" && result.RefundKey != refund.RefundKey || result.Provider != refund.Channel || result.ProviderRefundKey != refund.ProviderRefundKey || result.OrderKey != refund.OrderKey || result.TransactionID != refund.ProviderTransactionID || result.MerchantID != refund.ProviderMerchantID || result.AppID != refund.ProviderAppID || result.AmountFen != refund.AmountFen || result.TotalFen != refund.OriginalAmountFen || result.Currency != refund.Currency {
		return false
	}
	if refund.Channel == "wechat" && (result.Outcome == payment.RefundSucceeded || result.Outcome == payment.RefundDefiniteFailed || result.Outcome == payment.RefundAccepted) && result.ProviderRefundID == "" {
		return false
	}
	return result.Outcome == payment.RefundAccepted || result.Outcome == payment.RefundSucceeded || result.Outcome == payment.RefundDefiniteFailed || result.Outcome == payment.RefundUnknown || result.Outcome == payment.RefundAbnormal
}

func PersistRefundProviderResult(result payment.RefundResult, source string, rawVerifiedBody []byte, operationToken ...string) (*model.PointRefundInbox, bool, error) {
	if source != "apply" && source != "query" && source != "notification" {
		return nil, false, errors.New("invalid refund evidence source")
	}
	var refund *model.PointRefund
	var err error
	if result.RefundKey != "" {
		refund, err = model.GetPointRefundByKey(result.RefundKey)
	} else {
		refund, err = model.GetPointRefundForProvider(result.Provider, result.ProviderRefundKey, result.OrderKey)
	}
	if err != nil {
		return nil, false, err
	}
	if !resultMatchesRefund(result, refund) {
		return nil, false, payment.ErrOrderMismatch
	}
	result.RefundKey = refund.RefundKey
	result.EvidenceSource = source
	canonical, err := json.Marshal(struct {
		Source string               `json:"source"`
		Result payment.RefundResult `json:"result"`
	}{source, result})
	if err != nil {
		return nil, false, err
	}
	digestBytes := sha256.Sum256(canonical)
	if len(rawVerifiedBody) != 0 {
		digestBytes = sha256.Sum256(rawVerifiedBody)
	}
	digest := hex.EncodeToString(digestBytes[:])
	claimToken := ""
	if len(operationToken) > 0 {
		claimToken = operationToken[0]
	}
	keyMaterial := result.ProviderEventID
	if keyMaterial == "" {
		keyMaterial = digest
	}
	keyHash := sha256.Sum256([]byte(refund.RefundKey + "\x00" + source + "\x00" + keyMaterial + "\x00" + claimToken))
	evidenceKey := "rf:" + hex.EncodeToString(keyHash[:])
	outcome := string(result.Outcome)
	input := model.PointRefundInboxInput{EvidenceKey: evidenceKey, RefundKey: refund.RefundKey, Provider: result.Provider,
		EvidenceSource: source, OperationToken: claimToken, ProviderEventID: result.ProviderEventID, ProviderRefundID: result.ProviderRefundID,
		ProviderRefundKey: result.ProviderRefundKey, OrderKey: result.OrderKey, ProviderTransactionID: result.TransactionID,
		MerchantID: result.MerchantID, AppID: result.AppID, Outcome: outcome, ProviderStatus: safeProviderStatus(result.Status),
		AmountFen: result.AmountFen, TotalFen: result.TotalFen, Currency: result.Currency,
		ProviderOccurredAt: result.ProviderOccurredAt.UTC().Unix(), Digest: digest}
	return model.PersistPointRefundInbox(input)
}

func safeProviderStatus(status string) string {
	status = strings.TrimSpace(status)
	if len(status) > 48 {
		status = status[:48]
	}
	for _, r := range status {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return "unclassified"
		}
	}
	return status
}

// VerifyAndPersistWeChatRefundNotification verifies the signed/encrypted
// envelope before using any event identity. The raw bytes are only hashed.
func VerifyAndPersistWeChatRefundNotification(headers http.Header, body []byte) (*model.PointRefundInbox, bool, error) {
	registered, ok := ProviderFor("wechat")
	if !ok || registered.Provider == nil {
		return nil, false, errors.New("WeChat Pay provider is not configured")
	}
	verifier, ok := registered.Provider.(payment.RefundNotificationVerifier)
	if !ok {
		return nil, false, errors.New("WeChat Pay refund notifications are unavailable")
	}
	result, err := verifier.VerifyRefundNotification(headers, body)
	if err != nil {
		return nil, false, err
	}
	if result.Provider != "wechat" || result.ProviderRefundKey == "" || result.OrderKey == "" {
		return nil, false, payment.ErrInvalidNotice
	}
	refund, err := model.GetPointRefundForProvider("wechat", result.ProviderRefundKey, result.OrderKey)
	if err != nil {
		return nil, false, err
	}
	result.RefundKey = refund.RefundKey
	if registered.Identity.Provider != refund.Channel || registered.Identity.MerchantID != refund.ProviderMerchantID || registered.Identity.AppID != refund.ProviderAppID {
		return nil, false, payment.ErrOrderMismatch
	}
	return PersistRefundProviderResult(result, "notification", body)
}

// ProcessPointRefundInbox replays one durable normalized result. The ledger
// transition and immutable evidence are atomic; marking the inbox is a second
// idempotent step, so a crash between them safely replays the same EvidenceKey.
func ProcessPointRefundInbox(inboxID uint) (*model.PointRefundInbox, error) {
	var inbox model.PointRefundInbox
	if err := model.DB.First(&inbox, inboxID).Error; err != nil {
		return nil, err
	}
	if inbox.State == "processed" || inbox.State == "quarantined" {
		return &inbox, nil
	}
	if inbox.State != "received" {
		return nil, errors.New("refund inbox item is not awaiting processing")
	}
	outcome := inbox.Outcome
	if outcome == string(payment.RefundAccepted) {
		outcome = "pending"
	}
	if outcome == string(payment.RefundUnknown) && inbox.ProviderRefundID == "" && inbox.OperationToken != "" {
		if err := model.RecordPointRefundOperationUnknown(inbox.RefundKey, inbox.OperationToken, time.Now().UTC()); err != nil {
			return nil, err
		}
		if err := model.MarkPointRefundInbox(inbox.ID, "processed", "", time.Now().UTC()); err != nil {
			return nil, err
		}
		inbox.State, inbox.ProcessedAt = "processed", timePtr(time.Now().UTC())
		return &inbox, nil
	}
	err := model.RecordPointRefundEvidence(model.PointRefundEvidenceInput{EvidenceKey: inbox.EvidenceKey, RefundKey: inbox.RefundKey,
		Provider: inbox.Provider, OperationToken: inbox.OperationToken, Outcome: outcome, ProviderRefundID: inbox.ProviderRefundID, ProviderRefundKey: inbox.ProviderRefundKey,
		OrderKey: inbox.OrderKey, ProviderTransactionID: inbox.ProviderTransactionID, MerchantID: inbox.MerchantID,
		AppID: inbox.AppID, AmountFen: inbox.AmountFen, TotalFen: inbox.TotalFen, Currency: inbox.Currency, Digest: inbox.Digest})
	if err != nil {
		return nil, err
	}
	if err := model.MarkPointRefundInbox(inbox.ID, "processed", "", time.Now().UTC()); err != nil {
		return nil, err
	}
	inbox.State, inbox.ProcessedAt = "processed", timePtr(time.Now().UTC())
	return &inbox, nil
}

func ReplayReceivedPointRefundInbox(afterID uint, limit int) (RefundInboxReplayReport, error) {
	rows, hasMore, err := model.ListReceivedPointRefundInbox(afterID, limit)
	report := RefundInboxReplayReport{}
	if err != nil {
		return report, err
	}
	report.HasMore = hasMore
	for _, row := range rows {
		report.Scanned++
		report.NextID = row.ID
		if _, err := ProcessPointRefundInbox(row.ID); err != nil {
			report.Failed++
			report.FailedIDs = append(report.FailedIDs, row.ID)
			continue
		}
		report.Processed++
	}
	return report, nil
}

func timePtr(value time.Time) *time.Time { return &value }

// Verify normalized inbox row identities once more before exposing a provider
// call, including the merchant binding frozen on the paid order.
func validateRefundOrderBinding(refund *model.PointRefund) error {
	var order model.PointPurchaseOrder
	if err := model.DB.Where("order_key = ?", refund.OrderKey).First(&order).Error; err != nil {
		return err
	}
	if order.UserID != refund.UserID || order.Channel != refund.Channel || order.ProviderMerchantID != refund.ProviderMerchantID || order.ProviderAppID != refund.ProviderAppID || order.ProviderTransactionID != refund.ProviderTransactionID || order.AmountFen != refund.OriginalAmountFen || order.Currency != refund.Currency {
		return fmt.Errorf("refund no longer matches its immutable paid order")
	}
	return nil
}
