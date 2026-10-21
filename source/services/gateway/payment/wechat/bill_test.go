package wechat

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/songquanpeng/one-api/payment/bill"
)

type billResolver []net.IPAddr

func (r billResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return []net.IPAddr(r), nil
}

type billRoundTripper func(*http.Request) (*http.Response, error)

func (f billRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestDownloadWeChatTradeBillVerifiesSignedMetadataHashAndPreservesRaw(t *testing.T) {
	keys := newTestKeys(t)
	date := time.Now().In(time.FixedZone("CST", 8*60*60)).AddDate(0, 0, -1)
	day := date.Format("2006-01-02")
	data := syntheticWeChatBill(day)
	digest := sha1.Sum(data)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v3/bill/tradebill" || r.URL.Query().Get("bill_date") != day || r.URL.Query().Get("bill_type") != "ALL" {
			t.Errorf("unexpected bill request: %s %s", r.Method, r.URL.RequestURI())
		}
		verifyMerchantAuthorization(t, keys.merchant, r.Header.Get("Authorization"), r.Method, r.URL.RequestURI(), nil)
		downloadURL := "https://" + r.Host + "/v3/bill/downloadurl?token=short-lived"
		body, _ := json.Marshal(tradeBillResponse{HashType: "SHA1", HashValue: hex.EncodeToString(digest[:]), DownloadURL: downloadURL})
		for name, values := range signedHeaders(t, keys.platform, body, time.Now()) {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		_, _ = w.Write(body)
	}))
	defer server.Close()
	keys.provider.baseURL = server.URL
	keys.provider.billResolver = billResolver{{IP: net.ParseIP("1.1.1.1")}}
	var downloadCalls int
	keys.provider.billTransport = billRoundTripper(func(req *http.Request) (*http.Response, error) {
		downloadCalls++
		if req.URL.Path != "/v3/bill/downloadurl" || req.URL.Query().Get("token") != "short-lived" || req.Header.Get("Authorization") == "" {
			t.Errorf("unexpected signed bill download request: %s %v", req.URL, req.Header)
		}
		verifyMerchantAuthorization(t, keys.merchant, req.Header.Get("Authorization"), http.MethodGet, req.URL.RequestURI(), nil)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(data))), ContentLength: int64(len(data))}, nil
	})
	statement, err := keys.provider.DownloadTradeBill(context.Background(), date)
	if err != nil {
		t.Fatal(err)
	}
	if downloadCalls != 1 || statement.SourceSHA256 != bill.SHA256(data) || string(statement.SourceBytes) != string(data) || statement.ProviderHashType != "SHA1" || !statement.ProviderHashVerified || statement.RequestedAppID != "app-1" || len(statement.Rows) != 1 {
		t.Fatalf("downloaded bill provenance incomplete: calls=%d statement=%+v", downloadCalls, statement)
	}
}

func TestWeChatBillMetadataRequiresSignatureExactJSONAndHash(t *testing.T) {
	for _, mode := range []string{"trailing-garbage", "wrong-hash", "wrong-host", "invalid-signature"} {
		t.Run(mode, func(t *testing.T) {
			keys := newTestKeys(t)
			date := time.Now().In(time.FixedZone("CST", 8*60*60)).AddDate(0, 0, -1)
			data := syntheticWeChatBill(date.Format("2006-01-02"))
			digest := sha1.Sum(data)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				downloadURL := "https://" + r.Host + "/v3/bill/downloadurl?token=short-lived"
				if mode == "wrong-host" {
					downloadURL = "https://example.invalid/v3/bill/downloadurl?token=short-lived"
				}
				hash := hex.EncodeToString(digest[:])
				if mode == "wrong-hash" {
					hash = strings.Repeat("0", 40)
				}
				body, _ := json.Marshal(tradeBillResponse{HashType: "SHA1", HashValue: hash, DownloadURL: downloadURL})
				if mode == "trailing-garbage" {
					body = append(body, []byte(" garbage")...)
				}
				for name, values := range signedHeaders(t, keys.platform, body, time.Now()) {
					for _, value := range values {
						w.Header().Add(name, value)
					}
				}
				if mode == "invalid-signature" {
					w.Header().Set("Wechatpay-Signature", "invalid")
				}
				_, _ = w.Write(body)
			}))
			defer server.Close()
			keys.provider.baseURL = server.URL
			keys.provider.billResolver = billResolver{{IP: net.ParseIP("1.1.1.1")}}
			var downloads int
			keys.provider.billTransport = billRoundTripper(func(req *http.Request) (*http.Response, error) {
				downloads++
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(data))), ContentLength: int64(len(data))}, nil
			})
			_, err := keys.provider.DownloadTradeBill(context.Background(), date)
			if err == nil {
				t.Fatal("invalid bill metadata or content was accepted")
			}
			wantDownloads := 0
			if mode == "wrong-hash" {
				wantDownloads = 1
			}
			if downloads != wantDownloads {
				t.Fatalf("download count=%d want=%d for mode %s", downloads, wantDownloads, mode)
			}
		})
	}
}

func syntheticWeChatBill(day string) []byte {
	header := "交易时间,公众账号ID,商户号,特约商户号,设备号,微信订单号,商户订单号,用户标识,交易类型,交易状态,付款银行,货币种类,应结订单金额,代金券金额,微信退款单号,商户退款单号,退款金额,充值券退款金额,退款类型,退款状态,商品名称,商户数据包,手续费,费率,订单金额,申请退款金额,费率备注"
	row := []string{"`" + day + " 11:00:00", "`app-1", "`merchant-1", "`0", "`device", "`wx-transaction-1", "`order-key-01", "`user", "`NATIVE", "`SUCCESS", "`OTHERS", "`CNY", "`1.00", "`0.00", "`0", "`0", "`0.00", "`0.00", "`", "`", "`text with \\\"quote\\\" and \\\\ space", "`", "`0.01", "`0.60%", "`1.00", "`0.00", "`"}
	return []byte(header + "\n" + strings.Join(row, ",") + "\n总交易单数,应结订单总金额,退款总金额,充值券退款总金额,手续费总金额,订单总金额,申请退款总金额\n`1,`1.00,`0.00,`0.00,`0.01,`1.00,`0.00\n")
}

func TestValidateWeChatTradeBillDateUsesChinaCalendarBoundaries(t *testing.T) {
	location := time.FixedZone("CST", 8*60*60)
	now := time.Date(2026, 9, 28, 0, 5, 0, 0, location)
	if day, err := validateWeChatBillDate(now.AddDate(0, 0, -1), now); err != nil || day != "2026-09-27" {
		t.Fatalf("yesterday rejected: day=%q err=%v", day, err)
	}
	for _, date := range []time.Time{now, now.AddDate(0, 0, 1), now.AddDate(0, -3, -1)} {
		if _, err := validateWeChatBillDate(date, now); err == nil {
			t.Fatalf("out of range bill date accepted: %s", date)
		}
	}
	if _, err := validateWeChatBillDate(now.AddDate(0, -3, 0), now); err != nil {
		t.Fatalf("exact three-month boundary rejected: %v", err)
	}
}
