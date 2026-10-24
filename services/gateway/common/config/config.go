package config

import (
	"github.com/songquanpeng/one-api/common/env"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

var SystemName = "Miraphant"
var ServerAddress = "http://localhost:3000"
var Footer = ""
var Logo = "/miraphant.svg"
var TopUpLink = ""
var ChatLink = ""
var QuotaPerUnit = 500 * 1000.0 // $0.002 / 1K tokens
var DisplayInCurrencyEnabled = true
var DisplayTokenStatEnabled = true

// Any options with "Secret", "Token" in its key won't be return by GetOptions

var SessionSecret = uuid.New().String()

var OptionMap map[string]string
var OptionMapRWMutex sync.RWMutex

var ItemsPerPage = 10
var MaxRecentItems = 100

var PasswordLoginEnabled = true
var PasswordRegisterEnabled = true
var EmailVerificationEnabled = false
var GitHubOAuthEnabled = false
var OidcEnabled = false
var WeChatAuthEnabled = false
var TurnstileCheckEnabled = false
var RegisterEnabled = true

var EmailDomainRestrictionEnabled = false
var EmailDomainWhitelist = []string{
	"gmail.com",
	"163.com",
	"126.com",
	"qq.com",
	"outlook.com",
	"hotmail.com",
	"icloud.com",
	"yahoo.com",
	"foxmail.com",
}

var DebugEnabled = strings.ToLower(os.Getenv("DEBUG")) == "true"
var DebugSQLEnabled = strings.ToLower(os.Getenv("DEBUG_SQL")) == "true"
var MemoryCacheEnabled = strings.ToLower(os.Getenv("MEMORY_CACHE_ENABLED")) == "true"

var LogConsumeEnabled = true

var SMTPServer = ""
var SMTPPort = 587
var SMTPAccount = ""
var SMTPFrom = ""
var SMTPToken = ""

var GitHubClientId = ""
var GitHubClientSecret = ""

var LarkClientId = ""
var LarkClientSecret = ""

var OidcClientId = ""
var OidcClientSecret = ""
var OidcWellKnown = ""
var OidcAuthorizationEndpoint = ""
var OidcTokenEndpoint = ""
var OidcUserinfoEndpoint = ""

var WeChatServerAddress = ""
var WeChatServerToken = ""
var WeChatAccountQRCodeImageURL = ""

var MessagePusherAddress = ""
var MessagePusherToken = ""

var TurnstileSiteKey = ""
var TurnstileSecretKey = ""

var QuotaForNewUser int64 = 0
var QuotaForInviter int64 = 0
var QuotaForInvitee int64 = 0
var ChannelDisableThreshold = 5.0
var AutomaticDisableChannelEnabled = false
var AutomaticEnableChannelEnabled = false
var QuotaRemindThreshold int64 = 1000
var PreConsumedQuota int64 = 500
var ApproximateTokenEnabled = false
var RetryTimes = 0

var RootUserEmail = ""

var IsMasterNode = os.Getenv("NODE_TYPE") != "slave"

var requestInterval, _ = strconv.Atoi(os.Getenv("POLLING_INTERVAL"))
var RequestInterval = time.Duration(requestInterval) * time.Second

var SyncFrequency = env.Int("SYNC_FREQUENCY", 10*60) // unit is second

var BatchUpdateEnabled = false
var BatchUpdateInterval = env.Int("BATCH_UPDATE_INTERVAL", 5)

var RelayTimeout = env.Int("RELAY_TIMEOUT", 0) // unit is second

var GeminiSafetySetting = env.String("GEMINI_SAFETY_SETTING", "BLOCK_NONE")

var Theme = "default"
var ValidThemes = map[string]bool{
	"default": true,
	"berry":   true,
	"air":     true,
}

// All duration's unit is seconds
// Shouldn't larger then RateLimitKeyExpirationDuration
var (
	GlobalApiRateLimitNum            = env.Int("GLOBAL_API_RATE_LIMIT", 240)
	GlobalApiRateLimitDuration int64 = 3 * 60

	GlobalWebRateLimitNum            = env.Int("GLOBAL_WEB_RATE_LIMIT", 120)
	GlobalWebRateLimitDuration int64 = 3 * 60

	UploadRateLimitNum            = 10
	UploadRateLimitDuration int64 = 60

	DownloadRateLimitNum            = 10
	DownloadRateLimitDuration int64 = 60

	CriticalRateLimitNum            = 20
	CriticalRateLimitDuration int64 = 20 * 60
)

var RateLimitKeyExpirationDuration = 20 * time.Minute

var EnableMetric = env.Bool("ENABLE_METRIC", false)
var MetricQueueSize = env.Int("METRIC_QUEUE_SIZE", 10)
var MetricSuccessRateThreshold = env.Float64("METRIC_SUCCESS_RATE_THRESHOLD", 0.8)
var MetricSuccessChanSize = env.Int("METRIC_SUCCESS_CHAN_SIZE", 1024)
var MetricFailChanSize = env.Int("METRIC_FAIL_CHAN_SIZE", 128)

var InitialRootToken = os.Getenv("INITIAL_ROOT_TOKEN")

var InitialRootAccessToken = os.Getenv("INITIAL_ROOT_ACCESS_TOKEN")

var GeminiVersion = env.String("GEMINI_VERSION", "v1")

var OnlyOneLogFile = env.Bool("ONLY_ONE_LOG_FILE", false)

var RelayProxy = env.String("RELAY_PROXY", "")
var UserContentRequestProxy = env.String("USER_CONTENT_REQUEST_PROXY", "")
var UserContentRequestTimeout = env.Int("USER_CONTENT_REQUEST_TIMEOUT", 30)

var EnforceIncludeUsage = env.Bool("ENFORCE_INCLUDE_USAGE", false)

// PointsBillingEnabled remains opt-in until the points ledger is connected to
// every quota mutation and relay settlement path.
var PointsBillingEnabled = env.Bool("POINTS_BILLING_ENABLED", false)
var PointsMaxInputBytes = env.Int("POINTS_MAX_INPUT_BYTES", 131072)
var PointsMaxOutputTokens = env.Int("POINTS_MAX_OUTPUT_TOKENS", 8192)
var PointsRelayTimeout = env.Int("POINTS_RELAY_TIMEOUT", 120)

// PointRefundOperationsEnabled gates issuing/using step-up tickets for refund
// actions. Capability grants remain available to the exact root account while
// this switch is off so permissions can be prepared ahead of launch.
var PointRefundOperationsEnabled = env.Bool("POINTS_REFUND_OPERATIONS_ENABLED", false)

// Payment credentials are loaded from server environment or server-owned files.
// These do not follow the new-order toggle: historical callbacks and recovery
// remain verifiable after new orders are disabled.
var PaymentNewOrdersEnabled = env.Bool("POINTS_NEW_ORDERS_ENABLED", false)
var WeChatPayEnabled = env.Bool("WECHAT_PAY_CONFIGURED", false)
var WeChatPayMerchantID = env.String("WECHAT_PAY_MERCHANT_ID", "")
var WeChatPayAppID = env.String("WECHAT_PAY_APP_ID", "")
var WeChatPayMerchantSerial = env.String("WECHAT_PAY_MERCHANT_SERIAL", "")
var WeChatPayPlatformSerial = env.String("WECHAT_PAY_PLATFORM_SERIAL", "")
var WeChatPayPrivateKeyFile = env.String("WECHAT_PAY_PRIVATE_KEY_FILE", "")
var WeChatPayPlatformKeyFile = env.String("WECHAT_PAY_PLATFORM_KEY_FILE", "")
var WeChatPayAPIv3Key = env.String("WECHAT_PAY_API_V3_KEY", "")
var WeChatPayNotifyURL = env.String("WECHAT_PAY_NOTIFY_URL", "")
var WeChatPayRefundNotifyURL = env.String("WECHAT_PAY_REFUND_NOTIFY_URL", "")

var AlipayConfigured = env.Bool("ALIPAY_CONFIGURED", false)
var AlipayAppID = env.String("ALIPAY_APP_ID", "")
var AlipaySellerID = env.String("ALIPAY_SELLER_ID", "")
var AlipayPrivateKeyFile = env.String("ALIPAY_PRIVATE_KEY_FILE", "")
var AlipayPublicKeyFile = env.String("ALIPAY_PUBLIC_KEY_FILE", "")
var AlipayNotifyURL = env.String("ALIPAY_NOTIFY_URL", "")
var AlipayReturnURL = env.String("ALIPAY_RETURN_URL", "")

// Reconciliation source encryption is independent from session and payment
// secrets. Import is disabled unless both values are explicitly configured.
var PointReconciliationSourceKeyID = env.String("POINTS_RECONCILIATION_SOURCE_KEY_ID", "")
var PointReconciliationSourceKeyBase64 = env.String("POINTS_RECONCILIATION_SOURCE_KEY_BASE64", "")
