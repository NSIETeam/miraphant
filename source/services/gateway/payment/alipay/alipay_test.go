package alipay

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/songquanpeng/one-api/payment"
)

type testKeys struct {
	merchant *rsa.PrivateKey
	alipay   *rsa.PrivateKey
	provider *Provider
}

func newTestKeys(t *testing.T) testKeys {
	t.Helper()
	merchant, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	alipay, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(Config{Enabled: true, AppID: "app-test", MerchantID: "seller-test", MerchantPrivateKey: merchant, AlipayPublicKey: &alipay.PublicKey, NotifyURL: "https://merchant.example.test/pay/alipay/notify", ReturnURL: "https://merchant.example.test/pay/alipay/return"})
	if err != nil {
		t.Fatal(err)
	}
	return testKeys{merchant: merchant, alipay: alipay, provider: p}
}

func signAPIResponse(t *testing.T, key *rsa.PrivateKey, method string, node []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(node)
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	responseName := strings.ReplaceAll(method, ".", "_") + "_response"
	return []byte(`{"` + responseName + `":` + string(node) + `,"sign":"` + base64.StdEncoding.EncodeToString(signature) + `"}`)
}

func independentCanonical(params map[string]string, request bool) string {
	keys := make([]string, 0, len(params))
	for key, value := range params {
		if key == "sign" || value == "" || !request && key == "sign_type" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+params[key])
	}
	return strings.Join(parts, "&")
}

func verifyRequestSignature(t *testing.T, public *rsa.PublicKey, params map[string]string) {
	t.Helper()
	if params["sign_type"] != "RSA2" {
		t.Fatalf("request sign_type = %q", params["sign_type"])
	}
	signature, err := base64.StdEncoding.DecodeString(params["sign"])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(independentCanonical(params, true)))
	if err := rsa.VerifyPKCS1v15(public, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatalf("request did not sign the independently constructed canonical parameters: %v", err)
	}
}

func TestDisabledAndInvalidConfigurationFailClosed(t *testing.T) {
	if _, err := New(Config{}); err != payment.ErrDisabled {
		t.Fatalf("disabled provider error = %v", err)
	}
	keys := newTestKeys(t)
	config := keys.provider.config
	config.NotifyURL = "http://merchant.example.test/notify"
	if _, err := New(config); err == nil {
		t.Fatal("non-HTTPS notification endpoint accepted")
	}
	config = keys.provider.config
	config.ReturnURL = "https://user:pass@merchant.example.test/return"
	if _, err := New(config); err == nil {
		t.Fatal("return URL with userinfo accepted")
	}
}

func TestPagePaySignsExactFenAmountAndUsesFixedEndpoint(t *testing.T) {
	keys := newTestKeys(t)
	expiresAt := time.Now().Add(20 * time.Minute).Truncate(time.Second)
	checkout, err := keys.provider.Create(context.Background(), payment.Order{OrderKey: "order_123456", AmountFen: 12345, Currency: "CNY", Description: "Miraphant 积分包", ExpiresAt: expiresAt})
	if err != nil {
		t.Fatal(err)
	}
	if checkout.Kind != "form" || checkout.GatewayURL == "" || checkout.Fields["method"] != "alipay.trade.page.pay" {
		t.Fatalf("unexpected checkout: %+v", checkout)
	}
	parsed, err := url.Parse(checkout.GatewayURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "https" || parsed.Host != "openapi.alipay.com" || parsed.Path != "/gateway.do" {
		t.Fatalf("checkout escaped fixed Alipay endpoint: %s", checkout.GatewayURL)
	}
	if parsed.RawQuery != "" {
		t.Fatalf("form action must not duplicate submitted fields in its query: %s", checkout.GatewayURL)
	}
	params := checkout.Fields
	verifyRequestSignature(t, &keys.merchant.PublicKey, params)
	var biz map[string]string
	if err := json.Unmarshal([]byte(params["biz_content"]), &biz); err != nil {
		t.Fatal(err)
	}
	if biz["out_trade_no"] != "order_123456" || biz["total_amount"] != "123.45" || biz["product_code"] != "FAST_INSTANT_TRADE_PAY" || biz["subject"] != "Miraphant 积分包" || biz["time_expire"] != expiresAt.In(time.FixedZone("CST", 8*60*60)).Format("2006-01-02 15:04:05") {
		t.Fatalf("unexpected signed order content: %+v", biz)
	}
	for _, order := range []payment.Order{{OrderKey: "bad/order", AmountFen: 100, Currency: "CNY", Description: "x"}, {OrderKey: "good-order", AmountFen: 0, Currency: "CNY", Description: "x"}, {OrderKey: "good-order", AmountFen: 100, Currency: "USD", Description: "x"}} {
		order.ExpiresAt = time.Now().Add(15 * time.Minute)
		if _, err := keys.provider.Create(context.Background(), order); err == nil {
			t.Fatalf("invalid order was accepted: %+v", order)
		}
	}
}

func signedForm(t *testing.T, key *rsa.PrivateKey, params map[string]string) []byte {
	t.Helper()
	params["sign_type"] = "RSA2"
	digest := sha256.Sum256([]byte(independentCanonical(params, false)))
	signatureBytes, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	params["sign"] = base64.StdEncoding.EncodeToString(signatureBytes)
	values := make(url.Values)
	for name, value := range params {
		values.Set(name, value)
	}
	return []byte(values.Encode())
}

func TestNotificationVerifiesDecodedOriginalValuesAndRejectsDuplicates(t *testing.T) {
	keys := newTestKeys(t)
	params := map[string]string{
		"app_id": "app-test", "seller_id": "seller-test", "notify_id": "notify-1",
		"out_trade_no": "order_123456", "trade_no": "alipay-trade-1", "trade_status": "TRADE_SUCCESS",
		"total_amount": "12.30", "gmt_payment": "2026-09-27 10:20:30",
		"subject": "中文 空格+百分比%与&符号",
	}
	body := signedForm(t, keys.alipay, params)
	parsed, err := url.ParseQuery(string(body))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Get("subject") != params["subject"] {
		t.Fatalf("form decoding changed signed value: %q", parsed.Get("subject"))
	}
	trade, err := keys.provider.VerifyNotification(http.Header{}, body)
	if err != nil {
		t.Fatalf("valid signed notification rejected: %v", err)
	}
	if trade.Provider != "alipay" || trade.OrderKey != params["out_trade_no"] || trade.TransactionID != params["trade_no"] || trade.AmountFen != 1230 || trade.Status != "TRADE_SUCCESS" || trade.ProviderOccurredAt.IsZero() {
		t.Fatalf("wrong verified notification: %+v", trade)
	}
	if _, err := keys.provider.VerifyNotification(http.Header{}, append(body, []byte("&app_id=app-test")...)); err == nil {
		t.Fatal("duplicate signed form field accepted")
	}
	tampered := strings.Replace(string(body), "12.30", "12.31", 1)
	if _, err := keys.provider.VerifyNotification(http.Header{}, []byte(tampered)); err == nil {
		t.Fatal("tampered amount accepted")
	}
	for _, field := range []string{"seller_id", "app_id"} {
		changed := make(map[string]string, len(params))
		for key, value := range params {
			changed[key] = value
		}
		changed[field] = "wrong-" + field
		if _, err := keys.provider.VerifyNotification(http.Header{}, signedForm(t, keys.alipay, changed)); err == nil {
			t.Fatalf("mismatched %s accepted", field)
		}
	}
}

func TestParseFenNeverUsesFloatingPoint(t *testing.T) {
	cases := map[string]int64{"1": 100, "1.2": 120, "1.20": 120, "100000000.00": 10_000_000_000}
	for input, want := range cases {
		got, err := parseFen(input)
		if err != nil || got != want {
			t.Fatalf("parseFen(%q)=%d,%v want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"", "0", "-1.00", "+1.00", "1.001", "1e2", "100000000.01", "1.x", ".50", "1."} {
		if _, err := parseFen(input); err == nil {
			t.Fatalf("invalid amount %q accepted", input)
		}
	}
}

func TestQueryVerifiesRawResponseBytesAndMatchesMerchantAppOrder(t *testing.T) {
	keys := newTestKeys(t)
	node := []byte(`{"code":"10000","out_trade_no":"order_123456","send_pay_date":"2026-09-27 10:20:30","total_amount":"12.30","trade_no":"alipay-trade-1","trade_status":"TRADE_SUCCESS"}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/gateway.do" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded; charset=utf-8" {
			t.Errorf("unexpected signed API request: %s %s %s", r.Method, r.URL, r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		values, _ := url.ParseQuery(string(body))
		params := make(map[string]string, len(values))
		for key := range values {
			params[key] = values.Get(key)
		}
		verifyRequestSignature(t, &keys.merchant.PublicKey, params)
		var biz map[string]string
		_ = json.Unmarshal([]byte(params["biz_content"]), &biz)
		if params["method"] != "alipay.trade.query" || biz["out_trade_no"] != "order_123456" {
			t.Errorf("query request mismatch: params=%+v biz=%+v", params, biz)
		}
		_, _ = w.Write(signAPIResponse(t, keys.alipay, "alipay.trade.query", node))
	}))
	defer server.Close()
	keys.provider.baseURL = server.URL + "/gateway.do"
	trade, err := keys.provider.Query(context.Background(), "order_123456")
	if err != nil || trade.TransactionID != "alipay-trade-1" || trade.AmountFen != 1230 || trade.AppID != "app-test" || trade.MerchantID != "seller-test" || trade.ProviderOccurredAt.IsZero() {
		t.Fatalf("query trade=%+v err=%v", trade, err)
	}

	for _, badNode := range [][]byte{
		[]byte(`{"code":"10000","out_trade_no":"other-order","send_pay_date":"2026-09-27 10:20:30","total_amount":"12.30","trade_no":"alipay-trade-1","trade_status":"TRADE_SUCCESS"}`),
		[]byte(`{"app_id":"other","code":"10000","out_trade_no":"order_123456","send_pay_date":"2026-09-27 10:20:30","total_amount":"12.30","trade_no":"alipay-trade-1","trade_status":"TRADE_SUCCESS"}`),
		[]byte(`{"code":"10000","out_trade_no":"order_123456","seller_id":"other-seller","send_pay_date":"2026-09-27 10:20:30","total_amount":"12.30","trade_no":"alipay-trade-1","trade_status":"TRADE_SUCCESS"}`),
	} {
		badServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(signAPIResponse(t, keys.alipay, "alipay.trade.query", badNode))
		}))
		keys.provider.baseURL = badServer.URL
		if _, err := keys.provider.Query(context.Background(), "order_123456"); err == nil {
			badServer.Close()
			t.Fatalf("mismatched signed query response accepted: %s", badNode)
		}
		badServer.Close()
	}

	// The gateway response verifier must authenticate the received JSON bytes,
	// rather than silently normalizing/re-serializing them before verification.
	canonical := []byte(`{"code":"10000","out_trade_no":"order_123456","send_pay_date":"2026-09-27 10:20:30","total_amount":"12.30","trade_no":"alipay-trade-1","trade_status":"TRADE_SUCCESS"}`)
	spaced := []byte(`{ "code":"10000", "out_trade_no":"order_123456", "send_pay_date":"2026-09-27 10:20:30", "total_amount":"12.30", "trade_no":"alipay-trade-1", "trade_status":"TRADE_SUCCESS" }`)
	digest := sha256.Sum256(canonical)
	sig, _ := rsa.SignPKCS1v15(rand.Reader, keys.alipay, crypto.SHA256, digest[:])
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"alipay_trade_query_response":%s,"sign":"%s"}`, spaced, base64.StdEncoding.EncodeToString(sig))
	}))
	defer server.Close()
	keys.provider.baseURL = server.URL
	if _, err := keys.provider.Query(context.Background(), "order_123456"); err == nil {
		t.Fatal("response signed over re-serialized content was accepted")
	}
}

func TestCloseRequiresAuthoritativeUnpaidQueryAndSignedCloseResponse(t *testing.T) {
	keys := newTestKeys(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		values, _ := url.ParseQuery(string(body))
		method := values.Get("method")
		call := calls.Add(1)
		if method == "alipay.trade.query" {
			query := []byte(`{"code":"10000","out_trade_no":"order_123456","total_amount":"12.30","trade_no":"alipay-trade-1","trade_status":"WAIT_BUYER_PAY"}`)
			_, _ = w.Write(signAPIResponse(t, keys.alipay, method, query))
			return
		}
		if call != 2 || method != "alipay.trade.close" {
			t.Errorf("unexpected close call %d method=%s", call, method)
		}
		closeNode := []byte(`{"code":"10000","out_trade_no":"order_123456"}`)
		_, _ = w.Write(signAPIResponse(t, keys.alipay, method, closeNode))
	}))
	defer server.Close()
	keys.provider.baseURL = server.URL
	if err := keys.provider.Close(context.Background(), "order_123456"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected query plus close, calls=%d", calls.Load())
	}

	calls.Store(0)
	paidServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		query := []byte(`{"code":"10000","out_trade_no":"order_123456","total_amount":"12.30","trade_no":"alipay-trade-1","trade_status":"TRADE_SUCCESS"}`)
		_, _ = w.Write(signAPIResponse(t, keys.alipay, "alipay.trade.query", query))
	}))
	keys.provider.baseURL = paidServer.URL
	if err := keys.provider.Close(context.Background(), "order_123456"); err != payment.ErrAlreadyPaid {
		t.Fatalf("paid order close error=%v", err)
	}
	paidServer.Close()
	if calls.Load() != 1 {
		t.Fatalf("close API called without an unpaid query: %d", calls.Load())
	}
}

func TestQueryDistinguishesNotFoundFromUnknownAndCloseRefusesUnknownStatus(t *testing.T) {
	for _, tc := range []struct {
		name    string
		node    string
		wantErr error
	}{
		{name: "not found", node: `{"code":"40004","sub_code":"ACQ.TRADE_NOT_EXIST"}`, wantErr: payment.ErrNotPaid},
		{name: "provider error", node: `{"code":"20000","sub_code":"isp.unknown-error"}`, wantErr: payment.ErrUnknownStatus},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keys := newTestKeys(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(signAPIResponse(t, keys.alipay, "alipay.trade.query", []byte(tc.node)))
			}))
			defer server.Close()
			keys.provider.baseURL = server.URL
			if _, err := keys.provider.Query(context.Background(), "order_123456"); err != tc.wantErr {
				t.Fatalf("query error=%v want=%v", err, tc.wantErr)
			}
		})
	}
	keys := newTestKeys(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		unknown := []byte(`{"code":"10000","out_trade_no":"order_123456","total_amount":"12.30","trade_no":"alipay-trade-1","trade_status":"TRADE_CLOSED"}`)
		_, _ = w.Write(signAPIResponse(t, keys.alipay, "alipay.trade.query", unknown))
	}))
	defer server.Close()
	keys.provider.baseURL = server.URL
	if err := keys.provider.Close(context.Background(), "order_123456"); err != payment.ErrNotCloseable {
		t.Fatalf("unknown query state close error=%v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("close endpoint called without WAIT_BUYER_PAY: %d", calls.Load())
	}
}

func TestCloseRejectsResponseForDifferentOrderOrTrade(t *testing.T) {
	for _, closeNode := range []string{
		`{"code":"10000","out_trade_no":"other-order","trade_no":"alipay-trade-1"}`,
		`{"code":"10000","out_trade_no":"order_123456","trade_no":"other-trade"}`,
	} {
		keys := newTestKeys(t)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			values, _ := url.ParseQuery(readBody(t, r))
			method := values.Get("method")
			var node []byte
			if method == "alipay.trade.query" {
				node = []byte(`{"code":"10000","out_trade_no":"order_123456","total_amount":"12.30","trade_no":"alipay-trade-1","trade_status":"WAIT_BUYER_PAY"}`)
			} else {
				node = []byte(closeNode)
			}
			_, _ = w.Write(signAPIResponse(t, keys.alipay, method, node))
		}))
		keys.provider.baseURL = server.URL
		if err := keys.provider.Close(context.Background(), "order_123456"); err != payment.ErrOrderMismatch {
			t.Fatalf("mismatched close response accepted: node=%s err=%v", closeNode, err)
		}
		server.Close()
	}
}

func readBody(t *testing.T, r *http.Request) string {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestAPIClientDoesNotFollowRedirects(t *testing.T) {
	keys := newTestKeys(t)
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { targetHits.Add(1); w.WriteHeader(http.StatusOK) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirect.Close()
	keys.provider.baseURL = redirect.URL
	if _, err := keys.provider.Query(context.Background(), "order_123456"); err == nil {
		t.Fatal("redirect response was accepted")
	}
	if targetHits.Load() != 0 {
		t.Fatalf("HTTP client followed a redirect to another endpoint: %d", targetHits.Load())
	}
}
