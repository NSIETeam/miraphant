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
)

func pointRefundFixture(t *testing.T) (*gorm.DB, func(), PointPurchaseOrder) {
	t.Helper()
	db, cleanup := withPointsFixture(t, 0, 500_000_000, true)
	orderKey := "refund-order-0001"
	expires := time.Now().UTC().Add(24 * time.Hour).Unix()
	order := PointPurchaseOrder{
		OrderKey: orderKey, UserID: 41, Channel: "wechat", PackageID: "starter", PackageVersion: "v1",
		Currency: "CNY", AmountFen: 1000, PurchaseMicro: 1_000_000_000, BonusMicro: 200_000_000,
		BonusExpiresAt: &expires, ProviderTransactionID: "wx-trade-0001", ProviderMerchantID: "merchant-1",
		ProviderAppID: "app-1", State: "credited",
	}
	if err := db.Create(&order).Error; err != nil {
		cleanup()
		t.Fatal(err)
	}
	purchase := PointLot{UserID: 41, BusinessKey: "purchase:" + orderKey, Kind: "purchase", SourceRef: orderKey, AmountFen: 1000, InitialMicro: 1_000_000_000, AvailableMicro: 1_000_000_000}
	bonus := PointLot{UserID: 41, BusinessKey: "bonus:" + orderKey, Kind: "bonus", SourceRef: orderKey, InitialMicro: 200_000_000, AvailableMicro: 200_000_000, ExpiresAt: &expires}
	if err := db.Create(&purchase).Error; err != nil {
		cleanup()
		t.Fatal(err)
	}
	if err := db.Create(&bonus).Error; err != nil {
		cleanup()
		t.Fatal(err)
	}
	if err := db.Model(&PointAccount{}).Where("user_id = ?", 41).Update("available_micro", 1_200_000_000).Error; err != nil {
		cleanup()
		t.Fatal(err)
	}
	if err := db.Create(&PointLedger{UserID: 41, BusinessKey: "credit:" + orderKey, Kind: "purchase_credit", AvailableDelta: 1_200_000_000, AvailableAfter: 1_200_000_000, Reason: "fixture"}).Error; err != nil {
		cleanup()
		t.Fatal(err)
	}
	return db, cleanup, order
}

func refundRequest(order PointPurchaseOrder, idempotency, refundKey, providerKey string, amountFen int64) PointRefundRequest {
	return PointRefundRequest{UserID: order.UserID, OrderKey: order.OrderKey, IdempotencyKey: idempotency, RefundKey: refundKey, ProviderRefundKey: providerKey, AmountFen: amountFen, Reason: "客户申请部分退款"}
}

func refundEvidence(refund *PointRefund, evidenceKey, outcome, providerID, digest string) PointRefundEvidenceInput {
	return PointRefundEvidenceInput{
		EvidenceKey: evidenceKey, RefundKey: refund.RefundKey, Provider: refund.Channel, Outcome: outcome,
		ProviderRefundID: providerID, ProviderRefundKey: refund.ProviderRefundKey, OrderKey: refund.OrderKey,
		ProviderTransactionID: refund.ProviderTransactionID, MerchantID: refund.ProviderMerchantID, AppID: refund.ProviderAppID,
		AmountFen: refund.AmountFen, TotalFen: refund.OriginalAmountFen, Currency: refund.Currency, Digest: digest,
	}
}

func TestPointRefundReserveApproveSuccessIdempotencyAndWallet(t *testing.T) {
	db, cleanup, order := pointRefundFixture(t)
	defer cleanup()
	request := refundRequest(order, "refund-request-1", "refund-key-1", "out-refund-1", 100)
	refund, err := RequestPointRefund(request)
	if err != nil {
		t.Fatal(err)
	}
	if refund.PurchaseMicro != 100_000_000 || refund.BonusRevokeMicro != 20_000_000 || refund.State != "awaiting_review" {
		t.Fatalf("unexpected immutable refund snapshot: %+v", refund)
	}
	replay, err := RequestPointRefund(refundRequest(order, "refund-request-1", "different-generated-id", "different-provider-key", 100))
	if err != nil || replay.ID != refund.ID {
		t.Fatalf("same scoped idempotency key did not return original refund: %+v %v", replay, err)
	}
	if _, err := RequestPointRefund(refundRequest(order, "refund-request-1", "refund-key-1", "out-refund-1", 101)); !errors.Is(err, ErrPointsConflict) {
		t.Fatalf("changed idempotent parameters should conflict: %v", err)
	}
	if _, err := RequestPointRefund(refundRequest(order, "refund-request-2", "refund-key-2", "out-refund-2", 100)); !errors.Is(err, ErrPointRefundInFlight) {
		t.Fatalf("second active refund should be rejected: %v", err)
	}
	var account PointAccount
	if err := db.First(&account, "user_id = ?", 41).Error; err != nil {
		t.Fatal(err)
	}
	if account.AvailableMicro != 1_080_000_000 || account.HeldMicro != 120_000_000 || account.SpentMicro != 0 {
		t.Fatalf("refund reservation changed wrong account fields: %+v", account)
	}
	if err := ApprovePointRefund(refund.RefundKey, "decision-approve-1", 99, "核实退款申请"); err != nil {
		t.Fatal(err)
	}
	evidence := refundEvidence(refund, "refund-event-1", "succeeded", "wx-refund-1", "digest-1")
	if err := RecordPointRefundEvidence(evidence); err != nil {
		t.Fatal(err)
	}
	if err := RecordPointRefundEvidence(evidence); err != nil {
		t.Fatalf("same verified event replay was not idempotent: %v", err)
	}
	evidence.EvidenceKey, evidence.Digest = "refund-event-2", "digest-2"
	if err := RecordPointRefundEvidence(evidence); err != nil {
		t.Fatalf("second signed event for same provider refund should be accepted as evidence: %v", err)
	}
	if err := RecordPointRefundEvidence(refundEvidence(refund, "refund-event-pending-late", "pending", "wx-refund-1", "digest-3")); err != nil {
		t.Fatalf("late pending evidence should be recorded without reverting completed refund: %v", err)
	}
	var completed PointRefund
	if err := db.First(&completed, "refund_key = ?", refund.RefundKey).Error; err != nil {
		t.Fatal(err)
	}
	if completed.State != "succeeded" {
		t.Fatalf("late pending evidence changed completed refund state: %+v", completed)
	}
	var purchase, bonus PointLot
	if err := db.First(&purchase, "business_key = ?", "purchase:"+order.OrderKey).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&bonus, "business_key = ?", "bonus:"+order.OrderKey).Error; err != nil {
		t.Fatal(err)
	}
	if purchase.RefundedMicro != 100_000_000 || purchase.HeldMicro != 0 || bonus.RevokedMicro != 20_000_000 || bonus.HeldMicro != 0 {
		t.Fatalf("refund finalization counters incorrect: purchase=%+v bonus=%+v", purchase, bonus)
	}
	if err := db.First(&account, "user_id = ?", 41).Error; err != nil {
		t.Fatal(err)
	}
	if account.AvailableMicro != 1_080_000_000 || account.HeldMicro != 0 || account.SpentMicro != 0 {
		t.Fatalf("refund settlement changed spend/account availability: %+v", account)
	}
	var ledgerCount int64
	if err := db.Model(&PointLedger{}).Where("business_key = ?", "refund-success:"+refund.RefundKey).Count(&ledgerCount).Error; err != nil || ledgerCount != 1 {
		t.Fatalf("repeated success posted %d settlement ledgers: %v", ledgerCount, err)
	}
	wallet, err := GetPointWallet(41)
	if err != nil {
		t.Fatal(err)
	}
	if wallet.PurchasedTotalMicro != 900_000_000 || wallet.GiftedTotalMicro != 180_000_000 || wallet.SpentMicro != 0 {
		t.Fatalf("wallet totals do not reflect refund while preserving usage: %+v", wallet)
	}
	var budget PointTokenBudget
	if err := db.First(&budget, "token_id = ?", 77).Error; err != nil {
		t.Fatal(err)
	}
	if budget.HeldMicro != 0 || budget.SpentMicro != 0 {
		t.Fatalf("cash refund changed token usage budget: %+v", budget)
	}
}

func TestPointRefundDefiniteFailureAfterGiftExpiryDoesNotReviveGift(t *testing.T) {
	db, cleanup, order := pointRefundFixture(t)
	defer cleanup()
	refund, err := RequestPointRefund(refundRequest(order, "refund-request-expire", "refund-key-expire", "out-refund-expire", 100))
	if err != nil {
		t.Fatal(err)
	}
	if err := ApprovePointRefund(refund.RefundKey, "decision-approve-expire", 99, "核实退款申请"); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&PointLot{}).Where("business_key = ?", "bonus:"+order.OrderKey).Update("expires_at", time.Now().UTC().Add(-time.Minute).Unix()).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&PointPurchaseOrder{}).Where("order_key = ?", order.OrderKey).Update("bonus_expires_at", time.Now().UTC().Add(-time.Minute).Unix()).Error; err != nil {
		t.Fatal(err)
	}
	evidence := refundEvidence(refund, "refund-event-failed", "definite_failed", "wx-refund-failed", "digest-failed")
	if err := RecordPointRefundEvidence(evidence); err != nil {
		t.Fatal(err)
	}
	var account PointAccount
	var purchase, bonus PointLot
	if err := db.First(&account, "user_id = ?", 41).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&purchase, "business_key = ?", "purchase:"+order.OrderKey).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&bonus, "business_key = ?", "bonus:"+order.OrderKey).Error; err != nil {
		t.Fatal(err)
	}
	if account.AvailableMicro != 1_180_000_000 || account.HeldMicro != 0 || account.SpentMicro != 0 || purchase.AvailableMicro != 1_000_000_000 || bonus.AvailableMicro != 180_000_000 || bonus.ExpiredMicro != 20_000_000 || bonus.HeldMicro != 0 {
		t.Fatalf("definite failure incorrectly revived/consumed expired gift: account=%+v purchase=%+v bonus=%+v", account, purchase, bonus)
	}
	if err := validatePointLotConservation(purchase); err != nil {
		t.Fatal(err)
	}
	if err := validatePointLotConservation(bonus); err != nil {
		t.Fatal(err)
	}
	wallet, err := GetPointWallet(41)
	if err != nil {
		t.Fatal(err)
	}
	if wallet.GiftedAvailableMicro != 0 || wallet.AvailableMicro != 1_000_000_000 {
		t.Fatalf("wallet exposed expired bonus as spendable after failed refund: %+v", wallet)
	}
}

func TestPointRefundVerifiedEvidenceBindingAndAbnormalRecovery(t *testing.T) {
	_, cleanup, order := pointRefundFixture(t)
	defer cleanup()
	refund, err := RequestPointRefund(refundRequest(order, "refund-request-recovery", "refund-key-recovery", "out-refund-recovery", 100))
	if err != nil {
		t.Fatal(err)
	}
	if err := ApprovePointRefund(refund.RefundKey, "decision-approve-recovery", 99, "核实退款申请"); err != nil {
		t.Fatal(err)
	}
	bad := refundEvidence(refund, "refund-event-wrong-key", "succeeded", "wx-refund-recovery", "digest-wrong")
	bad.ProviderRefundKey = "another-request-number"
	if err := RecordPointRefundEvidence(bad); err == nil {
		t.Fatal("evidence with wrong provider refund key was accepted")
	}
	missingID := refundEvidence(refund, "refund-event-missing-id", "succeeded", "", "digest-missing")
	if err := RecordPointRefundEvidence(missingID); err == nil {
		t.Fatal("WeChat refund evidence without the protocol refund_id was accepted")
	}
	missingPendingID := refundEvidence(refund, "refund-event-pending-missing-id", "pending", "", "digest-pending-missing")
	if err := RecordPointRefundEvidence(missingPendingID); err == nil {
		t.Fatal("pending WeChat refund evidence without refund_id was accepted")
	}
	abnormal := refundEvidence(refund, "refund-event-abnormal", "abnormal", "wx-refund-recovery", "digest-abnormal")
	if err := RecordPointRefundEvidence(abnormal); err != nil {
		t.Fatal(err)
	}
	pending := refundEvidence(refund, "refund-event-late-pending", "pending", "wx-refund-recovery", "digest-pending")
	if err := RecordPointRefundEvidence(pending); err != nil {
		t.Fatal(err)
	}
	var saved PointRefund
	if err := DB.First(&saved, "refund_key = ?", refund.RefundKey).Error; err != nil {
		t.Fatal(err)
	}
	if saved.State != "needs_manual_review" {
		t.Fatalf("late pending evidence reverted manual review state: %s", saved.State)
	}
	success := refundEvidence(refund, "refund-event-recovered-success", "succeeded", "wx-refund-recovery", "digest-success")
	if err := RecordPointRefundEvidence(success); err != nil {
		t.Fatalf("matching terminal evidence did not recover manual review: %v", err)
	}
	otherID := refundEvidence(refund, "refund-event-other-id", "succeeded", "wx-refund-other", "digest-other")
	if err := RecordPointRefundEvidence(otherID); !errors.Is(err, ErrPointsConflict) {
		t.Fatalf("same refund request accepted a second provider refund id: %v", err)
	}
}

func TestPointRefundDoesNotOverlapUsageSettleOrUnsentRelease(t *testing.T) {
	db, cleanup, order := pointRefundFixture(t)
	defer cleanup()
	refund, err := RequestPointRefund(refundRequest(order, "refund-request-hold", "refund-key-hold", "out-refund-hold", 100))
	if err != nil {
		t.Fatal(err)
	}
	usageHold, err := ReservePoints(PointReserveRequest{UserID: 41, TokenID: 77, LogicalRequestKey: "usage-alongside-refund", AttemptKey: "attempt-settle", LocalRequestID: "local-settle", AttemptFingerprint: "fingerprint-settle", BudgetMicro: 10_000_000, PriceVersion: "test-v1"})
	if err != nil {
		t.Fatal(err)
	}
	settled, err := RecordPointAttemptUsage(PointAttemptUsage{LogicalRequestKey: usageHold.LogicalRequestKey, AttemptKey: "attempt-settle", State: "succeeded", UsageMicro: 1_000_000, UsageSource: "provider", Authoritative: true, Fingerprint: "usage-settle", Billable: true})
	if err != nil || settled.State != "settled" {
		t.Fatalf("usage hold did not settle independently: %+v %v", settled, err)
	}
	unsent, err := ReservePoints(PointReserveRequest{UserID: 41, TokenID: 77, LogicalRequestKey: "usage-unsent-alongside-refund", AttemptKey: "attempt-unsent", LocalRequestID: "local-unsent", AttemptFingerprint: "fingerprint-unsent", BudgetMicro: 10_000_000, PriceVersion: "test-v1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ReleaseUnsentPointHold(unsent.LogicalRequestKey, "尚未发送上游"); err != nil {
		t.Fatal(err)
	}
	if err := RejectPointRefund(refund.RefundKey, "decision-reject-hold", 99, "不予退款"); err != nil {
		t.Fatal(err)
	}
	var account PointAccount
	if err := db.First(&account, "user_id = ?", 41).Error; err != nil {
		t.Fatal(err)
	}
	if account.HeldMicro != 0 || account.AvailableMicro != 1_199_000_000 || account.SpentMicro != 1_000_000 {
		t.Fatalf("usage settle/release crossed refund reservation accounting: %+v", account)
	}
	var budget PointTokenBudget
	if err := db.First(&budget, "token_id = ?", 77).Error; err != nil {
		t.Fatal(err)
	}
	if budget.HeldMicro != 0 || budget.SpentMicro != 1_000_000 {
		t.Fatalf("refund changed usage budget while settling/releasing usage: %+v", budget)
	}
	var saved PointRefund
	if err := db.First(&saved, "refund_key = ?", refund.RefundKey).Error; err != nil {
		t.Fatal(err)
	}
	if saved.State != "rejected" {
		t.Fatalf("unexpected refund state: %s", saved.State)
	}
}

func TestPointRefundAlipayUsesOutRequestNumberAsStableOwner(t *testing.T) {
	db, cleanup, order := pointRefundFixture(t)
	defer cleanup()
	if err := db.Model(&PointPurchaseOrder{}).Where("order_key = ?", order.OrderKey).Update("channel", "alipay").Error; err != nil {
		t.Fatal(err)
	}
	order.Channel = "alipay"
	refund, err := RequestPointRefund(refundRequest(order, "refund-request-ali", "refund-key-ali", "ali-out-request-001", 100))
	if err != nil {
		t.Fatal(err)
	}
	if err := ApprovePointRefund(refund.RefundKey, "decision-approve-ali", 99, "核实退款申请"); err != nil {
		t.Fatal(err)
	}
	evidence := refundEvidence(refund, "ali-refund-event-1", "succeeded", "", "ali-digest-1")
	if err := RecordPointRefundEvidence(evidence); err != nil {
		t.Fatalf("Alipay success using out_request_no was rejected: %v", err)
	}
	evidence.EvidenceKey, evidence.Digest = "ali-refund-event-2", "ali-digest-2"
	if err := RecordPointRefundEvidence(evidence); err != nil {
		t.Fatalf("duplicate Alipay notification was not idempotent: %v", err)
	}
	var owner PointRefundProviderOwner
	if err := db.Where("provider = ? AND merchant_id = ? AND provider_refund_id = ?", "alipay", "merchant-1", "ali-out-request-001").First(&owner).Error; err != nil {
		t.Fatalf("out_request_no was not bound as the channel refund owner: %v", err)
	}
	var count int64
	if err := db.Model(&PointLedger{}).Where("business_key = ?", "refund-success:"+refund.RefundKey).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("Alipay duplicate notifications posted %d refunds: %v", count, err)
	}
}

func TestPointRefundCumulativeBonusTargetUsesWholeOrderRounding(t *testing.T) {
	db, cleanup, order := pointRefundFixture(t)
	defer cleanup()
	order.BonusMicro = 10
	if err := db.Model(&PointPurchaseOrder{}).Where("order_key = ?", order.OrderKey).Update("bonus_micro", 10).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&PointLot{}).Where("business_key = ?", "bonus:"+order.OrderKey).Updates(map[string]any{"initial_micro": 10, "available_micro": 10}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&PointAccount{}).Where("user_id = ?", 41).Update("available_micro", 1_000_000_010).Error; err != nil {
		t.Fatal(err)
	}
	for i, item := range []struct {
		fen   int64
		bonus int64
	}{{333, 3}, {333, 3}, {334, 4}} {
		refund, err := RequestPointRefund(refundRequest(order, fmt.Sprintf("cumulative-idem-%d", i), fmt.Sprintf("cumulative-refund-%d", i), fmt.Sprintf("cumulative-provider-%d", i), item.fen))
		if err != nil {
			t.Fatal(err)
		}
		if refund.BonusRevokeMicro != item.bonus {
			t.Fatalf("refund %d bonus target difference=%d want=%d", i, refund.BonusRevokeMicro, item.bonus)
		}
		if err := ApprovePointRefund(refund.RefundKey, fmt.Sprintf("cumulative-decision-%d", i), 99, "按累计比例审核"); err != nil {
			t.Fatal(err)
		}
		if err := RecordPointRefundEvidence(refundEvidence(refund, fmt.Sprintf("cumulative-event-%d", i), "succeeded", fmt.Sprintf("wx-cumulative-%d", i), fmt.Sprintf("cumulative-digest-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	var bonus PointLot
	if err := db.First(&bonus, "business_key = ?", "bonus:"+order.OrderKey).Error; err != nil {
		t.Fatal(err)
	}
	if bonus.RevokedMicro != 10 || bonus.AvailableMicro != 0 || bonus.HeldMicro != 0 {
		t.Fatalf("cumulative rounding failed to claw back exact full bonus: %+v", bonus)
	}
	var saved PointPurchaseOrder
	if err := db.First(&saved, "order_key = ?", order.OrderKey).Error; err != nil {
		t.Fatal(err)
	}
	if saved.State != "refunded" {
		t.Fatalf("fully refunded order did not reach terminal state: %s", saved.State)
	}
}

func TestPointRefundConcurrentWithUsageSettleAndRelease(t *testing.T) {
	for _, mode := range []string{"settle", "release"} {
		t.Run(mode, func(t *testing.T) {
			oldDB, oldSQLite, oldEnabled := DB, common.UsingSQLite, config.PointsBillingEnabled
			path := filepath.Join(t.TempDir(), mode+"-refund-race.db")
			dbA, dbB := openPointsTestDB(t, path), openPointsTestDB(t, path)
			DB, common.UsingSQLite, config.PointsBillingEnabled = dbA, true, true
			defer func() {
				for _, db := range []*gorm.DB{dbA, dbB} {
					sqlDB, _ := db.DB()
					_ = sqlDB.Close()
				}
				DB, common.UsingSQLite, config.PointsBillingEnabled = oldDB, oldSQLite, oldEnabled
			}()
			if err := dbA.Create(&PointsSchemaMigration{Version: pointsSchemaVersion, AppliedAt: time.Now().UTC()}).Error; err != nil {
				t.Fatal(err)
			}
			seedPointUser(t, dbA, 0, 500_000_000, true)
			order := PointPurchaseOrder{OrderKey: "refund-usage-race-" + mode, UserID: 41, Channel: "wechat", Currency: "CNY", AmountFen: 1000, PurchaseMicro: 1_000_000_000, ProviderTransactionID: "wx-trade-" + mode, ProviderMerchantID: "merchant-1", ProviderAppID: "app-1", State: "credited"}
			if err := dbA.Create(&order).Error; err != nil {
				t.Fatal(err)
			}
			if err := dbA.Create(&PointLot{UserID: 41, BusinessKey: "purchase:" + order.OrderKey, Kind: "purchase", SourceRef: order.OrderKey, AmountFen: 1000, InitialMicro: 1_000_000_000, AvailableMicro: 1_000_000_000}).Error; err != nil {
				t.Fatal(err)
			}
			if err := dbA.Model(&PointAccount{}).Where("user_id = ?", 41).Update("available_micro", 1_000_000_000).Error; err != nil {
				t.Fatal(err)
			}
			logicalKey := "usage-before-refund-" + mode
			hold, err := ReservePoints(PointReserveRequest{UserID: 41, TokenID: 77, LogicalRequestKey: logicalKey, AttemptKey: "attempt-" + mode, LocalRequestID: "local-" + mode, AttemptFingerprint: "fingerprint-" + mode, BudgetMicro: 100_000_000, PriceVersion: "test-v1"})
			if err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			go func() {
				<-start
				_, err := requestPointRefundOn(dbB, refundRequest(order, "refund-idem-"+mode, "refund-key-"+mode, "out-refund-"+mode, 100))
				results <- err
			}()
			go func() {
				<-start
				if mode == "release" {
					results <- ReleaseUnsentPointHold(logicalKey, "尚未发送上游")
				} else {
					_, err := RecordPointAttemptUsage(PointAttemptUsage{LogicalRequestKey: logicalKey, AttemptKey: "attempt-" + mode, State: "succeeded", UsageMicro: 50_000_000, UsageSource: "provider", Authoritative: true, Fingerprint: "usage-" + mode, Billable: true})
					results <- err
				}
			}()
			close(start)
			for i := 0; i < 2; i++ {
				select {
				case err := <-results:
					if err != nil {
						t.Fatalf("concurrent %s/refund operation failed: %v (hold %+v)", mode, err, hold)
					}
				case <-time.After(5 * time.Second):
					t.Fatalf("concurrent %s/refund operations did not finish", mode)
				}
			}
			var account PointAccount
			var lot PointLot
			if err := dbA.First(&account, "user_id = ?", 41).Error; err != nil {
				t.Fatal(err)
			}
			if err := dbA.First(&lot, "business_key = ?", "purchase:"+order.OrderKey).Error; err != nil {
				t.Fatal(err)
			}
			wantSpent := int64(0)
			if mode == "settle" {
				wantSpent = 50_000_000
			}
			if account.AvailableMicro != 900_000_000-wantSpent || account.HeldMicro != 100_000_000 || account.SpentMicro != wantSpent || lot.AvailableMicro+lot.HeldMicro+lot.ConsumedMicro+lot.RefundedMicro+lot.RevokedMicro+lot.ExpiredMicro != lot.InitialMicro {
				t.Fatalf("concurrent %s/refund accounting mismatch: account=%+v lot=%+v", mode, account, lot)
			}
		})
	}
}

func TestPointRefundOperationClaimIsDurableSingleWinnerAndRecoversQueryOnly(t *testing.T) {
	dbA, cleanup, order := pointRefundFixture(t)
	defer cleanup()
	dsn := dbA.Dialector.(*sqlite.Dialector).DSN
	dbB, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlB, _ := dbB.DB()
	sqlB.SetMaxOpenConns(4)
	defer sqlB.Close()
	refund, err := requestPointRefundOn(dbA, refundRequest(order, "claim-idem", "claim-refund", "claim-provider", 100))
	if err != nil {
		t.Fatal(err)
	}
	if err := decidePointRefundOn(dbA, refund.RefundKey, "claim-approve", 99, "approve", "批准"); err != nil {
		t.Fatal(err)
	}
	barrier := make(chan struct{})
	results := make(chan error, 2)
	var winners int
	var winnerToken string
	var mu sync.Mutex
	for _, item := range []struct {
		db    *gorm.DB
		token string
	}{{dbA, "worker-a"}, {dbB, "worker-b"}} {
		item := item
		go func() {
			<-barrier
			_, err := claimPointRefundOperationOn(item.db, refund.RefundKey, item.token, 2_000, 90)
			if err == nil {
				mu.Lock()
				winners++
				winnerToken = item.token
				mu.Unlock()
			}
			results <- err
		}()
	}
	close(barrier)
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if err != nil && !errors.Is(err, ErrPointRefundInFlight) {
				t.Fatalf("losing claim failed for an unexpected reason: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent refund claims did not finish")
		}
	}
	if winners != 1 {
		t.Fatalf("independent SQLite handles claimed apply %d times; winner=%q", winners, winnerToken)
	}
	// A process restart and a caller choosing a shorter lease cannot shorten
	// the persisted owner deadline. At expiry, the only returned operation is
	// query, never a second apply.
	_ = sqlB.Close()
	dbRestart, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sqlDB, _ := dbRestart.DB(); _ = sqlDB.Close() }()
	if _, err := claimPointRefundOperationOn(dbRestart, refund.RefundKey, "early-worker", 2_020, 1); !errors.Is(err, ErrPointRefundInFlight) {
		t.Fatalf("shorter new lease stole a still-live claim: %v", err)
	}
	recovered, err := claimPointRefundOperationOn(dbRestart, refund.RefundKey, "recovery-worker", 2_090, 1)
	if err != nil || recovered.Kind != "query" || recovered.Refund.PriorRefundedFen != 0 || recovered.Refund.OperationToken != "recovery-worker" {
		t.Fatalf("expired apply claim did not recover as query: %+v %v", recovered, err)
	}
}

func TestPointsSchemaV9BackfillsFrozenPriorRefundAmountAndInbox(t *testing.T) {
	db, cleanup, order := pointRefundFixture(t)
	defer cleanup()
	first, err := RequestPointRefund(refundRequest(order, "v9-first", "v9-refund-1", "v9-provider-1", 100))
	if err != nil {
		t.Fatal(err)
	}
	if err := ApprovePointRefund(first.RefundKey, "v9-first-approved", 99, "批准首笔"); err != nil {
		t.Fatal(err)
	}
	if err := RecordPointRefundEvidence(refundEvidence(first, "v9-first-success", "succeeded", "v9-provider-refund-1", "v9-first-digest")); err != nil {
		t.Fatal(err)
	}
	second, err := RequestPointRefund(refundRequest(order, "v9-second", "v9-refund-2", "v9-provider-2", 100))
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"prior_refunded_fen", "operation_token", "operation_kind", "operation_claimed_at", "operation_lease_until"} {
		if err := db.Migrator().DropColumn(&PointRefund{}, column); err != nil {
			t.Fatalf("simulate v9 missing column %s: %v", column, err)
		}
	}
	if err := db.Migrator().DropTable(&PointRefundInbox{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("1 = 1").Delete(&PointsSchemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&PointsSchemaMigration{Version: 9, AppliedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := MigratePointsSchema(); err != nil {
		t.Fatalf("v9 refund orchestration migration failed: %v", err)
	}
	if !db.Migrator().HasTable(&PointRefundInbox{}) || !db.Migrator().HasColumn(&PointRefund{}, "operation_lease_until") {
		t.Fatal("v10 refund orchestration schema is incomplete")
	}
	var firstAfter, secondAfter PointRefund
	if err := db.First(&firstAfter, first.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&secondAfter, second.ID).Error; err != nil {
		t.Fatal(err)
	}
	if firstAfter.PriorRefundedFen != 0 || secondAfter.PriorRefundedFen != 100 || secondAfter.State != "awaiting_review" {
		t.Fatalf("prior amounts were not backfilled from proven successful history: first=%+v second=%+v", firstAfter, secondAfter)
	}
	if err := MigratePointsSchema(); err != nil {
		t.Fatalf("repeat v10 migration failed: %v", err)
	}
	if err := db.First(&secondAfter, second.ID).Error; err != nil || secondAfter.PriorRefundedFen != 100 {
		t.Fatalf("repeat migration changed frozen prior amount: %+v err=%v", secondAfter, err)
	}
}

func TestPointsSchemaV9RejectsUnprovableRefundHistoryWithoutVersionMarker(t *testing.T) {
	db, cleanup, order := pointRefundFixture(t)
	defer cleanup()
	for i, amount := range []int64{100, 100, 900} {
		refund := PointRefund{RefundKey: fmt.Sprintf("v9-unprovable-%d", i), UserID: order.UserID, OrderKey: order.OrderKey, Channel: order.Channel, ProviderMerchantID: order.ProviderMerchantID, ProviderAppID: order.ProviderAppID, ProviderTransactionID: order.ProviderTransactionID, ProviderRefundKey: fmt.Sprintf("v9-unprovable-provider-%d", i), Currency: order.Currency, AmountFen: amount, OriginalAmountFen: order.AmountFen, State: "succeeded", PurchaseMicro: amount * PointMicroPerPoint}
		if err := db.Create(&refund).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, column := range []string{"prior_refunded_fen", "operation_token", "operation_kind", "operation_claimed_at", "operation_lease_until"} {
		if err := db.Migrator().DropColumn(&PointRefund{}, column); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Migrator().DropTable(&PointRefundInbox{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("1 = 1").Delete(&PointsSchemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&PointsSchemaMigration{Version: 9, AppliedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := MigratePointsSchema(); err == nil {
		t.Fatal("migration accepted successful refund history exceeding the original payment")
	}
	var latest PointsSchemaMigration
	if err := db.Order("version DESC").First(&latest).Error; err != nil {
		t.Fatal(err)
	}
	if latest.Version != 9 {
		t.Fatalf("failed migration wrote a new schema marker: %+v", latest)
	}
	var second PointRefund
	if err := db.First(&second, "refund_key = ?", "v9-unprovable-1").Error; err != nil {
		t.Fatal(err)
	}
	if second.PriorRefundedFen != 0 {
		t.Fatalf("partial prior-amount backfill escaped rolled-back transaction: %+v", second)
	}
}

func TestPointsSchemaV10AddsPersistentRecoveryStateWithoutChangingRefundHold(t *testing.T) {
	db, cleanup, order := pointRefundFixture(t)
	defer cleanup()
	refund, err := RequestPointRefund(refundRequest(order, "v10-recovery-idem", "v10-recovery-refund", "v10-provider-key", 100))
	if err != nil {
		t.Fatal(err)
	}
	if err := ApprovePointRefund(refund.RefundKey, "v10-recovery-approve", 99, "批准"); err != nil {
		t.Fatal(err)
	}
	var beforeAccount PointAccount
	if err := db.First(&beforeAccount, "user_id = ?", order.UserID).Error; err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"recovery_action", "recovery_next_at", "recovery_attempts", "recovery_query_failures", "recovery_last_error_code"} {
		if err := db.Migrator().DropColumn(&PointRefund{}, column); err != nil {
			t.Fatalf("simulate v10 missing refund column %s: %v", column, err)
		}
	}
	for _, column := range []string{"retry_same_key", "recovery_action", "recovery_next_at", "recovery_query_failures"} {
		if err := db.Migrator().DropColumn(&PointRefundInbox{}, column); err != nil {
			t.Fatalf("simulate v10 missing inbox column %s: %v", column, err)
		}
	}
	if err := db.Migrator().DropTable(&PointRefundRecoveryCursor{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("1 = 1").Delete(&PointsSchemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&PointsSchemaMigration{Version: 10, AppliedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := MigratePointsSchema(); err != nil {
		t.Fatalf("v10 recovery schema migration failed: %v", err)
	}
	for _, column := range []string{"recovery_action", "recovery_next_at", "recovery_attempts", "recovery_query_failures"} {
		if !db.Migrator().HasColumn(&PointRefund{}, column) {
			t.Fatalf("v11 missing point refund recovery column %s", column)
		}
	}
	if !db.Migrator().HasTable(&PointRefundRecoveryCursor{}) || !db.Migrator().HasColumn(&PointRefundInbox{}, "retry_same_key") {
		t.Fatal("v11 recovery inbox or cursor schema is incomplete")
	}
	var afterRefund PointRefund
	var afterAccount PointAccount
	if err := db.First(&afterRefund, "refund_key = ?", refund.RefundKey).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&afterAccount, "user_id = ?", order.UserID).Error; err != nil {
		t.Fatal(err)
	}
	if afterRefund.State != "approved" || afterRefund.PurchaseMicro != refund.PurchaseMicro || afterRefund.ProviderRefundKey != refund.ProviderRefundKey || afterRefund.ActiveOrderKey == nil || afterRefund.RecoveryAction != "" || afterAccount.AvailableMicro != beforeAccount.AvailableMicro || afterAccount.HeldMicro != beforeAccount.HeldMicro {
		t.Fatalf("v11 migration changed a frozen refund or balance: refund=%+v account=%+v", afterRefund, afterAccount)
	}
}

func TestPointRefundInboxReplayReusesFirstScheduleButRejectsDirectMutation(t *testing.T) {
	db, cleanup, order := pointRefundFixture(t)
	defer cleanup()
	refund, err := RequestPointRefund(refundRequest(order, "inbox-schedule-idem", "inbox-schedule-refund", "inbox-schedule-provider", 100))
	if err != nil {
		t.Fatal(err)
	}
	if err := ApprovePointRefund(refund.RefundKey, "inbox-schedule-approve", 99, "批准"); err != nil {
		t.Fatal(err)
	}
	input := PointRefundInboxInput{EvidenceKey: "stable-evidence-schedule", RefundKey: refund.RefundKey, Provider: "wechat", EvidenceSource: "query", OperationToken: "schedule-token", ProviderRefundKey: refund.ProviderRefundKey, OrderKey: refund.OrderKey, ProviderTransactionID: refund.ProviderTransactionID, MerchantID: refund.ProviderMerchantID, AppID: refund.ProviderAppID, Outcome: "unknown", ProviderStatus: "RESOURCE_NOT_EXISTS", AmountFen: refund.AmountFen, TotalFen: refund.OriginalAmountFen, Currency: "CNY", Digest: "stable-digest", RetrySameKey: true, RecoveryAction: "apply", RecoveryNextAt: 800, RecoveryQueryFailures: 0}
	first, duplicate, err := PersistPointRefundInboxReplaySafe(input)
	if err != nil || duplicate || first.RecoveryAction != "apply" || first.RecoveryNextAt != 800 {
		t.Fatalf("first durable retry decision failed: %+v duplicate=%t err=%v", first, duplicate, err)
	}
	changed := input
	changed.RecoveryNextAt = 900
	if _, _, err := PersistPointRefundInbox(changed); !errors.Is(err, ErrPointsConflict) {
		t.Fatalf("direct same-key scheduling mutation was accepted: %v", err)
	}
	changedRetryDisposition := input
	changedRetryDisposition.RetrySameKey, changedRetryDisposition.RecoveryAction = false, "query"
	if _, _, err := PersistPointRefundInboxReplaySafe(changedRetryDisposition); !errors.Is(err, ErrPointsConflict) {
		t.Fatalf("same evidence changed its idempotent retry permission: %v", err)
	}
	changedEvidence := input
	changedEvidence.AmountFen++
	if _, _, err := PersistPointRefundInboxReplaySafe(changedEvidence); err == nil {
		t.Fatal("same evidence key accepted a changed refund amount")
	}
	replayed, duplicate, err := PersistPointRefundInboxReplaySafe(changed)
	if err != nil || !duplicate || replayed.RecoveryAction != "apply" || replayed.RecoveryNextAt != 800 {
		t.Fatalf("replay replaced first schedule decision: %+v duplicate=%t err=%v", replayed, duplicate, err)
	}
	_ = db
}

func TestPointRefundRequestUsesProviderReasonByteLimits(t *testing.T) {
	t.Run("wechat rejects 81 UTF-8 bytes without reserving points", func(t *testing.T) {
		db, cleanup, order := pointRefundFixture(t)
		defer cleanup()
		reason := strings.Repeat("汉", 27) // 81 bytes, though 27 characters.
		if _, err := RequestPointRefund(PointRefundRequest{UserID: order.UserID, OrderKey: order.OrderKey, IdempotencyKey: "reason-wechat-too-long", RefundKey: "reason-wechat-refund", ProviderRefundKey: "reason-wx-key", AmountFen: 100, Reason: reason}); err == nil {
			t.Fatal("WeChat reason beyond 80 UTF-8 bytes was accepted")
		}
		var account PointAccount
		if err := db.First(&account, "user_id = ?", order.UserID).Error; err != nil {
			t.Fatal(err)
		}
		var count int64
		db.Model(&PointRefund{}).Where("order_key = ?", order.OrderKey).Count(&count)
		if account.AvailableMicro != 1_200_000_000 || account.HeldMicro != 0 || count != 0 {
			t.Fatalf("invalid reason changed the ledger: account=%+v refunds=%d", account, count)
		}
	})
	t.Run("alipay accepts exactly 256 UTF-8 bytes", func(t *testing.T) {
		db, cleanup, order := pointRefundFixture(t)
		defer cleanup()
		order.Channel, order.ProviderMerchantID, order.ProviderAppID, order.ProviderTransactionID = "alipay", "seller-1", "ali-app-1", "ali-trade-reason"
		if err := db.Model(&PointPurchaseOrder{}).Where("order_key = ?", order.OrderKey).Updates(map[string]interface{}{"channel": order.Channel, "provider_merchant_id": order.ProviderMerchantID, "provider_app_id": order.ProviderAppID, "provider_transaction_id": order.ProviderTransactionID}).Error; err != nil {
			t.Fatal(err)
		}
		reason := strings.Repeat("汉", 85) + "a" // 256 UTF-8 bytes.
		if _, err := RequestPointRefund(PointRefundRequest{UserID: order.UserID, OrderKey: order.OrderKey, IdempotencyKey: "reason-alipay-valid", RefundKey: "reason-alipay-valid-refund", ProviderRefundKey: "reason-ali-valid", AmountFen: 100, Reason: reason}); err != nil {
			t.Fatalf("exact Alipay byte limit rejected: %v", err)
		}
	})
	t.Run("alipay rejects 258 UTF-8 bytes without reserving points", func(t *testing.T) {
		db, cleanup, order := pointRefundFixture(t)
		defer cleanup()
		order.Channel, order.ProviderMerchantID, order.ProviderAppID, order.ProviderTransactionID = "alipay", "seller-1", "ali-app-1", "ali-trade-reason"
		if err := db.Model(&PointPurchaseOrder{}).Where("order_key = ?", order.OrderKey).Updates(map[string]interface{}{"channel": order.Channel, "provider_merchant_id": order.ProviderMerchantID, "provider_app_id": order.ProviderAppID, "provider_transaction_id": order.ProviderTransactionID}).Error; err != nil {
			t.Fatal(err)
		}
		reason := strings.Repeat("汉", 86)
		if _, err := RequestPointRefund(PointRefundRequest{UserID: order.UserID, OrderKey: order.OrderKey, IdempotencyKey: "reason-alipay-too-long", RefundKey: "reason-alipay-refund", ProviderRefundKey: "reason-ali-key", AmountFen: 100, Reason: reason}); err == nil {
			t.Fatal("Alipay reason beyond 256 UTF-8 bytes was accepted")
		}
		var account PointAccount
		if err := db.First(&account, "user_id = ?", order.UserID).Error; err != nil {
			t.Fatal(err)
		}
		if account.AvailableMicro != 1_200_000_000 || account.HeldMicro != 0 {
			t.Fatalf("invalid reason changed the ledger: %+v", account)
		}
	})
}

func TestLateNonterminalRefundEvidenceCannotClearNewerOperationClaim(t *testing.T) {
	db, cleanup, order := pointRefundFixture(t)
	defer cleanup()
	claimNow := time.Now().UTC().Unix()
	refund, err := RequestPointRefund(refundRequest(order, "stale-worker-idem", "stale-worker-refund", "stale-worker-provider", 100))
	if err != nil {
		t.Fatal(err)
	}
	if err := ApprovePointRefund(refund.RefundKey, "stale-worker-approve", 99, "批准"); err != nil {
		t.Fatal(err)
	}
	if _, err := ClaimPointRefundOperation(refund.RefundKey, "worker-a", claimNow, 5); err != nil {
		t.Fatal(err)
	}
	// The replacement claim must wait until both the old lease and the
	// persisted recovery backoff have elapsed.
	claimBAt := claimNow + 65
	claimB, err := ClaimPointRefundOperation(refund.RefundKey, "worker-b", claimBAt, 30)
	if err != nil || claimB.Kind != "query" {
		t.Fatalf("expected expired apply claim to become query claim: %+v %v", claimB, err)
	}
	late := refundEvidence(refund, "stale-worker-late-pending", "pending", "wx-refund-stale", "stale-worker-digest")
	late.OperationToken = "worker-a"
	if err := RecordPointRefundEvidence(late); err != nil {
		t.Fatal(err)
	}
	var current PointRefund
	if err := db.First(&current, "refund_key = ?", refund.RefundKey).Error; err != nil {
		t.Fatal(err)
	}
	if current.OperationToken != "worker-b" || current.OperationKind != "query" || current.OperationLeaseUntil != claimBAt+30 {
		t.Fatalf("late old response cleared/replaced the newer claim: %+v", current)
	}
	queryResult := refundEvidence(refund, "stale-worker-query-pending", "pending", "wx-refund-stale", "stale-worker-query-digest")
	queryResult.OperationToken = "worker-b"
	if err := RecordPointRefundEvidence(queryResult); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&current, "refund_key = ?", refund.RefundKey).Error; err != nil {
		t.Fatal(err)
	}
	if current.State != "submitted" || current.OperationToken != "" || current.OperationLeaseUntil != 0 {
		t.Fatalf("current query result did not safely complete its claim: %+v", current)
	}
}

func TestUnknownRecoveryDoesNotDowngradeManualReview(t *testing.T) {
	db, cleanup, order := pointRefundFixture(t)
	defer cleanup()
	refund, err := RequestPointRefund(refundRequest(order, "manual-query-idem", "manual-query-refund", "manual-query-provider", 100))
	if err != nil {
		t.Fatal(err)
	}
	if err := ApprovePointRefund(refund.RefundKey, "manual-query-approve", 99, "批准"); err != nil {
		t.Fatal(err)
	}
	abnormal := refundEvidence(refund, "manual-query-abnormal", "abnormal", "wx-refund-manual", "manual-query-abnormal-digest")
	if err := RecordPointRefundEvidence(abnormal); err != nil {
		t.Fatal(err)
	}
	claim, err := ClaimPointRefundOperation(refund.RefundKey, "manual-query-worker", 4_000, 30)
	if err != nil || claim.Kind != "query" {
		t.Fatalf("manual review should allow query-only recovery: %+v %v", claim, err)
	}
	if err := RecordPointRefundOperationUnknown(refund.RefundKey, "manual-query-worker", time.Unix(4_005, 0)); err != nil {
		t.Fatal(err)
	}
	var current PointRefund
	if err := db.First(&current, "refund_key = ?", refund.RefundKey).Error; err != nil {
		t.Fatal(err)
	}
	if current.State != "needs_manual_review" || current.OperationToken != "" || current.ActiveOrderKey == nil {
		t.Fatalf("unknown recovery changed manual-review state or released funds: %+v", current)
	}
}

func TestPointRefundConcurrentWithPaidOrderCreditAcrossSQLiteConnections(t *testing.T) {
	oldDB, oldSQLite, oldEnabled := DB, common.UsingSQLite, config.PointsBillingEnabled
	path := filepath.Join(t.TempDir(), "refund-vs-credit.db")
	dbA, dbB := openPointsTestDB(t, path), openPointsTestDB(t, path)
	DB, common.UsingSQLite, config.PointsBillingEnabled = dbA, true, true
	defer func() {
		for _, db := range []*gorm.DB{dbA, dbB} {
			sqlDB, _ := db.DB()
			_ = sqlDB.Close()
		}
		DB, common.UsingSQLite, config.PointsBillingEnabled = oldDB, oldSQLite, oldEnabled
	}()
	if err := dbA.Create(&PointsSchemaMigration{Version: pointsSchemaVersion, AppliedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	seedPointUser(t, dbA, 0, 500_000_000, true)
	order := PointPurchaseOrder{OrderKey: "refund-vs-credit-original", UserID: 41, Channel: "wechat", Currency: "CNY", AmountFen: 1000, PurchaseMicro: 1_000_000_000, BonusMicro: 200_000_000, ProviderTransactionID: "wx-trade-original", ProviderMerchantID: "merchant-1", ProviderAppID: "app-1", State: "credited"}
	expires := time.Now().UTC().Add(24 * time.Hour).Unix()
	order.BonusExpiresAt = &expires
	if err := dbA.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	for _, lot := range []PointLot{
		{UserID: 41, BusinessKey: "purchase:" + order.OrderKey, Kind: "purchase", SourceRef: order.OrderKey, AmountFen: 1000, InitialMicro: 1_000_000_000, AvailableMicro: 1_000_000_000},
		{UserID: 41, BusinessKey: "bonus:" + order.OrderKey, Kind: "bonus", SourceRef: order.OrderKey, InitialMicro: 200_000_000, AvailableMicro: 200_000_000, ExpiresAt: &expires},
	} {
		if err := dbA.Create(&lot).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := dbA.Model(&PointAccount{}).Where("user_id = ?", 41).Update("available_micro", 1_200_000_000).Error; err != nil {
		t.Fatal(err)
	}
	paidEventKey := "verified-credit-event"
	creditOrder := PointPurchaseOrder{OrderKey: "new-paid-order", UserID: 41, Channel: "alipay", Currency: "CNY", AmountFen: 500, PurchaseMicro: 500_000_000, ProviderTransactionID: "ali-trade-new", ProviderMerchantID: "merchant-1", ProviderAppID: "app-1", State: "paid", PaidEventKey: &paidEventKey}
	if err := dbA.Create(&creditOrder).Error; err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		_, err := requestPointRefundOn(dbB, refundRequest(order, "refund-credit-idem", "refund-credit-key", "refund-credit-provider", 100))
		results <- err
	}()
	go func() {
		<-start
		results <- CreditPaidPointOrder(creditOrder.OrderKey, paidEventKey)
	}()
	close(start)
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("concurrent refund/paid credit failed: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent refund/paid credit did not finish")
		}
	}
	var account PointAccount
	if err := dbA.First(&account, "user_id = ?", 41).Error; err != nil {
		t.Fatal(err)
	}
	if account.AvailableMicro != 1_580_000_000 || account.HeldMicro != 120_000_000 || account.SpentMicro != 0 {
		t.Fatalf("refund/credit race lost or duplicated account update: %+v", account)
	}
	var credited PointPurchaseOrder
	if err := dbA.First(&credited, "order_key = ?", creditOrder.OrderKey).Error; err != nil {
		t.Fatal(err)
	}
	if credited.State != "credited" {
		t.Fatalf("concurrent paid order was not credited: %s", credited.State)
	}
}

func TestPointRefundUsesActualSQLiteImmediateDatabaseGate(t *testing.T) {
	db, cleanup, order := pointRefundFixture(t)
	defer cleanup()
	if _, err := RequestPointRefund(refundRequest(order, "refund-request-gate", "refund-key-gate", "out-refund-gate", 100)); err != nil {
		t.Fatal(err)
	}
	deferredDSN, err := sqliteDSN(filepath.Join(t.TempDir(), "deferred.db"), 100)
	if err != nil {
		t.Fatal(err)
	}
	deferredDSN = strings.Replace(deferredDSN, "_txlock=immediate", "_txlock=deferred", 1)
	deferredDB, err := gorm.Open(sqlite.Open(deferredDSN), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := deferredDB.AutoMigrate(&PointsSchemaMigration{}, &PointAccount{}, &PointLot{}, &PointLedger{}, &PointPurchaseOrder{}, &PointRefund{}, &PointRefundAllocation{}, &PointRefundDecision{}, &PointRefundEvidence{}, &PointRefundProviderOwner{}, &PointRefundInbox{}, &PointRefundRecoveryCursor{}); err != nil {
		t.Fatal(err)
	}
	if err := deferredDB.Create(&PointsSchemaMigration{Version: pointsSchemaVersion, AppliedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := pointRefundDBReady(deferredDB); !errors.Is(err, ErrPointRefundUnavailable) {
		t.Fatalf("deferred SQLite DSN should disable refunds: %v", err)
	}
	if err := pointRefundDBReady(db); err != nil {
		t.Fatalf("production-style immediate SQLite gate rejected: %v", err)
	}
}

func TestPointRefundConcurrentIdempotentClaimAcrossSQLiteConnections(t *testing.T) {
	oldDB, oldSQLite, oldEnabled := DB, common.UsingSQLite, config.PointsBillingEnabled
	path := filepath.Join(t.TempDir(), "refund-concurrent.db")
	dbA, dbB := openPointsTestDB(t, path), openPointsTestDB(t, path)
	DB, common.UsingSQLite, config.PointsBillingEnabled = dbA, true, true
	defer func() {
		for _, db := range []*gorm.DB{dbA, dbB} {
			sqlDB, _ := db.DB()
			_ = sqlDB.Close()
		}
		DB, common.UsingSQLite, config.PointsBillingEnabled = oldDB, oldSQLite, oldEnabled
	}()
	if err := dbA.Create(&PointsSchemaMigration{Version: pointsSchemaVersion, AppliedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	seedPointUser(t, dbA, 0, 500_000_000, true)
	order := PointPurchaseOrder{OrderKey: "refund-concurrent-order", UserID: 41, Channel: "wechat", Currency: "CNY", AmountFen: 1000, PurchaseMicro: 1_000_000_000, ProviderTransactionID: "wx-trade-concurrent", ProviderMerchantID: "merchant-1", ProviderAppID: "app-1", State: "credited"}
	if err := dbA.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	if err := dbA.Create(&PointLot{UserID: 41, BusinessKey: "purchase:" + order.OrderKey, Kind: "purchase", SourceRef: order.OrderKey, AmountFen: 1000, InitialMicro: 1_000_000_000, AvailableMicro: 1_000_000_000}).Error; err != nil {
		t.Fatal(err)
	}
	if err := dbA.Model(&PointAccount{}).Where("user_id = ?", 41).Update("available_micro", 1_000_000_000).Error; err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, db := range []*gorm.DB{dbA, dbB} {
		i, db := i, db
		go func() {
			<-start
			_, err := requestPointRefundOn(db, refundRequest(order, fmt.Sprintf("concurrent-idem-%d", i), fmt.Sprintf("concurrent-refund-%d", i), fmt.Sprintf("concurrent-provider-%d", i), 100))
			results <- err
		}()
	}
	close(start)
	readResult := func() error {
		t.Helper()
		select {
		case err := <-results:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("refund/reserve race did not finish")
			return nil
		}
	}
	errA, errB := readResult(), readResult()
	successes := 0
	activeConflicts := 0
	for _, err := range []error{errA, errB} {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrPointRefundInFlight) {
			activeConflicts++
		} else {
			t.Fatalf("unexpected concurrent request result: %v", err)
		}
	}
	if successes != 1 || activeConflicts != 1 {
		t.Fatalf("concurrent active refund claim results: success=%d conflict=%d", successes, activeConflicts)
	}
	var account PointAccount
	if err := dbA.First(&account, "user_id = ?", 41).Error; err != nil {
		t.Fatal(err)
	}
	if account.AvailableMicro != 900_000_000 || account.HeldMicro != 100_000_000 {
		t.Fatalf("concurrent refund requests overspent/froze twice: %+v", account)
	}
	var refunds int64
	if err := dbA.Model(&PointRefund{}).Where("active_order_key = ?", order.OrderKey).Count(&refunds).Error; err != nil || refunds != 1 {
		t.Fatalf("active refund claim count=%d err=%v", refunds, err)
	}
}

func TestPointRefundConcurrentReservationAndUsageReserveAcrossSQLiteConnections(t *testing.T) {
	oldDB, oldSQLite, oldEnabled := DB, common.UsingSQLite, config.PointsBillingEnabled
	path := filepath.Join(t.TempDir(), "refund-vs-usage.db")
	dbA, dbB := openPointsTestDB(t, path), openPointsTestDB(t, path)
	DB, common.UsingSQLite, config.PointsBillingEnabled = dbA, true, true
	defer func() {
		for _, db := range []*gorm.DB{dbA, dbB} {
			sqlDB, _ := db.DB()
			_ = sqlDB.Close()
		}
		DB, common.UsingSQLite, config.PointsBillingEnabled = oldDB, oldSQLite, oldEnabled
	}()
	if err := dbA.Create(&PointsSchemaMigration{Version: pointsSchemaVersion, AppliedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	seedPointUser(t, dbA, 0, 2_000_000_000, true)
	order := PointPurchaseOrder{OrderKey: "refund-vs-usage-order", UserID: 41, Channel: "wechat", Currency: "CNY", AmountFen: 1000, PurchaseMicro: 1_000_000_000, ProviderTransactionID: "wx-trade-vs-usage", ProviderMerchantID: "merchant-1", ProviderAppID: "app-1", State: "credited"}
	if err := dbA.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	if err := dbA.Create(&PointLot{UserID: 41, BusinessKey: "purchase:" + order.OrderKey, Kind: "purchase", SourceRef: order.OrderKey, AmountFen: 1000, InitialMicro: 1_000_000_000, AvailableMicro: 1_000_000_000}).Error; err != nil {
		t.Fatal(err)
	}
	if err := dbA.Model(&PointAccount{}).Where("user_id = ?", 41).Update("available_micro", 1_000_000_000).Error; err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		_, err := requestPointRefundOn(dbB, refundRequest(order, "refund-vs-usage-idem", "refund-vs-usage-key", "refund-vs-usage-provider", 100))
		results <- err
	}()
	go func() {
		<-start
		_, err := reservePointsOn(dbA, PointReserveRequest{UserID: 41, TokenID: 77, LogicalRequestKey: "usage-races-refund", AttemptKey: "usage-race-attempt", LocalRequestID: "usage-race-local", AttemptFingerprint: "usage-race-fingerprint", BudgetMicro: 950_000_000, PriceVersion: "test-v1"})
		results <- err
	}()
	close(start)
	readResult := func() error {
		t.Helper()
		select {
		case err := <-results:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("refund/reserve race did not finish")
			return nil
		}
	}
	errA, errB := readResult(), readResult()
	successes, insufficient := 0, 0
	for _, err := range []error{errA, errB} {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrPointsInsufficient) {
			insufficient++
		} else {
			t.Fatalf("unexpected reserve/refund race result: %v", err)
		}
	}
	if successes != 1 || insufficient != 1 {
		t.Fatalf("expected exactly one winner with a 1B balance: success=%d insufficient=%d", successes, insufficient)
	}
	var account PointAccount
	var lot PointLot
	if err := dbA.First(&account, "user_id = ?", 41).Error; err != nil {
		t.Fatal(err)
	}
	if err := dbA.First(&lot, "business_key = ?", "purchase:"+order.OrderKey).Error; err != nil {
		t.Fatal(err)
	}
	if account.AvailableMicro < 0 || account.HeldMicro < 0 || account.AvailableMicro+account.HeldMicro != 1_000_000_000 || lot.AvailableMicro+lot.HeldMicro+lot.ConsumedMicro+lot.RefundedMicro+lot.RevokedMicro+lot.ExpiredMicro != lot.InitialMicro {
		t.Fatalf("concurrent usage/refund broke account or lot conservation: account=%+v lot=%+v", account, lot)
	}
}

func TestPointRefundFinalizationMismatchRollsBackAllWrites(t *testing.T) {
	db, cleanup, order := pointRefundFixture(t)
	defer cleanup()
	refund, err := RequestPointRefund(refundRequest(order, "refund-request-rollback", "refund-key-rollback", "out-refund-rollback", 100))
	if err != nil {
		t.Fatal(err)
	}
	if err := ApprovePointRefund(refund.RefundKey, "decision-approve-rollback", 99, "核实退款申请"); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE point_refund_allocations SET purchase_micro = purchase_micro + 1 WHERE refund_id = ? AND lot_kind = ?", refund.ID, "purchase").Error; err != nil {
		t.Fatal(err)
	}
	if err := RecordPointRefundEvidence(refundEvidence(refund, "refund-event-rollback", "succeeded", "wx-refund-rollback", "digest-rollback")); err == nil {
		t.Fatal("finalization accepted corrupted allocation totals")
	}
	var account PointAccount
	var current PointRefund
	var evidenceCount, ownerCount, successLedgerCount int64
	if err := db.First(&account, "user_id = ?", 41).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&current, "id = ?", refund.ID).Error; err != nil {
		t.Fatal(err)
	}
	db.Model(&PointRefundEvidence{}).Where("evidence_key = ?", "refund-event-rollback").Count(&evidenceCount)
	db.Model(&PointRefundProviderOwner{}).Where("provider_refund_id = ?", "wx-refund-rollback").Count(&ownerCount)
	db.Model(&PointLedger{}).Where("business_key = ?", "refund-success:"+refund.RefundKey).Count(&successLedgerCount)
	if account.AvailableMicro != 1_080_000_000 || account.HeldMicro != 120_000_000 || current.State != "approved" || evidenceCount != 0 || ownerCount != 0 || successLedgerCount != 0 {
		t.Fatalf("failed finalization left partial side effects: account=%+v refund=%+v evidence=%d owner=%d ledger=%d", account, current, evidenceCount, ownerCount, successLedgerCount)
	}
}

func TestPointRefundFinalizationRejectsOverRefundAndChangedOrderIdentity(t *testing.T) {
	for _, mode := range []string{"cumulative-overflow", "order-identity"} {
		t.Run(mode, func(t *testing.T) {
			db, cleanup, order := pointRefundFixture(t)
			defer cleanup()
			refund, err := RequestPointRefund(refundRequest(order, "refund-request-"+mode, "refund-key-"+mode, "out-refund-"+mode, 100))
			if err != nil {
				t.Fatal(err)
			}
			if err := ApprovePointRefund(refund.RefundKey, "decision-"+mode, 99, "核实退款申请"); err != nil {
				t.Fatal(err)
			}
			if mode == "cumulative-overflow" {
				prior := PointRefund{RefundKey: "historical-corrupt-refund", UserID: 41, OrderKey: order.OrderKey, Channel: "wechat", ProviderMerchantID: "merchant-1", ProviderAppID: "app-1", ProviderTransactionID: order.ProviderTransactionID, ProviderRefundKey: "historical-out-refund", Currency: "CNY", AmountFen: 950, PurchaseMicro: 950_000_000, OriginalAmountFen: 1000, OriginalPurchaseMicro: 1_000_000_000, Reason: "historical fixture", State: "succeeded"}
				if err := db.Create(&prior).Error; err != nil {
					t.Fatal(err)
				}
			} else if err := db.Model(&PointPurchaseOrder{}).Where("order_key = ?", order.OrderKey).Update("provider_app_id", "merchant-changed-app").Error; err != nil {
				t.Fatal(err)
			}
			if err := RecordPointRefundEvidence(refundEvidence(refund, "finalize-invalid-"+mode, "succeeded", "wx-refund-"+mode, "digest-"+mode)); err == nil {
				t.Fatal("finalization accepted over-refund total or changed original identity")
			}
			var account PointAccount
			var current PointRefund
			var purchase, bonus PointLot
			if err := db.First(&account, "user_id = ?", 41).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.First(&current, "id = ?", refund.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.First(&purchase, "business_key = ?", "purchase:"+order.OrderKey).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.First(&bonus, "business_key = ?", "bonus:"+order.OrderKey).Error; err != nil {
				t.Fatal(err)
			}
			var evidenceCount, ownerCount, successLedgerCount int64
			db.Model(&PointRefundEvidence{}).Where("evidence_key = ?", "finalize-invalid-"+mode).Count(&evidenceCount)
			db.Model(&PointRefundProviderOwner{}).Where("provider_refund_id = ?", "wx-refund-"+mode).Count(&ownerCount)
			db.Model(&PointLedger{}).Where("business_key = ?", "refund-success:"+refund.RefundKey).Count(&successLedgerCount)
			if account.AvailableMicro != 1_080_000_000 || account.HeldMicro != 120_000_000 || current.State != "approved" || purchase.RefundedMicro != 0 || bonus.RevokedMicro != 0 || evidenceCount != 0 || ownerCount != 0 || successLedgerCount != 0 {
				t.Fatalf("failed finalization left partial state: account=%+v refund=%+v purchase=%+v bonus=%+v evidence=%d owner=%d ledger=%d", account, current, purchase, bonus, evidenceCount, ownerCount, successLedgerCount)
			}
		})
	}
}
