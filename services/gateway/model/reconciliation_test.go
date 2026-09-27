package model

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/songquanpeng/one-api/payment/bill"
	"gorm.io/gorm"
)

func migrateReconciliationTestTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&PointReconciliationBatch{}, &PointReconciliationRow{}, &PointReconciliationDifference{}, &PointReconciliationAction{}); err != nil {
		t.Fatal(err)
	}
}

func reconciliationSource(t *testing.T, date, app, gross, product string) (PointReconciliationBillInput, []byte) {
	t.Helper()
	day, err := time.ParseInLocation("2006-01-02", date, reconTestLocation(t))
	if err != nil {
		t.Fatal(err)
	}
	header := strings.Join([]string{"交易时间", "公众账号ID", "商户号", "特约商户号", "设备号", "微信订单号", "商户订单号", "用户标识", "交易类型", "交易状态", "付款银行", "货币种类", "应结订单金额", "代金券金额", "微信退款单号", "商户退款单号", "退款金额", "充值券退款金额", "退款类型", "退款状态", "商品名称", "商户数据包", "手续费", "费率", "订单金额", "申请退款金额", "费率备注"}, ",")
	row := []string{"`" + date + " 12:00:00", "`" + app, "`merchant-1", "`0", "`device", "`wx-transaction-1", "`order-1", "`user", "`NATIVE", "`SUCCESS", "`OTHERS", "`CNY", "`1.00", "`0.00", "`0", "`0", "`0.00", "`0.00", "`", "`", "`" + product, "`", "`0.01", "`0.60%", "`" + gross, "`0.00", "`"}
	summary := fmt.Sprintf("总交易单数,应结订单总金额,退款总金额,充值券退款总金额,手续费总金额,订单总金额,申请退款总金额\n`1,`1.00,`0.00,`0.00,`0.01,`%s,`0.00\n", gross)
	data := []byte(header + "\n" + strings.Join(row, ",") + "\n" + summary)
	statement, err := bill.ParseWeChatTradeBill(data, day, "merchant-1", "configured-app")
	if err != nil {
		t.Fatal(err)
	}
	statement.ProviderHashType = "SHA1"
	providerHash := sha1.Sum(data)
	statement.ProviderHashValue = hex.EncodeToString(providerHash[:])
	statement.ProviderHashVerified = true
	return PointReconciliationInputFromStatement(statement, "ALL"), data
}

func reconciliationKey() ReconciliationSourceKey {
	return ReconciliationSourceKey{KeyID: "recon-key-v1", Key: []byte("0123456789abcdef0123456789abcdef")}
}

func TestReconciliationImportIsEncryptedImmutableAndIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reconciliation.db")
	db := openPointsTestDB(t, path)
	oldDB := DB
	DB = db
	activeDB := db
	defer func() {
		if sqlDB, err := activeDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		DB = oldDB
	}()
	migrateReconciliationTestTables(t, db)
	input, source := reconciliationSource(t, "2026-09-27", "configured-app", "1.00", "fixture-secret-title")
	first, created, err := ImportPointReconciliationBill(input, reconciliationKey())
	if err != nil || !created {
		t.Fatalf("first import: created=%v err=%v", created, err)
	}
	second, created, err := ImportPointReconciliationBill(input, reconciliationKey())
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("idempotent replay: %+v created=%v err=%v", second, created, err)
	}
	var batch PointReconciliationBatch
	if err := db.First(&batch, first.ID).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(batch.SourceCiphertext), "fixture-secret-title") || string(batch.SourceCiphertext) == string(source) {
		t.Fatal("source bill appears to be stored in plaintext")
	}
	plain, err := DecryptPointReconciliationSource(batch, reconciliationKey())
	if err != nil || string(plain) != string(source) {
		t.Fatalf("source decryption: %v", err)
	}
	if _, err := DecryptPointReconciliationSource(batch, ReconciliationSourceKey{KeyID: "other", Key: reconciliationKey().Key}); err == nil {
		t.Fatal("wrong key ID decrypted source")
	}
	if _, err := DecryptPointReconciliationSource(batch, ReconciliationSourceKey{KeyID: "recon-key-v1", Key: []byte("abcdef0123456789abcdef0123456789")}); err == nil {
		t.Fatal("wrong key decrypted source")
	}
	changedAAD := batch
	changedAAD.BatchKey = strings.Repeat("f", 64)
	if _, err := DecryptPointReconciliationSource(changedAAD, reconciliationKey()); err == nil {
		t.Fatal("modified AAD decrypted source")
	}
	changedCiphertext := batch
	changedCiphertext.SourceCiphertext = append([]byte(nil), batch.SourceCiphertext...)
	changedCiphertext.SourceCiphertext[0] ^= 1
	if _, err := DecryptPointReconciliationSource(changedCiphertext, reconciliationKey()); err == nil {
		t.Fatal("modified ciphertext decrypted source")
	}
	var batches, rows int64
	db.Model(&PointReconciliationBatch{}).Count(&batches)
	db.Model(&PointReconciliationRow{}).Count(&rows)
	if batches != 1 || rows != 1 {
		t.Fatalf("idempotent replay duplicated evidence: batches=%d rows=%d", batches, rows)
	}
	if err := db.Model(&batch).Update("status", "tampered").Error; err == nil {
		t.Fatal("immutable batch accepted update")
	}
	firstSQL, _ := db.DB()
	if err := firstSQL.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openPointsTestDB(t, path)
	activeDB = reopened
	DB = reopened
	migrateReconciliationTestTables(t, reopened)
	var persisted PointReconciliationBatch
	if err := reopened.First(&persisted, first.ID).Error; err != nil {
		t.Fatal(err)
	}
	if plain, err := DecryptPointReconciliationSource(persisted, reconciliationKey()); err != nil || string(plain) != string(source) {
		t.Fatalf("source did not decrypt after reopening DB: %v", err)
	}
	if replayed, created, err := ImportPointReconciliationBill(input, reconciliationKey()); err != nil || created || replayed.ID != first.ID {
		t.Fatalf("same source was not idempotent after reopening DB: %+v created=%v err=%v", replayed, created, err)
	}
}

func TestReconciliationDifferentSourceCreatesVersionAndOpaqueAlipayIsExplicit(t *testing.T) {
	db := openPointsTestDB(t, filepath.Join(t.TempDir(), "reconciliation-versions.db"))
	oldDB := DB
	DB = db
	defer func() { DB = oldDB }()
	migrateReconciliationTestTables(t, db)
	input, _ := reconciliationSource(t, "2026-09-27", "configured-app", "1.00", "first")
	first, created, err := ImportPointReconciliationBill(input, reconciliationKey())
	if err != nil || !created || first.ImportVersion != 1 {
		t.Fatalf("first version: %+v %v", first, err)
	}
	changed, _ := reconciliationSource(t, "2026-09-27", "configured-app", "1.00", "different source")
	second, created, err := ImportPointReconciliationBill(changed, reconciliationKey())
	if err != nil || !created || second.ImportVersion != 2 {
		t.Fatalf("second source version: %+v %v", second, err)
	}
	raw := bill.RawBill{Provider: "alipay", BillDate: "2026-09-27", Timezone: "Asia/Shanghai", FormatVersion: "alipay-trade-raw-unparsed-v1", RequestedMerchantID: "alipay-merchant", RequestedAppID: "alipay-app", Bytes: []byte("opaque-alipay-file")}
	raw.SHA256 = bill.SHA256(raw.Bytes)
	opaque, created, err := ImportPointReconciliationBill(PointReconciliationInputFromRaw(raw, "trade"), reconciliationKey())
	if err != nil || !created || opaque.Status != "unsupported_format" || opaque.RowCount != 0 {
		t.Fatalf("opaque Alipay bill must stay unsupported: %+v created=%v err=%v", opaque, created, err)
	}
}

func TestReconciliationRejectsRowsDetachedFromSourceAndRollsBackAtomically(t *testing.T) {
	db := openPointsTestDB(t, filepath.Join(t.TempDir(), "reconciliation-rollback.db"))
	oldDB := DB
	DB = db
	defer func() { DB = oldDB }()
	migrateReconciliationTestTables(t, db)
	input, _ := reconciliationSource(t, "2026-09-27", "configured-app", "1.00", "source product")
	input.Rows[0].GrossFen++
	if _, _, err := ImportPointReconciliationBill(input, reconciliationKey()); err == nil {
		t.Fatal("detached normalized row was imported")
	}
	input, _ = reconciliationSource(t, "2026-09-27", "configured-app", "1.00", "source product")
	// Force a failure after the batch and row have been inserted; transaction
	// rollback must leave no partial source evidence.
	if err := db.Exec("CREATE TRIGGER fail_reconciliation_difference BEFORE INSERT ON point_reconciliation_differences BEGIN SELECT RAISE(ABORT, 'forced difference failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&PointPurchaseOrder{OrderKey: "order-1", UserID: 1, Channel: "wechat", ProviderMerchantID: "merchant-1", ProviderAppID: "configured-app", ProviderTransactionID: "other-transaction", Currency: "CNY", AmountFen: 100, State: "credited"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := ImportPointReconciliationBill(input, reconciliationKey()); err == nil {
		t.Fatal("trigger failure did not abort import")
	}
	for _, modelValue := range []any{&PointReconciliationBatch{}, &PointReconciliationRow{}, &PointReconciliationDifference{}} {
		var count int64
		if err := db.Model(modelValue).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("partial batch survived rollback: model=%T count=%d err=%v", modelValue, count, err)
		}
	}
}

func TestReconciliationConcurrentSameImportCreatesOneBatch(t *testing.T) {
	db := openPointsTestDB(t, filepath.Join(t.TempDir(), "reconciliation-concurrent.db"))
	oldDB := DB
	DB = db
	defer func() { DB = oldDB }()
	migrateReconciliationTestTables(t, db)
	input, _ := reconciliationSource(t, "2026-09-27", "configured-app", "1.00", "concurrent")
	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	results := make(chan struct {
		created bool
		err     error
	}, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready <- struct{}{}
			<-start
			_, created, err := ImportPointReconciliationBill(input, reconciliationKey())
			results <- struct {
				created bool
				err     error
			}{created, err}
		}()
	}
	<-ready
	<-ready
	close(start)
	wg.Wait()
	close(results)
	createdCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent import: %v", result.err)
		}
		if result.created {
			createdCount++
		}
	}
	var batches, rows int64
	db.Model(&PointReconciliationBatch{}).Count(&batches)
	db.Model(&PointReconciliationRow{}).Count(&rows)
	if createdCount != 1 || batches != 1 || rows != 1 {
		t.Fatalf("concurrent import duplicated evidence: created=%d batches=%d rows=%d", createdCount, batches, rows)
	}
}

func TestReconciliationSchemaV11UpgradeCreatesTables(t *testing.T) {
	db := openPointsTestDB(t, filepath.Join(t.TempDir(), "reconciliation-migrate.db"))
	oldDB := DB
	DB = db
	defer func() { DB = oldDB }()
	if err := db.Create(&PointsSchemaMigration{Version: 11, AppliedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := MigratePointsSchema(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []any{&PointReconciliationBatch{}, &PointReconciliationRow{}, &PointReconciliationDifference{}, &PointReconciliationAction{}} {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("missing v12 table %T", table)
		}
	}
	var current PointsSchemaMigration
	if err := db.Order("version DESC").First(&current).Error; err != nil || current.Version != 12 {
		t.Fatalf("schema marker not advanced: %+v %v", current, err)
	}
	if err := MigratePointsSchema(); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&PointsSchemaMigration{}).Where("version = ?", 12).Count(&count)
	if count != 1 {
		t.Fatalf("schema migration is not idempotent: %d", count)
	}
}

func TestReconciliationMissingProviderUsesExactVerifiedPaymentEventDate(t *testing.T) {
	db := openPointsTestDB(t, filepath.Join(t.TempDir(), "reconciliation-event-date.db"))
	oldDB := DB
	DB = db
	defer func() { DB = oldDB }()
	migrateReconciliationTestTables(t, db)
	date := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	createPaidEvidence := func(orderKey, txID, eventID string, occurred time.Time, state, verification string) {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{"provider_occurred_at": occurred, "order_key": orderKey, "transaction_id": txID, "merchant_id": "merchant-1", "app_id": "configured-app", "amount_fen": 100, "currency": "CNY", "status": "SUCCESS"})
		event := PaymentEvent{Provider: "wechat", ProviderEventID: eventID, OrderKey: orderKey, ProviderTransactionID: txID, Digest: fmt.Sprintf("digest-%s", eventID), Payload: string(payload), Verification: verification, State: state}
		if err := db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
		owner := PaymentTransaction{Provider: "wechat", MerchantID: "merchant-1", AppID: "configured-app", ProviderTransactionID: txID, OrderKey: orderKey, EventID: event.ID}
		if err := db.Create(&owner).Error; err != nil {
			t.Fatal(err)
		}
		paidKey := "wechat:" + eventID
		order := PointPurchaseOrder{OrderKey: orderKey, UserID: 5, Channel: "wechat", ProviderMerchantID: "merchant-1", ProviderAppID: "configured-app", ProviderTransactionID: txID, Currency: "CNY", AmountFen: 100, PurchaseMicro: 100 * PointMicroPerPoint, State: "credited", PaidEventKey: &paidKey}
		if err := db.Create(&order).Error; err != nil {
			t.Fatal(err)
		}
	}
	createPaidEvidence("order-1", "wx-transaction-1", "paid-same-day", date, "processed", "verified")
	createPaidEvidence("order-2", "wx-transaction-2", "paid-other-day", date.AddDate(0, 0, -1), "processed", "verified")
	createPaidEvidence("order-3", "wx-transaction-3", "isolated-event", date, "quarantined", "mismatch")
	// This provider row has the right order/transaction identity but the wrong
	// gross amount. It must not also create a missing-provider finding.
	input, _ := reconciliationSourceFor(t, "2026-09-27", "configured-app", "1.01", "order-1", "wx-transaction-1")
	batch, _, err := ImportPointReconciliationBill(input, reconciliationKey())
	if err != nil {
		t.Fatal(err)
	}
	var differences []PointReconciliationDifference
	if err := db.Where("batch_id = ?", batch.ID).Find(&differences).Error; err != nil {
		t.Fatal(err)
	}
	countMissing, countAmount := 0, 0
	for _, difference := range differences {
		if difference.Classification == "missing_provider" {
			countMissing++
		}
		if difference.Classification == "amount_mismatch" {
			countAmount++
		}
	}
	if countAmount != 1 || countMissing != 0 {
		t.Fatalf("exact existing provider row was misclassified: amount=%d missing=%d", countAmount, countMissing)
	}
	// Empty provider day import finds only exact verified/processed same-day
	// events; cross-day and quarantined events do not become missing-provider.
	emptyInput := reconciliationEmptySource(t, "2026-09-27")
	emptyBatch, _, err := ImportPointReconciliationBill(emptyInput, reconciliationKey())
	if err != nil {
		t.Fatal(err)
	}
	var missing []PointReconciliationDifference
	if err := db.Where("batch_id = ? AND classification = ?", emptyBatch.ID, "missing_provider").Find(&missing).Error; err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0].LocalOrderID == nil {
		t.Fatalf("missing provider inference did not use exact verified same-day evidence: %+v", missing)
	}
}

func TestReconciliationRefundRequiresBothIdentifiersAndKeepsProcessingInformational(t *testing.T) {
	db := openPointsTestDB(t, filepath.Join(t.TempDir(), "reconciliation-refund.db"))
	oldDB := DB
	DB = db
	defer func() { DB = oldDB }()
	migrateReconciliationTestTables(t, db)
	first := PointRefund{RefundKey: "refund-local-1", UserID: 11, OrderKey: "order-refund-1", Channel: "wechat", ProviderMerchantID: "merchant-1", ProviderAppID: "configured-app", ProviderTransactionID: "wx-payment-1", ProviderRefundKey: "merchant-refund-1", Currency: "CNY", AmountFen: 100, State: "succeeded"}
	second := PointRefund{RefundKey: "refund-local-2", UserID: 12, OrderKey: "order-refund-2", Channel: "wechat", ProviderMerchantID: "merchant-1", ProviderAppID: "configured-app", ProviderTransactionID: "wx-payment-2", ProviderRefundKey: "merchant-refund-2", Currency: "CNY", AmountFen: 100, State: "succeeded"}
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	for _, owner := range []PointRefundProviderOwner{{Provider: "wechat", MerchantID: "merchant-1", ProviderRefundID: "wx-refund-1", RefundKey: first.RefundKey}, {Provider: "wechat", MerchantID: "merchant-1", ProviderRefundID: "wx-refund-2", RefundKey: second.RefundKey}} {
		if err := db.Create(&owner).Error; err != nil {
			t.Fatal(err)
		}
	}
	row := bill.Row{Provider: "wechat", Kind: bill.RowRefund, Status: "REFUND", RefundStatus: "PROCESSING", MerchantID: "merchant-1", AppID: "configured-app", OrderKey: first.OrderKey, TransactionID: first.ProviderTransactionID, MerchantRefundKey: first.ProviderRefundKey, ProviderRefundID: "wx-refund-1", Currency: "CNY", RefundRequestedFen: 100, SourceLine: 2, SourceDigest: strings.Repeat("a", 64)}
	finding, err := classifyReconciliationRow(db, PointReconciliationBillInput{Provider: "wechat", MerchantID: "merchant-1", AppID: "configured-app"}, row, map[string]matchFinding{})
	if err != nil || finding.Classification != "historical_processing" || finding.Severity != "informational" || finding.RefundID == nil || *finding.RefundID != first.ID {
		t.Fatalf("processing refund should remain informational: %+v %v", finding, err)
	}
	row.ProviderRefundID = "wx-refund-2" // each number exists, but belongs to a different local refund
	finding, err = classifyReconciliationRow(db, PointReconciliationBillInput{Provider: "wechat", MerchantID: "merchant-1", AppID: "configured-app"}, row, map[string]matchFinding{})
	if err != nil || finding.Classification != "identity_mismatch" {
		t.Fatalf("cross-paired refund identifiers were accepted: %+v %v", finding, err)
	}
	row.MerchantRefundKey = "merchant-refund-missing"
	row.ProviderRefundID = "wx-refund-missing"
	finding, err = classifyReconciliationRow(db, PointReconciliationBillInput{Provider: "wechat", MerchantID: "merchant-1", AppID: "configured-app"}, row, map[string]matchFinding{})
	if err != nil || finding.Classification != "missing_local" {
		t.Fatalf("fully missing refund was not classified missing_local: %+v %v", finding, err)
	}
	row.MerchantRefundKey = first.ProviderRefundKey
	finding, err = classifyReconciliationRow(db, PointReconciliationBillInput{Provider: "wechat", MerchantID: "merchant-1", AppID: "configured-app"}, row, map[string]matchFinding{})
	if err != nil || finding.Classification != "identity_mismatch" {
		t.Fatalf("partial refund identity mismatch was not isolated: %+v %v", finding, err)
	}
}

func TestReconciliationPreservesDuplicateAndOtherAppRows(t *testing.T) {
	db := openPointsTestDB(t, filepath.Join(t.TempDir(), "reconciliation-rows.db"))
	oldDB := DB
	DB = db
	defer func() { DB = oldDB }()
	migrateReconciliationTestTables(t, db)
	input, source := reconciliationSource(t, "2026-09-27", "configured-app", "1.00", "duplicate scope")
	// Keep two duplicate local-scope rows and one other-app row. All three
	// remain separate line evidence; the other app is never attributed locally.
	lines := strings.Split(string(source), "\n")
	rowLine := lines[1]
	otherAppLine := strings.Replace(rowLine, "configured-app", "other-app", 1)
	lines = append(lines[:2], append([]string{rowLine, otherAppLine}, lines[2:]...)...)
	lines[len(lines)-2] = "`3,`3.00,`0.00,`0.00,`0.03,`3.00,`0.00"
	data := []byte(strings.Join(lines, "\n"))
	statement, err := bill.ParseWeChatTradeBill(data, mustParseDate(t, "2026-09-27"), "merchant-1", "configured-app")
	if err != nil {
		t.Fatal(err)
	}
	statement.ProviderHashType = "SHA1"
	h := sha1.Sum(data)
	statement.ProviderHashValue = hex.EncodeToString(h[:])
	statement.ProviderHashVerified = true
	input = PointReconciliationInputFromStatement(statement, "ALL")
	batch, _, err := ImportPointReconciliationBill(input, reconciliationKey())
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := ListPointReconciliationRows(batch.BatchKey, 0, 10)
	if err != nil || len(rows) != 3 {
		t.Fatalf("duplicate evidence rows were collapsed: rows=%d err=%v", len(rows), err)
	}
	var duplicateCount, scopeCount int
	for _, row := range rows {
		if row.ReconciliationResult == "duplicate" {
			duplicateCount++
		}
		if row.ReconciliationResult == "other_scope" {
			scopeCount++
		}
	}
	if duplicateCount != 1 || scopeCount != 1 {
		t.Fatalf("wrong duplicate/scope preservation: duplicate=%d other_scope=%d", duplicateCount, scopeCount)
	}
	var differenceCount int64
	db.Model(&PointReconciliationDifference{}).Where("batch_id = ? AND classification = ?", batch.ID, "other_scope").Count(&differenceCount)
	if differenceCount != 1 {
		t.Fatalf("other app row not independently represented: %d", differenceCount)
	}
}

func TestReconciliationImportedProcessingRefundIsInformational(t *testing.T) {
	db := openPointsTestDB(t, filepath.Join(t.TempDir(), "reconciliation-processing-refund.db"))
	oldDB := DB
	DB = db
	defer func() { DB = oldDB }()
	migrateReconciliationTestTables(t, db)
	refund := PointRefund{RefundKey: "refund-local-processing", UserID: 15, OrderKey: "order-refund-15", Channel: "wechat", ProviderMerchantID: "merchant-1", ProviderAppID: "configured-app", ProviderTransactionID: "wx-paid-15", ProviderRefundKey: "merchant-refund-15", Currency: "CNY", AmountFen: 100, State: "succeeded"}
	if err := db.Create(&refund).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&PointRefundProviderOwner{Provider: "wechat", MerchantID: "merchant-1", ProviderRefundID: "wx-refund-15", RefundKey: refund.RefundKey}).Error; err != nil {
		t.Fatal(err)
	}
	columns := []string{"交易时间", "公众账号ID", "商户号", "特约商户号", "设备号", "微信订单号", "商户订单号", "用户标识", "交易类型", "交易状态", "付款银行", "货币种类", "应结订单金额", "代金券金额", "微信退款单号", "商户退款单号", "退款金额", "充值券退款金额", "退款类型", "退款状态", "商品名称", "商户数据包", "手续费", "费率", "订单金额", "申请退款金额", "费率备注"}
	row := []string{"`2026-09-27 12:00:00", "`configured-app", "`merchant-1", "`0", "`device", "`wx-paid-15", "`order-refund-15", "`user", "`NATIVE", "`REFUND", "`OTHERS", "`CNY", "`0.00", "`0.00", "`wx-refund-15", "`merchant-refund-15", "`0.00", "`0.00", "`ORIGINAL", "`PROCESSING", "`product", "`", "`-0.01", "`0.60%", "`0.00", "`1.00", "`"}
	data := []byte(strings.Join(columns, ",") + "\n" + strings.Join(row, ",") + "\n总交易单数,应结订单总金额,退款总金额,充值券退款总金额,手续费总金额,订单总金额,申请退款总金额\n`1,`0.00,`0.00,`0.00,`-0.01,`0.00,`1.00\n")
	statement, err := bill.ParseWeChatTradeBill(data, mustParseDate(t, "2026-09-27"), "merchant-1", "configured-app")
	if err != nil {
		t.Fatal(err)
	}
	statement.ProviderHashType = "SHA1"
	h := sha1.Sum(data)
	statement.ProviderHashValue = hex.EncodeToString(h[:])
	statement.ProviderHashVerified = true
	batch, _, err := ImportPointReconciliationBill(PointReconciliationInputFromStatement(statement, "ALL"), reconciliationKey())
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := ListPointReconciliationRows(batch.BatchKey, 0, 10)
	if err != nil || len(rows) != 1 || rows[0].ReconciliationResult != "historical_processing" || rows[0].RefundStatus != "PROCESSING" || rows[0].GrossFen != 0 || rows[0].FeeFen != -1 {
		t.Fatalf("processing refund was not kept as point-in-time evidence: %+v %v", rows, err)
	}
	differences, _, err := ListPointReconciliationDifferences(PointReconciliationDifferenceFilter{BatchID: batch.ID, Limit: 10})
	if err != nil || len(differences) != 1 || differences[0].Classification != "historical_processing" || differences[0].Severity != "informational" {
		t.Fatalf("processing refund must not be treated as a confirmed error: %+v %v", differences, err)
	}
}

func reconciliationSourceFor(t *testing.T, date, app, gross, orderKey, transactionID string) (PointReconciliationBillInput, []byte) {
	_, source := reconciliationSource(t, date, app, gross, "custom")
	// Rebuild the source's order/transaction identifiers and summary remains
	// valid because IDs do not affect totals.
	text := strings.Replace(string(source), "wx-transaction-1", transactionID, 1)
	text = strings.Replace(text, "order-1", orderKey, 1)
	statement, err := bill.ParseWeChatTradeBill([]byte(text), mustParseDate(t, date), "merchant-1", "configured-app")
	if err != nil {
		t.Fatal(err)
	}
	statement.ProviderHashType = "SHA1"
	h := sha1.Sum([]byte(text))
	statement.ProviderHashValue = hex.EncodeToString(h[:])
	statement.ProviderHashVerified = true
	return PointReconciliationInputFromStatement(statement, "ALL"), []byte(text)
}

func reconciliationEmptySource(t *testing.T, date string) PointReconciliationBillInput {
	t.Helper()
	columns := strings.Join([]string{"交易时间", "公众账号ID", "商户号", "特约商户号", "设备号", "微信订单号", "商户订单号", "用户标识", "交易类型", "交易状态", "付款银行", "货币种类", "应结订单金额", "代金券金额", "微信退款单号", "商户退款单号", "退款金额", "充值券退款金额", "退款类型", "退款状态", "商品名称", "商户数据包", "手续费", "费率", "订单金额", "申请退款金额", "费率备注"}, ",")
	summary := "总交易单数,应结订单总金额,退款总金额,充值券退款总金额,手续费总金额,订单总金额,申请退款总金额\n`0,`0.00,`0.00,`0.00,`0.00,`0.00,`0.00\n"
	data := []byte(columns + "\n" + summary)
	statement, err := bill.ParseWeChatTradeBill(data, mustParseDate(t, date), "merchant-1", "configured-app")
	if err != nil {
		t.Fatal(err)
	}
	statement.ProviderHashType = "SHA1"
	h := sha1.Sum(data)
	statement.ProviderHashValue = hex.EncodeToString(h[:])
	statement.ProviderHashVerified = true
	return PointReconciliationInputFromStatement(statement, "ALL")
}

func mustParseDate(t *testing.T, value string) time.Time {
	t.Helper()
	date, err := time.ParseInLocation("2006-01-02", value, reconTestLocation(t))
	if err != nil {
		t.Fatal(err)
	}
	return date
}

func reconTestLocation(t *testing.T) *time.Location {
	t.Helper()
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	return location
}

func TestReconciliationValidationAndPagination(t *testing.T) {
	db := openPointsTestDB(t, filepath.Join(t.TempDir(), "reconciliation-pages.db"))
	oldDB := DB
	DB = db
	defer func() { DB = oldDB }()
	migrateReconciliationTestTables(t, db)
	input, _ := reconciliationSource(t, "2026-09-27", "configured-app", "1.00", "page")
	if _, _, err := ImportPointReconciliationBill(input, ReconciliationSourceKey{}); !errors.Is(err, ErrReconciliationDisabled) {
		t.Fatalf("missing key not rejected: %v", err)
	}
	for i := 0; i < 3; i++ {
		changed, _ := reconciliationSource(t, "2026-09-27", "configured-app", "1.00", fmt.Sprintf("page-%d", i))
		if _, _, err := ImportPointReconciliationBill(changed, reconciliationKey()); err != nil {
			t.Fatal(err)
		}
	}
	page, more, err := ListPointReconciliationBatches(PointReconciliationBatchFilter{Limit: 1})
	if err != nil || len(page) != 1 || !more {
		t.Fatalf("limit 1 pagination: rows=%d more=%v err=%v", len(page), more, err)
	}
	page2, more2, err := ListPointReconciliationBatches(PointReconciliationBatchFilter{Limit: 1, BeforeID: page[0].ID})
	if err != nil || len(page2) != 1 || !more2 || page2[0].ID >= page[0].ID {
		t.Fatalf("second page: rows=%d more=%v err=%v", len(page2), more2, err)
	}
	page3, more3, err := ListPointReconciliationBatches(PointReconciliationBatchFilter{Limit: 1, BeforeID: page2[0].ID})
	if err != nil || len(page3) != 1 || more3 || page3[0].ID >= page2[0].ID {
		t.Fatalf("last page: rows=%d more=%v err=%v", len(page3), more3, err)
	}
	if _, more, err := ListPointReconciliationBatches(PointReconciliationBatchFilter{Limit: 1, BeforeID: page3[0].ID}); err != nil || more {
		t.Fatalf("empty page: more=%v err=%v", more, err)
	}
	if _, err := GetPointReconciliationBatch(page[0].BatchKey); err != nil {
		t.Fatalf("metadata lookup failed: %v", err)
	}
	rows, rowMore, err := ListPointReconciliationRows(page[0].BatchKey, 0, 1)
	if err != nil || len(rows) != 1 || rowMore {
		t.Fatalf("single-row limit should return the row without a phantom next page: rows=%d more=%v err=%v", len(rows), rowMore, err)
	}
	for i := 0; i < 3; i++ {
		difference := PointReconciliationDifference{DifferenceKey: fmt.Sprintf("pagination-diff-%d", i), BatchID: page[0].ID, Classification: "missing_local", Severity: "attention", EvidenceFingerprint: strings.Repeat(fmt.Sprint(i), 64)}
		if err := db.Create(&difference).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&PointReconciliationAction{ActionKey: fmt.Sprintf("pagination-action-%d", i), DifferenceID: difference.ID, ActorUserID: 9, Action: "note", Reason: "checked"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	diffPage, diffMore, err := ListPointReconciliationDifferences(PointReconciliationDifferenceFilter{BatchID: page[0].ID, Limit: 1})
	if err != nil || len(diffPage) != 1 || !diffMore {
		t.Fatalf("difference limit-one page: len=%d more=%v err=%v", len(diffPage), diffMore, err)
	}
	diffPage2, diffMore2, err := ListPointReconciliationDifferences(PointReconciliationDifferenceFilter{BatchID: page[0].ID, BeforeID: diffPage[0].ID, Limit: 1})
	if err != nil || len(diffPage2) != 1 || !diffMore2 || diffPage2[0].ID >= diffPage[0].ID {
		t.Fatalf("difference page two: len=%d more=%v err=%v", len(diffPage2), diffMore2, err)
	}
	actionPage, actionMore, err := ListPointReconciliationActions(diffPage[0].ID, 0, 1)
	if err != nil || len(actionPage) != 1 || actionMore {
		t.Fatalf("action limit-one page: len=%d more=%v err=%v", len(actionPage), actionMore, err)
	}
}
