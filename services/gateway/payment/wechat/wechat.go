package wechat

import (
	"bytes"
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/songquanpeng/one-api/payment"
	"github.com/songquanpeng/one-api/payment/bill"
)

const apiBaseURL = "https://api.mch.weixin.qq.com"

type Config struct {
	Enabled           bool
	MerchantID        string
	AppID             string
	MerchantSerial    string
	PlatformSerial    string
	APIPrivateKey     *rsa.PrivateKey
	PlatformPublicKey *rsa.PublicKey
	APIv3Key          []byte
	NotifyURL         string
	RefundNotifyURL   string
}

type Provider struct {
	config        Config
	client        *http.Client
	baseURL       string // private so runtime/user input cannot redirect payment requests
	billTransport http.RoundTripper
	billResolver  bill.Resolver
}

var (
	_ payment.Provider                   = (*Provider)(nil)
	_ payment.RefundProvider             = (*Provider)(nil)
	_ payment.RefundNotificationVerifier = (*Provider)(nil)
)

func New(config Config) (*Provider, error) {
	if !config.Enabled {
		return nil, payment.ErrDisabled
	}
	if config.MerchantID == "" || config.AppID == "" || config.MerchantSerial == "" || config.PlatformSerial == "" || config.APIPrivateKey == nil || config.PlatformPublicKey == nil || len(config.APIv3Key) != 32 {
		return nil, errors.New("incomplete WeChat Pay configuration")
	}
	if err := validateNotifyURL(config.NotifyURL); err != nil {
		return nil, err
	}
	if config.RefundNotifyURL != "" {
		if err := validateNotifyURL(config.RefundNotifyURL); err != nil {
			return nil, fmt.Errorf("invalid WeChat Pay refund notification URL: %w", err)
		}
	}
	return &Provider{config: config, client: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, baseURL: apiBaseURL}, nil
}

func validateNotifyURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("payment notification URL must be an absolute HTTPS URL without userinfo, query, or fragment")
	}
	return nil
}

type amount struct {
	Total    int64  `json:"total"`
	Currency string `json:"currency"`
}
type nativeRequest struct {
	AppID       string `json:"appid"`
	MerchantID  string `json:"mchid"`
	Description string `json:"description"`
	OrderKey    string `json:"out_trade_no"`
	TimeExpire  string `json:"time_expire,omitempty"`
	NotifyURL   string `json:"notify_url"`
	Amount      amount `json:"amount"`
}

func (p *Provider) Create(ctx context.Context, order payment.Order) (payment.Checkout, error) {
	if order.OrderKey == "" || order.AmountFen <= 0 || order.Currency != "CNY" || order.Description == "" {
		return payment.Checkout{}, errors.New("invalid WeChat Pay order")
	}
	if len(order.OrderKey) < 6 || len(order.OrderKey) > 32 || !validOrderKey(order.OrderKey) {
		return payment.Checkout{}, errors.New("WeChat Pay order number must be 6-32 allowed characters")
	}
	body, err := json.Marshal(nativeRequest{AppID: p.config.AppID, MerchantID: p.config.MerchantID, Description: order.Description, OrderKey: order.OrderKey, TimeExpire: order.ExpiresAt.UTC().Format(time.RFC3339), NotifyURL: p.config.NotifyURL, Amount: amount{Total: order.AmountFen, Currency: order.Currency}})
	if err != nil {
		return payment.Checkout{}, err
	}
	response, headers, _, err := p.call(ctx, http.MethodPost, "/v3/pay/transactions/native", body)
	if err != nil {
		return payment.Checkout{}, err
	}
	if err := p.verifyResponse(headers, response); err != nil {
		return payment.Checkout{}, err
	}
	var decoded struct {
		CodeURL string `json:"code_url"`
	}
	if err := json.Unmarshal(response, &decoded); err != nil || !strings.HasPrefix(decoded.CodeURL, "weixin://") {
		return payment.Checkout{}, errors.New("invalid WeChat Pay prepay response")
	}
	return payment.Checkout{Kind: "qr", CodeURL: decoded.CodeURL}, nil
}

func (p *Provider) VerifyNotification(headers http.Header, body []byte) (payment.VerifiedNotification, error) {
	if len(body) == 0 || len(body) > 1<<20 {
		return payment.VerifiedNotification{}, payment.ErrInvalidNotice
	}
	timestamp := headers.Get("Wechatpay-Timestamp")
	nonce := headers.Get("Wechatpay-Nonce")
	signature := headers.Get("Wechatpay-Signature")
	serial := headers.Get("Wechatpay-Serial")
	if timestamp == "" || nonce == "" || signature == "" || serial == "" || serial != p.config.PlatformSerial {
		return payment.VerifiedNotification{}, payment.ErrInvalidNotice
	}
	if !freshTimestamp(timestamp, time.Now()) {
		return payment.VerifiedNotification{}, payment.ErrInvalidNotice
	}
	message := []byte(timestamp + "\n" + nonce + "\n" + string(body) + "\n")
	if err := verifyRSA(p.config.PlatformPublicKey, message, signature); err != nil {
		return payment.VerifiedNotification{}, payment.ErrInvalidNotice
	}
	var envelope struct {
		ID         string `json:"id"`
		CreateTime string `json:"create_time"`
		EventType  string `json:"event_type"`
		Resource   struct {
			Algorithm      string `json:"algorithm"`
			Ciphertext     string `json:"ciphertext"`
			Nonce          string `json:"nonce"`
			AssociatedData string `json:"associated_data"`
		} `json:"resource"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.ID == "" || envelope.Resource.Algorithm != "AEAD_AES_256_GCM" {
		return payment.VerifiedNotification{}, payment.ErrInvalidNotice
	}
	plaintext, err := decryptResource(p.config.APIv3Key, envelope.Resource.Nonce, envelope.Resource.AssociatedData, envelope.Resource.Ciphertext)
	if err != nil {
		return payment.VerifiedNotification{}, payment.ErrInvalidNotice
	}
	var data struct {
		AppID         string `json:"appid"`
		MerchantID    string `json:"mchid"`
		OrderKey      string `json:"out_trade_no"`
		TransactionID string `json:"transaction_id"`
		TradeState    string `json:"trade_state"`
		SuccessTime   string `json:"success_time"`
		Amount        struct {
			Total    int64  `json:"total"`
			Currency string `json:"currency"`
		} `json:"amount"`
	}
	if err := json.Unmarshal(plaintext, &data); err != nil || data.OrderKey == "" || data.TransactionID == "" || data.Amount.Total <= 0 {
		return payment.VerifiedNotification{}, payment.ErrInvalidNotice
	}
	occurredAt, _ := time.Parse(time.RFC3339, data.SuccessTime)
	if occurredAt.IsZero() {
		occurredAt, _ = time.Parse(time.RFC3339, envelope.CreateTime)
	}
	return payment.VerifiedNotification{Provider: "wechat", ProviderEventID: envelope.ID, ProviderOccurredAt: occurredAt, OrderKey: data.OrderKey, TransactionID: data.TransactionID, MerchantID: data.MerchantID, AppID: data.AppID, AmountFen: data.Amount.Total, Currency: data.Amount.Currency, Status: data.TradeState}, nil
}

func (p *Provider) Query(ctx context.Context, orderKey string) (payment.Trade, error) {
	if !validOrderKey(orderKey) {
		return payment.Trade{}, errors.New("invalid order number")
	}
	path := "/v3/pay/transactions/out-trade-no/" + url.PathEscape(orderKey) + "?mchid=" + url.QueryEscape(p.config.MerchantID)
	body, headers, _, err := p.call(ctx, http.MethodGet, path, nil)
	if err != nil {
		return payment.Trade{}, err
	}
	if err := p.verifyResponse(headers, body); err != nil {
		return payment.Trade{}, err
	}
	var decoded struct {
		AppID         string `json:"appid"`
		MerchantID    string `json:"mchid"`
		OrderKey      string `json:"out_trade_no"`
		TransactionID string `json:"transaction_id"`
		TradeState    string `json:"trade_state"`
		SuccessTime   string `json:"success_time"`
		Amount        struct {
			Total    int64  `json:"total"`
			Currency string `json:"currency"`
		} `json:"amount"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return payment.Trade{}, err
	}
	if decoded.OrderKey != orderKey || decoded.MerchantID != p.config.MerchantID || decoded.AppID != p.config.AppID {
		return payment.Trade{}, payment.ErrOrderMismatch
	}
	when, _ := time.Parse(time.RFC3339, decoded.SuccessTime)
	return payment.Trade{Provider: "wechat", OrderKey: decoded.OrderKey, TransactionID: decoded.TransactionID, MerchantID: decoded.MerchantID, AppID: decoded.AppID, AmountFen: decoded.Amount.Total, Currency: decoded.Amount.Currency, Status: decoded.TradeState, ProviderOccurredAt: when}, nil
}

func (p *Provider) Close(ctx context.Context, orderKey string) error {
	trade, err := p.Query(ctx, orderKey)
	if err != nil {
		return err
	}
	if trade.Status == "SUCCESS" {
		return payment.ErrAlreadyPaid
	}
	if trade.Status != "NOTPAY" {
		return payment.ErrNotCloseable
	}
	body, _ := json.Marshal(map[string]string{"mchid": p.config.MerchantID})
	path := "/v3/pay/transactions/out-trade-no/" + url.PathEscape(orderKey) + "/close"
	response, headers, status, err := p.call(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	if status != http.StatusNoContent {
		return fmt.Errorf("unexpected WeChat Pay close status %d", status)
	}
	if err := p.verifyResponse(headers, response); err != nil {
		return err
	}
	return nil
}

type refundAmount struct {
	Refund   int64  `json:"refund"`
	Total    int64  `json:"total"`
	Currency string `json:"currency"`
}

type refundApplyRequest struct {
	TransactionID string       `json:"transaction_id,omitempty"`
	OrderKey      string       `json:"out_trade_no,omitempty"`
	RefundNumber  string       `json:"out_refund_no"`
	Reason        string       `json:"reason,omitempty"`
	NotifyURL     string       `json:"notify_url,omitempty"`
	Amount        refundAmount `json:"amount"`
}

type refundResponse struct {
	RefundID      string       `json:"refund_id"`
	RefundNumber  string       `json:"out_refund_no"`
	TransactionID string       `json:"transaction_id"`
	OrderKey      string       `json:"out_trade_no"`
	Status        string       `json:"status"`
	SuccessTime   string       `json:"success_time"`
	CreateTime    string       `json:"create_time"`
	Amount        refundAmount `json:"amount"`
}

func (p *Provider) ApplyRefund(ctx context.Context, request payment.RefundRequest) (payment.RefundResult, error) {
	if err := p.validateRefundRequest(request); err != nil {
		return payment.RefundResult{}, err
	}
	if len(request.ProviderRefundKey) > 64 || !validRefundNumber(request.ProviderRefundKey) || len([]byte(request.Reason)) > 80 {
		return payment.RefundResult{}, errors.New("invalid WeChat Pay refund number or reason")
	}
	body, err := json.Marshal(refundApplyRequest{
		TransactionID: request.TransactionID,
		RefundNumber:  request.ProviderRefundKey,
		Reason:        request.Reason,
		NotifyURL:     p.config.RefundNotifyURL,
		Amount:        refundAmount{Refund: request.AmountFen, Total: request.TotalFen, Currency: request.Currency},
	})
	if err != nil {
		return payment.RefundResult{}, err
	}
	responseBody, headers, _, err := p.call(ctx, http.MethodPost, "/v3/refund/domestic/refunds", body)
	if err != nil {
		return wechatRefundBase(request, payment.RefundUnknown, "", "apply"), err
	}
	if err := p.verifyResponse(headers, responseBody); err != nil {
		return wechatRefundBase(request, payment.RefundUnknown, "", "apply"), err
	}
	var response refundResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return wechatRefundBase(request, payment.RefundUnknown, "", "apply"), err
	}
	result, err := p.normalizeRefundResponse(request, response, "apply")
	if err != nil {
		return wechatRefundBase(request, payment.RefundUnknown, response.Status, "apply"), err
	}
	return result, nil
}

func (p *Provider) QueryRefund(ctx context.Context, request payment.RefundRequest) (payment.RefundResult, error) {
	if err := p.validateRefundRequest(request); err != nil {
		return payment.RefundResult{}, err
	}
	if len(request.ProviderRefundKey) > 64 || !validRefundNumber(request.ProviderRefundKey) {
		return payment.RefundResult{}, errors.New("invalid WeChat Pay refund number")
	}
	path := "/v3/refund/domestic/refunds/" + url.PathEscape(request.ProviderRefundKey)
	body, headers, _, err := p.call(ctx, http.MethodGet, path, nil)
	if err != nil {
		var httpErr *providerHTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound && len(body) > 0 && p.verifyResponse(headers, body) == nil {
			var absent struct {
				Code string `json:"code"`
			}
			if json.Unmarshal(body, &absent) == nil && absent.Code == "RESOURCE_NOT_EXISTS" {
				result := wechatRefundBase(request, payment.RefundUnknown, absent.Code, "query")
				result.RetrySameKey = true
				return result, nil
			}
		}
		return wechatRefundBase(request, payment.RefundUnknown, "", "query"), err
	}
	if err := p.verifyResponse(headers, body); err != nil {
		return wechatRefundBase(request, payment.RefundUnknown, "", "query"), err
	}
	var response refundResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return wechatRefundBase(request, payment.RefundUnknown, "", "query"), err
	}
	result, err := p.normalizeRefundResponse(request, response, "query")
	if err != nil {
		return wechatRefundBase(request, payment.RefundUnknown, response.Status, "query"), err
	}
	return result, nil
}

func (p *Provider) validateRefundRequest(request payment.RefundRequest) error {
	if request.RefundKey == "" || request.ProviderRefundKey == "" || request.OrderKey == "" || request.TransactionID == "" ||
		request.MerchantID != p.config.MerchantID || request.AppID != p.config.AppID || request.AmountFen <= 0 || request.TotalFen < request.AmountFen ||
		request.PriorRefundedFen < 0 || request.PriorRefundedFen > request.TotalFen-request.AmountFen || request.Currency != "CNY" {
		return errors.New("WeChat Pay refund snapshot does not match configured merchant or amount")
	}
	if !validOrderKey(request.OrderKey) || len(request.TransactionID) > 32 {
		return errors.New("invalid WeChat Pay original order identity")
	}
	return nil
}

func (p *Provider) normalizeRefundResponse(request payment.RefundRequest, response refundResponse, source string) (payment.RefundResult, error) {
	if response.RefundID == "" || response.RefundNumber != request.ProviderRefundKey || response.OrderKey != request.OrderKey ||
		response.TransactionID != request.TransactionID || response.Amount.Refund != request.AmountFen || response.Amount.Total != request.TotalFen ||
		response.Amount.Currency != request.Currency {
		return payment.RefundResult{}, payment.ErrOrderMismatch
	}
	outcome, ok := wechatRefundOutcome(response.Status)
	if !ok {
		return payment.RefundResult{}, payment.ErrUnknownStatus
	}
	if source == "apply" {
		// WeChat explicitly documents a successful apply response as accepted,
		// not as the final refund result. Finalization waits for a query or
		// verified refund notification even if the body contains a known state.
		outcome = payment.RefundAccepted
	}
	occurredAt, _ := time.Parse(time.RFC3339, response.SuccessTime)
	if occurredAt.IsZero() {
		occurredAt, _ = time.Parse(time.RFC3339, response.CreateTime)
	}
	result := wechatRefundBase(request, outcome, response.Status, source)
	result.ProviderRefundID = response.RefundID
	result.ProviderOccurredAt = occurredAt
	return result, nil
}

func wechatRefundOutcome(status string) (payment.RefundOutcome, bool) {
	switch status {
	case "SUCCESS":
		return payment.RefundSucceeded, true
	case "CLOSED":
		return payment.RefundDefiniteFailed, true
	case "PROCESSING":
		return payment.RefundAccepted, true
	case "ABNORMAL":
		return payment.RefundAbnormal, true
	default:
		return payment.RefundUnknown, false
	}
}

func wechatRefundBase(request payment.RefundRequest, outcome payment.RefundOutcome, status, source string) payment.RefundResult {
	return payment.RefundResult{Provider: "wechat", Outcome: outcome, Status: status, RefundKey: request.RefundKey,
		ProviderRefundKey: request.ProviderRefundKey, OrderKey: request.OrderKey, TransactionID: request.TransactionID,
		MerchantID: request.MerchantID, AppID: request.AppID, AmountFen: request.AmountFen, TotalFen: request.TotalFen,
		Currency: request.Currency, EvidenceSource: source}
}

func validRefundNumber(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || strings.ContainsRune("_-|*@", r)) {
			return false
		}
	}
	return true
}

// VerifyRefundNotification verifies the original signed envelope before AES-GCM
// decryption. The app identity is bound to this configured merchant context;
// the refund notification resource does not contain an appid field.
func (p *Provider) VerifyRefundNotification(headers http.Header, body []byte) (payment.RefundResult, error) {
	if len(body) == 0 || len(body) > 1<<20 {
		return payment.RefundResult{}, payment.ErrInvalidNotice
	}
	timestamp, nonce := headers.Get("Wechatpay-Timestamp"), headers.Get("Wechatpay-Nonce")
	signature, serial := headers.Get("Wechatpay-Signature"), headers.Get("Wechatpay-Serial")
	if timestamp == "" || nonce == "" || signature == "" || serial != p.config.PlatformSerial || !freshTimestamp(timestamp, time.Now()) {
		return payment.RefundResult{}, payment.ErrInvalidNotice
	}
	message := []byte(timestamp + "\n" + nonce + "\n" + string(body) + "\n")
	if err := verifyRSA(p.config.PlatformPublicKey, message, signature); err != nil {
		return payment.RefundResult{}, payment.ErrInvalidNotice
	}
	var envelope struct {
		ID           string `json:"id"`
		CreateTime   string `json:"create_time"`
		EventType    string `json:"event_type"`
		Summary      string `json:"summary"`
		ResourceType string `json:"resource_type"`
		Resource     struct {
			OriginalType   string `json:"original_type"`
			Algorithm      string `json:"algorithm"`
			Ciphertext     string `json:"ciphertext"`
			Nonce          string `json:"nonce"`
			AssociatedData string `json:"associated_data"`
		} `json:"resource"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.ID == "" || envelope.EventType == "" || envelope.Summary == "" ||
		envelope.ResourceType != "encrypt-resource" || envelope.Resource.Algorithm != "AEAD_AES_256_GCM" {
		return payment.RefundResult{}, payment.ErrInvalidNotice
	}
	if envelope.Resource.OriginalType != "refund" {
		return payment.RefundResult{}, payment.ErrInvalidNotice
	}
	plaintext, err := decryptResource(p.config.APIv3Key, envelope.Resource.Nonce, envelope.Resource.AssociatedData, envelope.Resource.Ciphertext)
	if err != nil {
		return payment.RefundResult{}, payment.ErrInvalidNotice
	}
	var data struct {
		MerchantID    string `json:"mchid"`
		OrderKey      string `json:"out_trade_no"`
		TransactionID string `json:"transaction_id"`
		RefundNumber  string `json:"out_refund_no"`
		RefundID      string `json:"refund_id"`
		Status        string `json:"refund_status"`
		SuccessTime   string `json:"success_time"`
		Amount        struct {
			Total    int64  `json:"total"`
			Refund   int64  `json:"refund"`
			Currency string `json:"currency"`
		} `json:"amount"`
	}
	if err := json.Unmarshal(plaintext, &data); err != nil || data.MerchantID != p.config.MerchantID || !validOrderKey(data.OrderKey) || data.TransactionID == "" || len(data.TransactionID) > 32 ||
		!validRefundNumber(data.RefundNumber) || data.RefundID == "" || len(data.RefundID) > 32 || data.Amount.Total <= 0 || data.Amount.Refund <= 0 || data.Amount.Refund > data.Amount.Total || data.Amount.Currency != "CNY" {
		return payment.RefundResult{}, payment.ErrInvalidNotice
	}
	outcome, ok := wechatRefundOutcome(data.Status)
	if !ok || !wechatRefundEventMatches(envelope.EventType, data.Status) {
		return payment.RefundResult{}, payment.ErrUnknownStatus
	}
	occurredAt, _ := time.Parse(time.RFC3339, data.SuccessTime)
	if occurredAt.IsZero() {
		occurredAt, _ = time.Parse(time.RFC3339, envelope.CreateTime)
	}
	return payment.RefundResult{Provider: "wechat", Outcome: outcome, Status: data.Status, ProviderRefundKey: data.RefundNumber,
		ProviderRefundID: data.RefundID, OrderKey: data.OrderKey, TransactionID: data.TransactionID, MerchantID: data.MerchantID,
		AppID: p.config.AppID, AmountFen: data.Amount.Refund, TotalFen: data.Amount.Total, Currency: data.Amount.Currency,
		ProviderEventID: envelope.ID, ProviderOccurredAt: occurredAt, EvidenceSource: "notification"}, nil
}

func wechatRefundEventMatches(eventType, status string) bool {
	switch eventType {
	case "REFUND.SUCCESS":
		return status == "SUCCESS"
	case "REFUND.CLOSED":
		return status == "CLOSED"
	case "REFUND.ABNORMAL":
		return status == "ABNORMAL"
	default:
		return false
	}
}

func (p *Provider) call(ctx context.Context, method, requestURI string, body []byte) ([]byte, http.Header, int, error) {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce, err := randomNonce()
	if err != nil {
		return nil, nil, 0, err
	}
	canonical := method + "\n" + requestURI + "\n" + timestamp + "\n" + nonce + "\n" + string(body) + "\n"
	signature, err := signRSA(p.config.APIPrivateKey, []byte(canonical))
	if err != nil {
		return nil, nil, 0, err
	}
	authorization := fmt.Sprintf(`WECHATPAY2-SHA256-RSA2048 mchid="%s",nonce_str="%s",signature="%s",timestamp="%s",serial_no="%s"`, p.config.MerchantID, nonce, signature, timestamp, p.config.MerchantSerial)
	url := p.baseURL + requestURI
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, 0, err
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err != nil {
		return nil, nil, resp.StatusCode, err
	}
	if len(data) > 1<<20 {
		return nil, nil, resp.StatusCode, errors.New("WeChat Pay response too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return data, resp.Header.Clone(), resp.StatusCode, &providerHTTPError{StatusCode: resp.StatusCode}
	}
	return data, resp.Header.Clone(), resp.StatusCode, nil
}

type providerHTTPError struct{ StatusCode int }

func (e *providerHTTPError) Error() string {
	return fmt.Sprintf("WeChat Pay HTTP status %d", e.StatusCode)
}

func (p *Provider) verifyResponse(headers http.Header, body []byte) error {
	timestamp := headers.Get("Wechatpay-Timestamp")
	nonce := headers.Get("Wechatpay-Nonce")
	if headers.Get("Wechatpay-Serial") != p.config.PlatformSerial || timestamp == "" || nonce == "" {
		return errors.New("untrusted WeChat Pay response serial")
	}
	if !freshTimestamp(timestamp, time.Now()) {
		return errors.New("stale WeChat Pay response signature")
	}
	return verifyRSA(p.config.PlatformPublicKey, []byte(timestamp+"\n"+nonce+"\n"+string(body)+"\n"), headers.Get("Wechatpay-Signature"))
}

func freshTimestamp(value string, now time.Time) bool {
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds <= 0 {
		return false
	}
	delta := now.Sub(time.Unix(seconds, 0))
	return delta >= -5*time.Minute && delta <= 5*time.Minute
}

func signRSA(key *rsa.PrivateKey, message []byte) (string, error) {
	digest := sha256.Sum256(message)
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	return base64.StdEncoding.EncodeToString(sig), err
}

func verifyRSA(key *rsa.PublicKey, message []byte, encoded string) error {
	signature, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(message)
	return rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature)
}

func decryptResource(key []byte, nonce, associatedData, ciphertext string) ([]byte, error) {
	sealed, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len([]byte(nonce)) != gcm.NonceSize() {
		return nil, errors.New("invalid WeChat Pay resource nonce length")
	}
	return gcm.Open(nil, []byte(nonce), sealed, []byte(associatedData))
}

func randomNonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func validOrderKey(s string) bool {
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_-|*", r)) {
			return false
		}
	}
	return true
}
