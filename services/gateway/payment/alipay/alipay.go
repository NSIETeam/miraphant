package alipay

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/songquanpeng/one-api/payment"
)

const apiBaseURL = "https://openapi.alipay.com/gateway.do"

type Config struct {
	Enabled            bool
	AppID              string
	MerchantID         string // Alipay seller_id
	MerchantPrivateKey *rsa.PrivateKey
	AlipayPublicKey    *rsa.PublicKey
	NotifyURL          string
	ReturnURL          string
}

type Provider struct {
	config  Config
	client  *http.Client
	baseURL string // unexported: tests may inject httptest, runtime input cannot change it
}

var (
	_ payment.Provider       = (*Provider)(nil)
	_ payment.RefundProvider = (*Provider)(nil)
)

func New(config Config) (*Provider, error) {
	if !config.Enabled {
		return nil, payment.ErrDisabled
	}
	if config.AppID == "" || config.MerchantID == "" || config.MerchantPrivateKey == nil || config.AlipayPublicKey == nil || config.MerchantPrivateKey.N.BitLen() < 2048 || config.AlipayPublicKey.N.BitLen() < 2048 {
		return nil, errors.New("incomplete Alipay RSA2 configuration")
	}
	if err := validateHTTPSURL(config.NotifyURL); err != nil {
		return nil, fmt.Errorf("invalid Alipay notification URL: %w", err)
	}
	if config.ReturnURL != "" {
		if err := validateHTTPSURL(config.ReturnURL); err != nil {
			return nil, fmt.Errorf("invalid Alipay return URL: %w", err)
		}
	}
	return &Provider{
		config:  config,
		client:  &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		baseURL: apiBaseURL,
	}, nil
}

func validateHTTPSURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("URL must be absolute HTTPS without userinfo or fragment")
	}
	return nil
}

func validOrderKey(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func formatFen(fen int64) (string, error) {
	if fen <= 0 || fen > 10_000_000_000 { // Alipay page-pay ceiling: CNY 100,000,000.00
		return "", errors.New("Alipay amount is outside the supported range")
	}
	return strconv.FormatInt(fen/100, 10) + "." + fmt.Sprintf("%02d", fen%100), nil
}

func (p *Provider) Create(_ context.Context, order payment.Order) (payment.Checkout, error) {
	if !validOrderKey(order.OrderKey) || order.Currency != "CNY" || order.Description == "" || !utf8.ValidString(order.Description) || utf8.RuneCountInString(order.Description) > 128 || order.ExpiresAt.IsZero() || !order.ExpiresAt.After(time.Now()) {
		return payment.Checkout{}, errors.New("invalid Alipay page-pay order")
	}
	amount, err := formatFen(order.AmountFen)
	if err != nil {
		return payment.Checkout{}, err
	}
	bizContent, err := json.Marshal(map[string]string{
		"out_trade_no": order.OrderKey,
		"product_code": "FAST_INSTANT_TRADE_PAY",
		"total_amount": amount,
		"subject":      order.Description,
		"time_expire":  order.ExpiresAt.In(time.FixedZone("CST", 8*60*60)).Format("2006-01-02 15:04:05"),
	})
	if err != nil {
		return payment.Checkout{}, err
	}
	params := map[string]string{
		"app_id":      p.config.AppID,
		"method":      "alipay.trade.page.pay",
		"charset":     "utf-8",
		"sign_type":   "RSA2",
		"timestamp":   time.Now().In(time.FixedZone("CST", 8*60*60)).Format("2006-01-02 15:04:05"),
		"version":     "1.0",
		"notify_url":  p.config.NotifyURL,
		"biz_content": string(bizContent),
	}
	if p.config.ReturnURL != "" {
		params["return_url"] = p.config.ReturnURL
	}
	signature, err := signParams(p.config.MerchantPrivateKey, params)
	if err != nil {
		return payment.Checkout{}, err
	}
	params["sign"] = signature
	return payment.Checkout{Kind: "form", GatewayURL: p.baseURL, Fields: params}, nil
}

func (p *Provider) VerifyNotification(_ http.Header, body []byte) (payment.VerifiedNotification, error) {
	if len(body) == 0 || len(body) > 1<<20 {
		return payment.VerifiedNotification{}, payment.ErrInvalidNotice
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return payment.VerifiedNotification{}, payment.ErrInvalidNotice
	}
	params := make(map[string]string, len(values))
	for key, items := range values {
		if len(items) != 1 {
			return payment.VerifiedNotification{}, payment.ErrInvalidNotice
		}
		params[key] = items[0]
	}
	if params["sign_type"] != "RSA2" || params["app_id"] != p.config.AppID || params["seller_id"] != p.config.MerchantID || params["notify_id"] == "" || !validOrderKey(params["out_trade_no"]) || params["trade_no"] == "" {
		return payment.VerifiedNotification{}, payment.ErrInvalidNotice
	}
	if err := verifyParams(p.config.AlipayPublicKey, params); err != nil {
		return payment.VerifiedNotification{}, payment.ErrInvalidNotice
	}
	fennies, err := parseFen(params["total_amount"])
	if err != nil {
		return payment.VerifiedNotification{}, payment.ErrInvalidNotice
	}
	var occurredAt time.Time
	if raw := params["gmt_payment"]; raw != "" {
		occurredAt, _ = time.ParseInLocation("2006-01-02 15:04:05", raw, time.FixedZone("CST", 8*60*60))
	}
	return payment.VerifiedNotification{Provider: "alipay", ProviderEventID: params["notify_id"], ProviderOccurredAt: occurredAt, OrderKey: params["out_trade_no"], TransactionID: params["trade_no"], MerchantID: params["seller_id"], AppID: params["app_id"], AmountFen: fennies, Currency: "CNY", Status: params["trade_status"]}, nil
}

func (p *Provider) Query(ctx context.Context, orderKey string) (payment.Trade, error) {
	if !validOrderKey(orderKey) {
		return payment.Trade{}, errors.New("invalid Alipay order number")
	}
	response, err := p.api(ctx, "alipay.trade.query", map[string]string{"out_trade_no": orderKey})
	if err != nil {
		return payment.Trade{}, err
	}
	var decoded tradeResponse
	if err := json.Unmarshal(response, &decoded); err != nil {
		return payment.Trade{}, err
	}
	if decoded.Code != "10000" {
		if decoded.SubCode == "ACQ.TRADE_NOT_EXIST" {
			return payment.Trade{}, payment.ErrNotPaid
		}
		return payment.Trade{}, payment.ErrUnknownStatus
	}
	if decoded.OrderKey != orderKey || decoded.TradeNo == "" || (decoded.SellerID != "" && decoded.SellerID != p.config.MerchantID) || (decoded.AppID != "" && decoded.AppID != p.config.AppID) {
		return payment.Trade{}, payment.ErrOrderMismatch
	}
	fennies, err := parseFen(decoded.TotalAmount)
	if err != nil {
		return payment.Trade{}, err
	}
	var occurredAt time.Time
	if decoded.SendPayDate != "" {
		occurredAt, _ = time.ParseInLocation("2006-01-02 15:04:05", decoded.SendPayDate, time.FixedZone("CST", 8*60*60))
	}
	return payment.Trade{Provider: "alipay", OrderKey: decoded.OrderKey, TransactionID: decoded.TradeNo, MerchantID: p.config.MerchantID, AppID: p.config.AppID, AmountFen: fennies, Currency: "CNY", Status: decoded.TradeStatus, ProviderOccurredAt: occurredAt}, nil
}

func (p *Provider) Close(ctx context.Context, orderKey string) error {
	trade, err := p.Query(ctx, orderKey)
	if err != nil {
		return err
	}
	if trade.Status == "TRADE_SUCCESS" || trade.Status == "TRADE_FINISHED" {
		return payment.ErrAlreadyPaid
	}
	if trade.Status != "WAIT_BUYER_PAY" {
		return payment.ErrNotCloseable
	}
	response, err := p.api(ctx, "alipay.trade.close", map[string]string{"out_trade_no": orderKey})
	if err != nil {
		return err
	}
	var closed tradeResponse
	if err := json.Unmarshal(response, &closed); err != nil {
		return err
	}
	if closed.Code != "10000" || closed.OrderKey != orderKey || (closed.TradeNo != "" && closed.TradeNo != trade.TransactionID) || (closed.SellerID != "" && closed.SellerID != p.config.MerchantID) || (closed.AppID != "" && closed.AppID != p.config.AppID) {
		return payment.ErrOrderMismatch
	}
	return nil
}

type refundRequestBody struct {
	OrderKey      string `json:"out_trade_no,omitempty"`
	TransactionID string `json:"trade_no,omitempty"`
	Amount        string `json:"refund_amount"`
	Reason        string `json:"refund_reason,omitempty"`
	RefundNumber  string `json:"out_request_no"`
}

type refundApplyResponse struct {
	Code          string `json:"code"`
	SubCode       string `json:"sub_code"`
	OrderKey      string `json:"out_trade_no"`
	TransactionID string `json:"trade_no"`
	Cumulative    string `json:"refund_fee"`
	FundChange    string `json:"fund_change"`
}

type refundQueryResponse struct {
	Code          string `json:"code"`
	SubCode       string `json:"sub_code"`
	OrderKey      string `json:"out_trade_no"`
	TransactionID string `json:"trade_no"`
	RefundNumber  string `json:"out_request_no"`
	TotalAmount   string `json:"total_amount"`
	RefundAmount  string `json:"refund_amount"`
	RefundStatus  string `json:"refund_status"`
}

func (p *Provider) ApplyRefund(ctx context.Context, request payment.RefundRequest) (payment.RefundResult, error) {
	if err := p.validateRefundRequest(request); err != nil {
		return payment.RefundResult{}, err
	}
	amount, err := formatFen(request.AmountFen)
	if err != nil {
		return payment.RefundResult{}, err
	}
	responseBody, err := p.api(ctx, "alipay.trade.refund", map[string]string{
		"out_trade_no":   request.OrderKey,
		"trade_no":       request.TransactionID,
		"refund_amount":  amount,
		"refund_reason":  request.Reason,
		"out_request_no": request.ProviderRefundKey,
	})
	if err != nil {
		return alipayRefundBase(request, payment.RefundUnknown, "", "apply"), err
	}
	var response refundApplyResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return alipayRefundBase(request, payment.RefundUnknown, "", "apply"), err
	}
	if response.Code != "10000" {
		// Even a signed business error only describes this response. The same
		// stable request number may have succeeded in an earlier timed-out try;
		// only a refund query can reconcile that history safely.
		result := alipayRefundBase(request, payment.RefundUnknown, response.SubCode, "apply")
		return result, fmt.Errorf("Alipay refund apply returned code %s", response.Code)
	}
	if response.OrderKey != request.OrderKey || response.TransactionID != request.TransactionID {
		return alipayRefundBase(request, payment.RefundUnknown, "", "apply"), payment.ErrOrderMismatch
	}
	cumulativeFen, err := parseFenAllowZero(response.Cumulative)
	if err != nil {
		return alipayRefundBase(request, payment.RefundUnknown, "", "apply"), errors.New("Alipay refund response lacks valid cumulative refund amount")
	}
	expected, ok := checkedAddFen(request.PriorRefundedFen, request.AmountFen)
	if !ok || (response.FundChange == "Y" && cumulativeFen != expected) ||
		(response.FundChange != "Y" && cumulativeFen != request.PriorRefundedFen && cumulativeFen != expected) {
		return alipayRefundBase(request, payment.RefundUnknown, "", "apply"), payment.ErrOrderMismatch
	}
	result := alipayRefundBase(request, payment.RefundAccepted, response.FundChange, "apply")
	if response.FundChange == "Y" {
		result.Outcome = payment.RefundSucceeded
		result.Status = "fund_change:Y"
	} else if response.FundChange != "N" && response.FundChange != "" {
		result.Outcome = payment.RefundUnknown
		return result, payment.ErrUnknownStatus
	}
	return result, nil
}

func (p *Provider) QueryRefund(ctx context.Context, request payment.RefundRequest) (payment.RefundResult, error) {
	if err := p.validateRefundRequest(request); err != nil {
		return payment.RefundResult{}, err
	}
	responseBody, err := p.api(ctx, "alipay.trade.fastpay.refund.query", map[string]string{
		"out_trade_no":   request.OrderKey,
		"trade_no":       request.TransactionID,
		"out_request_no": request.ProviderRefundKey,
	})
	if err != nil {
		return alipayRefundBase(request, payment.RefundUnknown, "", "query"), err
	}
	var response refundQueryResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return alipayRefundBase(request, payment.RefundUnknown, "", "query"), err
	}
	if response.Code != "10000" {
		return alipayRefundBase(request, payment.RefundUnknown, response.SubCode, "query"), fmt.Errorf("Alipay refund query returned code %s", response.Code)
	}
	if response.OrderKey != request.OrderKey || response.TransactionID != request.TransactionID || response.RefundNumber != request.ProviderRefundKey {
		return alipayRefundBase(request, payment.RefundUnknown, response.RefundStatus, "query"), payment.ErrOrderMismatch
	}
	totalFen, totalErr := parseFen(response.TotalAmount)
	refundFen, refundErr := parseFen(response.RefundAmount)
	if totalErr != nil || refundErr != nil || totalFen != request.TotalFen || refundFen != request.AmountFen {
		return alipayRefundBase(request, payment.RefundUnknown, response.RefundStatus, "query"), payment.ErrOrderMismatch
	}
	outcome := payment.RefundUnknown
	switch response.RefundStatus {
	case "REFUND_SUCCESS":
		outcome = payment.RefundSucceeded
	case "":
		// The official API says an absent refund_status can mean the request was
		// not received or failed. That ambiguity cannot release a ledger hold.
	default:
		// Unknown/future status strings remain non-terminal.
	}
	result := alipayRefundBase(request, outcome, response.RefundStatus, "query")
	return result, nil
}

func (p *Provider) validateRefundRequest(request payment.RefundRequest) error {
	if request.RefundKey == "" || request.ProviderRefundKey == "" || len(request.ProviderRefundKey) > 64 ||
		!validRefundNumber(request.ProviderRefundKey) || !validOrderKey(request.OrderKey) || request.TransactionID == "" || len(request.TransactionID) > 64 ||
		request.MerchantID != p.config.MerchantID || request.AppID != p.config.AppID || request.AmountFen <= 0 || request.TotalFen < request.AmountFen ||
		request.PriorRefundedFen < 0 || request.PriorRefundedFen > request.TotalFen-request.AmountFen || request.Currency != "CNY" || len([]byte(request.Reason)) > 256 {
		return errors.New("Alipay refund snapshot does not match configured merchant or amount")
	}
	return nil
}

func alipayRefundBase(request payment.RefundRequest, outcome payment.RefundOutcome, status, source string) payment.RefundResult {
	return payment.RefundResult{Provider: "alipay", Outcome: outcome, Status: status, RefundKey: request.RefundKey,
		ProviderRefundKey: request.ProviderRefundKey, OrderKey: request.OrderKey, TransactionID: request.TransactionID,
		MerchantID: request.MerchantID, AppID: request.AppID, AmountFen: request.AmountFen, TotalFen: request.TotalFen,
		Currency: request.Currency, EvidenceSource: source}
}

func checkedAddFen(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || left > int64(^uint64(0)>>1)-right {
		return 0, false
	}
	return left + right, true
}

func parseFenAllowZero(value string) (int64, error) {
	if value == "0" || value == "0.0" || value == "0.00" {
		return 0, nil
	}
	return parseFen(value)
}

func validRefundNumber(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

type tradeResponse struct {
	Code        string `json:"code"`
	SubCode     string `json:"sub_code"`
	OrderKey    string `json:"out_trade_no"`
	TradeNo     string `json:"trade_no"`
	SellerID    string `json:"seller_id"`
	AppID       string `json:"app_id"`
	TradeStatus string `json:"trade_status"`
	TotalAmount string `json:"total_amount"`
	SendPayDate string `json:"send_pay_date"`
}

func (p *Provider) api(ctx context.Context, method string, biz map[string]string) ([]byte, error) {
	bizContent, err := json.Marshal(biz)
	if err != nil {
		return nil, err
	}
	params := map[string]string{
		"app_id":      p.config.AppID,
		"method":      method,
		"charset":     "utf-8",
		"sign_type":   "RSA2",
		"timestamp":   time.Now().In(time.FixedZone("CST", 8*60*60)).Format("2006-01-02 15:04:05"),
		"version":     "1.0",
		"biz_content": string(bizContent),
	}
	signature, err := signParams(p.config.MerchantPrivateKey, params)
	if err != nil {
		return nil, err
	}
	params["sign"] = signature
	values := make(url.Values, len(params))
	for key, value := range params {
		values.Set(key, value)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 1<<20 {
		return nil, errors.New("Alipay response too large")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected Alipay HTTP status %d", resp.StatusCode)
	}
	return p.verifyAPIResponse(method, body)
}

func (p *Provider) verifyAPIResponse(method string, body []byte) ([]byte, error) {
	node := strings.ReplaceAll(method, ".", "_") + "_response"
	responseJSON, signature, err := extractSignedResponse(body, node)
	if err != nil || signature == "" {
		return nil, errors.New("invalid Alipay signed response")
	}
	decoded, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return nil, errors.New("invalid Alipay response signature")
	}
	digest := sha256.Sum256(responseJSON)
	if err := rsa.VerifyPKCS1v15(p.config.AlipayPublicKey, crypto.SHA256, digest[:], decoded); err != nil {
		return nil, errors.New("Alipay response signature verification failed")
	}
	return responseJSON, nil
}

// extractSignedResponse returns the exact raw JSON bytes of the method response
// node. It does not decode and re-encode the object before signature checking.
func extractSignedResponse(body []byte, wanted string) ([]byte, string, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, "", errors.New("invalid JSON response envelope")
	}
	var response []byte
	var signature string
	seen := make(map[string]bool)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, "", err
		}
		key, ok := keyToken.(string)
		if !ok || seen[key] {
			return nil, "", errors.New("duplicate or invalid JSON response key")
		}
		seen[key] = true
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, "", err
		}
		switch key {
		case wanted:
			if len(raw) == 0 || raw[0] != '{' {
				return nil, "", errors.New("Alipay response node is not an object")
			}
			response = append([]byte(nil), raw...)
		case "sign":
			if err := json.Unmarshal(raw, &signature); err != nil {
				return nil, "", err
			}
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, "", err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, "", errors.New("trailing JSON response data")
	}
	if len(response) == 0 || signature == "" {
		return nil, "", errors.New("Alipay response signature fields missing")
	}
	return response, signature, nil
}

func signParams(key *rsa.PrivateKey, params map[string]string) (string, error) {
	canonical := canonicalParams(params, true)
	digest := sha256.Sum256([]byte(canonical))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

func verifyParams(key *rsa.PublicKey, params map[string]string) error {
	signature, err := base64.StdEncoding.DecodeString(params["sign"])
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(canonicalParams(params, false)))
	return rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature)
}

func canonicalParams(params map[string]string, includeSignType bool) string {
	keys := make([]string, 0, len(params))
	for key, value := range params {
		if key != "sign" && (includeSignType || key != "sign_type") && value != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+params[key])
	}
	return strings.Join(parts, "&")
}

func parseFen(value string) (int64, error) {
	if value == "" || strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") || strings.ContainsAny(value, "eE") {
		return 0, errors.New("invalid Alipay amount")
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, errors.New("invalid Alipay amount")
	}
	yuan, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || yuan < 0 {
		return 0, errors.New("invalid Alipay amount")
	}
	fen := int64(0)
	if len(parts) == 2 {
		fraction := parts[1]
		if len(fraction) == 0 || len(fraction) > 2 {
			return 0, errors.New("invalid Alipay amount precision")
		}
		for _, r := range fraction {
			if r < '0' || r > '9' {
				return 0, errors.New("invalid Alipay amount")
			}
		}
		if len(fraction) == 1 {
			fraction += "0"
		}
		fen, err = strconv.ParseInt(fraction, 10, 64)
		if err != nil {
			return 0, errors.New("invalid Alipay amount")
		}
	}
	if yuan > int64(^uint64(0)>>1)/100 {
		return 0, errors.New("Alipay amount overflow")
	}
	total := yuan*100 + fen
	if total <= 0 || total > 10_000_000_000 {
		return 0, errors.New("Alipay amount is outside the supported range")
	}
	return total, nil
}
