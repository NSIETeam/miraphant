package alipay

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/songquanpeng/one-api/payment/bill"
)

// DownloadTradeBill verifies Alipay's signed bill-download API response and
// fetches the short-lived file URL. The normal-merchant trade-file schema is
// intentionally not parsed until an authoritative current format is available.
func (p *Provider) DownloadTradeBill(ctx context.Context, billDate time.Time) (bill.RawBill, error) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return bill.RawBill{}, err
	}
	date := billDate.In(location)
	today := time.Now().In(location)
	if !date.Before(time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, location)) {
		return bill.RawBill{}, errors.New("Alipay trade bill date must be a completed day")
	}
	day := date.Format("2006-01-02")
	response, err := p.api(ctx, "alipay.data.dataservice.bill.downloadurl.query", map[string]string{
		"bill_type": "trade",
		"bill_date": day,
		"secure":    "true",
	})
	if err != nil {
		return bill.RawBill{}, err
	}
	var result struct {
		Code            string `json:"code"`
		Msg             string `json:"msg"`
		SubCode         string `json:"sub_code"`
		SubMsg          string `json:"sub_msg"`
		BillDownloadURL string `json:"bill_download_url"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(response)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil || ensureAlipayJSONEOF(decoder) != nil || result.Code != "10000" || result.BillDownloadURL == "" {
		return bill.RawBill{}, errors.New("invalid signed Alipay bill URL response")
	}
	if len(result.BillDownloadURL) > 4096 {
		return bill.RawBill{}, bill.ErrUnsafeDownloadURL
	}
	downloadURL, err := url.Parse(result.BillDownloadURL)
	if err != nil || downloadURL.Scheme != "https" || downloadURL.Hostname() == "" || downloadURL.User != nil || downloadURL.Fragment != "" || (downloadURL.Port() != "" && downloadURL.Port() != "443") || !isAlipayBillHost(downloadURL.Hostname()) {
		return bill.RawBill{}, bill.ErrUnsafeDownloadURL
	}
	data, err := bill.FetchBillURL(ctx, result.BillDownloadURL, []string{downloadURL.Host}, nil, p.billResolver, p.billTransport)
	if err != nil {
		return bill.RawBill{}, err
	}
	return bill.RawBill{
		Provider:            "alipay",
		BillDate:            day,
		Timezone:            location.String(),
		FormatVersion:       "alipay-trade-raw-unparsed-v1",
		RequestedMerchantID: p.config.MerchantID,
		RequestedAppID:      p.config.AppID,
		Bytes:               data,
		SHA256:              bill.SHA256(data),
	}, nil
}

func isAlipayBillHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "dwbillcenter.alipay.com"
}

func ensureAlipayJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("trailing or malformed JSON data")
	}
	return nil
}
