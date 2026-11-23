package payments

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/payment/alipay"
	"github.com/songquanpeng/one-api/payment/bill"
	"github.com/songquanpeng/one-api/payment/wechat"
)

var ErrReconciliationImportBusy = errors.New("reconciliation import is busy")
var ErrReconciliationProviderUnavailable = errors.New("reconciliation provider is unavailable")
var ErrReconciliationSourceKeyUnavailable = errors.New("reconciliation source encryption is unavailable")

var reconciliationImportLock sync.Mutex
var reconciliationFetcherMu sync.RWMutex
var reconciliationFetcherOverride func(context.Context, string, string) (model.PointReconciliationBillInput, MerchantIdentity, error)

// ReplaceReconciliationFetcherForTest provides a synthetic, server-side
// adapter for HTTP tests. Production requests always use ProviderFor below.
func ReplaceReconciliationFetcherForTest(fetcher func(context.Context, string, string) (model.PointReconciliationBillInput, MerchantIdentity, error)) func() {
	reconciliationFetcherMu.Lock()
	previous := reconciliationFetcherOverride
	reconciliationFetcherOverride = fetcher
	reconciliationFetcherMu.Unlock()
	return func() {
		reconciliationFetcherMu.Lock()
		reconciliationFetcherOverride = previous
		reconciliationFetcherMu.Unlock()
	}
}

type ReconciliationProviderStatus struct {
	Provider   string `json:"provider"`
	Configured bool   `json:"configured"`
	Supported  bool   `json:"supported"`
}

func ReconciliationImportStatuses() []ReconciliationProviderStatus {
	statuses := make([]ReconciliationProviderStatus, 0, 2)
	for _, name := range []string{"wechat", "alipay"} {
		runtime, ok := ProviderFor(name)
		configured := ok && runtime.Provider != nil && runtime.Identity.MerchantID != "" && runtime.Identity.AppID != ""
		statuses = append(statuses, ReconciliationProviderStatus{Provider: name, Configured: configured, Supported: name == "wechat"})
	}
	return statuses
}

func ReconciliationSourceEncryptionReady() bool {
	_, err := ReconciliationSourceKey()
	return err == nil
}

func ReconciliationSourceKey() (model.ReconciliationSourceKey, error) {
	keyID := strings.TrimSpace(config.PointReconciliationSourceKeyID)
	encoded := strings.TrimSpace(config.PointReconciliationSourceKeyBase64)
	if keyID == "" || len(keyID) > 80 || encoded == "" {
		return model.ReconciliationSourceKey{}, ErrReconciliationSourceKeyUnavailable
	}
	key, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return model.ReconciliationSourceKey{}, ErrReconciliationSourceKeyUnavailable
	}
	return model.ReconciliationSourceKey{KeyID: keyID, Key: key}, nil
}

// ImportConfiguredReconciliationBill accepts only a provider and a completed
// bill date. Provider identities and URLs come exclusively from server config.
func ImportConfiguredReconciliationBill(ctx context.Context, providerName, day string, audit model.RecordPointReconciliationImportAuditRequest) (model.PointReconciliationBatch, bool, error) {
	if !reconciliationImportLock.TryLock() {
		return model.PointReconciliationBatch{}, false, ErrReconciliationImportBusy
	}
	defer reconciliationImportLock.Unlock()
	key, err := ReconciliationSourceKey()
	if err != nil {
		return model.PointReconciliationBatch{}, false, ErrReconciliationSourceKeyUnavailable
	}
	date, err := time.ParseInLocation("2006-01-02", day, mustShanghai())
	if err != nil || date.Format("2006-01-02") != day {
		return model.PointReconciliationBatch{}, false, errors.New("invalid reconciliation bill date")
	}
	reconciliationFetcherMu.RLock()
	fetchOverride := reconciliationFetcherOverride
	reconciliationFetcherMu.RUnlock()
	if fetchOverride != nil {
		input, identity, err := fetchOverride(ctx, providerName, day)
		if err != nil {
			return model.PointReconciliationBatch{}, false, err
		}
		if identity.Provider != providerName || identity.MerchantID == "" || identity.AppID == "" || input.Provider != providerName || input.MerchantID != identity.MerchantID || input.AppID != identity.AppID || input.BillDate != day {
			return model.PointReconciliationBatch{}, false, errors.New("provider bill identity does not match configured merchant")
		}
		return model.ImportPointReconciliationBillWithAudit(ctx, input, key, audit)
	}
	runtime, ok := ProviderFor(providerName)
	if !ok || runtime.Provider == nil || runtime.Identity.Provider != providerName || runtime.Identity.MerchantID == "" || runtime.Identity.AppID == "" {
		return model.PointReconciliationBatch{}, false, ErrReconciliationProviderUnavailable
	}
	if providerName == "wechat" {
		provider, ok := runtime.Provider.(*wechat.Provider)
		if !ok {
			return model.PointReconciliationBatch{}, false, ErrReconciliationProviderUnavailable
		}
		statement, err := provider.DownloadTradeBill(ctx, date)
		if err != nil {
			return model.PointReconciliationBatch{}, false, err
		}
		if statement.RequestedMerchantID != runtime.Identity.MerchantID || statement.RequestedAppID != runtime.Identity.AppID || statement.BillDate != day {
			return model.PointReconciliationBatch{}, false, errors.New("provider bill identity does not match configured merchant")
		}
		input := model.PointReconciliationInputFromStatement(statement, "ALL")
		return model.ImportPointReconciliationBillWithAudit(ctx, input, key, audit)
	}
	if providerName == "alipay" {
		provider, ok := runtime.Provider.(*alipay.Provider)
		if !ok {
			return model.PointReconciliationBatch{}, false, ErrReconciliationProviderUnavailable
		}
		raw, err := provider.DownloadTradeBill(ctx, date)
		if err != nil {
			return model.PointReconciliationBatch{}, false, err
		}
		if raw.RequestedMerchantID != runtime.Identity.MerchantID || raw.RequestedAppID != runtime.Identity.AppID || raw.BillDate != day {
			return model.PointReconciliationBatch{}, false, errors.New("provider bill identity does not match configured merchant")
		}
		input := model.PointReconciliationInputFromRaw(raw, "trade")
		return model.ImportPointReconciliationBillWithAudit(ctx, input, key, audit)
	}
	return model.PointReconciliationBatch{}, false, ErrReconciliationProviderUnavailable
}

func mustShanghai() *time.Location {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	return location
}

// Keep the adapter output contract checked at compile time.
var _ interface {
	DownloadTradeBill(context.Context, time.Time) (bill.Statement, error)
} = (*wechat.Provider)(nil)
var _ interface {
	DownloadTradeBill(context.Context, time.Time) (bill.RawBill, error)
} = (*alipay.Provider)(nil)
