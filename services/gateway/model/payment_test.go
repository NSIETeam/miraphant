package model

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestPointPackageReplayDoesNotRevertActiveVersionAndOrdersSnapshotTerms(t *testing.T) {
	db, cleanup := withPointsFixture(t, 0, 100, true)
	defer cleanup()
	makePackage := func(version, name string) *PointPackage {
		return &PointPackage{PackageID: "starter", Version: version, Name: name, AmountFen: 500, Currency: "CNY", PurchaseMicro: 500 * PointMicroPerPoint}
	}
	v1 := makePackage("v1", "Starter v1")
	if err := CreatePointPackageVersion(v1, 91, "publish-v1", true); err != nil {
		t.Fatal(err)
	}
	v2 := makePackage("v2", "Starter v2")
	if err := CreatePointPackageVersion(v2, 91, "publish-v2", true); err != nil {
		t.Fatal(err)
	}
	if err := CreatePointPackageVersion(v1, 91, "publish-v1", true); err != nil {
		t.Fatalf("idempotent publication replay failed: %v", err)
	}
	var active PointActivePackage
	if err := db.First(&active, "package_id = ?", "starter").Error; err != nil {
		t.Fatal(err)
	}
	if active.Version != "v2" {
		t.Fatalf("old publication replay rolled current version back to %q", active.Version)
	}
	order, err := CreatePointPurchaseOrder(CreatePointPurchaseOrderRequest{UserID: 41, PackageID: "starter", Channel: "wechat", MerchantID: "merchant-1", AppID: "app-1", IdempotencyKey: "checkout-attempt-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(order.OrderKey) != 32 || order.PackageVersion != "v2" || order.AmountFen != 500 || order.PurchaseMicro != 500*PointMicroPerPoint || order.State != "created" {
		t.Fatalf("unexpected immutable checkout snapshot: %+v", order)
	}
	replayed, err := CreatePointPurchaseOrder(CreatePointPurchaseOrderRequest{UserID: 41, PackageID: "starter", Channel: "wechat", MerchantID: "merchant-1", AppID: "app-1", IdempotencyKey: "checkout-attempt-1"})
	if err != nil || replayed.OrderKey != order.OrderKey {
		t.Fatalf("idempotent order replay changed order: %+v %v", replayed, err)
	}
	if _, err := CreatePointPurchaseOrder(CreatePointPurchaseOrderRequest{UserID: 41, PackageID: "starter", Channel: "alipay", MerchantID: "merchant-1", AppID: "app-1", IdempotencyKey: "checkout-attempt-1"}); !errors.Is(err, ErrPointsConflict) {
		t.Fatalf("changed checkout params should conflict, got %v", err)
	}
	if err := db.Model(&PointPackage{}).Where("package_id = ? AND version = ?", "starter", "v2").Update("name", "mutated").Error; err == nil {
		t.Fatal("immutable package version was updated")
	}
}

func TestNewPurchaseOrderRequiresFrozenProviderIdentity(t *testing.T) {
	_, cleanup := withPointsFixture(t, 0, 100, true)
	defer cleanup()
	pkg := &PointPackage{PackageID: "identity-check", Version: "v1", Name: "Identity", AmountFen: 100, Currency: "CNY", PurchaseMicro: 100 * PointMicroPerPoint}
	if err := CreatePointPackageVersion(pkg, 91, "identity-pkg", true); err != nil {
		t.Fatal(err)
	}
	_, err := CreatePointPurchaseOrder(CreatePointPurchaseOrderRequest{UserID: 41, PackageID: pkg.PackageID, Channel: "wechat", IdempotencyKey: "missing-identity"})
	if err == nil {
		t.Fatal("order without a frozen merchant/app identity was accepted")
	}
	var count int64
	DB.Model(&PointPurchaseOrder{}).Count(&count)
	if count != 0 {
		t.Fatalf("invalid order persisted: %d", count)
	}
}

func TestPaymentEventEvidenceIsImmutable(t *testing.T) {
	db, cleanup := withPointsFixture(t, 0, 100, true)
	defer cleanup()
	event := PaymentEvent{Provider: "wechat", ProviderEventID: "notify-1", OrderKey: "order-1", ProviderTransactionID: "wx-transaction-1", Digest: "digest", Payload: `{"status":"SUCCESS"}`, Verification: "verified", State: "received"}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&PaymentEvent{}).Where("id = ?", event.ID).Update("payload", `{"status":"OTHER"}`).Error; err == nil {
		t.Fatal("payment event payload mutation was allowed")
	}
	for _, field := range []string{"payload", "digest", "id"} {
		if err := db.Model(&PaymentEvent{}).Where("id = ?", event.ID).Updates(map[string]any{field: ""}).Error; err == nil {
			t.Fatalf("payment event %s clear/update was allowed", field)
		}
	}
	if err := db.Model(&event).Updates(PaymentEvent{Payload: ""}).Error; err == nil {
		t.Fatal("struct update bypassed payment event transition allowlist")
	}
	if err := db.Save(&event).Error; err == nil {
		t.Fatal("Save bypassed payment event transition allowlist")
	}
	if err := db.Model(&PaymentEvent{}).Where("id = ?", event.ID).Updates(map[string]any{"state": "processed", "error_code": ""}).Error; err != nil {
		t.Fatalf("mutable processing state update rejected: %v", err)
	}
	if err := db.Delete(&event).Error; err == nil {
		t.Fatal("payment event deletion was allowed")
	}
	var saved PaymentEvent
	if err := db.First(&saved, event.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Payload != event.Payload || saved.Digest != event.Digest || saved.State != "processed" {
		t.Fatalf("unexpected durable event state: %+v", saved)
	}
}

func testPointsSchemaUpgradeFrom(t *testing.T, version int) {
	db, cleanup := withPointsFixture(t, 0, 100, true)
	defer cleanup()
	if version == 4 {
		// Simulate the deployed v4 schema by removing every later payment
		// object and order column.
		for _, table := range []any{&PointPackage{}, &PointActivePackage{}, &PaymentEvent{}, &PaymentTransaction{}} {
			if err := db.Migrator().DropTable(table); err != nil {
				t.Fatal(err)
			}
		}
		for _, column := range []string{"idempotency_key", "channel", "package_id", "package_version", "package_snapshot", "currency", "bonus_validity_secs", "expires_at", "provider_transaction_id", "provider_merchant_id", "closed_reason"} {
			if err := db.Migrator().DropColumn(&PointPurchaseOrder{}, column); err != nil {
				t.Fatalf("drop simulated v4 column %s: %v", column, err)
			}
		}
	} else if version == 5 {
		// v5 already contains packages, inbox events, and expanded order terms;
		// v6 adds only transaction ownership.
		if err := db.Migrator().DropTable(&PaymentTransaction{}); err != nil {
			t.Fatal(err)
		}
	} else if version == 6 {
		for _, column := range []string{"provider_app_id", "provider_create_state", "provider_create_started_at", "checkout_snapshot"} {
			if err := db.Migrator().DropColumn(&PointPurchaseOrder{}, column); err != nil {
				t.Fatalf("drop simulated v6 column %s: %v", column, err)
			}
		}
	} else if version == 7 {
		for _, column := range []string{"provider_create_state", "provider_create_started_at", "checkout_snapshot"} {
			if err := db.Migrator().DropColumn(&PointPurchaseOrder{}, column); err != nil {
				t.Fatalf("drop simulated v7 column %s: %v", column, err)
			}
		}
	} else if version == 8 {
		// v8 has payment tables but no refund schema or lot expiry/revocation counters.
	} else {
		t.Fatalf("unsupported migration fixture version %d", version)
	}
	for _, table := range []any{&PointRefundProviderOwner{}, &PointRefundEvidence{}, &PointRefundDecision{}, &PointRefundAllocation{}, &PointRefund{}} {
		if err := db.Migrator().DropTable(table); err != nil {
			t.Fatal(err)
		}
	}
	for _, column := range []string{"revoked_micro", "expired_micro"} {
		if err := db.Migrator().DropColumn(&PointLot{}, column); err != nil {
			t.Fatalf("drop simulated v%d lot column %s: %v", version, column, err)
		}
	}
	if err := db.Where("1 = 1").Delete(&PointsSchemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&PointsSchemaMigration{Version: version, AppliedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	orderKey := fmt.Sprintf("v%d-order", version)
	oldOrder := map[string]any{"order_key": orderKey, "user_id": 41, "amount_fen": 700, "purchase_micro": 700 * PointMicroPerPoint, "bonus_micro": 0, "state": "paid"}
	if version >= 7 {
		oldOrder["provider_app_id"] = ""
	}
	if err := db.Table("point_purchase_orders").Create(oldOrder).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&PointAccount{UserID: 91, AvailableMicro: 700_000_000, HeldMicro: 120_000_000, SpentMicro: 330_000_000}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("point_lots").Create(map[string]any{"id": 9101, "user_id": 91, "business_key": fmt.Sprintf("v%d-purchase", version), "kind": "purchase", "source_ref": "historical", "amount_fen": 1200, "initial_micro": int64(1_200_000_000), "available_micro": int64(700_000_000), "held_micro": int64(100_000_000), "consumed_micro": int64(300_000_000), "refunded_micro": int64(100_000_000)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("point_lots").Create(map[string]any{"id": 9102, "user_id": 91, "business_key": fmt.Sprintf("v%d-bonus-available-expired", version), "kind": "bonus", "source_ref": "historical", "initial_micro": int64(100_000_000), "available_micro": int64(40_000_000), "held_micro": int64(0), "consumed_micro": int64(30_000_000), "expires_at": time.Now().UTC().Add(-time.Hour).Unix()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("point_lots").Create(map[string]any{"id": 9103, "user_id": 91, "business_key": fmt.Sprintf("v%d-bonus-expired-hold-release", version), "kind": "bonus", "source_ref": "historical", "initial_micro": int64(100_000_000), "available_micro": int64(0), "held_micro": int64(0), "consumed_micro": int64(70_000_000), "expires_at": time.Now().UTC().Add(-time.Hour).Unix()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&PointLedger{UserID: 91, BusinessKey: fmt.Sprintf("v%d-ledger", version), Kind: "migration_fixture", AvailableDelta: 700_000_000, AvailableAfter: 700_000_000, HeldAfter: 120_000_000, SpentAfter: 330_000_000, Reason: "historical balance"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := MigratePointsSchema(); err != nil {
		t.Fatalf("v%d to v9 migration failed: %v", version, err)
	}
	if !db.Migrator().HasTable(&PointPackage{}) || !db.Migrator().HasTable(&PointActivePackage{}) || !db.Migrator().HasTable(&PaymentEvent{}) || !db.Migrator().HasTable(&PaymentTransaction{}) || !db.Migrator().HasTable(&PointRefund{}) || !db.Migrator().HasTable(&PointRefundProviderOwner{}) || !db.Migrator().HasTable(&PointRefundEvidence{}) {
		t.Fatal("payment/refund tables missing after current migration")
	}
	if !db.Migrator().HasColumn(&PointRefundEvidence{}, "provider_refund_key") {
		t.Fatal("refund evidence stable provider refund key column missing")
	}
	for _, column := range []string{"channel", "package_id", "package_version", "package_snapshot", "currency", "bonus_validity_secs", "expires_at", "provider_transaction_id", "provider_merchant_id", "provider_app_id", "provider_create_state", "provider_create_started_at", "checkout_snapshot", "closed_reason", "idempotency_key"} {
		if !db.Migrator().HasColumn(&PointPurchaseOrder{}, column) {
			t.Fatalf("current order column missing: %s", column)
		}
	}
	for _, column := range []string{"expired_micro", "revoked_micro"} {
		if !db.Migrator().HasColumn(&PointLot{}, column) {
			t.Fatalf("current lot column missing: %s", column)
		}
	}
	var old PointPurchaseOrder
	if err := db.First(&old, "order_key = ?", orderKey).Error; err != nil {
		t.Fatal(err)
	}
	if old.AmountFen != 700 || old.PurchaseMicro != 700*PointMicroPerPoint || old.State != "paid" || (version == 6 && old.ProviderAppID != "") {
		t.Fatalf("historical order changed: %+v", old)
	}
	var account PointAccount
	var ledger PointLedger
	if err := db.First(&account, "user_id = ?", 91).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&ledger, "business_key = ?", fmt.Sprintf("v%d-ledger", version)).Error; err != nil {
		t.Fatal(err)
	}
	if account.AvailableMicro != 700_000_000 || account.HeldMicro != 120_000_000 || account.SpentMicro != 330_000_000 || ledger.AvailableAfter != 700_000_000 || ledger.HeldAfter != 120_000_000 || ledger.SpentAfter != 330_000_000 {
		t.Fatalf("historical point balances changed: account=%+v ledger=%+v", account, ledger)
	}
	var purchaseLot, availableExpiredLot, releasedExpiredLot PointLot
	if err := db.First(&purchaseLot, "business_key = ?", fmt.Sprintf("v%d-purchase", version)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&availableExpiredLot, "business_key = ?", fmt.Sprintf("v%d-bonus-available-expired", version)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&releasedExpiredLot, "business_key = ?", fmt.Sprintf("v%d-bonus-expired-hold-release", version)).Error; err != nil {
		t.Fatal(err)
	}
	if purchaseLot.ExpiredMicro != 0 || availableExpiredLot.ExpiredMicro != 30_000_000 || releasedExpiredLot.ExpiredMicro != 30_000_000 || purchaseLot.RevokedMicro != 0 || availableExpiredLot.RevokedMicro != 0 || releasedExpiredLot.RevokedMicro != 0 {
		t.Fatalf("historical expired/revoked lot counters wrong: purchase=%+v available_expired=%+v released_expired=%+v", purchaseLot, availableExpiredLot, releasedExpiredLot)
	}
	if err := MigratePointsSchema(); err != nil {
		t.Fatalf("repeat v%d migration failed: %v", version, err)
	}
	var count int64
	db.Model(&PointPurchaseOrder{}).Where("order_key = ?", orderKey).Count(&count)
	if count != 1 {
		t.Fatalf("repeat migration altered old order count: %d", count)
	}
}

func TestPointsSchemaV4UpgradesPaymentTablesAndOrderColumns(t *testing.T) {
	testPointsSchemaUpgradeFrom(t, 4)
}

func TestPointsSchemaV6AndV7UpgradePreservesLegacyOrderIdentityGap(t *testing.T) {
	testPointsSchemaUpgradeFrom(t, 6)
	testPointsSchemaUpgradeFrom(t, 7)
}

func TestPointsSchemaV5AddsTransactionOwnershipTable(t *testing.T) {
	testPointsSchemaUpgradeFrom(t, 5)
}

func TestPointsSchemaV8AddsRefundTablesAndBackfillsLotExpiry(t *testing.T) {
	testPointsSchemaUpgradeFrom(t, 8)
}

func TestPointsSchemaV8RejectsUnexplainedLotDeficitAndRollsBack(t *testing.T) {
	for _, expiry := range []struct {
		name string
		at   *int64
	}{
		{name: "missing"},
		{name: "future", at: ptrInt64(time.Now().UTC().Add(time.Hour).Unix())},
	} {
		t.Run(expiry.name, func(t *testing.T) {
			db, cleanup := withPointsFixture(t, 0, 100, true)
			defer cleanup()
			for _, table := range []any{&PointRefundProviderOwner{}, &PointRefundEvidence{}, &PointRefundDecision{}, &PointRefundAllocation{}, &PointRefund{}} {
				if err := db.Migrator().DropTable(table); err != nil {
					t.Fatal(err)
				}
			}
			for _, column := range []string{"revoked_micro", "expired_micro"} {
				if err := db.Migrator().DropColumn(&PointLot{}, column); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Where("1 = 1").Delete(&PointsSchemaMigration{}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&PointsSchemaMigration{Version: 8, AppliedAt: time.Now().UTC()}).Error; err != nil {
				t.Fatal(err)
			}
			past := time.Now().UTC().Add(-time.Hour).Unix()
			if err := db.Table("point_lots").Create(map[string]any{"id": 9201, "user_id": 41, "business_key": "v8-valid-expired", "kind": "bonus", "initial_micro": int64(10), "available_micro": int64(5), "held_micro": int64(0), "consumed_micro": int64(0), "refunded_micro": int64(0), "expires_at": past}).Error; err != nil {
				t.Fatal(err)
			}
			badLot := map[string]any{"id": 9202, "user_id": 41, "business_key": "v8-unexplained-deficit", "kind": "purchase", "initial_micro": int64(10), "available_micro": int64(5), "held_micro": int64(0), "consumed_micro": int64(0), "refunded_micro": int64(0)}
			if expiry.at != nil {
				badLot["expires_at"] = *expiry.at
			}
			if err := db.Table("point_lots").Create(badLot).Error; err != nil {
				t.Fatal(err)
			}
			if err := MigratePointsSchema(); err == nil {
				t.Fatal("migration accepted an unexplained lot deficit")
			}
			var count int64
			db.Model(&PointsSchemaMigration{}).Where("version = 9").Count(&count)
			if count != 0 {
				t.Fatal("failed migration wrote the v9 marker")
			}
			var valid PointLot
			if err := db.First(&valid, 9201).Error; err != nil {
				t.Fatal(err)
			}
			if valid.ExpiredMicro != 0 {
				t.Fatalf("partial expiry backfill escaped rolled-back transaction: %+v", valid)
			}
		})
	}
}

func ptrInt64(value int64) *int64 { return &value }
