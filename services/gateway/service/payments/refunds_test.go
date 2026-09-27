package payments

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/payment"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type refundTestProvider struct {
	mu           sync.Mutex
	applyCalls   int
	queryCalls   int
	applyErr     error
	queryErr     error
	applyResult  payment.RefundOutcome
	queryResult  payment.RefundOutcome
	queryStatus  string
	applyStarted chan struct{}
	applyRelease <-chan struct{}
}

func (p *refundTestProvider) Create(context.Context, payment.Order) (payment.Checkout, error) {
	return payment.Checkout{}, errors.New("unused payment create")
}
func (p *refundTestProvider) VerifyNotification(http.Header, []byte) (payment.VerifiedNotification, error) {
	return payment.VerifiedNotification{}, errors.New("unused payment notice")
}
func (p *refundTestProvider) Query(context.Context, string) (payment.Trade, error) {
	return payment.Trade{}, errors.New("unused payment query")
}
func (p *refundTestProvider) Close(context.Context, string) error {
	return errors.New("unused payment close")
}
func (p *refundTestProvider) ApplyRefund(_ context.Context, req payment.RefundRequest) (payment.RefundResult, error) {
	p.mu.Lock()
	p.applyCalls++
	applyErr, outcome := p.applyErr, p.applyResult
	started, release := p.applyStarted, p.applyRelease
	p.mu.Unlock()
	if started != nil {
		close(started)
	}
	if release != nil {
		select {
		case <-release:
		case <-time.After(5 * time.Second):
			return payment.RefundResult{}, context.DeadlineExceeded
		}
	}
	if applyErr != nil {
		return payment.RefundResult{}, applyErr
	}
	return refundTestResult(req, outcome, "APPLY_STATUS", "apply-event"), nil
}
func (p *refundTestProvider) QueryRefund(_ context.Context, req payment.RefundRequest) (payment.RefundResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queryCalls++
	if p.queryErr != nil {
		return payment.RefundResult{}, p.queryErr
	}
	return refundTestResult(req, p.queryResult, p.queryStatus, "query-event"), nil
}
func (p *refundTestProvider) VerifyRefundNotification(_ http.Header, body []byte) (payment.RefundResult, error) {
	if !strings.HasPrefix(string(body), "verified synthetic notice:") {
		return payment.RefundResult{}, payment.ErrInvalidNotice
	}
	return payment.RefundResult{Provider: "wechat", Outcome: payment.RefundSucceeded, Status: "SUCCESS", ProviderRefundID: "wx-refund-key-1", ProviderRefundKey: "refund-no-1", OrderKey: "refund-service-order", TransactionID: "wx-trade-service", MerchantID: "merchant-1", AppID: "app-1", AmountFen: 100, TotalFen: 1000, Currency: "CNY", ProviderEventID: strings.TrimPrefix(string(body), "verified synthetic notice:"), EvidenceSource: "notification", ProviderOccurredAt: time.Now().UTC()}, nil
}

func refundTestResult(req payment.RefundRequest, outcome payment.RefundOutcome, status, eventID string) payment.RefundResult {
	refundID := ""
	if req.MerchantID == "merchant-1" {
		refundID = "wx-refund-key-1"
	}
	return payment.RefundResult{Provider: "wechat", Outcome: outcome, Status: status, RefundKey: req.RefundKey,
		ProviderRefundKey: req.ProviderRefundKey, ProviderRefundID: refundID, OrderKey: req.OrderKey,
		TransactionID: req.TransactionID, MerchantID: req.MerchantID, AppID: req.AppID,
		AmountFen: req.AmountFen, TotalFen: req.TotalFen, Currency: req.Currency,
		ProviderEventID: eventID, EvidenceSource: "query", ProviderOccurredAt: time.Now().UTC()}
}

func setupRefundOrchestration(t *testing.T) (*gorm.DB, string, func()) {
	t.Helper()
	oldDB, oldSQLite := model.DB, common.UsingSQLite
	path := filepath.Join(t.TempDir(), "refund-orchestration.sqlite")
	dsn := path + "?_busy_timeout=10000&_txlock=immediate&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(8)
	model.DB, common.UsingSQLite = db, true
	if err := model.MigratePointsSchema(); err != nil {
		t.Fatal(err)
	}
	account := model.PointAccount{UserID: 41, AvailableMicro: 1_000_000_000}
	order := model.PointPurchaseOrder{OrderKey: "refund-service-order", UserID: 41, Channel: "wechat", ProviderMerchantID: "merchant-1", ProviderAppID: "app-1", ProviderTransactionID: "wx-trade-service", Currency: "CNY", AmountFen: 1000, PurchaseMicro: 1_000_000_000, State: "credited"}
	lot := model.PointLot{UserID: 41, BusinessKey: "purchase:" + order.OrderKey, Kind: "purchase", SourceRef: order.OrderKey, AmountFen: 1000, InitialMicro: 1_000_000_000, AvailableMicro: 1_000_000_000}
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&lot).Error; err != nil {
		t.Fatal(err)
	}
	refund, err := model.RequestPointRefund(model.PointRefundRequest{UserID: 41, OrderKey: order.OrderKey, IdempotencyKey: "refund-service-idem", RefundKey: "refund-service-key", ProviderRefundKey: "refund-no-1", AmountFen: 100, Reason: "synthetic refund request"})
	if err != nil {
		t.Fatal(err)
	}
	if err := model.ApprovePointRefund(refund.RefundKey, "refund-service-approve", 99, "synthetic approval"); err != nil {
		t.Fatal(err)
	}
	return db, dsn, func() {
		_ = sqlDB.Close()
		model.DB, common.UsingSQLite = oldDB, oldSQLite
	}
}

func TestRefundDispatchTimeoutRecoveryQueriesAndPersistsOnlyNormalizedEvidence(t *testing.T) {
	db, dsn, cleanup := setupRefundOrchestration(t)
	defer cleanup()
	provider := &refundTestProvider{applyErr: context.DeadlineExceeded, queryResult: payment.RefundSucceeded, queryStatus: "SUCCESS"}
	restore := ReplaceProvidersForTest(map[string]RuntimeProvider{"wechat": {Identity: MerchantIdentity{Provider: "wechat", MerchantID: "merchant-1", AppID: "app-1"}, Provider: provider}})
	defer restore()

	first, err := DispatchPointRefundOperation(context.Background(), "refund-service-key")
	if err != nil || first.Operation != "apply" || first.Outcome != payment.RefundUnknown {
		t.Fatalf("initial timeout should remain unknown and frozen: %+v %v", first, err)
	}
	var refund model.PointRefund
	if err := db.First(&refund, "refund_key = ?", "refund-service-key").Error; err != nil {
		t.Fatal(err)
	}
	if refund.State != "unknown" || refund.ActiveOrderKey == nil || refund.PriorRefundedFen != 0 {
		t.Fatalf("timeout released or changed the frozen refund: %+v", refund)
	}
	var inboxRows []model.PointRefundInbox
	if err := db.Find(&inboxRows).Error; err != nil {
		t.Fatal(err)
	}
	if len(inboxRows) != 1 || inboxRows[0].State != "processed" || inboxRows[0].Digest == "" {
		t.Fatalf("normalized timeout evidence was not durable: %+v", inboxRows)
	}

	// Reopen the same file to model process restart. The state already records
	// an uncertain apply; dispatch must query the same frozen refund number.
	sqlDB, _ := db.DB()
	_ = sqlDB.Close()
	restarted, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	restartedSQL, _ := restarted.DB()
	restartedSQL.SetMaxOpenConns(8)
	model.DB = restarted
	defer restartedSQL.Close()
	provider.mu.Lock()
	provider.applyErr = nil
	provider.queryResult = payment.RefundSucceeded
	provider.mu.Unlock()
	second, err := DispatchPointRefundOperation(context.Background(), "refund-service-key")
	if err != nil || second.Operation != "query" || second.Outcome != payment.RefundSucceeded {
		t.Fatalf("recovery did not query and accept verified success: %+v %v", second, err)
	}
	provider.mu.Lock()
	applyCalls, queryCalls := provider.applyCalls, provider.queryCalls
	provider.mu.Unlock()
	if applyCalls != 1 || queryCalls != 1 {
		t.Fatalf("recovery repeated external apply: apply=%d query=%d", applyCalls, queryCalls)
	}
	var account model.PointAccount
	var successLedgerCount int64
	if err := restarted.First(&account, "user_id = ?", 41).Error; err != nil {
		t.Fatal(err)
	}
	restarted.Model(&model.PointLedger{}).Where("business_key = ?", "refund-success:refund-service-key").Count(&successLedgerCount)
	if account.AvailableMicro != 900_000_000 || account.HeldMicro != 0 || successLedgerCount != 1 {
		t.Fatalf("recovered success was not applied once: account=%+v ledgers=%d", account, successLedgerCount)
	}
}

func TestWeChatVerifiedRefundInboxReplaysOnceAndKeepsTerminalState(t *testing.T) {
	db, _, cleanup := setupRefundOrchestration(t)
	defer cleanup()
	provider := &refundTestProvider{}
	restore := ReplaceProvidersForTest(map[string]RuntimeProvider{"wechat": {Identity: MerchantIdentity{Provider: "wechat", MerchantID: "merchant-1", AppID: "app-1"}, Provider: provider}})
	defer restore()
	body := []byte("verified synthetic notice:1")
	first, duplicate, err := VerifyAndPersistWeChatRefundNotification(http.Header{}, body)
	if err != nil || duplicate || first.State != "received" {
		t.Fatalf("verified callback did not enter durable inbox: %+v dup=%v err=%v", first, duplicate, err)
	}
	if first, err = ProcessPointRefundInbox(first.ID); err != nil || first.State != "processed" {
		t.Fatalf("callback inbox processing failed: %+v %v", first, err)
	}
	if _, err := ProcessPointRefundInbox(first.ID); err != nil {
		t.Fatalf("processed callback replay should be idempotent: %v", err)
	}
	second, duplicate, err := VerifyAndPersistWeChatRefundNotification(http.Header{}, []byte("verified synthetic notice:2"))
	if err != nil || duplicate || second.ID == first.ID {
		t.Fatalf("distinct provider event should be separately durable evidence: %+v duplicate=%v err=%v", second, duplicate, err)
	}
	if _, err := ProcessPointRefundInbox(second.ID); err != nil {
		t.Fatalf("second signed success event should replay as evidence only: %v", err)
	}
	var refund model.PointRefund
	if err := db.First(&refund, "refund_key = ?", "refund-service-key").Error; err != nil {
		t.Fatal(err)
	}
	var account model.PointAccount
	var ledgerCount int64
	if err := db.First(&account, "user_id = ?", 41).Error; err != nil {
		t.Fatal(err)
	}
	db.Model(&model.PointLedger{}).Where("business_key = ?", "refund-success:refund-service-key").Count(&ledgerCount)
	var evidenceCount, inboxCount int64
	db.Model(&model.PointRefundEvidence{}).Where("refund_key = ?", refund.RefundKey).Count(&evidenceCount)
	db.Model(&model.PointRefundInbox{}).Where("refund_key = ? AND state = ?", refund.RefundKey, "processed").Count(&inboxCount)
	if refund.State != "succeeded" || account.AvailableMicro != 900_000_000 || account.HeldMicro != 0 || ledgerCount != 1 || evidenceCount != 2 || inboxCount != 2 {
		t.Fatalf("callback was not applied exactly once: refund=%+v account=%+v ledgers=%d", refund, account, ledgerCount)
	}
}

func TestRefundInboxRecoveryCursorContinuesPastFailedRowsAndReturnsReadErrors(t *testing.T) {
	db, _, cleanup := setupRefundOrchestration(t)
	defer cleanup()
	provider := &refundTestProvider{}
	restore := ReplaceProvidersForTest(map[string]RuntimeProvider{"wechat": {Identity: MerchantIdentity{Provider: "wechat", MerchantID: "merchant-1", AppID: "app-1"}, Provider: provider}})
	defer restore()
	bad := model.PointRefundInbox{EvidenceKey: "bad-refund-inbox", RefundKey: "refund-service-key", Provider: "wechat", EvidenceSource: "notification", ProviderRefundKey: "wrong-provider-key", OrderKey: "refund-service-order", ProviderTransactionID: "wx-trade-service", MerchantID: "merchant-1", AppID: "app-1", Outcome: "succeeded", ProviderRefundID: "wx-refund-key-1", AmountFen: 100, TotalFen: 1000, Currency: "CNY", Digest: "bad-digest", State: "received"}
	if err := db.Create(&bad).Error; err != nil {
		t.Fatal(err)
	}
	valid := payment.RefundResult{Provider: "wechat", Outcome: payment.RefundSucceeded, Status: "SUCCESS", RefundKey: "refund-service-key", ProviderRefundID: "wx-refund-key-1", ProviderRefundKey: "refund-no-1", OrderKey: "refund-service-order", TransactionID: "wx-trade-service", MerchantID: "merchant-1", AppID: "app-1", AmountFen: 100, TotalFen: 1000, Currency: "CNY", ProviderEventID: "cursor-valid-result", ProviderOccurredAt: time.Now().UTC()}
	good, _, err := PersistRefundProviderResult(valid, "notification", []byte("normalized-valid"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := ReplayReceivedPointRefundInbox(0, 1)
	if err != nil || first.Scanned != 1 || first.Failed != 1 || !first.HasMore || first.NextID != bad.ID {
		t.Fatalf("first bounded recovery page did not report failure and cursor: %+v %v", first, err)
	}
	second, err := ReplayReceivedPointRefundInbox(first.NextID, 1)
	if err != nil || second.Scanned != 1 || second.Processed != 1 || second.NextID != good.ID {
		t.Fatalf("recovery cursor failed to reach a later valid inbox row: %+v %v", second, err)
	}
	if _, err := ProcessPointRefundInbox(bad.ID); err == nil {
		t.Fatal("mismatched evidence unexpectedly became processable")
	}
	if err := db.Exec("DROP TABLE point_refund_inboxes").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ReplayReceivedPointRefundInbox(0, 10); err == nil {
		t.Fatal("unreadable recovery inbox was reported as a successful empty scan")
	}
}

func TestVerifiedSuccessNotificationCanArriveBeforeApplyResponse(t *testing.T) {
	db, _, cleanup := setupRefundOrchestration(t)
	defer cleanup()
	started, release := make(chan struct{}), make(chan struct{})
	provider := &refundTestProvider{applyResult: payment.RefundAccepted, applyStarted: started, applyRelease: release}
	restore := ReplaceProvidersForTest(map[string]RuntimeProvider{"wechat": {Identity: MerchantIdentity{Provider: "wechat", MerchantID: "merchant-1", AppID: "app-1"}, Provider: provider}})
	defer restore()
	dispatch := make(chan error, 1)
	go func() {
		_, err := DispatchPointRefundOperation(context.Background(), "refund-service-key")
		dispatch <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("apply call did not reach the synthetic provider")
	}
	inbox, duplicate, err := VerifyAndPersistWeChatRefundNotification(http.Header{}, []byte("verified synthetic notice:before-apply-response"))
	if err != nil || duplicate {
		close(release)
		t.Fatalf("early verified callback was not persisted: %+v duplicate=%v err=%v", inbox, duplicate, err)
	}
	if _, err := ProcessPointRefundInbox(inbox.ID); err != nil {
		close(release)
		t.Fatalf("early callback did not finalize refund: %v", err)
	}
	close(release)
	select {
	case err := <-dispatch:
		if err != nil {
			t.Fatalf("late apply response failed after callback success: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("apply dispatch did not finish")
	}
	var refund model.PointRefund
	var account model.PointAccount
	var ledgerCount int64
	if err := db.First(&refund, "refund_key = ?", "refund-service-key").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&account, "user_id = ?", 41).Error; err != nil {
		t.Fatal(err)
	}
	db.Model(&model.PointLedger{}).Where("business_key = ?", "refund-success:refund-service-key").Count(&ledgerCount)
	if refund.State != "succeeded" || refund.OperationToken != "" || account.AvailableMicro != 900_000_000 || account.HeldMicro != 0 || ledgerCount != 1 {
		t.Fatalf("callback/apply race changed or double-applied refund: refund=%+v account=%+v ledgers=%d", refund, account, ledgerCount)
	}
}

func TestQueryTimeoutPreservesManualReviewAndRefundHold(t *testing.T) {
	db, _, cleanup := setupRefundOrchestration(t)
	defer cleanup()
	var refund model.PointRefund
	if err := db.First(&refund, "refund_key = ?", "refund-service-key").Error; err != nil {
		t.Fatal(err)
	}
	if err := model.RecordPointRefundEvidence(model.PointRefundEvidenceInput{EvidenceKey: "manual-before-query", RefundKey: refund.RefundKey, Provider: "wechat", Outcome: "abnormal", ProviderRefundID: "wx-refund-key-1", ProviderRefundKey: refund.ProviderRefundKey, OrderKey: refund.OrderKey, ProviderTransactionID: refund.ProviderTransactionID, MerchantID: refund.ProviderMerchantID, AppID: refund.ProviderAppID, AmountFen: refund.AmountFen, TotalFen: refund.OriginalAmountFen, Currency: refund.Currency, Digest: "manual-before-query-digest"}); err != nil {
		t.Fatal(err)
	}
	provider := &refundTestProvider{queryErr: context.DeadlineExceeded}
	restore := ReplaceProvidersForTest(map[string]RuntimeProvider{"wechat": {Identity: MerchantIdentity{Provider: "wechat", MerchantID: "merchant-1", AppID: "app-1"}, Provider: provider}})
	defer restore()
	result, err := DispatchPointRefundOperation(context.Background(), refund.RefundKey)
	if err != nil || result.Operation != "query" || result.Outcome != payment.RefundUnknown {
		t.Fatalf("manual review timeout should remain query-only unknown: %+v %v", result, err)
	}
	var current model.PointRefund
	var account model.PointAccount
	if err := db.First(&current, "refund_key = ?", refund.RefundKey).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&account, "user_id = ?", refund.UserID).Error; err != nil {
		t.Fatal(err)
	}
	if current.State != "needs_manual_review" || current.OperationToken != "" || current.ActiveOrderKey == nil || account.AvailableMicro != 900_000_000 || account.HeldMicro != 100_000_000 {
		t.Fatalf("unknown recovery downgraded manual state or released hold: refund=%+v account=%+v", current, account)
	}
}
