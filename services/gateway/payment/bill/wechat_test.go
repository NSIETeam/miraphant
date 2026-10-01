package bill

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParseWeChatTradeBillKeepsRowsAndVerifiesSummary(t *testing.T) {
	date, err := time.LoadLocation(wechatTimezone)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 9, 27, 0, 0, 0, 0, date)
	data := wechatTestBill(
		wechatPaymentRow("wx-app-other", "1.00", "1.00", "1.00", `title with \\"quote\\" and \\ space`),
		wechatRefundRow("wx-app-configured", "1.00", "1.00", "-1.00"),
		[]string{"2", "1.00", "1.00", "0.00", "0.00", "1.00", "1.00"},
	)
	statement, err := ParseWeChatTradeBill(data, day, "merchant-123", "wx-app-configured")
	if err != nil {
		t.Fatal(err)
	}
	if statement.FormatVersion != "wechat-all-27-column-v1" || statement.SourceSHA256 != SHA256(data) || string(statement.SourceBytes) != string(data) {
		t.Fatalf("statement provenance missing: %+v", statement)
	}
	if statement.RequestedMerchantID != "merchant-123" || statement.RequestedAppID != "wx-app-configured" || len(statement.Rows) != 2 {
		t.Fatalf("unexpected statement identity/row count: %+v", statement)
	}
	if statement.Rows[0].AppID != "wx-app-other" || statement.Rows[0].GrossFen != 100 || statement.Rows[0].SettlementFen != 100 || statement.Rows[0].FeeFen != 100 || statement.Rows[0].SourceDigest == "" {
		t.Fatalf("payment row not preserved as source evidence: %+v", statement.Rows[0])
	}
	refund := statement.Rows[1]
	if refund.Kind != RowRefund || refund.RefundStatus != "PROCESSING" || refund.GrossFen != 0 || refund.RefundFen != 100 || refund.RefundRequestedFen != 100 || refund.FeeFen != -100 {
		t.Fatalf("refund fields were conflated or lost: %+v", refund)
	}
}

func TestParseWeChatBillRejectsMissingOrIncorrectSummary(t *testing.T) {
	header := strings.Join(wechatAllColumns, ",") + "\n"
	row := strings.Join(wechatPaymentRow("app", "100", "100", "0.01", "title"), ",") + "\n"
	validSummary := strings.Join(wechatSummaryColumns, ",") + "\n`1,`1.00,`0.00,`0.00,`0.01,`1.00,`0.00\n"
	day := time.Date(2026, 9, 27, 0, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	if _, err := ParseWeChatTradeBill([]byte(header+row), day, "m", "a"); err == nil {
		t.Fatal("bill without the official summary was accepted as complete")
	}
	if _, err := ParseWeChatTradeBill([]byte(header+row+strings.Replace(validSummary, "1.00", "9.00", 1)), day, "m", "a"); err == nil {
		t.Fatal("mismatched summary was accepted")
	}
	zero := header + strings.Join(wechatSummaryColumns, ",") + "\n`0,`0.00,`0.00,`0.00,`0.00,`0.00,`0.00\n"
	statement, err := ParseWeChatTradeBill([]byte(zero), day, "m", "a")
	if err != nil || len(statement.Rows) != 0 {
		t.Fatalf("explicit zero-row bill rejected: rows=%d err=%v", len(statement.Rows), err)
	}
}

func TestParseWeChatBillRejectsUnknownLegacyAndMalformedRows(t *testing.T) {
	day := time.Date(2026, 9, 27, 0, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	for name, data := range map[string]string{
		"legacy-header":  strings.Replace(strings.Join(wechatAllColumns, ","), "应结订单金额", "总金额", 1) + "\n" + wechatEmptySummary(),
		"unknown-status": strings.Join(wechatAllColumns, ",") + "\n" + strings.Join(wechatPaymentRow("app", "100", "100", "0.01", "title"), ",") + "\n" + wechatEmptySummary(),
	} {
		t.Run(name, func(t *testing.T) {
			if name == "unknown-status" {
				data = strings.Replace(data, "SUCCESS", "SOMETHING_NEW", 1)
			}
			if _, err := ParseWeChatTradeBill([]byte(data), day, "m", "a"); err == nil {
				t.Fatal("unsupported bill was accepted")
			}
		})
	}
}

func TestParseWeChatBillEnforcesDocumentedGrossAndFeeSigns(t *testing.T) {
	day := time.Date(2026, 9, 27, 0, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	validPay := cleanTestWechatRecord(wechatPaymentRow("app", "1.00", "1.00", "0.01", "title"))
	if _, err := parseWeChatRow(validPay, day, 2); err != nil {
		t.Fatalf("valid payment row rejected: %v", err)
	}
	negativePaymentFee := append([]string(nil), validPay...)
	negativePaymentFee[22] = "`-0.01"
	if _, err := parseWeChatRow(negativePaymentFee, day, 2); err == nil {
		t.Fatal("negative payment fee accepted")
	}
	zeroGross := append([]string(nil), validPay...)
	zeroGross[24] = "`0.00"
	if _, err := parseWeChatRow(zeroGross, day, 2); err == nil {
		t.Fatal("zero gross payment accepted")
	}
	validRefund := cleanTestWechatRecord(wechatRefundRow("app", "1.00", "1.00", "-0.01"))
	if _, err := parseWeChatRow(validRefund, day, 2); err != nil {
		t.Fatalf("valid refund row rejected: %v", err)
	}
	wrongRefundGross := append([]string(nil), validRefund...)
	wrongRefundGross[24] = "`1.00"
	if _, err := parseWeChatRow(wrongRefundGross, day, 2); err == nil {
		t.Fatal("nonzero refund order amount accepted")
	}
	positiveRefundFee := append([]string(nil), validRefund...)
	positiveRefundFee[22] = "`0.01"
	if _, err := parseWeChatRow(positiveRefundFee, day, 2); err == nil {
		t.Fatal("positive refund fee accepted")
	}
}

func cleanTestWechatRecord(row []string) []string {
	cleaned := make([]string, len(row))
	for i, value := range row {
		cleaned[i] = cleanWechatCell(value)
	}
	return cleaned
}

func wechatTestBill(rows ...any) []byte {
	// Last argument is the seven-field summary; all preceding arguments are rows.
	var lines []string
	lines = append(lines, strings.Join(wechatAllColumns, ","))
	for _, value := range rows[:len(rows)-1] {
		lines = append(lines, strings.Join(value.([]string), ","))
	}
	lines = append(lines, strings.Join(wechatSummaryColumns, ","))
	values := rows[len(rows)-1].([]string)
	marked := make([]string, len(values))
	for i, value := range values {
		marked[i] = "`" + value
	}
	lines = append(lines, strings.Join(marked, ","))
	return []byte(strings.Join(lines, "\n") + "\n")
}

func wechatPaymentRow(app, gross, settlement, fee, product string) []string {
	row := []string{
		"`2026-09-27 12:00:00", "`" + app, "`merchant-123", "`0", "`device", "`wx-txn-1", "`order-payment-1", "`user",
		"`NATIVE", "`SUCCESS", "`OTHERS", "`CNY", "`" + settlement, "`0.00", "`0", "`0", "`0.00", "`0.00", "`", "`",
		"`" + product, "`", "`" + fee, "`0.60%", "`" + gross, "`0.00", "`",
	}
	return row
}

func wechatRefundRow(app, requested, refund, fee string) []string {
	return []string{
		"`2026-09-27 13:00:00", "`" + app, "`merchant-123", "`0", "`device", "`wx-txn-2", "`order-refund-1", "`user",
		"`NATIVE", "`REFUND", "`OTHERS", "`CNY", "`0.00", "`0.00", "`wx-refund-1", "`merchant-refund-1", "`" + refund,
		"`0.00", "`ORIGINAL", "`PROCESSING", "`title", "`", "`" + fee, "`0.60%", "`0.00", "`" + requested, "`",
	}
}

func wechatEmptySummary() string {
	return fmt.Sprintf("%s\n`0,`0.00,`0.00,`0.00,`0.00,`0.00,`0.00\n", strings.Join(wechatSummaryColumns, ","))
}
