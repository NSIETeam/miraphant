package payments

import (
	"crypto"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"strings"

	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/payment"
	"github.com/songquanpeng/one-api/payment/alipay"
	"github.com/songquanpeng/one-api/payment/wechat"
)

type RuntimeProvider struct {
	Identity MerchantIdentity
	Provider payment.Provider
}

var runtimeProviders = map[string]RuntimeProvider{}

// ConfigureProviders reads server-owned configuration. Credentials are never
// returned from the API. The registry is initialized independently of the
// new-order switch so existing callbacks remain verifiable while sales pause.
func ConfigureProviders() error {
	providers := map[string]RuntimeProvider{}
	if config.WeChatPayEnabled {
		privateKey, err := readRSAPrivateKey(config.WeChatPayPrivateKeyFile)
		if err != nil {
			return errors.New("WeChat Pay merchant key configuration is invalid")
		}
		platformKey, err := readRSAPublicKey(config.WeChatPayPlatformKeyFile)
		if err != nil {
			return errors.New("WeChat Pay platform key configuration is invalid")
		}
		p, err := wechat.New(wechat.Config{Enabled: true, MerchantID: config.WeChatPayMerchantID, AppID: config.WeChatPayAppID, MerchantSerial: config.WeChatPayMerchantSerial, PlatformSerial: config.WeChatPayPlatformSerial, APIPrivateKey: privateKey, PlatformPublicKey: platformKey, APIv3Key: []byte(config.WeChatPayAPIv3Key), NotifyURL: config.WeChatPayNotifyURL})
		if err != nil {
			return errors.New("WeChat Pay configuration is incomplete or invalid")
		}
		providers["wechat"] = RuntimeProvider{Identity: MerchantIdentity{Provider: "wechat", MerchantID: config.WeChatPayMerchantID, AppID: config.WeChatPayAppID}, Provider: p}
	}
	if config.AlipayConfigured {
		privateKey, err := readRSAPrivateKey(config.AlipayPrivateKeyFile)
		if err != nil {
			return errors.New("Alipay merchant key configuration is invalid")
		}
		publicKey, err := readRSAPublicKey(config.AlipayPublicKeyFile)
		if err != nil {
			return errors.New("Alipay platform key configuration is invalid")
		}
		p, err := alipay.New(alipay.Config{Enabled: true, AppID: config.AlipayAppID, MerchantID: config.AlipaySellerID, MerchantPrivateKey: privateKey, AlipayPublicKey: publicKey, NotifyURL: config.AlipayNotifyURL, ReturnURL: config.AlipayReturnURL})
		if err != nil {
			return errors.New("Alipay configuration is incomplete or invalid")
		}
		providers["alipay"] = RuntimeProvider{Identity: MerchantIdentity{Provider: "alipay", MerchantID: config.AlipaySellerID, AppID: config.AlipayAppID}, Provider: p}
	}
	runtimeProviders = providers
	return nil
}

func ProviderFor(name string) (RuntimeProvider, bool) {
	provider, ok := runtimeProviders[name]
	return provider, ok
}

// ReplaceProvidersForTest is intentionally explicit and restores the previous
// registry when its returned cleanup function is called.
func ReplaceProvidersForTest(providers map[string]RuntimeProvider) func() {
	previous := runtimeProviders
	runtimeProviders = providers
	return func() { runtimeProviders = previous }
}

func readRSAPrivateKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(strings.TrimSpace(path))
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("PEM required")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("RSA key required")
	}
	return key, nil
}

func readRSAPublicKey(path string) (*rsa.PublicKey, error) {
	data, err := os.ReadFile(strings.TrimSpace(path))
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("PEM required")
	}
	if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
		key, ok := cert.PublicKey.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("RSA key required")
		}
		return key, nil
	}
	if key, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok || !crypto.SHA256.Available() {
		return nil, errors.New("RSA SHA-256 key required")
	}
	return key, nil
}
