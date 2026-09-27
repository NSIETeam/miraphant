package model

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/config"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func openPointsTestDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path+"?_busy_timeout=10000&_journal_mode=WAL"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(8)
	if err = db.AutoMigrate(&Token{}); err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&PointsSchemaMigration{}, &PointAccount{}, &PointLot{}, &PointLedger{}, &PointPriceVersion{}, &PointActivePrice{}, &PointHold{}, &PointHoldAllocation{}, &PointHoldAttempt{}, &PointHoldDecision{}, &PointTokenBudget{}, &PointPurchaseOrder{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedPointUser(t *testing.T, db *gorm.DB, balance, budget int64, unlimited bool) {
	t.Helper()
	if err := db.Create(&Token{Id: 77, UserId: 41, Key: "points-test-token", Status: TokenStatusEnabled}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&PointAccount{UserID: 41, AvailableMicro: balance}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&PointLot{UserID: 41, BusinessKey: "seed:lot", Kind: "grant", InitialMicro: balance, AvailableMicro: balance}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&PointTokenBudget{TokenID: 77, UserID: 41, LimitMicro: budget, Unlimited: unlimited}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&PointPriceVersion{Version: "test-v1", ModelID: "test-model", InputMicroPer1K: 1000, CachedInputMicroPer1K: 100, OutputMicroPer1K: 2000, Source: "test fixture"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&PointActivePrice{ModelID: "test-model", Version: "test-v1"}).Error; err != nil {
		t.Fatal(err)
	}
}

func withPointsFixture(t *testing.T, balance, budget int64, unlimited bool) (*gorm.DB, func()) {
	t.Helper()
	oldDB, oldSQLite, oldEnabled := DB, common.UsingSQLite, config.PointsBillingEnabled
	path := filepath.Join(t.TempDir(), "points.db")
	db := openPointsTestDB(t, path)
	DB, common.UsingSQLite, config.PointsBillingEnabled = db, true, true
	seedPointUser(t, db, balance, budget, unlimited)
	return db, func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
		DB, common.UsingSQLite, config.PointsBillingEnabled = oldDB, oldSQLite, oldEnabled
	}
}

func TestPointArithmeticExactHalfUp(t *testing.T) {
	got, err := ParseMicroPoints("12.000001")
	if err != nil || got != 12_000_001 {
		t.Fatalf("ParseMicroPoints got %d, %v", got, err)
	}
	for _, bad := range []string{"1.-1", "1.0000001", "-1", "x"} {
		if _, err := ParseMicroPoints(bad); err == nil {
			t.Fatalf("expected rejection for %q", bad)
		}
	}
	usage, err := CalculatePointUsage(500, 0, 0, PointPriceVersion{InputMicroPer1K: 1, ExtraMicro: 5})
	if err != nil || usage != 6 {
		t.Fatalf("expected half-up 1 plus extra 5 micro-points, got %d, %v", usage, err)
	}
	if _, err := CalculatePointUsage(1, 2, 0, PointPriceVersion{}); err == nil {
		t.Fatal("cached input greater than prompt must fail")
	}
}

func TestPointReserveConcurrentHandlesCannotOverdraw(t *testing.T) {
	db, cleanup := withPointsFixture(t, 100, 100, false)
	defer cleanup()
	path := strings.SplitN(db.Dialector.(*sqlite.Dialector).DSN, "?", 2)[0]
	db2 := openPointsTestDB(t, path)
	defer func() { sqlDB, _ := db2.DB(); _ = sqlDB.Close() }()
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes, insufficient, unexpected := 0, 0, []error{}
	start := make(chan struct{})
	handles := []*gorm.DB{db, db2}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int, handle *gorm.DB) {
			defer wg.Done()
			<-start
			_, err := reservePointsOn(handle, PointReserveRequest{UserID: 41, TokenID: 77, LogicalRequestKey: fmt.Sprintf("concurrent-%d", i), AttemptKey: "attempt-1", AttemptFingerprint: fmt.Sprintf("request-%d", i), BudgetMicro: 20, PriceVersion: "test-v1"})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successes++
			} else if errors.Is(err, ErrPointsInsufficient) {
				insufficient++
			} else {
				unexpected = append(unexpected, err)
			}
		}(i, handles[i%len(handles)])
	}
	close(start)
	wg.Wait()
	if successes != 5 || insufficient != 3 || len(unexpected) != 0 {
		t.Fatalf("success=%d insufficient=%d unexpected=%v", successes, insufficient, unexpected)
	}
	var account PointAccount
	if err := db.First(&account, "user_id = ?", 41).Error; err != nil {
		t.Fatal(err)
	}
	if account.AvailableMicro != 0 || account.HeldMicro != 100 {
		t.Fatalf("unexpected account after concurrent reserves: %+v", account)
	}
}

func TestPointReserveRollsBackWhenTokenBudgetFails(t *testing.T) {
	db, cleanup := withPointsFixture(t, 100, 50, false)
	defer cleanup()
	_, err := ReservePoints(PointReserveRequest{UserID: 41, TokenID: 77, LogicalRequestKey: "rollback", AttemptKey: "a1", AttemptFingerprint: "rollback-fp", BudgetMicro: 60, PriceVersion: "test-v1"})
	if err == nil {
		t.Fatal("expected token budget rejection")
	}
	var account PointAccount
	_ = db.First(&account, "user_id = ?", 41)
	var lot PointLot
	_ = db.First(&lot, "business_key = ?", "seed:lot")
	var ledgerCount, holdCount int64
	db.Model(&PointLedger{}).Count(&ledgerCount)
	db.Model(&PointHold{}).Count(&holdCount)
	if account.AvailableMicro != 100 || account.HeldMicro != 0 || lot.AvailableMicro != 100 || lot.HeldMicro != 0 || ledgerCount != 0 || holdCount != 0 {
		t.Fatalf("failed reserve leaked partial writes: account=%+v lot=%+v ledger=%d holds=%d", account, lot, ledgerCount, holdCount)
	}
}

func TestPendingUsageCanBeAuthoritativelyResolvedOnce(t *testing.T) {
	db, cleanup := withPointsFixture(t, 100, 100, false)
	defer cleanup()
	req := PointReserveRequest{UserID: 41, TokenID: 77, LogicalRequestKey: "pending-case", AttemptKey: "attempt-1", AttemptFingerprint: "pending-fp", BudgetMicro: 60, PriceVersion: "test-v1"}
	if _, err := ReservePoints(req); err != nil {
		t.Fatal(err)
	}
	hold, err := RecordPointAttemptUsage(PointAttemptUsage{LogicalRequestKey: req.LogicalRequestKey, AttemptKey: req.AttemptKey, State: "unknown", UsageMicro: 10, UsageSource: "estimated", Authoritative: false, Billable: true})
	if err != nil || hold.State != "pending" {
		t.Fatalf("expected pending hold, got %+v err=%v", hold, err)
	}
	hold, err = RecordPointAttemptUsage(PointAttemptUsage{LogicalRequestKey: req.LogicalRequestKey, AttemptKey: req.AttemptKey, State: "succeeded", UsageMicro: 20, UsageSource: "provider-final", Authoritative: true, Fingerprint: "sha256:test", Billable: true})
	if err != nil || hold.State != "settled" {
		t.Fatalf("expected authoritative settlement, got %+v err=%v", hold, err)
	}
	_, err = RecordPointAttemptUsage(PointAttemptUsage{LogicalRequestKey: req.LogicalRequestKey, AttemptKey: req.AttemptKey, State: "succeeded", UsageMicro: 20, UsageSource: "provider-final", Authoritative: true, Fingerprint: "sha256:test", Billable: true})
	if err != nil {
		t.Fatal(err)
	}
	var account PointAccount
	_ = db.First(&account, "user_id = ?", 41)
	var settleCount int64
	db.Model(&PointLedger{}).Where("kind = ?", "settle").Count(&settleCount)
	if account.AvailableMicro != 80 || account.HeldMicro != 0 || account.SpentMicro != 20 || settleCount != 1 {
		t.Fatalf("duplicate settlement or bad balance: account=%+v settle rows=%d", account, settleCount)
	}
}

func TestExpiredBonusReleaseDoesNotRestoreAvailability(t *testing.T) {
	db, cleanup := withPointsFixture(t, 100, 100, false)
	defer cleanup()
	if err := db.Model(&PointLot{}).Where("business_key = ?", "seed:lot").Updates(map[string]interface{}{"kind": "bonus", "expires_at": time.Now().UTC().Unix() - 60}).Error; err != nil {
		t.Fatal(err)
	}
	var expiredCount int64
	if err := db.Model(&PointLot{}).Where("expires_at IS NOT NULL AND expires_at <= ?", time.Now().UTC().Unix()).Count(&expiredCount).Error; err != nil || expiredCount != 1 {
		t.Fatalf("test setup did not persist expired lot: count=%d err=%v", expiredCount, err)
	}
	req := PointReserveRequest{UserID: 41, TokenID: 77, LogicalRequestKey: "expiry-release", AttemptKey: "a1", AttemptFingerprint: "expiry-fp", BudgetMicro: 50, PriceVersion: "test-v1"}
	if _, err := ReservePoints(req); err == nil {
		t.Fatal("expired lot must not fund a new hold")
	}
	// Seed a hold before expiry, then verify an adjudicated release does not revive the expired lot.
	if err := db.Model(&PointLot{}).Where("business_key = ?", "seed:lot").Update("available_micro", 100).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&PointAccount{}).Where("user_id = ?", 41).Update("available_micro", 100).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&PointLot{}).Where("business_key = ?", "seed:lot").Update("expires_at", time.Now().UTC().Unix()+3600).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ReservePoints(req); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&PointLot{}).Where("business_key = ?", "seed:lot").Update("expires_at", time.Now().UTC().Unix()-60).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := RecordPointAttemptUsage(PointAttemptUsage{LogicalRequestKey: req.LogicalRequestKey, AttemptKey: "a1", State: "unknown", UsageSource: "missing", Authoritative: false}); err != nil {
		t.Fatal(err)
	}
	if err := ResolvePointHold(req.LogicalRequestKey, "provider-decision-1", "release", 0, "provider confirmed no billable usage"); err != nil {
		t.Fatal(err)
	}
	var account PointAccount
	_ = db.First(&account, "user_id = ?", 41)
	var lot PointLot
	_ = db.First(&lot, "business_key = ?", "seed:lot")
	if account.AvailableMicro != 0 || account.HeldMicro != 0 || lot.AvailableMicro != 0 || lot.HeldMicro != 0 {
		t.Fatalf("expired release revived funds: account=%+v lot=%+v", account, lot)
	}
}

func TestPaidOrderCreditIsIdempotent(t *testing.T) {
	db, cleanup := withPointsFixture(t, 0, 100, true)
	defer cleanup()
	paidEvent := "verified-notification-1"
	order := PointPurchaseOrder{OrderKey: "order-1", UserID: 41, AmountFen: 10, PurchaseMicro: 10_000_000, BonusMicro: 2_000_000, State: "paid", PaidEventKey: &paidEvent}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	if err := CreditPaidPointOrder(order.OrderKey, paidEvent); err != nil {
		t.Fatal(err)
	}
	if err := CreditPaidPointOrder(order.OrderKey, paidEvent); err != nil {
		t.Fatal(err)
	}
	var account PointAccount
	_ = db.First(&account, "user_id = ?", 41)
	var lots, credits int64
	db.Model(&PointLot{}).Where("source_ref = ?", order.OrderKey).Count(&lots)
	db.Model(&PointLedger{}).Where("kind = ?", "purchase_credit").Count(&credits)
	if account.AvailableMicro != 12_000_000 || lots != 2 || credits != 1 {
		t.Fatalf("unexpected paid order credit: account=%+v lots=%d credits=%d", account, lots, credits)
	}
}

func TestReviewedUsageAboveHoldRequiresAtomicBudgetTopUp(t *testing.T) {
	db, cleanup := withPointsFixture(t, 100, 100, false)
	defer cleanup()
	req := PointReserveRequest{UserID: 41, TokenID: 77, LogicalRequestKey: "over-budget", AttemptKey: "attempt-1", AttemptFingerprint: "route-fingerprint", BudgetMicro: 50, PriceVersion: "test-v1"}
	if _, err := ReservePoints(req); err != nil {
		t.Fatal(err)
	}
	hold, err := RecordPointAttemptUsage(PointAttemptUsage{LogicalRequestKey: req.LogicalRequestKey, AttemptKey: req.AttemptKey, State: "succeeded", UsageMicro: 80, UsageSource: "provider-final", Authoritative: true, Fingerprint: "usage-fingerprint", Billable: true})
	if err != nil || hold.State != "needs_review" {
		t.Fatalf("usage above hold must wait for review: hold=%+v err=%v", hold, err)
	}
	var before PointAccount
	_ = db.First(&before, "user_id = ?", 41)
	if before.AvailableMicro != 50 || before.HeldMicro != 50 || before.SpentMicro != 0 {
		t.Fatalf("excess usage was silently charged or truncated: %+v", before)
	}
	if err := ResolvePointHold(req.LogicalRequestKey, "review-approve-1", "settle", 80, "provider usage confirmed"); err != nil {
		t.Fatal(err)
	}
	var account PointAccount
	_ = db.First(&account, "user_id = ?", 41)
	var holdAfter PointHold
	_ = db.First(&holdAfter, "logical_request_key = ?", req.LogicalRequestKey)
	if holdAfter.State != "settled" || account.AvailableMicro != 20 || account.HeldMicro != 0 || account.SpentMicro != 80 {
		t.Fatalf("reviewed topup did not settle exact usage: hold=%+v account=%+v", holdAfter, account)
	}
}
