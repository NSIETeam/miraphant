package alipay

import (
	"context"
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

type billTestResolver []net.IPAddr

func (r billTestResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return []net.IPAddr(r), nil
}

type billTestRoundTripper func(*http.Request) (*http.Response, error)

func (f billTestRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestAlipayTradeBillVerifiesAPIResponseAndReturnsRawEvidence(t *testing.T) {
	keys := newTestKeys(t)
	data := []byte("opaque alipay trade bill fixture\n")
	date := time.Now().In(time.FixedZone("CST", 8*60*60)).AddDate(0, 0, -1)
	day := date.Format("2006-01-02")
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/gateway.do" {
			t.Errorf("unexpected API request: %s %s", r.Method, r.URL.RequestURI())
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		params := make(map[string]string)
		for key, values := range r.Form {
			if len(values) != 1 {
				t.Errorf("duplicate API form value for %s", key)
			}
			if len(values) > 0 {
				params[key] = values[0]
			}
		}
		verifyRequestSignature(t, &keys.merchant.PublicKey, params)
		var query map[string]string
		if err := json.Unmarshal([]byte(params["biz_content"]), &query); err != nil {
			t.Error(err)
		}
		if query["bill_type"] != "trade" || query["bill_date"] != day {
			t.Errorf("unexpected bill query: %+v", query)
		}
		response := []byte(`{"code":"10000","msg":"Success","bill_download_url":"https://dwbillcenter.alipay.com/bill.csv?token=short-lived"}`)
		_, _ = w.Write(signAPIResponse(t, keys.alipay, "alipay.data.dataservice.bill.downloadurl.query", response))
	}))
	defer server.Close()
	keys.provider.baseURL = server.URL + "/gateway.do"
	keys.provider.billResolver = billTestResolver{{IP: net.ParseIP("1.1.1.1")}}
	var downloads int
	keys.provider.billTransport = billTestRoundTripper(func(req *http.Request) (*http.Response, error) {
		downloads++
		if req.URL.Host != "dwbillcenter.alipay.com" || req.URL.Query().Get("token") != "short-lived" {
			t.Errorf("unexpected official short-lived URL: %s", req.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(data))), ContentLength: int64(len(data))}, nil
	})
	result, err := keys.provider.DownloadTradeBill(context.Background(), date)
	if err != nil {
		t.Fatal(err)
	}
	if downloads != 1 || string(result.Bytes) != string(data) || result.SHA256 != bill.SHA256(data) || result.Provider != "alipay" || result.FormatVersion != "alipay-trade-raw-unparsed-v1" || result.RequestedMerchantID != "seller-test" || result.RequestedAppID != "app-test" {
		t.Fatalf("raw bill evidence incomplete: downloads=%d result=%+v", downloads, result)
	}
}

func TestAlipayBillRejectsInvalidSignedResponseBeforeDownload(t *testing.T) {
	keys := newTestKeys(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := []byte(`{"alipay.data.dataservice.bill.downloadurl.query_response":{"code":"10000","bill_download_url":"https://dwbillcenter.alipay.com/file?token=secret"},"sign":"invalid"}`)
		_, _ = w.Write(body)
	}))
	defer server.Close()
	keys.provider.baseURL = server.URL + "/gateway.do"
	keys.provider.billResolver = billTestResolver{{IP: net.ParseIP("1.1.1.1")}}
	var downloads int
	keys.provider.billTransport = billTestRoundTripper(func(*http.Request) (*http.Response, error) {
		downloads++
		return nil, nil
	})
	date := time.Now().In(time.FixedZone("CST", 8*60*60)).AddDate(0, 0, -1)
	if _, err := keys.provider.DownloadTradeBill(context.Background(), date); err == nil || downloads != 0 {
		t.Fatalf("unsigned Alipay URL response accepted: downloads=%d err=%v", downloads, err)
	}
}

func TestAlipayBillHostAndDateFailClosed(t *testing.T) {
	for _, host := range []string{"dwbillcenter.alipay.com.evil.test", "notalipay.com", "evil-alipay.com", "dwbillcenter.alipay.com.attacker.test", "DWBILLCENTER.ALIPAY.COM."} {
		if isAlipayBillHost(host) != (host == "DWBILLCENTER.ALIPAY.COM.") {
			t.Errorf("host allowlist decision wrong for %q", host)
		}
	}
	keys := newTestKeys(t)
	today := time.Now().In(time.FixedZone("CST", 8*60*60))
	today = time.Date(today.Year(), today.Month(), today.Day(), 12, 0, 0, 0, today.Location())
	if _, err := keys.provider.DownloadTradeBill(context.Background(), today); err == nil {
		t.Fatal("today's not-yet-complete bill was accepted")
	}
}
