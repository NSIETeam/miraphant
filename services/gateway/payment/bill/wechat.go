package bill

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	wechatTimezone = "Asia/Shanghai"
	maxBillRows    = 250000
)

var wechatAllColumns = []string{
	"交易时间", "公众账号ID", "商户号", "特约商户号", "设备号", "微信订单号", "商户订单号", "用户标识", "交易类型", "交易状态", "付款银行", "货币种类", "应结订单金额", "代金券金额", "微信退款单号", "商户退款单号", "退款金额", "充值券退款金额", "退款类型", "退款状态", "商品名称", "商户数据包", "手续费", "费率", "订单金额", "申请退款金额", "费率备注",
}

var wechatSummaryColumns = []string{
	"总交易单数", "应结订单总金额", "退款总金额", "充值券退款总金额", "手续费总金额", "订单总金额", "申请退款总金额",
}

// ParseWeChatTradeBill parses the documented ALL transaction bill. It accepts
// only the current 27-column shape; older variants and unknown columns fail
// closed so amounts are never assigned by position to an unrecognized format.
func ParseWeChatTradeBill(data []byte, billDate time.Time, merchantID, appID string) (Statement, error) {
	if len(data) == 0 || int64(len(data)) > MaxDownloadedBillBytes || !utf8.Valid(data) {
		return Statement{}, errors.New("invalid WeChat trade bill bytes")
	}
	location, err := time.LoadLocation(wechatTimezone)
	if err != nil {
		return Statement{}, err
	}
	date := billDate.In(location)
	statement := Statement{Provider: "wechat", BillDate: date.Format("2006-01-02"), Timezone: wechatTimezone, RequestedMerchantID: merchantID, RequestedAppID: appID, Rows: make([]Row, 0)}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 256*1024)
	var headerFound, summarySeen bool
	var pendingSummary bool
	var summaryValues []string
	line := 0
	for scanner.Scan() {
		line++
		sourceLine := scanner.Text()
		rawLine := strings.TrimSuffix(sourceLine, "\r")
		record := strings.Split(rawLine, ",")
		for i := range record {
			record[i] = cleanWechatCell(record[i])
		}
		if allEmpty(record) {
			continue
		}
		if !headerFound {
			if equalColumns(record, wechatAllColumns) {
				headerFound = true
				continue
			}
			return Statement{}, errors.New("unsupported WeChat trade bill header")
		}
		if pendingSummary {
			if len(record) != len(wechatSummaryColumns) {
				return Statement{}, errors.New("invalid WeChat trade bill summary row")
			}
			summaryValues = append([]string(nil), record...)
			pendingSummary = false
			continue
		}
		if equalColumns(record, wechatSummaryColumns) {
			if summarySeen {
				return Statement{}, errors.New("duplicate WeChat bill summary")
			}
			summarySeen, pendingSummary = true, true
			continue
		}
		if summarySeen {
			return Statement{}, errors.New("unexpected records after WeChat bill summary")
		}
		if len(record) != len(wechatAllColumns) {
			return Statement{}, fmt.Errorf("unsupported WeChat bill row width at record %d", line)
		}
		if len(statement.Rows) >= maxBillRows {
			return Statement{}, errors.New("WeChat trade bill has too many rows")
		}
		row, parseErr := parseWeChatRow(record, date, line)
		if parseErr != nil {
			return Statement{}, fmt.Errorf("invalid WeChat bill row %d: %w", line, parseErr)
		}
		digest := sha256.Sum256([]byte(sourceLine))
		row.SourceDigest = hex.EncodeToString(digest[:])
		statement.Rows = append(statement.Rows, row)
	}
	if err := scanner.Err(); err != nil {
		return Statement{}, errors.New("WeChat bill line is malformed or too long")
	}
	if !headerFound || !summarySeen || pendingSummary {
		return Statement{}, errors.New("incomplete WeChat trade bill")
	}
	if err := validateWeChatSummary(summaryValues, statement.Rows); err != nil {
		return Statement{}, err
	}
	statement.FormatVersion = "wechat-all-27-column-v1"
	statement.SourceSHA256 = SHA256(data)
	statement.SourceBytes = append([]byte(nil), data...)
	return statement, nil
}

func parseWeChatRow(record []string, billDate time.Time, line int) (Row, error) {
	text := func(index int) string { return strings.TrimSpace(record[index]) }
	if text(1) == "" || text(2) == "" || text(11) != "CNY" {
		return Row{}, errors.New("missing bill identity or unsupported currency")
	}
	when, err := time.ParseInLocation("2006-01-02 15:04:05", text(0), billDate.Location())
	if err != nil || when.Format("2006-01-02") != billDate.Format("2006-01-02") {
		return Row{}, errors.New("invalid transaction time or bill date")
	}
	row := Row{Provider: "wechat", MerchantID: text(2), AppID: text(1), OrderKey: text(6), TransactionID: text(5), Currency: "CNY", Status: text(9), RefundStatus: text(19), OccurredAt: when, SourceLine: line}
	if row.OrderKey == "" || row.TransactionID == "" {
		return Row{}, errors.New("missing order or provider transaction ID")
	}
	row.GrossFen, err = ParseFen(text(24), false)
	if err != nil {
		return Row{}, fmt.Errorf("invalid gross amount: %w", err)
	}
	row.SettlementFen, err = ParseFen(text(12), false)
	if err != nil {
		return Row{}, fmt.Errorf("invalid settlement amount: %w", err)
	}
	row.DiscountFen, err = ParseFen(text(13), false)
	if err != nil {
		return Row{}, fmt.Errorf("invalid discount amount: %w", err)
	}
	row.RefundFen, err = ParseFen(text(16), false)
	if err != nil {
		return Row{}, fmt.Errorf("invalid refund amount: %w", err)
	}
	row.CouponRefundFen, err = ParseFen(text(17), false)
	if err != nil {
		return Row{}, fmt.Errorf("invalid coupon refund amount: %w", err)
	}
	row.RefundRequestedFen, err = ParseFen(text(25), false)
	if err != nil {
		return Row{}, fmt.Errorf("invalid requested refund amount: %w", err)
	}
	row.FeeFen, err = ParseFen(text(22), true)
	if err != nil {
		return Row{}, fmt.Errorf("invalid fee amount: %w", err)
	}
	switch row.Status {
	case "SUCCESS":
		row.Kind = RowPayment
		if row.GrossFen <= 0 || text(14) != "0" || text(15) != "0" || row.RefundFen != 0 || row.CouponRefundFen != 0 || row.RefundRequestedFen != 0 || row.RefundStatus != "" || row.FeeFen < 0 {
			return Row{}, errors.New("payment row contains refund fields")
		}
	case "REFUND":
		row.Kind = RowRefund
		row.ProviderRefundID = text(14)
		row.MerchantRefundKey = text(15)
		if row.ProviderRefundID == "" || row.ProviderRefundID == "0" || row.MerchantRefundKey == "" || row.MerchantRefundKey == "0" {
			return Row{}, errors.New("refund row is missing refund identifiers")
		}
		if !knownWechatRefundStatus(row.RefundStatus) {
			return Row{}, errors.New("unknown WeChat refund status")
		}
		if row.GrossFen != 0 || row.SettlementFen != 0 || row.DiscountFen != 0 || row.RefundRequestedFen <= 0 || row.RefundFen > row.RefundRequestedFen || row.FeeFen > 0 {
			return Row{}, errors.New("invalid WeChat refund amount fields")
		}
	case "REVOKED":
		row.Kind = RowReversal
		if row.RefundStatus != "" && !knownWechatRefundStatus(row.RefundStatus) {
			return Row{}, errors.New("unknown WeChat reversal refund status")
		}
		if row.GrossFen != 0 || row.SettlementFen != 0 || row.DiscountFen != 0 || row.RefundRequestedFen <= 0 || row.RefundFen > row.RefundRequestedFen || row.FeeFen > 0 {
			return Row{}, errors.New("invalid WeChat reversal amount fields")
		}
	default:
		return Row{}, errors.New("unknown WeChat transaction status")
	}
	return row, nil
}

func knownWechatRefundStatus(value string) bool {
	switch value {
	case "SUCCESS", "PROCESSING", "FAIL", "CHANGE":
		return true
	default:
		return false
	}
}

func validateWeChatSummary(values []string, rows []Row) error {
	count, err := parseBillCount(values[0])
	if err != nil || count != int64(len(rows)) {
		return errors.New("WeChat bill summary row count does not match details")
	}
	var settlement, refund, couponRefund, fee, gross, request int64
	for _, row := range rows {
		if !addFen(&settlement, row.SettlementFen) || !addFen(&refund, row.RefundFen) || !addFen(&couponRefund, row.CouponRefundFen) || !addFen(&fee, row.FeeFen) || !addFen(&gross, row.GrossFen) || !addFen(&request, row.RefundRequestedFen) {
			return errors.New("WeChat bill summary amount overflow")
		}
	}
	expected := []int64{settlement, refund, couponRefund, fee, gross, request}
	for i, want := range expected {
		got, parseErr := ParseFen(values[i+1], true)
		if parseErr != nil || got != want {
			return errors.New("WeChat bill summary does not match detail rows")
		}
	}
	return nil
}

func parseBillCount(value string) (int64, error) {
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, errors.New("invalid bill row count")
	}
	if len(parts) == 2 && strings.Trim(parts[1], "0") != "" {
		return 0, errors.New("non-integer bill row count")
	}
	for _, digit := range parts[0] {
		if digit < '0' || digit > '9' {
			return 0, errors.New("invalid bill row count")
		}
	}
	return strconv.ParseInt(parts[0], 10, 64)
}

func addFen(sum *int64, value int64) bool {
	if value > 0 && *sum > math.MaxInt64-value || value < 0 && *sum < math.MinInt64-value {
		return false
	}
	*sum += value
	return true
}

func cleanWechatCell(value string) string {
	value = strings.TrimSuffix(value, "\r")
	if strings.HasPrefix(value, "`") {
		value = value[1:]
	}
	return value
}

func equalColumns(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	for i := range expected {
		if strings.TrimSpace(actual[i]) != expected[i] {
			return false
		}
	}
	return true
}

func allEmpty(record []string) bool {
	for _, value := range record {
		if strings.TrimSpace(value) != "" {
			return false
		}
	}
	return true
}
