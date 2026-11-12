package wechat

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/songquanpeng/one-api/payment"
)

type testKeys struct {
	merchant *rsa.PrivateKey
	platform *rsa.PrivateKey
	provider *Provider
}

func newTestKeys(t *testing.T) testKeys {
	t.Helper()
	merchant, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	platform, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(Config{Enabled: true, MerchantID: "merchant-1", AppID: "app-1", MerchantSerial: "merchant-serial", PlatformSerial: "platform-serial", APIPrivateKey: merchant, PlatformPublicKey: &platform.PublicKey, APIv3Key: []byte("0123456789abcdef0123456789abcdef"), NotifyURL: "https://pay.example.test/api/payments/notify/wechat"})
	if err != nil {
		t.Fatal(err)
	}
	return testKeys{merchant: merchant, platform: platform, provider: p}
}

func signedHeaders(t *testing.T, key *rsa.PrivateKey, body []byte, ts time.Time) http.Header {
	t.Helper()
	timestamp := fmt.Sprint(ts.Unix())
	nonce := "response-nonce"
	signature, err := signRSA(key, []byte(timestamp+"\n"+nonce+"\n"+string(body)+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	h := make(http.Header)
	h.Set("Wechatpay-Serial", "platform-serial")
	h.Set("Wechatpay-Timestamp", timestamp)
	h.Set("Wechatpay-Nonce", nonce)
	h.Set("Wechatpay-Signature", signature)
	return h
}

func TestNativeCreateFixedOrderAndNoRedirect(t *testing.T) {
	keys := newTestKeys(t)
	var redirectTargetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirectTargetHits.Add(1); w.WriteHeader(http.StatusOK) }))
	defer target.Close()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/pay/transactions/native" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		requestBody, _ := io.ReadAll(r.Body)
		var req nativeRequest
		if err := json.Unmarshal(requestBody, &req); err != nil {
			t.Error(err)
		}
		verifyMerchantAuthorization(t, keys.merchant, r.Header.Get("Authorization"), http.MethodPost, r.URL.RequestURI(), requestBody)
		if len(req.OrderKey) != 32 || req.MerchantID != "merchant-1" || req.AppID != "app-1" || req.Amount.Total != 1599 || req.Amount.Currency != "CNY" {
			t.Errorf("unexpected signed order: %+v", req)
		}
		body := []byte(`{"code_url":"weixin://wxpay/bizpayurl?pr=fixture"}`)
		for k, values := range signedHeaders(t, keys.platform, body, time.Now()) {
			for _, value := range values {
				w.Header().Add(k, value)
			}
		}
		_, _ = w.Write(body)
	}))
	keys.provider.baseURL = server.URL
	checkout, err := keys.provider.Create(context.Background(), payment.Order{OrderKey: strings.Repeat("a", 32), AmountFen: 1599, Currency: "CNY", Description: "test package", ExpiresAt: time.Now().Add(10 * time.Minute)})
	if err != nil || checkout.Kind != "qr" || !strings.HasPrefix(checkout.CodeURL, "weixin://") {
		t.Fatalf("checkout=%+v err=%v", checkout, err)
	}
	server.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirect.Close()
	keys.provider.baseURL = redirect.URL
	_, err = keys.provider.Create(context.Background(), payment.Order{OrderKey: strings.Repeat("b", 32), AmountFen: 100, Currency: "CNY", Description: "test", ExpiresAt: time.Now().Add(time.Minute)})
	if err == nil || redirectTargetHits.Load() != 0 {
		t.Fatalf("redirect followed or accepted: err=%v target hits=%d", err, redirectTargetHits.Load())
	}
}

func TestWechatNotificationSignatureAndNormalizedFields(t *testing.T) {
	keys := newTestKeys(t)
	body, headers := signedNotification(t, keys, "merchant-1", "app-1", "order-key-01", 1200, "CNY", []byte("123456789012"))
	got, err := keys.provider.VerifyNotification(headers, body)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProviderEventID != "notify-1" || got.OrderKey != "order-key-01" || got.TransactionID != "txn-1" || got.AmountFen != 1200 || got.MerchantID != "merchant-1" || got.AppID != "app-1" || got.Status != "SUCCESS" {
		t.Fatalf("unexpected normalized payment: %+v", got)
	}

	wrongSignature := headers.Clone()
	wrongSignature.Set("Wechatpay-Signature", "not-a-signature")
	if _, err := keys.provider.VerifyNotification(wrongSignature, body); err == nil {
		t.Fatal("invalid signature was accepted")
	}
	for _, age := range []time.Duration{-6 * time.Minute, 6 * time.Minute} {
		stale := signedHeaders(t, keys.platform, body, time.Now().Add(age))
		if _, err := keys.provider.VerifyNotification(stale, body); err == nil {
			t.Fatalf("stale signed request timestamp was accepted at %s", age)
		}
	}
	for _, mismatch := range []struct {
		merchant, app, order string
		amount               int64
	}{{"wrong-merchant", "app-1", "order-key-01", 1200}, {"merchant-1", "wrong-app", "order-key-01", 1200}, {"merchant-1", "app-1", "wrong-order", 1200}, {"merchant-1", "app-1", "order-key-01", 1201}} {
		b, h := signedNotification(t, keys, mismatch.merchant, mismatch.app, mismatch.order, mismatch.amount, "CNY", []byte("123456789012"))
		trade, err := keys.provider.VerifyNotification(h, b)
		if err != nil {
			t.Fatalf("signed mismatch should be normalized for durable quarantine: %v", err)
		}
		if trade.MerchantID == "merchant-1" && trade.AppID == "app-1" && trade.OrderKey == "order-key-01" && trade.AmountFen == 1200 {
			t.Fatal("mismatch fixture unexpectedly matched order")
		}
	}
	badNonceBody, _ := signedNotification(t, keys, "merchant-1", "app-1", "order-key-01", 1200, "CNY", []byte("123456789012"))
	var badEnvelope map[string]any
	if err := json.Unmarshal(badNonceBody, &badEnvelope); err != nil {
		t.Fatal(err)
	}
	badEnvelope["resource"].(map[string]any)["nonce"] = "short"
	badNonceBody, _ = json.Marshal(badEnvelope)
	if _, err := keys.provider.VerifyNotification(signedEnvelope(t, keys, badNonceBody), badNonceBody); err == nil {
		t.Fatal("malformed resource nonce accepted")
	}
}

func TestWechatResponseTimestampAndEmpty204Signature(t *testing.T) {
	keys := newTestKeys(t)
	for _, age := range []time.Duration{-4 * time.Minute, 4 * time.Minute} {
		body := []byte(`{}`)
		if err := keys.provider.verifyResponse(signedHeaders(t, keys.platform, body, time.Now().Add(age)), body); err != nil {
			t.Fatalf("fresh response rejected at %s: %v", age, err)
		}
	}
	body := []byte(`{}`)
	if err := keys.provider.verifyResponse(signedHeaders(t, keys.platform, body, time.Now().Add(-6*time.Minute)), body); err == nil {
		t.Fatal("stale response accepted")
	}
	empty := []byte{}
	if err := keys.provider.verifyResponse(signedHeaders(t, keys.platform, empty, time.Now()), empty); err != nil {
		t.Fatalf("valid signed 204 empty body rejected: %v", err)
	}
	if err := keys.provider.verifyResponse(http.Header{"Wechatpay-Serial": []string{"platform-serial"}}, empty); err == nil {
		t.Fatal("unsigned empty 204 accepted")
	}
}

func TestWechatCloseRequiresPaidQueryMatchAndSigned204(t *testing.T) {
	keys := newTestKeys(t)
	query := map[string]string{"appid": "app-1", "mchid": "merchant-1", "out_trade_no": "order-key-01", "trade_state": "NOTPAY", "amount": "100"}
	closeStatus := http.StatusNoContent
	closeSignature := true
	var closeCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			body := []byte(fmt.Sprintf(`{"appid":%q,"mchid":%q,"out_trade_no":%q,"trade_state":%q,"amount":{"total":%s,"currency":"CNY"}}`, query["appid"], query["mchid"], query["out_trade_no"], query["trade_state"], query["amount"]))
			for k, values := range signedHeaders(t, keys.platform, body, time.Now()) {
				for _, value := range values {
					w.Header().Add(k, value)
				}
			}
			_, _ = w.Write(body)
			return
		}
		closeCalls.Add(1)
		if closeSignature {
			for k, values := range signedHeaders(t, keys.platform, nil, time.Now()) {
				for _, value := range values {
					w.Header().Add(k, value)
				}
			}
		}
		w.WriteHeader(closeStatus)
	}))
	defer server.Close()
	keys.provider.baseURL = server.URL
	if err := keys.provider.Close(context.Background(), "order-key-01"); err != nil {
		t.Fatalf("valid signed 204 close failed: %v", err)
	}
	if closeCalls.Load() != 1 {
		t.Fatalf("expected exactly one close call; got %d", closeCalls.Load())
	}
	for _, tc := range []struct {
		name      string
		mutate    func()
		wantCalls int32
	}{
		{"unsigned204", func() { closeSignature = false; closeStatus = http.StatusNoContent }, 2},
		{"signed200", func() { closeSignature = true; closeStatus = http.StatusOK }, 3},
		{"wrongMerchant", func() { query["mchid"] = "other" }, 3},
		{"wrongApp", func() { query["mchid"] = "merchant-1"; query["appid"] = "other" }, 3},
		{"wrongOrder", func() { query["appid"] = "app-1"; query["out_trade_no"] = "other-order" }, 3},
		{"alreadyPaid", func() { query["out_trade_no"] = "order-key-01"; query["trade_state"] = "SUCCESS" }, 3},
		{"unknownState", func() { query["trade_state"] = "USERPAYING" }, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.mutate()
			err := keys.provider.Close(context.Background(), "order-key-01")
			if err == nil {
				t.Fatal("close unexpectedly succeeded")
			}
			if got := closeCalls.Load(); got != tc.wantCalls {
				t.Fatalf("close calls=%d want=%d err=%v", got, tc.wantCalls, err)
			}
		})
	}
}

func signedNotification(t *testing.T, keys testKeys, merchant, app, order string, amount int64, currency string, nonce []byte) ([]byte, http.Header) {
	t.Helper()
	body := makeNotificationBody(t, keys, merchant, app, order, amount, currency, nonce)
	return body, signedEnvelope(t, keys, body)
}

func makeNotificationBody(t *testing.T, keys testKeys, merchant, app, order string, amount int64, currency string, nonce []byte) []byte {
	t.Helper()
	plain, _ := json.Marshal(map[string]any{"mchid": merchant, "appid": app, "out_trade_no": order, "transaction_id": "txn-1", "trade_state": "SUCCESS", "success_time": time.Now().Format(time.RFC3339), "amount": map[string]any{"total": amount, "currency": currency}})
	block, err := aes.NewCipher(keys.provider.config.APIv3Key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	associated := "payment"
	sealed := gcm.Seal(nil, nonce, plain, []byte(associated))
	encoded := base64.StdEncoding.EncodeToString(sealed)
	resource := map[string]any{"algorithm": "AEAD_AES_256_GCM", "ciphertext": encoded, "nonce": string(nonce), "associated_data": associated}
	body, err := json.Marshal(map[string]any{"id": "notify-1", "create_time": time.Now().Format(time.RFC3339), "event_type": "TRANSACTION.SUCCESS", "resource": resource})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func signedEnvelope(t *testing.T, keys testKeys, body []byte) http.Header {
	t.Helper()
	return signedHeaders(t, keys.platform, body, time.Now())
}

func verifyMerchantAuthorization(t *testing.T, key *rsa.PrivateKey, authorization, method, requestURI string, body []byte) {
	t.Helper()
	if !strings.HasPrefix(authorization, "WECHATPAY2-SHA256-RSA2048 ") {
		t.Fatalf("wrong auth type: %q", authorization)
	}
	attrs := map[string]string{}
	for _, match := range regexp.MustCompile(`([a-z_]+)="([^"]*)"`).FindAllStringSubmatch(authorization, -1) {
		attrs[match[1]] = match[2]
	}
	for _, name := range []string{"mchid", "nonce_str", "signature", "timestamp", "serial_no"} {
		if attrs[name] == "" {
			t.Fatalf("missing authorization attribute %s", name)
		}
	}
	if _, err := strconv.ParseInt(attrs["timestamp"], 10, 64); err != nil {
		t.Fatal(err)
	}
	canonical := method + "\n" + requestURI + "\n" + attrs["timestamp"] + "\n" + attrs["nonce_str"] + "\n" + string(body) + "\n"
	if err := verifyRSA(&key.PublicKey, []byte(canonical), attrs["signature"]); err != nil {
		t.Fatalf("request signature invalid: %v", err)
	}
}
