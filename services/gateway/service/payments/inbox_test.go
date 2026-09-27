package payments

import (
	"path/filepath"
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

func setupPaymentInbox(t *testing.T, orderState string) (*gorm.DB, func()) {
	t.Helper()
	oldDB, oldSQLite := model.DB, common.UsingSQLite
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "payments.db")+"?_busy_timeout=10000&_journal_mode=WAL"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(8)
	if err := db.AutoMigrate(&model.PointsSchemaMigration{}, &model.PointAccount{}, &model.PointLot{}, &model.PointLedger{}, &model.PointPriceVersion{}, &model.PointActivePrice{}, &model.PointHold{}, &model.PointHoldAllocation{}, &model.PointHoldAttempt{}, &model.PointHoldDecision{}, &model.PointAdminAudit{}, &model.PointTokenBudget{}, &model.PointPurchaseOrder{}, &model.PointPackage{}, &model.PointActivePackage{}, &model.PaymentEvent{}, &model.PaymentTransaction{}); err != nil {
		t.Fatal(err)
	}
	model.DB, common.UsingSQLite = db, true
	order := model.PointPurchaseOrder{OrderKey: "payment-order-0001", UserID: 41, Channel: "wechat", PackageID: "starter", PackageVersion: "v1", PackageSnapshot: `{"package_id":"starter","version":"v1"}`, Currency: "CNY", AmountFen: 500, PurchaseMicro: 500 * model.PointMicroPerPoint, BonusMicro: 25 * model.PointMicroPerPoint, BonusValiditySecs: 3600, State: orderState}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	return db, func() { _ = sqlDB.Close(); model.DB, common.UsingSQLite = oldDB, oldSQLite }
}

func testTrade(eventID, orderKey, txID string, amount int64) payment.VerifiedNotification {
	return payment.VerifiedNotification{Provider: "wechat", ProviderEventID: eventID, ProviderOccurredAt: time.Now().UTC(), OrderKey: orderKey, TransactionID: txID, MerchantID: "merchant-1", AppID: "app-1", AmountFen: amount, Currency: "CNY", Status: "SUCCESS"}
}

func TestVerifiedPaymentInboxCreditsAtomicallyAndDeduplicates(t *testing.T) {
	db, cleanup := setupPaymentInbox(t, "pending")
	defer cleanup()
	trade := testTrade("notify-1", "payment-order-0001", "wx-tx-1", 500)
	trade.ProviderOccurredAt = time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)
	first, err := PersistVerifiedNotification(trade, []byte("signed raw notification"))
	if err != nil || first.State != "received" || first.Duplicate {
		t.Fatalf("persist: %+v %v", first, err)
	}
	result, err := ProcessVerifiedPaymentEvent(first.EventID, MerchantIdentity{Provider: "wechat", MerchantID: "merchant-1", AppID: "app-1"})
	if err != nil || result.State != "processed" {
		t.Fatalf("process: %+v %v", result, err)
	}
	second, err := PersistVerifiedNotification(trade, []byte("signed raw notification"))
	if err != nil || !second.Duplicate || second.EventID != first.EventID || second.State != "processed" {
		t.Fatalf("duplicate inbox event: %+v %v", second, err)
	}
	if _, err := ProcessVerifiedPaymentEvent(first.EventID, MerchantIdentity{Provider: "wechat", MerchantID: "merchant-1", AppID: "app-1"}); err != nil {
		t.Fatal(err)
	}
	var order model.PointPurchaseOrder
	if err := db.First(&order, "order_key = ?", trade.OrderKey).Error; err != nil {
		t.Fatal(err)
	}
	if order.State != "credited" || order.ProviderTransactionID != trade.TransactionID || order.PaidEventKey == nil || *order.PaidEventKey != "wechat:notify-1" {
		t.Fatalf("order not credited with provider evidence: %+v", order)
	}
	var account model.PointAccount
	if err := db.First(&account, "user_id = ?", 41).Error; err != nil {
		t.Fatal(err)
	}
	if account.AvailableMicro != 525*model.PointMicroPerPoint {
		t.Fatalf("wrong credit amount: %+v", account)
	}
	var lots, ledgers, processed int64
	db.Model(&model.PointLot{}).Where("source_ref = ?", order.OrderKey).Count(&lots)
	db.Model(&model.PointLedger{}).Where("business_key = ?", "credit:"+order.OrderKey).Count(&ledgers)
	db.Model(&model.PaymentEvent{}).Where("state = ?", "processed").Count(&processed)
	if lots != 2 || ledgers != 1 || processed != 1 {
		t.Fatalf("duplicate side effects: lots=%d ledgers=%d processed events=%d", lots, ledgers, processed)
	}
	var bonus model.PointLot
	if err := db.Where("business_key = ?", "bonus:"+order.OrderKey).First(&bonus).Error; err != nil {
		t.Fatal(err)
	}
	expectedExpiry := trade.ProviderOccurredAt.Unix() + 3600
	if bonus.ExpiresAt == nil || *bonus.ExpiresAt != expectedExpiry {
		t.Fatalf("bonus expiration was not anchored to verified payment time: got=%v want=%d", bonus.ExpiresAt, expectedExpiry)
	}
}

func TestVerifiedPaymentInboxMismatchQuarantinesAndLateClosedOrderCredits(t *testing.T) {
	t.Run("mismatch quarantine", func(t *testing.T) {
		db, cleanup := setupPaymentInbox(t, "pending")
		defer cleanup()
		trade := testTrade("notify-mismatch", "payment-order-0001", "wx-tx-mismatch", 501)
		inbox, err := PersistVerifiedNotification(trade, []byte("verified mismatch raw"))
		if err != nil {
			t.Fatal(err)
		}
		result, err := ProcessVerifiedPaymentEvent(inbox.EventID, MerchantIdentity{Provider: "wechat", MerchantID: "wrong-merchant", AppID: "app-1"})
		if err == nil || result.State != "quarantined" {
			t.Fatalf("mismatch was not quarantined: %+v %v", result, err)
		}
		var accountCount, auditCount int64
		db.Model(&model.PointAccount{}).Where("user_id = ?", 41).Count(&accountCount)
		db.Model(&model.PointAdminAudit{}).Where("action = ?", "payment_event_quarantined").Count(&auditCount)
		if accountCount != 0 || auditCount != 1 {
			t.Fatalf("mismatch credited or unaudited: account=%d audit=%d", accountCount, auditCount)
		}
	})
	t.Run("paid after local close", func(t *testing.T) {
		db, cleanup := setupPaymentInbox(t, "closed")
		defer cleanup()
		trade := testTrade("notify-late", "payment-order-0001", "wx-tx-late", 500)
		inbox, err := PersistVerifiedNotification(trade, []byte("verified late raw"))
		if err != nil {
			t.Fatal(err)
		}
		result, err := ProcessVerifiedPaymentEvent(inbox.EventID, MerchantIdentity{Provider: "wechat", MerchantID: "merchant-1", AppID: "app-1"})
		if err != nil || result.State != "processed" {
			t.Fatalf("late verified payment dropped: %+v %v", result, err)
		}
		var lateAudit int64
		db.Model(&model.PointAdminAudit{}).Where("action = ?", "late_payment_after_close").Count(&lateAudit)
		if lateAudit != 1 {
			t.Fatalf("late payment lacked audit: %d", lateAudit)
		}
	})
}

func TestPaymentInboxEachMismatchFieldQuarantinesWithoutClaim(t *testing.T) {
	checks := []struct {
		name   string
		mutate func(*gorm.DB, *payment.VerifiedNotification)
	}{
		{name: "merchant", mutate: func(_ *gorm.DB, n *payment.VerifiedNotification) { n.MerchantID = "other-merchant" }},
		{name: "app", mutate: func(_ *gorm.DB, n *payment.VerifiedNotification) { n.AppID = "other-app" }},
		{name: "amount", mutate: func(_ *gorm.DB, n *payment.VerifiedNotification) { n.AmountFen++ }},
		{name: "currency", mutate: func(_ *gorm.DB, n *payment.VerifiedNotification) { n.Currency = "USD" }},
		{name: "status", mutate: func(_ *gorm.DB, n *payment.VerifiedNotification) { n.Status = "USERPAYING" }},
		{name: "channel", mutate: func(db *gorm.DB, _ *payment.VerifiedNotification) {
			if err := db.Model(&model.PointPurchaseOrder{}).Where("order_key = ?", "payment-order-0001").Update("channel", "alipay").Error; err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			db, cleanup := setupPaymentInbox(t, "pending")
			defer cleanup()
			trade := testTrade("notify-mismatch-"+check.name, "payment-order-0001", "wx-tx-mismatch-"+check.name, 500)
			check.mutate(db, &trade)
			inbox, err := PersistVerifiedNotification(trade, []byte("verified mismatch "+check.name))
			if err != nil {
				t.Fatal(err)
			}
			result, err := ProcessVerifiedPaymentEvent(inbox.EventID, MerchantIdentity{Provider: "wechat", MerchantID: "merchant-1", AppID: "app-1"})
			if err == nil || result.State != "quarantined" {
				t.Fatalf("%s mismatch was not quarantined: %+v %v", check.name, result, err)
			}
			var accountCount, auditCount, ownerCount int64
			db.Model(&model.PointAccount{}).Where("user_id = ?", 41).Count(&accountCount)
			db.Model(&model.PointAdminAudit{}).Where("action = ?", "payment_event_quarantined").Count(&auditCount)
			db.Model(&model.PaymentTransaction{}).Count(&ownerCount)
			if accountCount != 0 || auditCount != 1 || ownerCount != 0 {
				t.Fatalf("%s mismatch side effects: accounts=%d audits=%d owners=%d", check.name, accountCount, auditCount, ownerCount)
			}
		})
	}
}

func TestDifferentNotificationIDsForOneTransactionDoNotRecreditOrExtendBonus(t *testing.T) {
	db, cleanup := setupPaymentInbox(t, "pending")
	defer cleanup()
	identity := MerchantIdentity{Provider: "wechat", MerchantID: "merchant-1", AppID: "app-1"}
	first := testTrade("notify-replay-a", "payment-order-0001", "wx-tx-replayed", 500)
	first.ProviderOccurredAt = time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	firstEvent, err := PersistVerifiedNotification(first, []byte("signed notice a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ProcessVerifiedPaymentEvent(firstEvent.EventID, identity); err != nil {
		t.Fatal(err)
	}
	var initialBonus model.PointLot
	if err := db.Where("business_key = ?", "bonus:payment-order-0001").First(&initialBonus).Error; err != nil {
		t.Fatal(err)
	}
	second := first
	second.ProviderEventID = "notify-replay-b"
	second.ProviderOccurredAt = time.Now().UTC().Add(20 * time.Minute)
	secondEvent, err := PersistVerifiedNotification(second, []byte("signed notice b"))
	if err != nil {
		t.Fatal(err)
	}
	if result, err := ProcessVerifiedPaymentEvent(secondEvent.EventID, identity); err != nil || result.State != "processed" {
		t.Fatalf("same transaction notice replay was not acknowledged: %+v %v", result, err)
	}
	var afterBonus model.PointLot
	if err := db.Where("business_key = ?", "bonus:payment-order-0001").First(&afterBonus).Error; err != nil {
		t.Fatal(err)
	}
	var credits, owners int64
	db.Model(&model.PointLedger{}).Where("business_key = ?", "credit:payment-order-0001").Count(&credits)
	db.Model(&model.PaymentTransaction{}).Count(&owners)
	if credits != 1 || owners != 1 || initialBonus.ExpiresAt == nil || afterBonus.ExpiresAt == nil || *initialBonus.ExpiresAt != *afterBonus.ExpiresAt {
		t.Fatalf("same transaction replay changed credit/expiry: credits=%d owners=%d before=%v after=%v", credits, owners, initialBonus.ExpiresAt, afterBonus.ExpiresAt)
	}
}

func TestPaymentInboxRecoveryAndCreditRollback(t *testing.T) {
	db, cleanup := setupPaymentInbox(t, "pending")
	defer cleanup()
	trade := testTrade("notify-recover", "payment-order-0001", "wx-tx-recover", 500)
	inbox, err := PersistVerifiedNotification(trade, []byte("verified recovery raw"))
	if err != nil {
		t.Fatal(err)
	}
	// The separate event persistence transaction models a process crash before
	// order transition. Recovery processes the same durable inbox without I/O.
	identity := MerchantIdentity{Provider: "wechat", MerchantID: "merchant-1", AppID: "app-1"}
	count, err := RetryReceivedPaymentEvents(10, map[string]MerchantIdentity{"wechat": identity})
	if err != nil || count != 1 {
		t.Fatalf("recovery did not process event: count=%d err=%v", count, err)
	}
	again, err := ProcessVerifiedPaymentEvent(inbox.EventID, identity)
	if err != nil || again.State != "processed" {
		t.Fatalf("processed replay failed: %+v %v", again, err)
	}

	// A failed credit must roll back paid state and retain the durable inbox as
	// received so a later recovery can safely retry.
	if err := db.Create(&model.PointPurchaseOrder{OrderKey: "payment-order-0002", UserID: 42, Channel: "wechat", Currency: "CNY", AmountFen: 10, PurchaseMicro: 10 * model.PointMicroPerPoint, State: "pending"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.PointAccount{UserID: 42, AvailableMicro: int64(^uint64(0) >> 1)}).Error; err != nil {
		t.Fatal(err)
	}
	secondTrade := testTrade("notify-overflow", "payment-order-0002", "wx-tx-overflow", 10)
	second, err := PersistVerifiedNotification(secondTrade, []byte("verified overflow raw"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ProcessVerifiedPaymentEvent(second.EventID, identity); err == nil {
		t.Fatal("overflowing credit unexpectedly succeeded")
	}
	var order model.PointPurchaseOrder
	if err := db.First(&order, "order_key = ?", secondTrade.OrderKey).Error; err != nil {
		t.Fatal(err)
	}
	var event model.PaymentEvent
	if err := db.First(&event, second.EventID).Error; err != nil {
		t.Fatal(err)
	}
	if order.State != "pending" || event.State != "received" {
		t.Fatalf("failed credit did not rollback: order=%s event=%s", order.State, event.State)
	}
	if err := db.Model(&model.PointAccount{}).Where("user_id = ?", 42).Update("available_micro", 0).Error; err != nil {
		t.Fatal(err)
	}
	count, err = RetryReceivedPaymentEvents(10, map[string]MerchantIdentity{"wechat": identity})
	if err != nil || count != 1 {
		t.Fatalf("recovery after rollback failed: count=%d err=%v", count, err)
	}
}

func TestConcurrentSameVerifiedNotificationCreditsOnce(t *testing.T) {
	db, cleanup := setupPaymentInbox(t, "pending")
	defer cleanup()
	trade := testTrade("notify-concurrent", "payment-order-0001", "wx-tx-concurrent", 500)
	identity := MerchantIdentity{Provider: "wechat", MerchantID: "merchant-1", AppID: "app-1"}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			inbox, err := PersistVerifiedNotification(trade, []byte("concurrent raw"))
			if err != nil {
				errs <- err
				return
			}
			_, err = ProcessVerifiedPaymentEvent(inbox.EventID, identity)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var account model.PointAccount
	if err := db.First(&account, "user_id = ?", 41).Error; err != nil {
		t.Fatal(err)
	}
	if account.AvailableMicro != 525*model.PointMicroPerPoint {
		t.Fatalf("concurrent notification double credited: %d", account.AvailableMicro)
	}
	var ledgerCount int64
	db.Model(&model.PointLedger{}).Where("kind = ?", "purchase_credit").Count(&ledgerCount)
	if ledgerCount != 1 {
		t.Fatalf("expected one credit ledger event, got %d", ledgerCount)
	}
}

func TestQuarantinedEventDoesNotClaimProviderTransaction(t *testing.T) {
	db, cleanup := setupPaymentInbox(t, "pending")
	defer cleanup()
	identity := MerchantIdentity{Provider: "wechat", MerchantID: "merchant-1", AppID: "app-1"}
	bad := testTrade("notify-bad-first", "payment-order-0001", "wx-tx-shared", 501)
	badEvent, err := PersistVerifiedNotification(bad, []byte("verified bad amount"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ProcessVerifiedPaymentEvent(badEvent.EventID, identity); err == nil {
		t.Fatal("mismatched event unexpectedly accepted")
	}
	var owners int64
	db.Model(&model.PaymentTransaction{}).Count(&owners)
	if owners != 0 {
		t.Fatalf("quarantined event claimed transaction: %d", owners)
	}
	good := testTrade("notify-good-second", "payment-order-0001", "wx-tx-shared", 500)
	goodEvent, err := PersistVerifiedNotification(good, []byte("verified good amount"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ProcessVerifiedPaymentEvent(goodEvent.EventID, identity); err != nil {
		t.Fatalf("valid event was blocked by quarantined notification: %v", err)
	}
	db.Model(&model.PaymentTransaction{}).Count(&owners)
	if owners != 1 {
		t.Fatalf("valid payment did not claim one transaction owner: %d", owners)
	}
}

func TestConflictForCreditedOrderDoesNotClaimSecondTransaction(t *testing.T) {
	db, cleanup := setupPaymentInbox(t, "pending")
	defer cleanup()
	identity := MerchantIdentity{Provider: "wechat", MerchantID: "merchant-1", AppID: "app-1"}
	first := testTrade("notify-original", "payment-order-0001", "wx-tx-original", 500)
	event, err := PersistVerifiedNotification(first, []byte("verified original"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ProcessVerifiedPaymentEvent(event.EventID, identity); err != nil {
		t.Fatal(err)
	}
	conflict := testTrade("notify-conflicting", "payment-order-0001", "wx-tx-second", 500)
	second, err := PersistVerifiedNotification(conflict, []byte("verified conflicting transaction"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ProcessVerifiedPaymentEvent(second.EventID, identity); err == nil {
		t.Fatal("second transaction for an already credited order was accepted")
	}
	var owners int64
	db.Model(&model.PaymentTransaction{}).Count(&owners)
	if owners != 1 {
		t.Fatalf("conflicting paid-order notice claimed a new transaction: owners=%d", owners)
	}
}

func TestConcurrentDifferentOrdersCannotClaimSameProviderTransaction(t *testing.T) {
	db, cleanup := setupPaymentInbox(t, "pending")
	defer cleanup()
	if err := db.Create(&model.PointPurchaseOrder{OrderKey: "payment-order-0002", UserID: 42, Channel: "wechat", Currency: "CNY", AmountFen: 500, PurchaseMicro: 500 * model.PointMicroPerPoint, State: "pending"}).Error; err != nil {
		t.Fatal(err)
	}
	identity := MerchantIdentity{Provider: "wechat", MerchantID: "merchant-1", AppID: "app-1"}
	trades := []payment.VerifiedNotification{
		testTrade("notify-shared-a", "payment-order-0001", "wx-tx-shared-concurrent", 500),
		testTrade("notify-shared-b", "payment-order-0002", "wx-tx-shared-concurrent", 500),
	}
	var events [2]model.PaymentEvent
	for i, trade := range trades {
		inbox, err := PersistVerifiedNotification(trade, []byte("verified shared transaction "+trade.ProviderEventID))
		if err != nil {
			t.Fatal(err)
		}
		if err := db.First(&events[i], inbox.EventID).Error; err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range events {
		wg.Add(1)
		go func(eventID uint) {
			defer wg.Done()
			<-start
			_, _ = ProcessVerifiedPaymentEvent(eventID, identity)
		}(events[i].ID)
	}
	close(start)
	wg.Wait()
	// A safe SQLite lock conflict may leave one durable event received. Run the
	// normal recovery processor after both contenders finish to settle the
	// losing transaction against the committed unique owner.
	for _, event := range events {
		var saved model.PaymentEvent
		if err := db.First(&saved, event.ID).Error; err != nil {
			t.Fatal(err)
		}
		if saved.State == "received" {
			_, _ = ProcessVerifiedPaymentEvent(saved.ID, identity)
		}
	}
	var credited, quarantined, owners int64
	db.Model(&model.PointPurchaseOrder{}).Where("state = ?", "credited").Count(&credited)
	db.Model(&model.PaymentEvent{}).Where("state = ?", "quarantined").Count(&quarantined)
	db.Model(&model.PaymentTransaction{}).Count(&owners)
	if credited != 1 || quarantined != 1 || owners != 1 {
		t.Fatalf("transaction ownership race was not arbitrated: credited=%d quarantined=%d owners=%d", credited, quarantined, owners)
	}
	var total int64
	db.Model(&model.PointAccount{}).Select("COALESCE(SUM(available_micro), 0)").Scan(&total)
	var paidOrder model.PointPurchaseOrder
	if err := db.Where("state = ?", "credited").First(&paidOrder).Error; err != nil {
		t.Fatal(err)
	}
	expected := paidOrder.PurchaseMicro + paidOrder.BonusMicro
	if total != expected {
		t.Fatalf("same provider transaction credited more than its single owner: total=%d credited_order=%s expected=%d", total, paidOrder.OrderKey, expected)
	}
}

func TestPaymentInboxDuplicateEventIDPayloadConflict(t *testing.T) {
	_, cleanup := setupPaymentInbox(t, "pending")
	defer cleanup()
	trade := testTrade("notify-conflict", "payment-order-0001", "wx-tx-conflict", 500)
	if _, err := PersistVerifiedNotification(trade, []byte("raw one")); err != nil {
		t.Fatal(err)
	}
	if _, err := PersistVerifiedNotification(trade, []byte("raw two")); err == nil {
		t.Fatal("same provider event ID with different raw payload was accepted")
	}
}
