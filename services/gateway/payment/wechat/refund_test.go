package wechat

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/songquanpeng/one-api/payment"
)

func refundRequestFixture() payment.RefundRequest {
	return payment.RefundRequest{
		RefundKey: "internal-refund-01", ProviderRefundKey: "refund_0001", OrderKey: "order_0001",
		TransactionID: "wx-transaction-01", MerchantID: "merchant-1", AppID: "app-1",
		AmountFen: 250, TotalFen: 1000, PriorRefundedFen: 100, Currency: "CNY", Reason: "测试退款",
	}
}

func TestApplyAndQueryRefundVerifySignedResponsesAndBindSnapshot(t *testing.T) {
	keys := newTestKeys(t)
	keys.provider.config.RefundNotifyURL = "https://merchant.example.test/api/payments/refund/wechat"
	request := refundRequestFixture()
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if r.URL.Path == "/v3/refund/domestic/refunds" && r.Method == http.MethodPost {
			verifyMerchantAuthorization(t, keys.merchant, r.Header.Get("Authorization"), r.Method, r.URL.RequestURI(), body)
			var decoded refundApplyRequest
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.TransactionID != request.TransactionID || decoded.OrderKey != "" || decoded.RefundNumber != request.ProviderRefundKey || decoded.Amount.Refund != 250 || decoded.Amount.Total != 1000 || decoded.Amount.Currency != "CNY" || decoded.NotifyURL != keys.provider.config.RefundNotifyURL {
				t.Errorf("unexpected signed refund request: %+v", decoded)
			}
		} else if r.URL.Path != "/v3/refund/domestic/refunds/refund_0001" || r.Method != http.MethodGet {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		response := []byte(`{"refund_id":"wx-refund-01","out_refund_no":"refund_0001","transaction_id":"wx-transaction-01","out_trade_no":"order_0001","status":"SUCCESS","amount":{"refund":250,"total":1000,"currency":"CNY"}}`)
		for key, values := range signedHeaders(t, keys.platform, response, time.Now()) {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		_, _ = w.Write(response)
	}))
	defer server.Close()
	keys.provider.baseURL = server.URL

	accepted, err := keys.provider.ApplyRefund(context.Background(), request)
	if err != nil || accepted.Outcome != payment.RefundAccepted || accepted.AmountFen != 250 || accepted.ProviderRefundID != "wx-refund-01" {
		t.Fatalf("apply result=%+v err=%v", accepted, err)
	}
	queried, err := keys.provider.QueryRefund(context.Background(), request)
	if err != nil || queried.Outcome != payment.RefundSucceeded || queried.OrderKey != request.OrderKey || queried.TotalFen != request.TotalFen {
		t.Fatalf("query result=%+v err=%v", queried, err)
	}
	if calls != 2 {
		t.Fatalf("expected apply and query calls, got %d", calls)
	}
}

func TestWechatRefundQueryOutcomeAndIdentityMatrix(t *testing.T) {
	for _, tc := range []struct {
		status  string
		outcome payment.RefundOutcome
		wantErr bool
	}{
		{"SUCCESS", payment.RefundSucceeded, false},
		{"CLOSED", payment.RefundDefiniteFailed, false},
		{"PROCESSING", payment.RefundAccepted, false},
		{"ABNORMAL", payment.RefundAbnormal, false},
		{"NEW_STATUS", payment.RefundUnknown, true},
	} {
		t.Run(tc.status, func(t *testing.T) {
			keys := newTestKeys(t)
			request := refundRequestFixture()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				response := []byte(fmt.Sprintf(`{"refund_id":"wx-refund-01","out_refund_no":%q,"transaction_id":%q,"out_trade_no":%q,"status":%q,"amount":{"refund":250,"total":1000,"currency":"CNY"}}`, request.ProviderRefundKey, request.TransactionID, request.OrderKey, tc.status))
				for key, values := range signedHeaders(t, keys.platform, response, time.Now()) {
					for _, value := range values {
						w.Header().Add(key, value)
					}
				}
				_, _ = w.Write(response)
			}))
			defer server.Close()
			keys.provider.baseURL = server.URL
			result, err := keys.provider.QueryRefund(context.Background(), request)
			if (err != nil) != tc.wantErr || result.Outcome != tc.outcome {
				t.Fatalf("result=%+v err=%v want outcome=%s err=%v", result, err, tc.outcome, tc.wantErr)
			}
		})
	}

	keys := newTestKeys(t)
	request := refundRequestFixture()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		response := []byte(`{"refund_id":"wx-refund-01","out_refund_no":"refund_0001","transaction_id":"wrong-transaction","out_trade_no":"order_0001","status":"SUCCESS","amount":{"refund":250,"total":1000,"currency":"CNY"}}`)
		for key, values := range signedHeaders(t, keys.platform, response, time.Now()) {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		_, _ = w.Write(response)
	}))
	defer server.Close()
	keys.provider.baseURL = server.URL
	if result, err := keys.provider.QueryRefund(context.Background(), request); err == nil || result.Outcome != payment.RefundUnknown {
		t.Fatalf("mismatched original transaction accepted: result=%+v err=%v", result, err)
	}
}

func TestWechatRefundNotificationUsesOfficialEnvelopeAndDecrypts(t *testing.T) {
	keys := newTestKeys(t)
	body, headers := signedRefundNotification(t, keys, "SUCCESS", "merchant-1", true, true)
	result, err := keys.provider.VerifyRefundNotification(headers, body)
	if err != nil || result.Outcome != payment.RefundSucceeded || result.ProviderRefundID != "wx-refund-01" || result.ProviderRefundKey != "refund_0001" ||
		result.OrderKey != "order_0001" || result.TransactionID != "wx-transaction-01" || result.MerchantID != "merchant-1" || result.AppID != "app-1" ||
		result.AmountFen != 250 || result.TotalFen != 1000 || result.ProviderEventID != "refund-notify-01" {
		t.Fatalf("refund notification result=%+v err=%v", result, err)
	}
	for _, tc := range []struct {
		name        string
		status      string
		merchant    string
		resourceTop bool
		refundID    bool
		wantOutcome payment.RefundOutcome
		wantErr     bool
	}{
		{"closed", "CLOSED", "merchant-1", true, true, payment.RefundDefiniteFailed, false},
		{"abnormal", "ABNORMAL", "merchant-1", true, true, payment.RefundAbnormal, false},
		{"unknown", "FUTURE", "merchant-1", true, true, payment.RefundUnknown, true},
		{"missing refund id", "SUCCESS", "merchant-1", true, false, payment.RefundUnknown, true},
		{"wrong merchant", "SUCCESS", "other-merchant", true, true, payment.RefundUnknown, true},
		{"nested resource type rejected", "SUCCESS", "merchant-1", false, true, payment.RefundUnknown, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, h := signedRefundNotification(t, keys, tc.status, tc.merchant, tc.resourceTop, tc.refundID)
			got, err := keys.provider.VerifyRefundNotification(h, b)
			if (err != nil) != tc.wantErr || (!tc.wantErr && got.Outcome != tc.wantOutcome) {
				t.Fatalf("result=%+v err=%v want outcome=%s err=%v", got, err, tc.wantOutcome, tc.wantErr)
			}
		})
	}

	bad := headers.Clone()
	bad.Set("Wechatpay-Signature", "bad-signature")
	if _, err := keys.provider.VerifyRefundNotification(bad, body); err == nil {
		t.Fatal("invalid notification signature accepted")
	}
	for _, age := range []time.Duration{-6 * time.Minute, 6 * time.Minute} {
		stale := signedHeaders(t, keys.platform, body, time.Now().Add(age))
		if _, err := keys.provider.VerifyRefundNotification(stale, body); err == nil {
			t.Fatalf("stale refund notification signature accepted at %s", age)
		}
	}
}

func signedRefundNotification(t *testing.T, keys testKeys, status, merchant string, topLevelResourceType, includeRefundID bool) ([]byte, http.Header) {
	t.Helper()
	notice := map[string]any{
		"mchid": merchant, "out_trade_no": "order_0001", "transaction_id": "wx-transaction-01",
		"out_refund_no": "refund_0001", "refund_status": status,
		"amount": map[string]any{"total": 1000, "refund": 250, "currency": "CNY"},
	}
	if includeRefundID {
		notice["refund_id"] = "wx-refund-01"
	}
	plain, err := json.Marshal(notice)
	if err != nil {
		t.Fatal(err)
	}
	nonce := []byte("123456789012")
	block, err := aes.NewCipher(keys.provider.config.APIv3Key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	associated := ""
	sealed := gcm.Seal(nil, nonce, plain, []byte(associated))
	resource := map[string]any{"algorithm": "AEAD_AES_256_GCM", "original_type": "refund", "ciphertext": base64.StdEncoding.EncodeToString(sealed), "nonce": string(nonce), "associated_data": associated}
	envelope := map[string]any{"id": "refund-notify-01", "create_time": time.Now().Format(time.RFC3339), "event_type": "REFUND." + status, "summary": "refund event", "resource": resource}
	if status == "FUTURE" {
		envelope["event_type"] = "REFUND.FUTURE"
	}
	if topLevelResourceType {
		envelope["resource_type"] = "encrypt-resource"
	} else {
		resource["resource_type"] = "encrypt-resource"
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return body, signedEnvelope(t, keys, body)
}

func TestWechatRefundNon2xxRemainsUnknown(t *testing.T) {
	keys := newTestKeys(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"code":"SYSTEM_ERROR"}`)
	}))
	defer server.Close()
	keys.provider.baseURL = server.URL
	result, err := keys.provider.ApplyRefund(context.Background(), refundRequestFixture())
	if err == nil || result.Outcome != payment.RefundUnknown {
		t.Fatalf("unverified non-2xx classified terminal: result=%+v err=%v", result, err)
	}
}

func TestWechatRefundUsesSeparateNotificationURLAndValidatesRefundIdentity(t *testing.T) {
	keys := newTestKeys(t)
	keys.provider.config.RefundNotifyURL = "https://merchant.example.test/refund-notify"
	request := refundRequestFixture()
	request.MerchantID = "other-merchant"
	if _, err := keys.provider.ApplyRefund(context.Background(), request); err == nil {
		t.Fatal("refund request with another merchant was accepted")
	}
	if err := validateNotifyURL("http://merchant.example.test/refund"); err == nil {
		t.Fatal("non-HTTPS refund notification URL accepted")
	}
	if strings.Contains(keys.provider.config.RefundNotifyURL, "payment-notify") {
		t.Fatal("refund notification URL unexpectedly aliases payment route")
	}
	request = refundRequestFixture()
	request.Reason = strings.Repeat("中", 27) // 81 UTF-8 bytes
	if _, err := keys.provider.ApplyRefund(context.Background(), request); err == nil {
		t.Fatal("refund reason above the 80-byte provider limit was accepted")
	}
}
