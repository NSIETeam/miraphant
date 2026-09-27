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
	order, err := CreatePointPurchaseOrder(CreatePointPurchaseOrderRequest{UserID: 41, PackageID: "starter", Channel: "wechat", IdempotencyKey: "checkout-attempt-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(order.OrderKey) != 32 || order.PackageVersion != "v2" || order.AmountFen != 500 || order.PurchaseMicro != 500*PointMicroPerPoint || order.State != "created" {
		t.Fatalf("unexpected immutable checkout snapshot: %+v", order)
	}
	replayed, err := CreatePointPurchaseOrder(CreatePointPurchaseOrderRequest{UserID: 41, PackageID: "starter", Channel: "wechat", IdempotencyKey: "checkout-attempt-1"})
	if err != nil || replayed.OrderKey != order.OrderKey {
		t.Fatalf("idempotent order replay changed order: %+v %v", replayed, err)
	}
	if _, err := CreatePointPurchaseOrder(CreatePointPurchaseOrderRequest{UserID: 41, PackageID: "starter", Channel: "alipay", IdempotencyKey: "checkout-attempt-1"}); !errors.Is(err, ErrPointsConflict) {
		t.Fatalf("changed checkout params should conflict, got %v", err)
	}
	if err := db.Model(&PointPackage{}).Where("package_id = ? AND version = ?", "starter", "v2").Update("name", "mutated").Error; err == nil {
		t.Fatal("immutable package version was updated")
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
	} else {
		t.Fatalf("unsupported migration fixture version %d", version)
	}
	if err := db.Where("1 = 1").Delete(&PointsSchemaMigration{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&PointsSchemaMigration{Version: version, AppliedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	orderKey := fmt.Sprintf("v%d-order", version)
	if err := db.Table("point_purchase_orders").Create(map[string]any{"order_key": orderKey, "user_id": 41, "amount_fen": 700, "purchase_micro": 700 * PointMicroPerPoint, "bonus_micro": 0, "state": "paid"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&PointAccount{UserID: 91, AvailableMicro: 700_000_000, SpentMicro: 25_000_000}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&PointLedger{UserID: 91, BusinessKey: fmt.Sprintf("v%d-ledger", version), Kind: "migration_fixture", AvailableDelta: 700_000_000, AvailableAfter: 700_000_000, SpentAfter: 25_000_000, Reason: "historical balance"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := MigratePointsSchema(); err != nil {
		t.Fatalf("v%d to v6 migration failed: %v", version, err)
	}
	if !db.Migrator().HasTable(&PointPackage{}) || !db.Migrator().HasTable(&PointActivePackage{}) || !db.Migrator().HasTable(&PaymentEvent{}) || !db.Migrator().HasTable(&PaymentTransaction{}) {
		t.Fatal("v6 payment tables missing")
	}
	for _, column := range []string{"channel", "package_id", "package_version", "package_snapshot", "currency", "bonus_validity_secs", "expires_at", "provider_transaction_id", "provider_merchant_id", "closed_reason", "idempotency_key"} {
		if !db.Migrator().HasColumn(&PointPurchaseOrder{}, column) {
			t.Fatalf("v6 order column missing: %s", column)
		}
	}
	var old PointPurchaseOrder
	if err := db.First(&old, "order_key = ?", orderKey).Error; err != nil {
		t.Fatal(err)
	}
	if old.AmountFen != 700 || old.PurchaseMicro != 700*PointMicroPerPoint || old.State != "paid" {
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
	if account.AvailableMicro != 700_000_000 || account.SpentMicro != 25_000_000 || ledger.AvailableAfter != 700_000_000 || ledger.SpentAfter != 25_000_000 {
		t.Fatalf("historical point balances changed: account=%+v ledger=%+v", account, ledger)
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

func TestPointsSchemaV5AddsTransactionOwnershipTable(t *testing.T) {
	testPointsSchemaUpgradeFrom(t, 5)
}
