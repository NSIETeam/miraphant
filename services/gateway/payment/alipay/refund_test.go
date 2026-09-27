package alipay

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/songquanpeng/one-api/payment"
)

func refundRequestFixture() payment.RefundRequest {
	return payment.RefundRequest{
		RefundKey: "internal-refund-01", ProviderRefundKey: "request_0001", OrderKey: "order_123456",
		TransactionID: "alipay-trade-01", MerchantID: "seller-test", AppID: "app-test",
		AmountFen: 250, TotalFen: 1000, PriorRefundedFen: 100, Currency: "CNY", Reason: "退款测试",
	}
}

func TestApplyRefundSignsExactRequestAndKeepsCumulativeRefundSeparate(t *testing.T) {
	for _, tc := range []struct {
		fundChange string
		cumulative string
		want       payment.RefundOutcome
	}{
		{"Y", "3.50", payment.RefundSucceeded},
		{"N", "3.50", payment.RefundAccepted},
		{"N", "1.00", payment.RefundAccepted},
		{"", "3.50", payment.RefundAccepted},
	} {
		t.Run("fund_change_"+tc.fundChange, func(t *testing.T) {
			keys := newTestKeys(t)
			request := refundRequestFixture()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				params := parseSignedAPIRequest(t, r, &keys.merchant.PublicKey)
				if params["method"] != "alipay.trade.refund" || params["app_id"] != request.AppID {
					t.Errorf("wrong request method/identity: %+v", params)
				}
				var biz refundRequestBody
				if err := json.Unmarshal([]byte(params["biz_content"]), &biz); err != nil {
					t.Fatal(err)
				}
				if biz.OrderKey != request.OrderKey || biz.TransactionID != request.TransactionID || biz.RefundNumber != request.ProviderRefundKey || biz.Amount != "2.50" || biz.Reason != request.Reason {
					t.Errorf("wrong refund payload: %+v", biz)
				}
				node := []byte(fmt.Sprintf(`{"code":"10000","out_trade_no":%q,"trade_no":%q,"refund_fee":%q,"fund_change":%q}`, request.OrderKey, request.TransactionID, tc.cumulative, tc.fundChange))
				_, _ = w.Write(signAPIResponse(t, keys.alipay, "alipay.trade.refund", node))
			}))
			defer server.Close()
			keys.provider.baseURL = server.URL + "/gateway.do"
			result, err := keys.provider.ApplyRefund(context.Background(), request)
			if err != nil || result.Outcome != tc.want || result.AmountFen != 250 || result.TotalFen != 1000 || result.ProviderRefundKey != request.ProviderRefundKey {
				t.Fatalf("apply result=%+v err=%v", result, err)
			}
		})
	}
}

func TestApplyRefundAcceptsVerifiedZeroPriorCumulativeButDoesNotFinalize(t *testing.T) {
	keys := newTestKeys(t)
	request := refundRequestFixture()
	request.PriorRefundedFen = 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		node := []byte(fmt.Sprintf(`{"code":"10000","out_trade_no":%q,"trade_no":%q,"refund_fee":"0.00","fund_change":"N"}`, request.OrderKey, request.TransactionID))
		_, _ = w.Write(signAPIResponse(t, keys.alipay, "alipay.trade.refund", node))
	}))
	defer server.Close()
	keys.provider.baseURL = server.URL + "/gateway.do"
	result, err := keys.provider.ApplyRefund(context.Background(), request)
	if err != nil || result.Outcome != payment.RefundAccepted || result.AmountFen != request.AmountFen {
		t.Fatalf("zero cumulative apply result=%+v err=%v", result, err)
	}
}

func TestApplyRefundUnknownOnCumulativeMismatchOrBusinessError(t *testing.T) {
	for _, tc := range []struct {
		name string
		node string
	}{
		{"cumulative mismatch", `{"code":"10000","out_trade_no":"order_123456","trade_no":"alipay-trade-01","refund_fee":"9.00","fund_change":"Y"}`},
		{"signed business error after possible earlier timeout", `{"code":"40004","sub_code":"ACQ.TRADE_STATUS_ERROR"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keys := newTestKeys(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(signAPIResponse(t, keys.alipay, "alipay.trade.refund", []byte(tc.node)))
			}))
			defer server.Close()
			keys.provider.baseURL = server.URL + "/gateway.do"
			result, err := keys.provider.ApplyRefund(context.Background(), refundRequestFixture())
			if err == nil || result.Outcome != payment.RefundUnknown {
				t.Fatalf("ambiguous/mismatched response classified terminal: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestQueryRefundVerifiesSignedIdentityAmountAndStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		want   payment.RefundOutcome
	}{
		{"success", "REFUND_SUCCESS", payment.RefundSucceeded},
		{"not terminal yet", "", payment.RefundUnknown},
		{"future status", "REFUND_PENDING_FUTURE", payment.RefundUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keys := newTestKeys(t)
			request := refundRequestFixture()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				params := parseSignedAPIRequest(t, r, &keys.merchant.PublicKey)
				if params["method"] != "alipay.trade.fastpay.refund.query" {
					t.Errorf("unexpected query method %q", params["method"])
				}
				var biz map[string]string
				if err := json.Unmarshal([]byte(params["biz_content"]), &biz); err != nil {
					t.Fatal(err)
				}
				if biz["trade_no"] != request.TransactionID || biz["out_trade_no"] != request.OrderKey || biz["out_request_no"] != request.ProviderRefundKey {
					t.Errorf("query lacks frozen refund identity: %+v", biz)
				}
				node := []byte(fmt.Sprintf(`{"code":"10000","trade_no":%q,"out_trade_no":%q,"out_request_no":%q,"total_amount":"10.00","refund_amount":"2.50","refund_status":%q}`, request.TransactionID, request.OrderKey, request.ProviderRefundKey, tc.status))
				_, _ = w.Write(signAPIResponse(t, keys.alipay, "alipay.trade.fastpay.refund.query", node))
			}))
			defer server.Close()
			keys.provider.baseURL = server.URL + "/gateway.do"
			result, err := keys.provider.QueryRefund(context.Background(), request)
			if err != nil || result.Outcome != tc.want || result.AmountFen != request.AmountFen || result.TotalFen != request.TotalFen {
				t.Fatalf("query result=%+v err=%v", result, err)
			}
		})
	}
}

func TestQueryRefundRejectsIdentityAndFenMismatches(t *testing.T) {
	for _, tc := range []struct {
		name string
		node string
	}{
		{"wrong order", `{"code":"10000","trade_no":"alipay-trade-01","out_trade_no":"other-order","out_request_no":"request_0001","total_amount":"10.00","refund_amount":"2.50","refund_status":"REFUND_SUCCESS"}`},
		{"wrong refund key", `{"code":"10000","trade_no":"alipay-trade-01","out_trade_no":"order_123456","out_request_no":"other-refund","total_amount":"10.00","refund_amount":"2.50","refund_status":"REFUND_SUCCESS"}`},
		{"wrong transaction", `{"code":"10000","trade_no":"other-trade","out_trade_no":"order_123456","out_request_no":"request_0001","total_amount":"10.00","refund_amount":"2.50","refund_status":"REFUND_SUCCESS"}`},
		{"wrong amount", `{"code":"10000","trade_no":"alipay-trade-01","out_trade_no":"order_123456","out_request_no":"request_0001","total_amount":"10.00","refund_amount":"9.00","refund_status":"REFUND_SUCCESS"}`},
		{"scientific amount", `{"code":"10000","trade_no":"alipay-trade-01","out_trade_no":"order_123456","out_request_no":"request_0001","total_amount":"1e1","refund_amount":"2.50","refund_status":"REFUND_SUCCESS"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keys := newTestKeys(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(signAPIResponse(t, keys.alipay, "alipay.trade.fastpay.refund.query", []byte(tc.node)))
			}))
			defer server.Close()
			keys.provider.baseURL = server.URL + "/gateway.do"
			result, err := keys.provider.QueryRefund(context.Background(), refundRequestFixture())
			if err == nil || result.Outcome != payment.RefundUnknown {
				t.Fatalf("mismatched signed refund query accepted: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestRefundProtocolErrorsStayUnknownAndRedirectIsNotFollowed(t *testing.T) {
	keys := newTestKeys(t)
	var targetHits int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { targetHits++; w.WriteHeader(http.StatusOK) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusFound)
	}))
	defer redirect.Close()
	keys.provider.baseURL = redirect.URL + "/gateway.do"
	result, err := keys.provider.QueryRefund(context.Background(), refundRequestFixture())
	if err == nil || result.Outcome != payment.RefundUnknown || targetHits != 0 {
		t.Fatalf("redirect/nontrusted protocol response accepted: result=%+v err=%v target hits=%d", result, err, targetHits)
	}

	keys = newTestKeys(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `gateway failure`)
	}))
	defer server.Close()
	keys.provider.baseURL = server.URL + "/gateway.do"
	result, err = keys.provider.ApplyRefund(context.Background(), refundRequestFixture())
	if err == nil || result.Outcome != payment.RefundUnknown {
		t.Fatalf("unverified HTTP failure classified terminal: result=%+v err=%v", result, err)
	}
}

func parseSignedAPIRequest(t *testing.T, r *http.Request, merchant *rsa.PublicKey) map[string]string {
	t.Helper()
	if r.Method != http.MethodPost || r.URL.Path != "/gateway.do" {
		t.Fatalf("unexpected API request %s %s", r.Method, r.URL)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		t.Fatal(err)
	}
	params := make(map[string]string, len(values))
	for key, entries := range values {
		if len(entries) != 1 {
			t.Fatalf("duplicate API parameter %s", key)
		}
		params[key] = entries[0]
	}
	verifyRequestSignature(t, merchant, params)
	return params
}

func TestRefundAPIRequiresFrozenConfiguredIdentity(t *testing.T) {
	keys := newTestKeys(t)
	request := refundRequestFixture()
	request.AppID = "other-app"
	if result, err := keys.provider.ApplyRefund(context.Background(), request); err == nil || result.Provider != "" {
		t.Fatalf("mismatched config identity reached protocol: result=%+v err=%v", result, err)
	}
	request = refundRequestFixture()
	request.ProviderRefundKey = strings.Repeat("x", 65)
	if _, err := keys.provider.QueryRefund(context.Background(), request); err == nil {
		t.Fatal("overlong stable refund key accepted")
	}
}
