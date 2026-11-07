package payments

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/payment/bill"
)

func TestReconciliationMatchesOrderCreatedByVerifiedPaymentProcessor(t *testing.T) {
	db, cleanup := setupPaymentInbox(t, "pending")
	defer cleanup()
	if err := db.AutoMigrate(&model.PointReconciliationBatch{}, &model.PointReconciliationRow{}, &model.PointReconciliationDifference{}, &model.PointReconciliationAction{}); err != nil {
		t.Fatal(err)
	}
	occurredAt := time.Date(2026, 9, 27, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	trade := testTrade("reconciliation-verified-notify", "payment-order-0001", "wx-reconciliation-transaction", 500)
	trade.ProviderOccurredAt = occurredAt
	inbox, err := PersistVerifiedNotification(trade, []byte("verified provider notification source"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := ProcessVerifiedPaymentEvent(inbox.EventID, MerchantIdentity{Provider: "wechat", MerchantID: "merchant-1", AppID: "app-1"})
	if err != nil || result.State != "processed" {
		t.Fatalf("payment processor setup: %+v %v", result, err)
	}
	data := reconciliationWeChatBillForPayment("2026-09-27", "app-1", "merchant-1", "payment-order-0001", "wx-reconciliation-transaction", "5.00")
	day := time.Date(2026, 9, 27, 0, 0, 0, 0, occurredAt.Location())
	statement, err := bill.ParseWeChatTradeBill(data, day, "merchant-1", "app-1")
	if err != nil {
		t.Fatal(err)
	}
	providerDigest := sha1.Sum(data)
	statement.ProviderHashType = "SHA1"
	statement.ProviderHashValue = hex.EncodeToString(providerDigest[:])
	statement.ProviderHashVerified = true
	input := model.PointReconciliationInputFromStatement(statement, "ALL")
	batch, created, err := model.ImportPointReconciliationBill(input, model.ReconciliationSourceKey{KeyID: "test-key", Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil || !created {
		t.Fatalf("import verified event bill: %+v created=%v err=%v", batch, created, err)
	}
	rows, more, err := model.ListPointReconciliationRows(batch.BatchKey, 0, 10)
	if err != nil || more || len(rows) != 1 || rows[0].ReconciliationResult != "matched" {
		t.Fatalf("verified payment row not matched: rows=%+v more=%v err=%v", rows, more, err)
	}
	differences, _, err := model.ListPointReconciliationDifferences(model.PointReconciliationDifferenceFilter{BatchID: batch.ID, Limit: 10})
	if err != nil || len(differences) != 0 {
		t.Fatalf("matched evidence generated differences: %+v %v", differences, err)
	}
}

func reconciliationWeChatBillForPayment(date, appID, merchantID, orderKey, transactionID, amount string) []byte {
	columns := []string{"交易时间", "公众账号ID", "商户号", "特约商户号", "设备号", "微信订单号", "商户订单号", "用户标识", "交易类型", "交易状态", "付款银行", "货币种类", "应结订单金额", "代金券金额", "微信退款单号", "商户退款单号", "退款金额", "充值券退款金额", "退款类型", "退款状态", "商品名称", "商户数据包", "手续费", "费率", "订单金额", "申请退款金额", "费率备注"}
	row := []string{"`" + date + " 12:00:00", "`" + appID, "`" + merchantID, "`0", "`device", "`" + transactionID, "`" + orderKey, "`user", "`NATIVE", "`SUCCESS", "`OTHERS", "`CNY", "`" + amount, "`0.00", "`0", "`0", "`0.00", "`0.00", "`", "`", "`product", "`", "`0.01", "`0.60%", "`" + amount, "`0.00", "`"}
	summary := fmt.Sprintf("总交易单数,应结订单总金额,退款总金额,充值券退款总金额,手续费总金额,订单总金额,申请退款总金额\n`1,`%s,`0.00,`0.00,`0.01,`%s,`0.00\n", amount, amount)
	return []byte(strings.Join(columns, ",") + "\n" + strings.Join(row, ",") + "\n" + summary)
}
