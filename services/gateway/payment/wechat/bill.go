package wechat

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/songquanpeng/one-api/payment/bill"
)

type tradeBillResponse struct {
	HashType    string `json:"hash_type"`
	HashValue   string `json:"hash_value"`
	DownloadURL string `json:"download_url"`
}

// DownloadTradeBill obtains and parses WeChat's documented ALL trade bill.
// The merchant and app identities come from server configuration; callers
// cannot provide a URL, merchant ID, or bill type.
func (p *Provider) DownloadTradeBill(ctx context.Context, billDate time.Time) (bill.Statement, error) {
	day, err := validateWeChatBillDate(billDate, time.Now())
	if err != nil {
		return bill.Statement{}, err
	}
	query := url.Values{}
	query.Set("bill_date", day)
	query.Set("bill_type", "ALL")
	requestURI := "/v3/bill/tradebill?" + query.Encode()
	response, headers, status, err := p.call(ctx, http.MethodGet, requestURI, nil)
	if err != nil {
		return bill.Statement{}, err
	}
	if status != http.StatusOK || p.verifyResponse(headers, response) != nil {
		return bill.Statement{}, errors.New("WeChat trade bill response could not be verified")
	}
	var metadata tradeBillResponse
	decoder := json.NewDecoder(strings.NewReader(string(response)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil || metadata.HashType != "SHA1" || !validSHA1(metadata.HashValue) {
		return bill.Statement{}, errors.New("invalid WeChat trade bill metadata")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return bill.Statement{}, errors.New("invalid WeChat trade bill metadata")
	}
	apiURL, err := url.Parse(p.baseURL)
	if err != nil {
		return bill.Statement{}, errors.New("WeChat bill API configuration is invalid")
	}
	downloadURL, err := url.Parse(metadata.DownloadURL)
	if err != nil || len(metadata.DownloadURL) > 4096 || downloadURL.Scheme != "https" || !strings.EqualFold(downloadURL.Hostname(), apiURL.Hostname()) || downloadURL.User != nil || downloadURL.Fragment != "" || !validWeChatBillPath(downloadURL.EscapedPath()) || downloadURL.RawQuery == "" {
		return bill.Statement{}, bill.ErrUnsafeDownloadURL
	}
	requestURI = downloadURL.RequestURI()
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	nonce, err := randomNonce()
	if err != nil {
		return bill.Statement{}, err
	}
	canonical := http.MethodGet + "\n" + requestURI + "\n" + timestamp + "\n" + nonce + "\n\n"
	signature, err := signRSA(p.config.APIPrivateKey, []byte(canonical))
	if err != nil {
		return bill.Statement{}, err
	}
	authorization := fmt.Sprintf(`WECHATPAY2-SHA256-RSA2048 mchid="%s",nonce_str="%s",signature="%s",timestamp="%s",serial_no="%s"`, p.config.MerchantID, nonce, signature, timestamp, p.config.MerchantSerial)
	requestHeaders := make(http.Header)
	requestHeaders.Set("Authorization", authorization)
	body, err := bill.FetchBillURL(ctx, metadata.DownloadURL, []string{apiURL.Host}, requestHeaders, p.billResolver, p.billTransport)
	if err != nil {
		return bill.Statement{}, err
	}
	digest := sha1.Sum(body)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), metadata.HashValue) {
		return bill.Statement{}, errors.New("WeChat trade bill hash mismatch")
	}
	location, _ := time.LoadLocation("Asia/Shanghai")
	parsed, err := bill.ParseWeChatTradeBill(body, mustParseBillDate(day, location), p.config.MerchantID, p.config.AppID)
	if err != nil {
		return bill.Statement{}, err
	}
	parsed.SourceSHA256 = bill.SHA256(body)
	parsed.ProviderHashType = metadata.HashType
	parsed.ProviderHashValue = strings.ToLower(metadata.HashValue)
	parsed.ProviderHashVerified = true
	return parsed, nil
}

func validateWeChatBillDate(value, now time.Time) (string, error) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return "", err
	}
	target := value.In(location)
	day := time.Date(target.Year(), target.Month(), target.Day(), 0, 0, 0, 0, location)
	today := now.In(location)
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, location)
	if !day.Before(today) || day.Before(today.AddDate(0, -3, 0)) {
		return "", errors.New("WeChat trade bill date must be a previous date within three months")
	}
	return day.Format("2006-01-02"), nil
}

func validWeChatBillPath(path string) bool {
	return path == "/v3/bill/downloadurl" || path == "/v3/billdownload/file"
}

func validSHA1(value string) bool {
	if len(value) != sha1.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha1.Size
}

func mustParseBillDate(value string, location *time.Location) time.Time {
	date, _ := time.ParseInLocation("2006-01-02", value, location)
	return date
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("trailing or invalid JSON data")
	}
	return nil
}
