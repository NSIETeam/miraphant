package model

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/songquanpeng/one-api/payment/bill"
	"gorm.io/gorm"
)

var (
	ErrReconciliationDisabled    = errors.New("reconciliation source encryption is not configured")
	ErrReconciliationConflict    = errors.New("reconciliation import key conflicts with existing evidence")
	ErrReconciliationUnsupported = errors.New("reconciliation bill format is unsupported")
)

const reconciliationBatchMetadataColumns = "id, batch_key, provider, merchant_id, app_id, bill_date, bill_type, format_version, import_version, timezone, status, source_sha256, provider_hash_type, provider_hash_value, provider_hash_verified, source_size, row_count, created_at"

// PointReconciliationBatch stores only an encrypted source file. The key is
// supplied by server configuration and is deliberately separate from session
// secrets. A batch and all its row/difference evidence commit atomically.
type PointReconciliationBatch struct {
	ID                   uint   `gorm:"primaryKey"`
	BatchKey             string `gorm:"size:64;not null;uniqueIndex"`
	Provider             string `gorm:"size:16;not null;index:idx_recon_batch_scope"`
	MerchantID           string `gorm:"size:80;not null;index:idx_recon_batch_scope"`
	AppID                string `gorm:"size:80;not null;index:idx_recon_batch_scope"`
	BillDate             string `gorm:"size:10;not null;index:idx_recon_batch_scope"`
	BillType             string `gorm:"size:16;not null;index:idx_recon_batch_scope"`
	FormatVersion        string `gorm:"size:80;not null;index:idx_recon_batch_scope"`
	ImportVersion        int    `gorm:"not null"`
	Timezone             string `gorm:"size:48;not null"`
	Status               string `gorm:"size:24;not null;index"` // imported, unsupported_format
	SourceSHA256         string `gorm:"size:64;not null"`
	ProviderHashType     string `gorm:"size:16;not null;default:''"`
	ProviderHashValue    string `gorm:"size:128;not null;default:''"`
	ProviderHashVerified bool   `gorm:"not null;default:false"`
	SourceSize           int64  `gorm:"not null"`
	SourceKeyID          string `gorm:"size:80;not null"`
	SourceNonce          []byte `gorm:"type:blob;not null"`
	SourceCiphertext     []byte `gorm:"type:blob;not null"`
	RowCount             int    `gorm:"not null;default:0"`
	CreatedAt            time.Time
}

func (*PointReconciliationBatch) BeforeUpdate(*gorm.DB) error {
	return errors.New("reconciliation source batches are immutable")
}
func (*PointReconciliationBatch) BeforeDelete(*gorm.DB) error {
	return errors.New("reconciliation source batches are immutable")
}

// PointReconciliationRow is a line-level, immutable projection of the source.
// It never contains payer identifiers, product names, or raw custom text.
type PointReconciliationRow struct {
	ID                   uint      `gorm:"primaryKey"`
	BatchID              uint      `gorm:"not null;uniqueIndex:idx_recon_row_source_line;index"`
	SourceLine           int       `gorm:"not null;uniqueIndex:idx_recon_row_source_line"`
	SourceDigest         string    `gorm:"size:64;not null"`
	Provider             string    `gorm:"size:16;not null;index"`
	Kind                 string    `gorm:"size:16;not null;index"`
	ProviderStatus       string    `gorm:"size:48;not null"`
	RefundStatus         string    `gorm:"size:48;not null;default:''"`
	OccurredAt           time.Time `gorm:"not null;index"`
	MerchantID           string    `gorm:"size:80;not null;index"`
	AppID                string    `gorm:"size:80;not null;index"`
	OrderKey             string    `gorm:"size:160;not null;index"`
	TransactionID        string    `gorm:"size:180;not null;index"`
	MerchantRefundKey    string    `gorm:"size:64;not null;default:'';index"`
	ProviderRefundID     string    `gorm:"size:180;not null;default:'';index"`
	Currency             string    `gorm:"size:3;not null"`
	GrossFen             int64     `gorm:"not null"`
	SettlementFen        int64     `gorm:"not null"`
	DiscountFen          int64     `gorm:"not null"`
	RefundFen            int64     `gorm:"not null"`
	CouponRefundFen      int64     `gorm:"not null"`
	RefundRequestedFen   int64     `gorm:"not null"`
	FeeFen               int64     `gorm:"not null"`
	NetFen               int64     `gorm:"not null"`
	HasNetFen            bool      `gorm:"not null;default:false"`
	ReconciliationResult string    `gorm:"size:32;not null;index"`
	CreatedAt            time.Time
}

func (*PointReconciliationRow) BeforeUpdate(*gorm.DB) error {
	return errors.New("reconciliation source rows are immutable")
}
func (*PointReconciliationRow) BeforeDelete(*gorm.DB) error {
	return errors.New("reconciliation source rows are immutable")
}

// PointReconciliationDifference records a fixed finding. Resolution and
// operator notes are separate append-only actions; neither edits the finding.
type PointReconciliationDifference struct {
	ID                  uint   `gorm:"primaryKey"`
	DifferenceKey       string `gorm:"size:64;not null;uniqueIndex"`
	BatchID             uint   `gorm:"not null;index"`
	RowID               *uint  `gorm:"index"`
	LocalOrderID        *uint  `gorm:"index"`
	LocalRefundID       *uint  `gorm:"index"`
	PaymentEventID      *uint  `gorm:"index"`
	Classification      string `gorm:"size:32;not null;index"`
	Severity            string `gorm:"size:16;not null"` // informational, attention
	EvidenceFingerprint string `gorm:"size:64;not null"`
	CreatedAt           time.Time
}

func (*PointReconciliationDifference) BeforeUpdate(*gorm.DB) error {
	return errors.New("reconciliation differences are append-only")
}
func (*PointReconciliationDifference) BeforeDelete(*gorm.DB) error {
	return errors.New("reconciliation differences are append-only")
}

type PointReconciliationAction struct {
	ID           uint   `gorm:"primaryKey"`
	ActionKey    string `gorm:"size:180;not null;uniqueIndex"`
	DifferenceID uint   `gorm:"not null;index"`
	ActorUserID  int    `gorm:"not null;index"`
	Action       string `gorm:"size:24;not null"` // note, request_query, record_reference; does not resolve a finding
	Reason       string `gorm:"size:512;not null"`
	BusinessRef  string `gorm:"size:180;not null;default:''"`
	CreatedAt    time.Time
}

func (*PointReconciliationAction) BeforeUpdate(*gorm.DB) error {
	return errors.New("reconciliation actions are append-only")
}
func (*PointReconciliationAction) BeforeDelete(*gorm.DB) error {
	return errors.New("reconciliation actions are append-only")
}

type ReconciliationSourceKey struct {
	KeyID string
	Key   []byte
}

// DecryptPointReconciliationSource is an internal evidence-access primitive.
// Callers must enforce capability checks before invoking it; list and detail
// DTOs never include the ciphertext or plaintext source.
func DecryptPointReconciliationSource(batch PointReconciliationBatch, key ReconciliationSourceKey) ([]byte, error) {
	if key.KeyID == "" || key.KeyID != batch.SourceKeyID || len(key.Key) != 32 || batch.BatchKey == "" {
		return nil, ErrReconciliationDisabled
	}
	block, err := aes.NewCipher(key.Key)
	if err != nil {
		return nil, ErrReconciliationDisabled
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(batch.SourceNonce) != gcm.NonceSize() {
		return nil, errors.New("reconciliation source evidence is invalid")
	}
	plaintext, err := gcm.Open(nil, batch.SourceNonce, batch.SourceCiphertext, []byte(batch.BatchKey))
	if err != nil || bill.SHA256(plaintext) != batch.SourceSHA256 {
		return nil, errors.New("reconciliation source evidence could not be verified")
	}
	return plaintext, nil
}

// PointReconciliationBillInput is an adapter output. Alipay's file currently
// enters only as UnsupportedFormat with raw bytes; callers cannot label opaque
// content as parsed or reconciled.
type PointReconciliationBillInput struct {
	Provider             string
	MerchantID           string
	AppID                string
	BillDate             string
	BillType             string
	Timezone             string
	FormatVersion        string
	SourceSHA256         string
	ProviderHashType     string
	ProviderHashValue    string
	ProviderHashVerified bool
	SourceBytes          []byte
	Rows                 []bill.Row
	UnsupportedFormat    bool
}

func PointReconciliationInputFromStatement(statement bill.Statement, billType string) PointReconciliationBillInput {
	return PointReconciliationBillInput{Provider: statement.Provider, MerchantID: statement.RequestedMerchantID, AppID: statement.RequestedAppID,
		BillDate: statement.BillDate, BillType: billType, Timezone: statement.Timezone, FormatVersion: statement.FormatVersion,
		SourceSHA256: statement.SourceSHA256, ProviderHashType: statement.ProviderHashType, ProviderHashValue: statement.ProviderHashValue,
		ProviderHashVerified: statement.ProviderHashVerified, SourceBytes: append([]byte(nil), statement.SourceBytes...), Rows: append([]bill.Row(nil), statement.Rows...)}
}

func PointReconciliationInputFromRaw(raw bill.RawBill, billType string) PointReconciliationBillInput {
	return PointReconciliationBillInput{Provider: raw.Provider, MerchantID: raw.RequestedMerchantID, AppID: raw.RequestedAppID,
		BillDate: raw.BillDate, BillType: billType, Timezone: raw.Timezone, FormatVersion: raw.FormatVersion,
		SourceSHA256: raw.SHA256, ProviderHashType: raw.ProviderHashType, ProviderHashValue: raw.ProviderHashValue,
		ProviderHashVerified: raw.ProviderHashOK, SourceBytes: append([]byte(nil), raw.Bytes...), UnsupportedFormat: true}
}

type reconciliationIdentity struct {
	Provider     string `json:"provider"`
	MerchantID   string `json:"merchant_id"`
	AppID        string `json:"app_id"`
	BillDate     string `json:"bill_date"`
	BillType     string `json:"bill_type"`
	Format       string `json:"format"`
	SourceSHA256 string `json:"source_sha256"`
}

func ImportPointReconciliationBill(input PointReconciliationBillInput, sourceKey ReconciliationSourceKey) (PointReconciliationBatch, bool, error) {
	if DB == nil || DB.Dialector.Name() != "sqlite" {
		return PointReconciliationBatch{}, false, errors.New("reconciliation import is available only on the verified SQLite backend")
	}
	if err := validatePointReconciliationInput(input, sourceKey); err != nil {
		return PointReconciliationBatch{}, false, err
	}
	identity := reconciliationIdentity{Provider: input.Provider, MerchantID: input.MerchantID, AppID: input.AppID, BillDate: input.BillDate,
		BillType: input.BillType, Format: input.FormatVersion, SourceSHA256: strings.ToLower(input.SourceSHA256)}
	canonical, _ := json.Marshal(identity)
	batchDigest := sha256.Sum256(canonical)
	batchKey := hex.EncodeToString(batchDigest[:])
	block, err := aes.NewCipher(sourceKey.Key)
	if err != nil {
		return PointReconciliationBatch{}, false, ErrReconciliationDisabled
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return PointReconciliationBatch{}, false, ErrReconciliationDisabled
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return PointReconciliationBatch{}, false, err
	}
	ciphertext := gcm.Seal(nil, nonce, input.SourceBytes, []byte(batchKey))
	status := "imported"
	if input.UnsupportedFormat {
		status = "unsupported_format"
	}
	var result PointReconciliationBatch
	created := false
	err = pointsTransaction(func(tx *gorm.DB) error {
		var prior PointReconciliationBatch
		lookup := tx.Select(reconciliationBatchMetadataColumns).Where("batch_key = ?", batchKey).First(&prior)
		if lookup.Error == nil {
			if !sameReconciliationBatch(prior, input, status) {
				return ErrReconciliationConflict
			}
			result = prior
			return nil
		}
		if lookup.Error != gorm.ErrRecordNotFound {
			return lookup.Error
		}
		var version int64
		if err := tx.Model(&PointReconciliationBatch{}).Where("provider = ? AND merchant_id = ? AND app_id = ? AND bill_date = ? AND bill_type = ? AND format_version = ?",
			input.Provider, input.MerchantID, input.AppID, input.BillDate, input.BillType, input.FormatVersion).Count(&version).Error; err != nil {
			return err
		}
		batch := PointReconciliationBatch{BatchKey: batchKey, Provider: input.Provider, MerchantID: input.MerchantID, AppID: input.AppID,
			BillDate: input.BillDate, BillType: input.BillType, FormatVersion: input.FormatVersion, ImportVersion: int(version) + 1,
			Timezone: input.Timezone, Status: status, SourceSHA256: strings.ToLower(input.SourceSHA256), ProviderHashType: input.ProviderHashType,
			ProviderHashValue: strings.ToLower(input.ProviderHashValue), ProviderHashVerified: input.ProviderHashVerified, SourceSize: int64(len(input.SourceBytes)),
			SourceKeyID: sourceKey.KeyID, SourceNonce: append([]byte(nil), nonce...), SourceCiphertext: append([]byte(nil), ciphertext...), RowCount: len(input.Rows)}
		if err := tx.Create(&batch).Error; err != nil {
			return err
		}
		presentPaymentKeys := make(map[string]bool)
		seenRows := make(map[string]matchFinding)
		for _, source := range input.Rows {
			finding, err := classifyReconciliationRow(tx, input, source, seenRows)
			if err != nil {
				return err
			}
			line := PointReconciliationRow{BatchID: batch.ID, SourceLine: source.SourceLine, SourceDigest: source.SourceDigest, Provider: source.Provider,
				Kind: string(source.Kind), ProviderStatus: source.Status, RefundStatus: source.RefundStatus, OccurredAt: source.OccurredAt.UTC(),
				MerchantID: source.MerchantID, AppID: source.AppID, OrderKey: source.OrderKey, TransactionID: source.TransactionID,
				MerchantRefundKey: source.MerchantRefundKey, ProviderRefundID: source.ProviderRefundID, Currency: source.Currency,
				GrossFen: source.GrossFen, SettlementFen: source.SettlementFen, DiscountFen: source.DiscountFen, RefundFen: source.RefundFen,
				CouponRefundFen: source.CouponRefundFen, RefundRequestedFen: source.RefundRequestedFen, FeeFen: source.FeeFen,
				NetFen: source.NetFen, HasNetFen: source.HasNetFen,
				ReconciliationResult: finding.Classification}
			if err := tx.Create(&line).Error; err != nil {
				return err
			}
			if finding.ProviderPaymentPresent {
				presentPaymentKeys[reconciliationPaymentKey(source)] = true
			}
			if finding.Classification != "matched" {
				if err := createReconciliationDifference(tx, batch.ID, &line.ID, finding, source.SourceDigest); err != nil {
					return err
				}
			}
			if finding.Classification != "duplicate" {
				seenRows[reconciliationRowIdentity(source)] = finding
			}
		}
		if !input.UnsupportedFormat {
			if err := addMissingProviderPaymentDifferences(tx, batch, input, presentPaymentKeys); err != nil {
				return err
			}
		}
		result = batch
		result.SourceNonce = nil
		result.SourceCiphertext = nil
		created = true
		return nil
	})
	if err != nil {
		var prior PointReconciliationBatch
		if lookupErr := DB.Select(reconciliationBatchMetadataColumns).Where("batch_key = ?", batchKey).First(&prior).Error; lookupErr == nil {
			if sameReconciliationBatch(prior, input, status) {
				return prior, false, nil
			}
			return PointReconciliationBatch{}, false, ErrReconciliationConflict
		}
		return PointReconciliationBatch{}, false, err
	}
	return result, created, nil
}

func validatePointReconciliationInput(input PointReconciliationBillInput, key ReconciliationSourceKey) error {
	if key.KeyID == "" || len(key.KeyID) > 80 || len(key.Key) != 32 {
		return ErrReconciliationDisabled
	}
	if input.Provider != "wechat" && input.Provider != "alipay" || input.MerchantID == "" || input.AppID == "" || input.Timezone == "" ||
		input.BillType == "" || input.FormatVersion == "" || len(input.SourceBytes) == 0 || int64(len(input.SourceBytes)) > bill.MaxDownloadedBillBytes {
		return errors.New("invalid reconciliation bill identity or source")
	}
	date, err := time.Parse("2006-01-02", input.BillDate)
	if err != nil || date.Format("2006-01-02") != input.BillDate {
		return errors.New("invalid reconciliation bill date")
	}
	location, err := time.LoadLocation(input.Timezone)
	if err != nil || (input.Provider == "wechat" && input.Timezone != "Asia/Shanghai") {
		return errors.New("invalid reconciliation bill timezone")
	}
	_ = location
	if len(input.SourceSHA256) != 64 || !validDigest(input.SourceSHA256) || !strings.EqualFold(input.SourceSHA256, bill.SHA256(input.SourceBytes)) {
		return errors.New("reconciliation source digest does not match raw bytes")
	}
	if input.Provider == "wechat" {
		if input.UnsupportedFormat || input.BillType != "ALL" || input.FormatVersion != "wechat-all-27-column-v1" || !input.ProviderHashVerified || input.ProviderHashType != "SHA1" || len(input.Rows) > 250000 {
			return ErrReconciliationUnsupported
		}
		if !validSHA1Digest(input.ProviderHashValue) || !strings.EqualFold(input.ProviderHashValue, sha1Hex(input.SourceBytes)) {
			return errors.New("WeChat provider hash does not match raw source")
		}
		billDate, _ := time.ParseInLocation("2006-01-02", input.BillDate, location)
		statement, err := bill.ParseWeChatTradeBill(input.SourceBytes, billDate, input.MerchantID, input.AppID)
		if err != nil || !sameBillRows(statement.Rows, input.Rows) {
			return errors.New("normalized WeChat rows do not match raw source")
		}
		return nil
	}
	if !input.UnsupportedFormat || input.BillType != "trade" || !strings.Contains(input.FormatVersion, "unparsed") || len(input.Rows) != 0 {
		return ErrReconciliationUnsupported
	}
	return nil
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func validSHA1Digest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha1.Size
}

func sha1Hex(data []byte) string {
	digest := sha1.Sum(data)
	return hex.EncodeToString(digest[:])
}

func sameBillRows(left, right []bill.Row) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		a, b := left[i], right[i]
		if a.Provider != b.Provider || a.Kind != b.Kind || a.Status != b.Status || a.RefundStatus != b.RefundStatus || !a.OccurredAt.Equal(b.OccurredAt) ||
			a.MerchantID != b.MerchantID || a.AppID != b.AppID || a.OrderKey != b.OrderKey || a.TransactionID != b.TransactionID || a.MerchantRefundKey != b.MerchantRefundKey ||
			a.ProviderRefundID != b.ProviderRefundID || a.Currency != b.Currency || a.GrossFen != b.GrossFen || a.SettlementFen != b.SettlementFen || a.DiscountFen != b.DiscountFen ||
			a.RefundFen != b.RefundFen || a.CouponRefundFen != b.CouponRefundFen || a.RefundRequestedFen != b.RefundRequestedFen || a.FeeFen != b.FeeFen || a.NetFen != b.NetFen ||
			a.HasNetFen != b.HasNetFen || a.SourceLine != b.SourceLine || !strings.EqualFold(a.SourceDigest, b.SourceDigest) {
			return false
		}
	}
	return true
}

func sameReconciliationBatch(batch PointReconciliationBatch, input PointReconciliationBillInput, status string) bool {
	return batch.Provider == input.Provider && batch.MerchantID == input.MerchantID && batch.AppID == input.AppID && batch.BillDate == input.BillDate &&
		batch.BillType == input.BillType && batch.FormatVersion == input.FormatVersion && batch.SourceSHA256 == strings.ToLower(input.SourceSHA256) &&
		batch.Timezone == input.Timezone && batch.Status == status && batch.RowCount == len(input.Rows) &&
		batch.ProviderHashType == input.ProviderHashType && batch.ProviderHashValue == strings.ToLower(input.ProviderHashValue) && batch.ProviderHashVerified == input.ProviderHashVerified
}

func reconciliationPaymentKey(row bill.Row) string {
	return strings.Join([]string{row.Provider, row.MerchantID, row.AppID, row.OrderKey, row.TransactionID}, "\x00")
}

func reconciliationRowIdentity(row bill.Row) string {
	if row.Kind == bill.RowPayment {
		return "payment\x00" + reconciliationPaymentKey(row)
	}
	return strings.Join([]string{string(row.Kind), row.Provider, row.MerchantID, row.AppID, row.OrderKey, row.TransactionID, row.MerchantRefundKey, row.ProviderRefundID}, "\x00")
}

type matchFinding struct {
	Classification         string
	Severity               string
	OrderID                *uint
	RefundID               *uint
	PaymentEventID         *uint
	ProviderPaymentPresent bool
}

func classifyReconciliationRow(tx *gorm.DB, input PointReconciliationBillInput, row bill.Row, seen map[string]matchFinding) (matchFinding, error) {
	if row.MerchantID != input.MerchantID || row.AppID != input.AppID {
		return matchFinding{Classification: "other_scope", Severity: "informational"}, nil
	}
	identity := reconciliationRowIdentity(row)
	if prior, exists := seen[identity]; exists {
		prior.Classification, prior.Severity = "duplicate", "attention"
		prior.OrderID, prior.RefundID, prior.PaymentEventID = nil, nil, nil
		return prior, nil
	}
	if row.Kind == bill.RowPayment {
		var order PointPurchaseOrder
		err := tx.Where("order_key = ?", row.OrderKey).First(&order).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			var transaction PaymentTransaction
			ownerErr := tx.Where("provider = ? AND merchant_id = ? AND app_id = ? AND provider_transaction_id = ?", row.Provider, row.MerchantID, row.AppID, row.TransactionID).First(&transaction).Error
			if ownerErr == nil && transaction.OrderKey != row.OrderKey {
				return matchFinding{Classification: "identity_mismatch", Severity: "attention"}, nil
			}
			if ownerErr != nil && !errors.Is(ownerErr, gorm.ErrRecordNotFound) {
				return matchFinding{}, ownerErr
			}
			return matchFinding{Classification: "missing_local", Severity: "attention"}, nil
		}
		if err != nil {
			return matchFinding{}, err
		}
		if order.Channel != row.Provider || order.ProviderMerchantID != row.MerchantID || order.ProviderAppID != row.AppID || order.ProviderTransactionID != row.TransactionID {
			return matchFinding{Classification: "identity_mismatch", Severity: "attention"}, nil
		}
		if order.Currency != "CNY" || order.AmountFen != row.GrossFen {
			return matchFinding{Classification: "amount_mismatch", Severity: "attention", OrderID: ptr(order.ID), ProviderPaymentPresent: true}, nil
		}
		if order.State != "credited" && order.State != "refunded" {
			return matchFinding{Classification: "state_mismatch", Severity: "attention", OrderID: ptr(order.ID), ProviderPaymentPresent: true}, nil
		}
		var owner PaymentTransaction
		if err := tx.Where("provider = ? AND merchant_id = ? AND app_id = ? AND provider_transaction_id = ? AND order_key = ?", row.Provider, row.MerchantID, row.AppID, row.TransactionID, row.OrderKey).First(&owner).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return matchFinding{Classification: "identity_mismatch", Severity: "attention", OrderID: ptr(order.ID)}, nil
			}
			return matchFinding{}, err
		}
		var event PaymentEvent
		if err := tx.Where("id = ? AND provider = ? AND order_key = ? AND verification = ? AND state = ?", owner.EventID, row.Provider, row.OrderKey, "verified", "processed").First(&event).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return matchFinding{Classification: "identity_mismatch", Severity: "attention", OrderID: ptr(order.ID)}, nil
			}
			return matchFinding{}, err
		}
		if order.PaidEventKey == nil || event.Provider+":"+event.ProviderEventID != *order.PaidEventKey {
			return matchFinding{Classification: "identity_mismatch", Severity: "attention", OrderID: ptr(order.ID)}, nil
		}
		return matchFinding{Classification: "matched", OrderID: ptr(order.ID), PaymentEventID: ptr(event.ID), ProviderPaymentPresent: true}, nil
	}
	if row.Kind == bill.RowRefund || row.Kind == bill.RowReversal {
		refund, mismatch, err := findLocalReconciliationRefund(tx, row)
		if mismatch {
			return matchFinding{Classification: "identity_mismatch", Severity: "attention"}, nil
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return matchFinding{Classification: "missing_local", Severity: "attention"}, nil
		}
		if err != nil {
			return matchFinding{}, err
		}
		if refund.Channel != row.Provider || refund.ProviderMerchantID != row.MerchantID || refund.ProviderAppID != row.AppID || refund.OrderKey != row.OrderKey || refund.ProviderTransactionID != row.TransactionID || refund.Currency != "CNY" {
			return matchFinding{Classification: "identity_mismatch", Severity: "attention"}, nil
		}
		if row.Currency != "CNY" || refund.AmountFen != row.RefundRequestedFen {
			return matchFinding{Classification: "amount_mismatch", Severity: "attention", RefundID: ptr(refund.ID)}, nil
		}
		if row.RefundStatus == "PROCESSING" {
			return matchFinding{Classification: "historical_processing", Severity: "informational", RefundID: ptr(refund.ID)}, nil
		}
		if !reconciliationRefundStateMatches(refund.State, row.RefundStatus) {
			return matchFinding{Classification: "state_mismatch", Severity: "attention", RefundID: ptr(refund.ID)}, nil
		}
		return matchFinding{Classification: "matched", RefundID: ptr(refund.ID)}, nil
	}
	return matchFinding{Classification: "state_mismatch", Severity: "attention"}, nil
}

func findLocalReconciliationRefund(tx *gorm.DB, row bill.Row) (PointRefund, bool, error) {
	var byMerchantKey, byProviderID PointRefund
	hasMerchantKey := row.MerchantRefundKey != "" && row.MerchantRefundKey != "0"
	hasProviderID := row.ProviderRefundID != "" && row.ProviderRefundID != "0"
	if hasMerchantKey {
		err := tx.Where("channel = ? AND provider_refund_key = ?", row.Provider, row.MerchantRefundKey).First(&byMerchantKey).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return PointRefund{}, false, err
		}
	}
	if hasProviderID {
		var owner PointRefundProviderOwner
		err := tx.Where("provider = ? AND merchant_id = ? AND provider_refund_id = ?", row.Provider, row.MerchantID, row.ProviderRefundID).First(&owner).Error
		if err == nil {
			err = tx.Where("refund_key = ?", owner.RefundKey).First(&byProviderID).Error
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return PointRefund{}, false, err
		}
	}
	if hasMerchantKey && hasProviderID {
		if byMerchantKey.ID == 0 && byProviderID.ID == 0 {
			return PointRefund{}, false, gorm.ErrRecordNotFound
		}
		if byMerchantKey.ID == 0 || byProviderID.ID == 0 || byMerchantKey.ID != byProviderID.ID {
			return PointRefund{}, true, nil
		}
		return byMerchantKey, false, nil
	}
	if hasMerchantKey {
		if byMerchantKey.ID == 0 {
			return PointRefund{}, false, gorm.ErrRecordNotFound
		}
		return byMerchantKey, false, nil
	}
	if hasProviderID {
		if byProviderID.ID == 0 {
			return PointRefund{}, false, gorm.ErrRecordNotFound
		}
		return byProviderID, false, nil
	}
	return PointRefund{}, false, gorm.ErrRecordNotFound
}

func reconciliationRefundStateMatches(local, provider string) bool {
	switch provider {
	case "SUCCESS":
		return local == "succeeded"
	case "FAIL":
		return local == "definite_failed"
	case "CHANGE":
		return local == "needs_manual_review"
	default:
		return false
	}
}

func createReconciliationDifference(tx *gorm.DB, batchID uint, rowID *uint, finding matchFinding, fingerprint string) error {
	material := fmt.Sprintf("%d\x00%d\x00%s\x00%s", batchID, derefUint(rowID), finding.Classification, fingerprint)
	digest := sha256.Sum256([]byte(material))
	return tx.Create(&PointReconciliationDifference{DifferenceKey: hex.EncodeToString(digest[:]), BatchID: batchID, RowID: rowID,
		LocalOrderID: finding.OrderID, LocalRefundID: finding.RefundID, PaymentEventID: finding.PaymentEventID,
		Classification: finding.Classification, Severity: finding.Severity, EvidenceFingerprint: fingerprint}).Error
}

func addMissingProviderPaymentDifferences(tx *gorm.DB, batch PointReconciliationBatch, input PointReconciliationBillInput, matched map[string]bool) error {
	location, err := time.LoadLocation(input.Timezone)
	if err != nil {
		return errors.New("invalid reconciliation timezone")
	}
	var orders []PointPurchaseOrder
	if err := tx.Where("channel = ? AND provider_merchant_id = ? AND provider_app_id = ? AND state IN ?", input.Provider, input.MerchantID, input.AppID, []string{"credited", "refunded"}).Order("id ASC").Find(&orders).Error; err != nil {
		return err
	}
	dateStart, _ := time.ParseInLocation("2006-01-02", input.BillDate, location)
	dateEnd := dateStart.AddDate(0, 0, 1)
	for _, order := range orders {
		var owner PaymentTransaction
		if err := tx.Where("provider = ? AND merchant_id = ? AND app_id = ? AND order_key = ? AND provider_transaction_id = ?", input.Provider, input.MerchantID, input.AppID, order.OrderKey, order.ProviderTransactionID).First(&owner).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return err
		}
		var event PaymentEvent
		if err := tx.Where("id = ? AND provider = ? AND order_key = ? AND verification = ? AND state = ?", owner.EventID, input.Provider, order.OrderKey, "verified", "processed").First(&event).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return err
		}
		if order.PaidEventKey == nil || event.Provider+":"+event.ProviderEventID != *order.PaidEventKey {
			continue
		}
		var payload struct {
			ProviderOccurredAt time.Time `json:"provider_occurred_at"`
			OrderKey           string    `json:"order_key"`
			TransactionID      string    `json:"transaction_id"`
			MerchantID         string    `json:"merchant_id"`
			AppID              string    `json:"app_id"`
			AmountFen          int64     `json:"amount_fen"`
			Currency           string    `json:"currency"`
			Status             string    `json:"status"`
		}
		if json.Unmarshal([]byte(event.Payload), &payload) != nil || payload.ProviderOccurredAt.IsZero() || payload.OrderKey != order.OrderKey || payload.TransactionID != order.ProviderTransactionID ||
			payload.MerchantID != order.ProviderMerchantID || payload.AppID != order.ProviderAppID || payload.AmountFen != order.AmountFen || payload.Currency != order.Currency || payload.Status != "SUCCESS" {
			continue
		}
		occurred := payload.ProviderOccurredAt.In(location)
		if occurred.Before(dateStart) || !occurred.Before(dateEnd) || matched[strings.Join([]string{input.Provider, input.MerchantID, input.AppID, order.OrderKey, order.ProviderTransactionID}, "\x00")] {
			continue
		}
		fingerprint := event.Digest
		finding := matchFinding{Classification: "missing_provider", Severity: "attention", OrderID: ptr(order.ID), PaymentEventID: ptr(event.ID)}
		if err := createReconciliationDifference(tx, batch.ID, nil, finding, fingerprint); err != nil {
			return err
		}
	}
	return nil
}

func ptr(value uint) *uint { return &value }

func derefUint(value *uint) uint {
	if value == nil {
		return 0
	}
	return *value
}

type PointReconciliationBatchFilter struct {
	BeforeID   uint
	Limit      int
	Provider   string
	BillDate   string
	Status     string
	MerchantID string
	AppID      string
}

type PointReconciliationBatchView struct {
	ID                   uint
	BatchKey             string
	Provider             string
	MerchantID           string
	AppID                string
	BillDate             string
	BillType             string
	FormatVersion        string
	ImportVersion        int
	Timezone             string
	Status               string
	SourceSHA256         string
	ProviderHashType     string
	ProviderHashValue    string
	ProviderHashVerified bool
	SourceSize           int64
	RowCount             int
	CreatedAt            time.Time
}

func GetPointReconciliationBatch(batchKey string) (PointReconciliationBatchView, error) {
	if batchKey == "" || len(batchKey) != 64 || !validDigest(batchKey) {
		return PointReconciliationBatchView{}, gorm.ErrRecordNotFound
	}
	var row PointReconciliationBatch
	err := DB.Model(&PointReconciliationBatch{}).Select(reconciliationBatchMetadataColumns).Where("batch_key = ?", batchKey).First(&row).Error
	if err != nil {
		return PointReconciliationBatchView{}, err
	}
	return PointReconciliationBatchView{ID: row.ID, BatchKey: row.BatchKey, Provider: row.Provider, MerchantID: row.MerchantID,
		AppID: row.AppID, BillDate: row.BillDate, BillType: row.BillType, FormatVersion: row.FormatVersion, ImportVersion: row.ImportVersion,
		Timezone: row.Timezone, Status: row.Status, SourceSHA256: row.SourceSHA256, ProviderHashType: row.ProviderHashType,
		ProviderHashValue: row.ProviderHashValue, ProviderHashVerified: row.ProviderHashVerified, SourceSize: row.SourceSize, RowCount: row.RowCount, CreatedAt: row.CreatedAt}, nil
}

func ListPointReconciliationBatches(filter PointReconciliationBatchFilter) ([]PointReconciliationBatchView, bool, error) {
	if filter.Limit < 1 || filter.Limit > 100 {
		filter.Limit = 30
	}
	if filter.Provider != "" && filter.Provider != "wechat" && filter.Provider != "alipay" {
		return nil, false, errors.New("invalid reconciliation provider filter")
	}
	if filter.Status != "" && filter.Status != "imported" && filter.Status != "unsupported_format" {
		return nil, false, errors.New("invalid reconciliation status filter")
	}
	query := DB.Model(&PointReconciliationBatch{}).Select(reconciliationBatchMetadataColumns)
	if filter.BeforeID > 0 {
		query = query.Where("id < ?", filter.BeforeID)
	}
	if filter.Provider != "" {
		query = query.Where("provider = ?", filter.Provider)
	}
	if filter.BillDate != "" {
		if _, err := time.Parse("2006-01-02", filter.BillDate); err != nil {
			return nil, false, errors.New("invalid reconciliation bill date filter")
		}
		query = query.Where("bill_date = ?", filter.BillDate)
	}
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	if filter.MerchantID != "" {
		query = query.Where("merchant_id = ?", filter.MerchantID)
	}
	if filter.AppID != "" {
		query = query.Where("app_id = ?", filter.AppID)
	}
	var rows []PointReconciliationBatch
	if err := query.Order("id DESC").Limit(filter.Limit + 1).Find(&rows).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(rows) > filter.Limit
	if hasMore {
		rows = rows[:filter.Limit]
	}
	views := make([]PointReconciliationBatchView, 0, len(rows))
	for _, row := range rows {
		views = append(views, PointReconciliationBatchView{ID: row.ID, BatchKey: row.BatchKey, Provider: row.Provider, MerchantID: row.MerchantID,
			AppID: row.AppID, BillDate: row.BillDate, BillType: row.BillType, FormatVersion: row.FormatVersion, ImportVersion: row.ImportVersion,
			Timezone: row.Timezone, Status: row.Status, SourceSHA256: row.SourceSHA256, ProviderHashType: row.ProviderHashType,
			ProviderHashValue: row.ProviderHashValue, ProviderHashVerified: row.ProviderHashVerified, SourceSize: row.SourceSize, RowCount: row.RowCount, CreatedAt: row.CreatedAt})
	}
	return views, hasMore, nil
}

func ListPointReconciliationRows(batchKey string, beforeID uint, limit int) ([]PointReconciliationRow, bool, error) {
	if batchKey == "" {
		return nil, false, errors.New("batch key is required")
	}
	if limit < 1 || limit > 100 {
		limit = 30
	}
	query := DB.Model(&PointReconciliationRow{}).Select("point_reconciliation_rows.id, point_reconciliation_rows.batch_id, point_reconciliation_rows.source_line, point_reconciliation_rows.source_digest, point_reconciliation_rows.provider, point_reconciliation_rows.kind, point_reconciliation_rows.provider_status, point_reconciliation_rows.refund_status, point_reconciliation_rows.occurred_at, point_reconciliation_rows.merchant_id, point_reconciliation_rows.app_id, point_reconciliation_rows.order_key, point_reconciliation_rows.transaction_id, point_reconciliation_rows.merchant_refund_key, point_reconciliation_rows.provider_refund_id, point_reconciliation_rows.currency, point_reconciliation_rows.gross_fen, point_reconciliation_rows.settlement_fen, point_reconciliation_rows.discount_fen, point_reconciliation_rows.refund_fen, point_reconciliation_rows.coupon_refund_fen, point_reconciliation_rows.refund_requested_fen, point_reconciliation_rows.fee_fen, point_reconciliation_rows.net_fen, point_reconciliation_rows.has_net_fen, point_reconciliation_rows.reconciliation_result, point_reconciliation_rows.created_at").Joins("JOIN point_reconciliation_batches b ON b.id = point_reconciliation_rows.batch_id").Where("b.batch_key = ?", batchKey)
	if beforeID > 0 {
		query = query.Where("point_reconciliation_rows.id < ?", beforeID)
	}
	var rows []PointReconciliationRow
	if err := query.Order("point_reconciliation_rows.id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	return rows, hasMore, nil
}

type PointReconciliationDifferenceFilter struct {
	BatchID        uint
	BeforeID       uint
	Limit          int
	Classification string
	Severity       string
}

func ListPointReconciliationDifferences(filter PointReconciliationDifferenceFilter) ([]PointReconciliationDifference, bool, error) {
	if filter.BatchID == 0 {
		return nil, false, errors.New("batch ID is required for a bounded difference query")
	}
	if filter.Limit < 1 || filter.Limit > 100 {
		filter.Limit = 30
	}
	if filter.Classification != "" && !validReconciliationClassification(filter.Classification) {
		return nil, false, errors.New("invalid reconciliation classification filter")
	}
	if filter.Severity != "" && filter.Severity != "attention" && filter.Severity != "informational" {
		return nil, false, errors.New("invalid reconciliation severity filter")
	}
	query := DB.Where("batch_id = ?", filter.BatchID)
	if filter.BeforeID > 0 {
		query = query.Where("id < ?", filter.BeforeID)
	}
	if filter.Classification != "" {
		query = query.Where("classification = ?", filter.Classification)
	}
	if filter.Severity != "" {
		query = query.Where("severity = ?", filter.Severity)
	}
	var rows []PointReconciliationDifference
	if err := query.Order("id DESC").Limit(filter.Limit + 1).Find(&rows).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(rows) > filter.Limit
	if hasMore {
		rows = rows[:filter.Limit]
	}
	return rows, hasMore, nil
}

type RecordPointReconciliationActionRequest struct {
	ActionKey    string
	DifferenceID uint
	ActorUserID  int
	Action       string
	Reason       string
	BusinessRef  string
}

func RecordPointReconciliationAction(request RecordPointReconciliationActionRequest) (PointReconciliationAction, error) {
	request.ActionKey, request.Reason, request.BusinessRef = strings.TrimSpace(request.ActionKey), strings.TrimSpace(request.Reason), strings.TrimSpace(request.BusinessRef)
	if request.ActionKey == "" || len(request.ActionKey) > 180 || request.DifferenceID == 0 || request.ActorUserID <= 0 || len(request.Reason) == 0 || len(request.Reason) > 512 || len(request.BusinessRef) > 180 {
		return PointReconciliationAction{}, errors.New("invalid reconciliation action")
	}
	switch request.Action {
	case "note", "request_query", "record_reference":
	default:
		return PointReconciliationAction{}, errors.New("unsupported reconciliation action")
	}
	if request.Action != "note" && request.BusinessRef == "" {
		return PointReconciliationAction{}, errors.New("reconciliation action requires a business reference")
	}
	var result PointReconciliationAction
	err := pointsTransaction(func(tx *gorm.DB) error {
		var finding PointReconciliationDifference
		if err := tx.Where("id = ?", request.DifferenceID).First(&finding).Error; err != nil {
			return err
		}
		var prior PointReconciliationAction
		err := tx.Where("action_key = ?", request.ActionKey).First(&prior).Error
		if err == nil {
			if prior.DifferenceID != request.DifferenceID || prior.ActorUserID != request.ActorUserID || prior.Action != request.Action || prior.Reason != request.Reason || prior.BusinessRef != request.BusinessRef {
				return ErrReconciliationConflict
			}
			result = prior
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		result = PointReconciliationAction{ActionKey: request.ActionKey, DifferenceID: request.DifferenceID, ActorUserID: request.ActorUserID,
			Action: request.Action, Reason: request.Reason, BusinessRef: request.BusinessRef}
		return tx.Create(&result).Error
	})
	return result, err
}

func ListPointReconciliationActions(differenceID uint, beforeID uint, limit int) ([]PointReconciliationAction, bool, error) {
	if differenceID == 0 {
		return nil, false, errors.New("difference ID is required")
	}
	if limit < 1 || limit > 100 {
		limit = 30
	}
	query := DB.Where("difference_id = ?", differenceID)
	if beforeID > 0 {
		query = query.Where("id < ?", beforeID)
	}
	var rows []PointReconciliationAction
	if err := query.Order("id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	return rows, hasMore, nil
}

func validReconciliationClassification(value string) bool {
	switch value {
	case "other_scope", "missing_local", "missing_provider", "duplicate", "identity_mismatch", "amount_mismatch", "state_mismatch", "historical_processing":
		return true
	default:
		return false
	}
}
