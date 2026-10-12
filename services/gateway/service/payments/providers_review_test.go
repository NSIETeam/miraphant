package payments

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/songquanpeng/one-api/common/config"
)

func TestPaymentProviderConfigurationSurvivesSalesPause(t *testing.T) {
	oldRegistry := runtimeProviders
	oldSales, oldWX, oldAli := config.PaymentNewOrdersEnabled, config.WeChatPayEnabled, config.AlipayConfigured
	// Restore every setting, including credentials, without printing their values.
	settings := map[*string]string{}
	for _, ptr := range []*string{
		&config.WeChatPayMerchantID, &config.WeChatPayAppID, &config.WeChatPayMerchantSerial,
		&config.WeChatPayPlatformSerial, &config.WeChatPayPrivateKeyFile, &config.WeChatPayPlatformKeyFile,
		&config.WeChatPayAPIv3Key, &config.WeChatPayNotifyURL, &config.AlipayAppID, &config.AlipaySellerID,
		&config.AlipayPrivateKeyFile, &config.AlipayPublicKeyFile, &config.AlipayNotifyURL, &config.AlipayReturnURL,
	} {
		settings[ptr] = *ptr
	}
	t.Cleanup(func() {
		runtimeProviders = oldRegistry
		config.PaymentNewOrdersEnabled, config.WeChatPayEnabled, config.AlipayConfigured = oldSales, oldWX, oldAli
		for ptr, value := range settings {
			*ptr = value
		}
	})
	config.PaymentNewOrdersEnabled, config.WeChatPayEnabled, config.AlipayConfigured = false, false, false
	if err := ConfigureProviders(); err != nil || len(runtimeProviders) != 0 {
		t.Fatal("disabled configuration must not register providers")
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	privatePath, publicPath := filepath.Join(dir, "synthetic-private.pem"), filepath.Join(dir, "synthetic-public.pem")
	for path, data := range map[string][]byte{
		privatePath: pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
		publicPath:  pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}),
	} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	config.WeChatPayEnabled, config.AlipayConfigured = true, true
	config.WeChatPayMerchantID, config.WeChatPayAppID = "fixture-wechat-merchant", "fixture-wechat-app"
	config.WeChatPayMerchantSerial, config.WeChatPayPlatformSerial = "fixture-merchant-serial", "fixture-platform-serial"
	config.WeChatPayPrivateKeyFile, config.WeChatPayPlatformKeyFile = privatePath, publicPath
	config.WeChatPayAPIv3Key = strings.Repeat("x", 32)
	config.WeChatPayNotifyURL = "https://fixture.invalid/api/payments/notify/wechat"
	config.AlipayAppID, config.AlipaySellerID = "fixture-alipay-app", "fixture-alipay-seller"
	config.AlipayPrivateKeyFile, config.AlipayPublicKeyFile = privatePath, publicPath
	config.AlipayNotifyURL, config.AlipayReturnURL = "https://fixture.invalid/api/payments/notify/alipay", ""
	if err := ConfigureProviders(); err != nil {
		t.Fatal("valid synthetic configuration was rejected")
	}
	for _, name := range []string{"wechat", "alipay"} {
		p, ok := ProviderFor(name)
		if !ok || p.Provider == nil || p.Identity.Provider != name || p.Identity.MerchantID == "" || p.Identity.AppID == "" {
			t.Fatalf("paused sales lost configured %s provider", name)
		}
	}
	// Neither configuration nor adapter construction performs a payment request.
	marker := "synthetic-invalid-key-content-do-not-echo"
	if err := os.WriteFile(privatePath, []byte(marker), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureProviders(); err == nil || strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), privatePath) {
		t.Fatal("invalid key must fail with a sanitized configuration error")
	}
}
