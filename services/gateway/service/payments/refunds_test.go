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
	mu                sync.Mutex
	channel           string
	applyCalls        int
	queryCalls        int
	applyErr          error
	queryErr          error
	applyResult       payment.RefundOutcome
	queryResult       payment.RefundOutcome
	queryStatus       string
	applyStarted      chan struct{}
	applyRelease      <-chan struct{}
	queryStarted      chan struct{}
	queryMutator      func(payment.RefundResult) payment.RefundResult
	queryRetrySameKey bool
	applyRequests     []payment.RefundRequest
	queryRequests     []payment.RefundRequest
	applyHook         func()
	applyMutator      func(payment.RefundResult) payment.RefundResult
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
func (p *refundTestProvider) ApplyRefund(ctx context.Context, req payment.RefundRequest) (payment.RefundResult, error) {
	p.mu.Lock()
	p.applyCalls++
	p.applyRequests = append(p.applyRequests, req)
	applyErr, outcome := p.applyErr, p.applyResult
	started, release := p.applyStarted, p.applyRelease
	hook := p.applyHook
	p.mu.Unlock()
	if hook != nil {
		hook()
	}
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return payment.RefundResult{}, ctx.Err()
		case <-time.After(5 * time.Second):
			return payment.RefundResult{}, context.DeadlineExceeded
		}
	}
	if applyErr != nil {
		return payment.RefundResult{}, applyErr
	}
	result := refundTestResultFor(req, outcome, "APPLY_STATUS", "apply-event", p.testChannel())
	if p.applyMutator != nil {
		result = p.applyMutator(result)
	}
	return result, nil
}
func (p *refundTestProvider) QueryRefund(_ context.Context, req payment.RefundRequest) (payment.RefundResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queryCalls++
	p.queryRequests = append(p.queryRequests, req)
	if p.queryStarted != nil {
		select {
		case p.queryStarted <- struct{}{}:
		default:
		}
	}
	if p.queryErr != nil {
		return payment.RefundResult{}, p.queryErr
	}
	result := refundTestResultFor(req, p.queryResult, p.queryStatus, "query-event", p.testChannel())
	result.RetrySameKey = p.queryRetrySameKey
	if p.queryMutator != nil {
		result = p.queryMutator(result)
	}
	return result, nil
}

func (p *refundTestProvider) testChannel() string {
	if p.channel == "" {
		return "wechat"
	}
	return p.channel
}
func (p *refundTestProvider) VerifyRefundNotification(_ http.Header, body []byte) (payment.RefundResult, error) {
	if !strings.HasPrefix(string(body), "verified synthetic notice:") {
		return payment.RefundResult{}, payment.ErrInvalidNotice
	}
	return payment.RefundResult{Provider: "wechat", Outcome: payment.RefundSucceeded, Status: "SUCCESS", ProviderRefundID: "wx-refund-key-1", ProviderRefundKey: "refund-no-1", OrderKey: "refund-service-order", TransactionID: "wx-trade-service", MerchantID: "merchant-1", AppID: "app-1", AmountFen: 100, TotalFen: 1000, Currency: "CNY", ProviderEventID: strings.TrimPrefix(string(body), "verified synthetic notice:"), EvidenceSource: "notification", ProviderOccurredAt: time.Now().UTC()}, nil
}

func refundTestResult(req payment.RefundRequest, outcome payment.RefundOutcome, status, eventID string) payment.RefundResult {
	return refundTestResultFor(req, outcome, status, eventID, "wechat")
}

func refundTestResultFor(req payment.RefundRequest, outcome payment.RefundOutcome, status, eventID, channel string) payment.RefundResult {
	refundID := ""
	if channel == "wechat" {
		refundID = "wx-refund-key-1"
	}
	return payment.RefundResult{Provider: channel, Outcome: outcome, Status: status, RefundKey: req.RefundKey,
		ProviderRefundKey: req.ProviderRefundKey, ProviderRefundID: refundID, OrderKey: req.OrderKey,
		TransactionID: req.TransactionID, MerchantID: req.MerchantID, AppID: req.AppID,
		AmountFen: req.AmountFen, TotalFen: req.TotalFen, Currency: req.Currency,
		ProviderEventID: eventID, EvidenceSource: "query", ProviderOccurredAt: time.Now().UTC()}
}

func setupRefundOrchestration(t *testing.T) (*gorm.DB, string, func()) {
	return setupRefundOrchestrationChannel(t, "wechat")
}

func setupRefundOrchestrationChannel(t *testing.T, channel string) (*gorm.DB, string, func()) {
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
	merchant, appID, transactionID := "merchant-1", "app-1", "wx-trade-service"
	if channel == "alipay" {
		merchant, appID, transactionID = "seller-1", "ali-app-1", "ali-trade-service"
	}
	order := model.PointPurchaseOrder{OrderKey: "refund-service-order", UserID: 41, Channel: channel, ProviderMerchantID: merchant, ProviderAppID: appID, ProviderTransactionID: transactionID, Currency: "CNY", AmountFen: 1000, PurchaseMicro: 1_000_000_000, State: "credited"}
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
	providerRefundKey := "refund-no-1"
	refund, err := model.RequestPointRefund(model.PointRefundRequest{UserID: 41, OrderKey: order.OrderKey, IdempotencyKey: "refund-service-idem", RefundKey: "refund-service-key", ProviderRefundKey: providerRefundKey, AmountFen: 100, Reason: "synthetic refund request"})
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
	if err := db.Model(&model.PointRefund{}).Where("refund_key = ?", refund.RefundKey).Update("recovery_next_at", time.Now().UTC().Unix()-1).Error; err != nil {
		t.Fatal(err)
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

func TestRefundDispatchIdentityMismatchNeverReportsProviderSuccess(t *testing.T) {
	db, _, cleanup := setupRefundOrchestration(t)
	defer cleanup()
	provider := &refundTestProvider{applyResult: payment.RefundSucceeded, applyMutator: func(result payment.RefundResult) payment.RefundResult {
		result.MerchantID = "wrong-merchant"
		return result
	}}
	restore := ReplaceProvidersForTest(map[string]RuntimeProvider{"wechat": {Identity: MerchantIdentity{Provider: "wechat", MerchantID: "merchant-1", AppID: "app-1"}, Provider: provider}})
	defer restore()
	dispatch, err := DispatchPointRefundOperation(context.Background(), "refund-service-key")
	if err != nil {
		t.Fatalf("identity mismatch should be preserved as an unknown outcome: %v", err)
	}
	if dispatch.Outcome == payment.RefundSucceeded || dispatch.State != "processed" {
		t.Fatalf("mismatched success was reported as trusted: %+v", dispatch)
	}
	var refund model.PointRefund
	var account model.PointAccount
	if err := db.First(&refund, "refund_key = ?", "refund-service-key").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&account, "user_id = ?", 41).Error; err != nil {
		t.Fatal(err)
	}
	if refund.State == "succeeded" || account.HeldMicro != 100_000_000 || account.AvailableMicro != 900_000_000 {
		t.Fatalf("identity-mismatched response changed refund balance: refund=%+v account=%+v", refund, account)
	}
}

func TestRefundRecoveryResubmitsSameProviderNumberAfterPreSendCrash(t *testing.T) {
	for _, channel := range []string{"wechat", "alipay"} {
		t.Run(channel, func(t *testing.T) {
			db, _, cleanup := setupRefundOrchestrationChannel(t, channel)
			defer cleanup()
			refund, err := model.GetPointRefundByKey("refund-service-key")
			if err != nil {
				t.Fatal(err)
			}
			crashTime := time.Now().UTC().Add(-2 * time.Hour).Unix()
			if _, err := model.ClaimPointRefundOperation(refund.RefundKey, "crashed-before-network", crashTime, 10); err != nil {
				t.Fatalf("persist pre-network apply claim: %v", err)
			}
			provider := &refundTestProvider{channel: channel, queryResult: payment.RefundUnknown, queryStatus: "NO_REFUND_RECORD", queryRetrySameKey: true, applyResult: payment.RefundSucceeded}
			restore := ReplaceProvidersForTest(map[string]RuntimeProvider{channel: {Identity: MerchantIdentity{Provider: channel, MerchantID: refund.ProviderMerchantID, AppID: refund.ProviderAppID}, Provider: provider}})
			defer restore()

			opts := RefundRecoveryOptions{Enabled: true, BatchSize: 10, MaxProviderCalls: 1}
			report, err := RunRefundRecoveryBatch(context.Background(), opts)
			if err != nil || report.ProviderCalls != 1 {
				t.Fatalf("recovery query batch failed: report=%+v err=%v", report, err)
			}
			var afterQuery model.PointRefund
			if err := db.First(&afterQuery, "refund_key = ?", refund.RefundKey).Error; err != nil {
				t.Fatal(err)
			}
			if afterQuery.State != "unknown" || afterQuery.RecoveryAction != "apply" || afterQuery.ActiveOrderKey == nil {
				t.Fatalf("query uncertainty released funds or lost same-key retry: %+v", afterQuery)
			}
			report, err = RunRefundRecoveryBatch(context.Background(), opts)
			if err != nil || report.ProviderCalls != 0 {
				t.Fatalf("recovery ignored persisted backoff: report=%+v err=%v", report, err)
			}
			// Advance the durable deadline explicitly to model time passing. The
			// production backoff remains intact and this test never sleeps.
			if err := db.Model(&model.PointRefund{}).Where("refund_key = ?", refund.RefundKey).Update("recovery_next_at", time.Now().UTC().Add(-time.Second).Unix()).Error; err != nil {
				t.Fatal(err)
			}
			report, err = RunRefundRecoveryBatch(context.Background(), opts)
			if err != nil || report.ProviderCalls != 1 {
				t.Fatalf("same-key retry batch failed: report=%+v err=%v", report, err)
			}
			provider.mu.Lock()
			applyCalls, queryCalls := provider.applyCalls, provider.queryCalls
			var appliedKey, queriedKey string
			if len(provider.applyRequests) > 0 {
				appliedKey = provider.applyRequests[0].ProviderRefundKey
			}
			if len(provider.queryRequests) > 0 {
				queriedKey = provider.queryRequests[0].ProviderRefundKey
			}
			provider.mu.Unlock()
			if applyCalls != 1 || queryCalls != 1 || appliedKey != refund.ProviderRefundKey || queriedKey != refund.ProviderRefundKey {
				t.Fatalf("recovery changed provider key or repeated work: apply=%d query=%d applyKey=%q queryKey=%q wanted=%q", applyCalls, queryCalls, appliedKey, queriedKey, refund.ProviderRefundKey)
			}
			var final model.PointRefund
			var account model.PointAccount
			var ledgerCount int64
			if err := db.First(&final, "refund_key = ?", refund.RefundKey).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.First(&account, "user_id = ?", refund.UserID).Error; err != nil {
				t.Fatal(err)
			}
			db.Model(&model.PointLedger{}).Where("business_key = ?", "refund-success:"+refund.RefundKey).Count(&ledgerCount)
			if final.State != "succeeded" || account.HeldMicro != 0 || ledgerCount != 1 {
				t.Fatalf("same-key recovery did not settle exactly once: refund=%+v account=%+v ledgers=%d", final, account, ledgerCount)
			}
		})
	}
}

func TestAlipayUnconfirmedQueriesEventuallyResubmitFrozenRequestSameKey(t *testing.T) {
	db, _, cleanup := setupRefundOrchestrationChannel(t, "alipay")
	defer cleanup()
	provider := &refundTestProvider{channel: "alipay", applyErr: context.DeadlineExceeded, queryErr: errors.New("signed query response omitted refund amount")}
	restore := ReplaceProvidersForTest(map[string]RuntimeProvider{"alipay": {Identity: MerchantIdentity{Provider: "alipay", MerchantID: "seller-1", AppID: "ali-app-1"}, Provider: provider}})
	defer restore()

	first, err := DispatchPointRefundOperation(context.Background(), "refund-service-key")
	if err != nil || first.Outcome != payment.RefundUnknown || !first.ProviderCalled {
		t.Fatalf("uncertain initial apply did not remain unknown: %+v err=%v", first, err)
	}
	for failure := 1; failure <= 3; failure++ {
		if err := db.Model(&model.PointRefund{}).Where("refund_key = ?", "refund-service-key").Updates(map[string]interface{}{
			"recovery_next_at":      time.Now().UTC().Add(-time.Second).Unix(),
			"operation_lease_until": time.Now().UTC().Add(-time.Second).Unix(),
		}).Error; err != nil {
			t.Fatal(err)
		}
		query, err := DispatchPointRefundOperation(context.Background(), "refund-service-key")
		if err != nil || query.Outcome != payment.RefundUnknown || !query.ProviderCalled {
			t.Fatalf("unconfirmed Alipay query %d was treated as terminal: %+v err=%v", failure, query, err)
		}
		var current model.PointRefund
		var account model.PointAccount
		if err := db.First(&current, "refund_key = ?", "refund-service-key").Error; err != nil {
			t.Fatal(err)
		}
		if err := db.First(&account, "user_id = ?", 41).Error; err != nil {
			t.Fatal(err)
		}
		if current.State != "unknown" || account.HeldMicro != 100_000_000 || account.AvailableMicro != 900_000_000 {
			t.Fatalf("unconfirmed query %d released frozen funds: refund=%+v account=%+v", failure, current, account)
		}
	}
	var afterQueries model.PointRefund
	if err := db.First(&afterQueries, "refund_key = ?", "refund-service-key").Error; err != nil {
		t.Fatal(err)
	}
	if afterQueries.RecoveryAction != "apply" || afterQueries.RecoveryQueryFailures != 3 {
		t.Fatalf("three unconfirmed Alipay query failures did not schedule exact-key retry: %+v", afterQueries)
	}
	provider.mu.Lock()
	provider.applyErr = nil
	provider.applyResult = payment.RefundAccepted
	provider.mu.Unlock()
	if err := db.Model(&model.PointRefund{}).Where("refund_key = ?", "refund-service-key").Updates(map[string]interface{}{
		"recovery_next_at":      time.Now().UTC().Add(-time.Second).Unix(),
		"operation_lease_until": time.Now().UTC().Add(-time.Second).Unix(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	retry, err := DispatchPointRefundOperation(context.Background(), "refund-service-key")
	if err != nil || retry.Operation != "apply" || retry.Outcome != payment.RefundAccepted || !retry.ProviderCalled {
		t.Fatalf("scheduled exact-key retry failed: %+v err=%v", retry, err)
	}
	provider.mu.Lock()
	applyCalls, queryCalls := provider.applyCalls, provider.queryCalls
	applyRequests := append([]payment.RefundRequest(nil), provider.applyRequests...)
	provider.mu.Unlock()
	if applyCalls != 2 || queryCalls != 3 || len(applyRequests) != 2 {
		t.Fatalf("unexpected provider operation counts apply=%d query=%d requests=%d", applyCalls, queryCalls, len(applyRequests))
	}
	for _, req := range applyRequests {
		if req.ProviderRefundKey != afterQueries.ProviderRefundKey || req.AmountFen != afterQueries.AmountFen || req.PriorRefundedFen != afterQueries.PriorRefundedFen || req.MerchantID != afterQueries.ProviderMerchantID || req.AppID != afterQueries.ProviderAppID {
			t.Fatalf("retry changed frozen provider request: %+v refund=%+v", req, afterQueries)
		}
	}
	var final model.PointRefund
	var account model.PointAccount
	if err := db.First(&final, "refund_key = ?", "refund-service-key").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&account, "user_id = ?", 41).Error; err != nil {
		t.Fatal(err)
	}
	if final.State == "succeeded" || account.HeldMicro != 100_000_000 || account.AvailableMicro != 900_000_000 {
		t.Fatalf("accepted retry was mistaken for final success: refund=%+v account=%+v", final, account)
	}
}

func TestRefundRecoveryWorkerDisabledAndStopWaitsForCancelledCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	disabled, err := StartRefundRecoveryWorker(ctx, RefundRecoveryOptions{})
	if err != nil || disabled.Enabled() {
		t.Fatalf("disabled worker unexpectedly started: worker=%+v err=%v", disabled, err)
	}
	disabled.Stop()
	cancel()

	_, _, cleanup := setupRefundOrchestration(t)
	defer cleanup()
	started := make(chan struct{})
	provider := &refundTestProvider{applyResult: payment.RefundAccepted, applyStarted: started, applyRelease: make(chan struct{})}
	restore := ReplaceProvidersForTest(map[string]RuntimeProvider{"wechat": {Identity: MerchantIdentity{Provider: "wechat", MerchantID: "merchant-1", AppID: "app-1"}, Provider: provider}})
	defer restore()
	workerCtx, workerCancel := context.WithCancel(context.Background())
	worker, err := StartRefundRecoveryWorker(workerCtx, RefundRecoveryOptions{Enabled: true, Interval: time.Hour, BatchSize: 10, MaxProviderCalls: 1})
	if err != nil || !worker.Enabled() {
		t.Fatalf("enabled worker did not start: %v", err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		workerCancel()
		worker.Stop()
		t.Fatal("worker did not begin its bounded initial batch")
	}
	workerCancel()
	done := make(chan struct{})
	go func() { worker.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop returned without waiting for the provider call to finish")
	}
	var refund model.PointRefund
	if err := model.DB.First(&refund, "refund_key = ?", "refund-service-key").Error; err != nil {
		t.Fatal(err)
	}
	if refund.State != "unknown" || refund.ActiveOrderKey == nil || refund.OperationToken != "" {
		t.Fatalf("cancelled worker did not persist safe unknown state before stopping: %+v", refund)
	}
}

func TestConcurrentRefundRecoveryBatchesMakeOnlyOneProviderCall(t *testing.T) {
	_, _, cleanup := setupRefundOrchestration(t)
	defer cleanup()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	provider := &refundTestProvider{applyResult: payment.RefundAccepted, applyStarted: started, applyRelease: release}
	restore := ReplaceProvidersForTest(map[string]RuntimeProvider{"wechat": {Identity: MerchantIdentity{Provider: "wechat", MerchantID: "merchant-1", AppID: "app-1"}, Provider: provider}})
	defer restore()
	opts := RefundRecoveryOptions{Enabled: true, BatchSize: 10, MaxProviderCalls: 1}
	type batchResult struct {
		report RefundRecoveryReport
		err    error
	}
	firstDone := make(chan batchResult, 1)
	go func() {
		report, err := RunRefundRecoveryBatch(context.Background(), opts)
		firstDone <- batchResult{report, err}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("first recovery batch did not enter its provider call")
	}
	secondDone := make(chan batchResult, 1)
	go func() {
		report, err := RunRefundRecoveryBatch(context.Background(), opts)
		secondDone <- batchResult{report, err}
	}()
	var second batchResult
	select {
	case second = <-secondDone:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("second recovery batch did not complete while first provider call was held")
	}
	if second.err != nil || second.report.ProviderCalls != 0 {
		close(release)
		t.Fatalf("second batch made a duplicate external call: %+v", second)
	}
	close(release)
	var first batchResult
	select {
	case first = <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("first recovery batch did not finish after provider release")
	}
	provider.mu.Lock()
	applyCalls := provider.applyCalls
	provider.mu.Unlock()
	if first.err != nil || first.report.ProviderCalls != 1 || applyCalls != 1 {
		t.Fatalf("competing recovery batches were not single-call: first=%+v second=%+v calls=%d", first, second, applyCalls)
	}
}

func TestRefundRecoveryWorkerRestartResumesPastBadRowAndCompletesDueRefund(t *testing.T) {
	db, _, cleanup := setupRefundOrchestration(t)
	defer cleanup()
	if err := db.Exec("UPDATE point_refunds SET reason = ? WHERE refund_key = ?", strings.Repeat("x", 81), "refund-service-key").Error; err != nil {
		t.Fatal(err)
	}
	secondOrder := model.PointPurchaseOrder{OrderKey: "refund-restart-order-2", UserID: 42, Channel: "wechat", ProviderMerchantID: "merchant-1", ProviderAppID: "app-1", ProviderTransactionID: "wx-restart-trade-2", Currency: "CNY", AmountFen: 1000, PurchaseMicro: 1_000_000_000, State: "credited"}
	if err := db.Create(&model.PointAccount{UserID: 42, AvailableMicro: 1_000_000_000}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&secondOrder).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.PointLot{UserID: 42, BusinessKey: "purchase:" + secondOrder.OrderKey, Kind: "purchase", SourceRef: secondOrder.OrderKey, AmountFen: 1000, InitialMicro: 1_000_000_000, AvailableMicro: 1_000_000_000}).Error; err != nil {
		t.Fatal(err)
	}
	secondRefund, err := model.RequestPointRefund(model.PointRefundRequest{UserID: 42, OrderKey: secondOrder.OrderKey, IdempotencyKey: "refund-restart-idem-2", RefundKey: "refund-restart-key-2", ProviderRefundKey: "refund-restart-provider-2", AmountFen: 100, Reason: "valid request"})
	if err != nil {
		t.Fatal(err)
	}
	if err := model.ApprovePointRefund(secondRefund.RefundKey, "refund-restart-approve-2", 99, "批准"); err != nil {
		t.Fatal(err)
	}
	thirdOrder := model.PointPurchaseOrder{OrderKey: "refund-restart-order-3", UserID: 43, Channel: "wechat", ProviderMerchantID: "merchant-1", ProviderAppID: "app-1", ProviderTransactionID: "wx-restart-trade-3", Currency: "CNY", AmountFen: 1000, PurchaseMicro: 1_000_000_000, State: "credited"}
	if err := db.Create(&model.PointAccount{UserID: 43, AvailableMicro: 1_000_000_000}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&thirdOrder).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.PointLot{UserID: 43, BusinessKey: "purchase:" + thirdOrder.OrderKey, Kind: "purchase", SourceRef: thirdOrder.OrderKey, AmountFen: 1000, InitialMicro: 1_000_000_000, AvailableMicro: 1_000_000_000}).Error; err != nil {
		t.Fatal(err)
	}
	thirdRefund, err := model.RequestPointRefund(model.PointRefundRequest{UserID: 43, OrderKey: thirdOrder.OrderKey, IdempotencyKey: "refund-restart-idem-3", RefundKey: "refund-restart-key-3", ProviderRefundKey: "refund-restart-provider-3", AmountFen: 100, Reason: "valid request"})
	if err != nil {
		t.Fatal(err)
	}
	if err := model.ApprovePointRefund(thirdRefund.RefundKey, "refund-restart-approve-3", 99, "批准"); err != nil {
		t.Fatal(err)
	}
	applyStarted := make(chan struct{}, 1)
	provider := &refundTestProvider{applyResult: payment.RefundSucceeded, queryResult: payment.RefundSucceeded, queryStatus: "SUCCESS", applyStarted: applyStarted, applyRelease: make(chan struct{}), queryStarted: make(chan struct{}, 1), queryMutator: func(result payment.RefundResult) payment.RefundResult {
		result.ProviderRefundID = "wx-refund-" + result.ProviderRefundKey
		return result
	}}
	restore := ReplaceProvidersForTest(map[string]RuntimeProvider{"wechat": {Identity: MerchantIdentity{Provider: "wechat", MerchantID: "merchant-1", AppID: "app-1"}, Provider: provider}})
	defer restore()
	preScan, err := RunRefundRecoveryBatch(context.Background(), RefundRecoveryOptions{Enabled: true, BatchSize: 1, MaxProviderCalls: 1})
	if err != nil || preScan.RefundScanned != 1 {
		t.Fatalf("bad-row pre-scan failed: %+v err=%v", preScan, err)
	}
	bad, err := model.GetPointRefundByKey("refund-service-key")
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := model.GetPointRefundRecoveryCursor("refunds")
	if err != nil || cursor != bad.ID {
		t.Fatalf("pre-scan did not persist a nonzero cursor after the isolated row: cursor=%d bad_id=%d err=%v", cursor, bad.ID, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	worker, err := StartRefundRecoveryWorker(ctx, RefundRecoveryOptions{Enabled: true, Interval: time.Hour, BatchSize: 1, MaxProviderCalls: 1})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-applyStarted:
	case <-time.After(5 * time.Second):
		cancel()
		worker.Stop()
		t.Fatal("initial worker did not reach the healthy row after bad row")
	}
	cancel()
	worker.Stop() // Waits for the cancelled external call and durable cursor update.
	var due model.PointRefund
	if err := db.First(&due, "refund_key = ?", secondRefund.RefundKey).Error; err != nil {
		t.Fatal(err)
	}
	if bad.State != "needs_manual_review" || due.State != "unknown" || due.OperationToken != "" {
		t.Fatalf("stop/recovery did not retain safe durable states: bad=%+v due=%+v", bad, due)
	}
	cursor, err = model.GetPointRefundRecoveryCursor("refunds")
	if err != nil || cursor != due.ID {
		t.Fatalf("stopped worker did not retain its nonzero continuation cursor: cursor=%d due_id=%d err=%v", cursor, due.ID, err)
	}
	if err := db.Model(&model.PointRefund{}).Where("refund_key = ?", due.RefundKey).Update("recovery_next_at", time.Now().UTC().Add(-time.Second).Unix()).Error; err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	provider.applyRelease = nil
	provider.mu.Unlock()
	restartCtx, restartCancel := context.WithCancel(context.Background())
	restarted, err := StartRefundRecoveryWorker(restartCtx, RefundRecoveryOptions{Enabled: true, Interval: time.Hour, BatchSize: 1, MaxProviderCalls: 1})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-applyStarted:
	case <-time.After(5 * time.Second):
		restartCancel()
		restarted.Stop()
		t.Fatal("restarted worker did not continue from the persisted nonzero cursor")
	}
	restartCancel()
	restarted.Stop()
	var third model.PointRefund
	if err := db.First(&third, "refund_key = ?", thirdRefund.RefundKey).Error; err != nil {
		t.Fatal(err)
	}
	cursor, err = model.GetPointRefundRecoveryCursor("refunds")
	if err != nil || cursor != 0 || third.State != "succeeded" {
		t.Fatalf("restarted worker did not finish the next cursor page: third=%+v cursor=%d err=%v", third, cursor, err)
	}
	if err := db.Model(&model.PointRefund{}).Where("refund_key = ?", due.RefundKey).Update("recovery_next_at", time.Now().UTC().Add(-time.Second).Unix()).Error; err != nil {
		t.Fatal(err)
	}
	resume, err := RunRefundRecoveryBatch(context.Background(), RefundRecoveryOptions{Enabled: true, BatchSize: 1, MaxProviderCalls: 1})
	if err != nil || resume.ProviderCalls != 1 || resume.RefundFailed != 0 || resume.InboxFailed != 0 {
		t.Fatalf("wrapped cursor did not return to the still-due earlier refund: %+v err=%v", resume, err)
	}
	var completed model.PointRefund
	var account model.PointAccount
	var credits int64
	if err := db.First(&completed, "refund_key = ?", due.RefundKey).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&account, "user_id = ?", due.UserID).Error; err != nil {
		t.Fatal(err)
	}
	db.Model(&model.PointLedger{}).Where("business_key = ?", "refund-success:"+due.RefundKey).Count(&credits)
	if completed.State != "succeeded" || account.HeldMicro != 0 || credits != 1 {
		t.Fatalf("restarted worker failed to complete once: refund=%+v account=%+v credit_ledgers=%d", completed, account, credits)
	}
	cursor, err = model.GetPointRefundRecoveryCursor("refunds")
	if err != nil || cursor != 0 {
		t.Fatalf("completed scan failed to wrap cursor: cursor=%d err=%v", cursor, err)
	}
	if bad.State != "needs_manual_review" {
		t.Fatalf("bad historical row blocked or was silently changed: %+v", bad)
	}
}

func TestRefundRecoveryProviderCallBudgetIncludesPersistenceErrors(t *testing.T) {
	db, _, cleanup := setupRefundOrchestration(t)
	defer cleanup()
	secondOrder := model.PointPurchaseOrder{OrderKey: "refund-budget-order-2", UserID: 42, Channel: "wechat", ProviderMerchantID: "merchant-1", ProviderAppID: "app-1", ProviderTransactionID: "wx-budget-trade-2", Currency: "CNY", AmountFen: 1000, PurchaseMicro: 1_000_000_000, State: "credited"}
	if err := db.Create(&model.PointAccount{UserID: 42, AvailableMicro: 1_000_000_000}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&secondOrder).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.PointLot{UserID: 42, BusinessKey: "purchase:" + secondOrder.OrderKey, Kind: "purchase", SourceRef: secondOrder.OrderKey, AmountFen: 1000, InitialMicro: 1_000_000_000, AvailableMicro: 1_000_000_000}).Error; err != nil {
		t.Fatal(err)
	}
	secondRefund, err := model.RequestPointRefund(model.PointRefundRequest{UserID: 42, OrderKey: secondOrder.OrderKey, IdempotencyKey: "refund-budget-idem-2", RefundKey: "refund-budget-key-2", ProviderRefundKey: "refund-budget-provider-2", AmountFen: 100, Reason: "synthetic refund request"})
	if err != nil {
		t.Fatal(err)
	}
	if err := model.ApprovePointRefund(secondRefund.RefundKey, "refund-budget-approve-2", 99, "批准"); err != nil {
		t.Fatal(err)
	}
	provider := &refundTestProvider{applyResult: payment.RefundSucceeded}
	provider.applyHook = func() { _ = db.Migrator().DropTable(&model.PointRefundInbox{}) }
	restore := ReplaceProvidersForTest(map[string]RuntimeProvider{"wechat": {Identity: MerchantIdentity{Provider: "wechat", MerchantID: "merchant-1", AppID: "app-1"}, Provider: provider}})
	defer restore()
	report, err := RunRefundRecoveryBatch(context.Background(), RefundRecoveryOptions{Enabled: true, BatchSize: 10, MaxProviderCalls: 1})
	if err != nil {
		t.Fatalf("recovery batch returned a scan-level error: %+v %v", report, err)
	}
	provider.mu.Lock()
	applyCalls := provider.applyCalls
	provider.mu.Unlock()
	if report.ProviderCalls != 1 || report.RefundFailed != 1 || applyCalls != 1 {
		t.Fatalf("external-call budget ignored a post-call persistence error: report=%+v calls=%d", report, applyCalls)
	}
	var second model.PointRefund
	if err := db.First(&second, "refund_key = ?", secondRefund.RefundKey).Error; err != nil {
		t.Fatal(err)
	}
	if second.State != "approved" || second.OperationToken != "" {
		t.Fatalf("budget overrun started a later refund: %+v", second)
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

func TestConcurrentVerifiedRefundNotificationsReuseFirstScheduleAndCreditOnce(t *testing.T) {
	db, _, cleanup := setupRefundOrchestration(t)
	defer cleanup()
	refund, err := model.GetPointRefundByKey("refund-service-key")
	if err != nil {
		t.Fatal(err)
	}
	result := payment.RefundResult{Provider: "wechat", Outcome: payment.RefundSucceeded, Status: "SUCCESS", RefundKey: refund.RefundKey, ProviderRefundKey: refund.ProviderRefundKey, ProviderRefundID: "wx-refund-key-1", OrderKey: refund.OrderKey, TransactionID: refund.ProviderTransactionID, MerchantID: refund.ProviderMerchantID, AppID: refund.ProviderAppID, AmountFen: refund.AmountFen, TotalFen: refund.OriginalAmountFen, Currency: refund.Currency, ProviderEventID: "same-notify-id", ProviderOccurredAt: time.Now().UTC(), EvidenceSource: "notification"}
	start := make(chan struct{})
	type persistResult struct {
		row *model.PointRefundInbox
		err error
	}
	results := make(chan persistResult, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			row, _, err := PersistRefundProviderResult(result, "notification", nil)
			results <- persistResult{row: row, err: err}
		}()
	}
	claimResult := make(chan error, 1)
	go func() {
		<-start
		_, err := model.ClaimPointRefundOperation(refund.RefundKey, "concurrent-notification-claim", time.Now().UTC().Unix(), 60)
		claimResult <- err
	}()
	close(start)
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil || first.row == nil || second.row == nil || first.row.ID != second.row.ID {
		t.Fatalf("concurrent identical notifications conflicted: first=%+v/%v second=%+v/%v", first.row, first.err, second.row, second.err)
	}
	if err := <-claimResult; err != nil {
		t.Fatalf("concurrent claim failed: %v", err)
	}
	for _, inboxID := range []uint{first.row.ID, second.row.ID} {
		if _, err := ProcessPointRefundInbox(inboxID); err != nil {
			t.Fatal(err)
		}
	}
	var final model.PointRefund
	var account model.PointAccount
	var inboxCount, ledgerCount int64
	if err := db.First(&final, "refund_key = ?", refund.RefundKey).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&account, "user_id = ?", refund.UserID).Error; err != nil {
		t.Fatal(err)
	}
	db.Model(&model.PointRefundInbox{}).Where("evidence_key = ?", first.row.EvidenceKey).Count(&inboxCount)
	db.Model(&model.PointLedger{}).Where("business_key = ?", "refund-success:"+refund.RefundKey).Count(&ledgerCount)
	if final.State != "succeeded" || account.HeldMicro != 0 || inboxCount != 1 || ledgerCount != 1 {
		t.Fatalf("duplicate notification/claim changed balances more than once: refund=%+v account=%+v inbox=%d ledger=%d", final, account, inboxCount, ledgerCount)
	}
}

func TestV10ReceivedLocalUnknownRefundInboxWithoutProviderIDProcessesAfterV11Upgrade(t *testing.T) {
	db, _, cleanup := setupRefundOrchestration(t)
	defer cleanup()
	refund, err := model.GetPointRefundByKey("refund-service-key")
	if err != nil {
		t.Fatal(err)
	}
	legacyToken := "legacy-v10-query-token"
	now := time.Now().UTC().Unix()
	if err := db.Exec("UPDATE point_refunds SET state = ?, operation_token = ?, operation_kind = ?, operation_claimed_at = ?, operation_lease_until = ? WHERE refund_key = ?", "submitting", legacyToken, "query", now-10, now+60, refund.RefundKey).Error; err != nil {
		t.Fatal(err)
	}
	legacy := model.PointRefundInbox{
		EvidenceKey: "legacy-v10-unknown-inbox", RefundKey: refund.RefundKey, Provider: "wechat", EvidenceSource: "query",
		OperationToken: legacyToken, ProviderRefundKey: refund.ProviderRefundKey, OrderKey: refund.OrderKey,
		ProviderTransactionID: refund.ProviderTransactionID, MerchantID: refund.ProviderMerchantID, AppID: refund.ProviderAppID,
		Outcome: "unknown", ProviderStatus: "PROCESSING", AmountFen: refund.AmountFen, TotalFen: refund.OriginalAmountFen,
		Currency: refund.Currency, Digest: "legacy-v10-evidence-digest", State: "received", CreatedAt: time.Now().UTC(),
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"recovery_action", "recovery_next_at", "recovery_attempts", "recovery_query_failures", "recovery_last_error_code"} {
		if err := db.Migrator().DropColumn(&model.PointRefund{}, column); err != nil {
			t.Fatalf("simulate v10 refund column %s: %v", column, err)
		}
	}
	for _, column := range []string{"retry_same_key", "recovery_action", "recovery_next_at", "recovery_query_failures"} {
		if err := db.Migrator().DropColumn(&model.PointRefundInbox{}, column); err != nil {
			t.Fatalf("simulate v10 inbox column %s: %v", column, err)
		}
	}
	if err := db.Migrator().DropTable(&model.PointRefundRecoveryCursor{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("1 = 1").Delete(&model.PointsSchemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.PointsSchemaMigration{Version: 10, AppliedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := model.MigratePointsSchema(); err != nil {
		t.Fatalf("v10 inbox recovery migration failed: %v", err)
	}
	processed, err := ProcessPointRefundInbox(legacy.ID)
	if err != nil || processed.State != "processed" {
		t.Fatalf("legacy unknown inbox did not process after upgrade: %+v err=%v", processed, err)
	}
	var after model.PointRefund
	var account model.PointAccount
	if err := db.First(&after, "refund_key = ?", refund.RefundKey).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&account, "user_id = ?", refund.UserID).Error; err != nil {
		t.Fatal(err)
	}
	if after.State != "unknown" || after.RecoveryAction != "query" || after.RecoveryNextAt <= time.Now().UTC().Unix() || after.OperationToken != "" || account.HeldMicro != 100_000_000 || account.AvailableMicro != 900_000_000 {
		t.Fatalf("legacy query evidence released or lost the frozen refund: refund=%+v account=%+v", after, account)
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
