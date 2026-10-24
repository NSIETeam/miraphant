package payments

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/songquanpeng/one-api/common/config"
)

func TestReconciliationSourceKeyRequiresExplicitIDAndExactly32DecodedBytes(t *testing.T) {
	oldID, oldKey := config.PointReconciliationSourceKeyID, config.PointReconciliationSourceKeyBase64
	t.Cleanup(func() {
		config.PointReconciliationSourceKeyID, config.PointReconciliationSourceKeyBase64 = oldID, oldKey
	})
	const encodedSecretMarker = "c2VjcmV0LWtleS1tYXJrZXI="
	for _, fixture := range []struct{ id, key string }{
		{"", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))},
		{"key-v1", "%%%not-base64%%%"},
		{"key-v1", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 31)))},
	} {
		config.PointReconciliationSourceKeyID, config.PointReconciliationSourceKeyBase64 = fixture.id, fixture.key
		if _, err := ReconciliationSourceKey(); err == nil || strings.Contains(err.Error(), fixture.key) {
			t.Fatalf("invalid encryption config returned source material or succeeded: %v", err)
		}
	}
	config.PointReconciliationSourceKeyID, config.PointReconciliationSourceKeyBase64 = "stable-key-id", encodedSecretMarker
	if _, err := ReconciliationSourceKey(); err == nil || strings.Contains(err.Error(), encodedSecretMarker) {
		t.Fatalf("wrong-sized secret config returned its encoded value or succeeded: %v", err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	config.PointReconciliationSourceKeyID, config.PointReconciliationSourceKeyBase64 = "stable-key-id", base64.StdEncoding.EncodeToString(key)
	parsed, err := ReconciliationSourceKey()
	if err != nil || parsed.KeyID != "stable-key-id" || string(parsed.Key) != string(key) {
		t.Fatalf("valid source encryption config did not load: id=%q err=%v", parsed.KeyID, err)
	}
}
