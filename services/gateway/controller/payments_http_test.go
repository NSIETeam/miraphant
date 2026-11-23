package controller_test

import (
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/middleware"
	dbmodel "github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/payment"
	"github.com/songquanpeng/one-api/payment/wechat"
	"github.com/songquanpeng/one-api/relay/channeltype"
	"github.com/songquanpeng/one-api/router"
	"github.com/songquanpeng/one-api/service/payments"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type routePaymentProvider struct {
	creates       atomic.Int32
	query         payment.Trade
	queryErr      error
	createStarted chan struct{}
	createGate    chan struct{}
	startOnce     sync.Once
}

func (p *routePaymentProvider) Create(context.Context, payment.Order) (payment.Checkout, error) {
	p.creates.Add(1)
	if p.createStarted != nil {
		p.startOnce.Do(func() { close(p.createStarted) })
		<-p.createGate
	}
	return payment.Checkout{Kind: "qr", CodeURL: "weixin://fixture"}, nil
}
func (p *routePaymentProvider) VerifyNotification(http.Header, []byte) (payment.VerifiedNotification, error) {
	return payment.VerifiedNotification{}, payment.ErrInvalidNotice
}
func (p *routePaymentProvider) Query(context.Context, string) (payment.Trade, error) {
	return p.query, p.queryErr
}
func (p *routePaymentProvider) Close(context.Context, string) error { return nil }

type paymentHTTPTestEnv struct {
	Router  *gin.Engine
	DB      *gorm.DB
	Login   func(int) *http.Cookie
	Request func(string, string, *http.Cookie, string, int, map[string]string) *httptest.ResponseRecorder
}

func paymentHTTPFixture(t *testing.T, pointsEnabled bool) paymentHTTPTestEnv {
	t.Helper()
	oldDB, oldSQLite, oldRedis, oldBilling, oldNewOrders, oldAddress, oldSecret, oldMode := dbmodel.DB, common.UsingSQLite, common.RedisEnabled, config.PointsBillingEnabled, config.PaymentNewOrdersEnabled, config.ServerAddress, config.SessionSecret, gin.Mode()
	config.PointsBillingEnabled = pointsEnabled
	config.PaymentNewOrdersEnabled = true
	common.UsingSQLite = true
	common.RedisEnabled = false
	config.ServerAddress = "http://payments.test"
	config.SessionSecret = "payment-http-test-secret"
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/payments-http.db?_busy_timeout=10000&_journal_mode=WAL"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(8)
	dbmodel.DB = db
	if err := db.AutoMigrate(&dbmodel.User{}, &dbmodel.Token{}, &dbmodel.Channel{}, &dbmodel.Ability{}); err != nil {
		t.Fatal(err)
	}
	if err := dbmodel.MigratePointsSchema(); err != nil {
		t.Fatal(err)
	}
	users := []dbmodel.User{{Id: 41, Username: "pay-customer", Password: "fixture-password", AccessToken: "customer-http-access", AffCode: "pay-customer-code", Role: dbmodel.RoleCommonUser, Status: dbmodel.UserStatusEnabled, Group: "default"}, {Id: 42, Username: "pay-admin", Password: "fixture-password", AccessToken: "admin-http-access", AffCode: "pay-admin-code", Role: dbmodel.RoleAdminUser, Status: dbmodel.UserStatusEnabled, Group: "default"}}
	if err := db.Create(&users).Error; err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(sessions.Sessions("session", cookie.NewStore([]byte(config.SessionSecret))))
	r.GET("/test-session/:id", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		var u dbmodel.User
		if err := db.Select("id", "username", "role", "status").First(&u, "id = ?", id).Error; err != nil {
			t.Fatal(err)
		}
		s := sessions.Default(c)
		s.Set("id", u.Id)
		s.Set("username", u.Username)
		s.Set("role", u.Role)
		s.Set("status", u.Status)
		if err := s.Save(); err != nil {
			t.Fatal(err)
		}
		c.Status(http.StatusNoContent)
	})
	router.SetApiRouter(r)
	login := func(id int) *http.Cookie {
		res := httptest.NewRecorder()
		r.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/test-session/"+strconv.Itoa(id), nil))
		if res.Code != http.StatusNoContent || len(res.Result().Cookies()) == 0 {
			t.Fatalf("session status %d", res.Code)
		}
		return res.Result().Cookies()[0]
	}
	request := func(method, path string, cookie *http.Cookie, body string, csrfID int, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if method != http.MethodGet {
			req.Header.Set("Origin", "http://payments.test")
			if csrfID > 0 {
				req.Header.Set("X-CSRF-Token", middleware.PointsCSRFToken(csrfID))
			}
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res := httptest.NewRecorder()
		r.ServeHTTP(res, req)
		return res
	}
	t.Cleanup(func() {
		dbmodel.DB = oldDB
		common.UsingSQLite = oldSQLite
		common.RedisEnabled = oldRedis
		config.PointsBillingEnabled = oldBilling
		config.PaymentNewOrdersEnabled = oldNewOrders
		config.ServerAddress = oldAddress
		config.SessionSecret = oldSecret
		gin.SetMode(oldMode)
		_ = sqlDB.Close()
	})
	return paymentHTTPTestEnv{Router: r, DB: db, Login: login, Request: request}
}

func TestPaymentOrdersRouterCSRFIdempotencyAndQueryOrderBinding(t *testing.T) {
	env := paymentHTTPFixture(t, true)
	provider := &routePaymentProvider{}
	identity := payments.MerchantIdentity{Provider: "wechat", MerchantID: "merchant-http", AppID: "app-http"}
	restore := payments.ReplaceProvidersForTest(map[string]payments.RuntimeProvider{"wechat": {Identity: identity, Provider: provider}})
	defer restore()
	adminCookie := env.Login(42)
	if err := dbmodel.PublishPointPriceVersion(42, &dbmodel.PointPriceVersion{ModelID: "model-http", Version: "price-http-v1", Source: "test published price", InputMicroPer1K: 1000, CachedInputMicroPer1K: 500, OutputMicroPer1K: 2000}); err != nil {
		t.Fatal(err)
	}
	channel := dbmodel.Channel{Id: 901, Type: channeltype.OpenAI, Status: dbmodel.ChannelStatusEnabled, Name: "text upstream", Models: "model-http", Group: "default"}
	if err := env.DB.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if err := env.DB.Create(&dbmodel.Ability{Group: "default", Model: "model-http", ChannelId: 901, Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	packageResp := env.Request(http.MethodPost, "/api/admin/points/payments/packages", adminCookie, `{"package_id":"starter-http","version":"v1","name":"测试套餐","amount_fen":500,"activate":true,"business_key":"publish-http-v1"}`, 42, nil)
	if packageResp.Code != http.StatusCreated {
		t.Fatalf("publish package status=%d body=%s", packageResp.Code, packageResp.Body.String())
	}
	var pkg dbmodel.PointPackage
	if err := env.DB.First(&pkg, "package_id = ? AND version = ?", "starter-http", "v1").Error; err != nil || pkg.CreatedBy != 42 {
		t.Fatalf("package creator not persisted: %+v err=%v", pkg, err)
	}
	customerCookie := env.Login(41)
	options := env.Request(http.MethodGet, "/api/payments/options", customerCookie, "", 0, nil)
	if options.Code != http.StatusOK || !strings.Contains(options.Body.String(), `"purchase_available":true`) || !strings.Contains(options.Body.String(), `"mobile_payment_available":false`) {
		t.Fatalf("customer purchase options status=%d body=%s", options.Code, options.Body.String())
	}
	adminPackages := env.Request(http.MethodGet, "/api/admin/points/payments/packages", adminCookie, "", 0, nil)
	if adminPackages.Code != http.StatusOK || !strings.Contains(adminPackages.Body.String(), `"active":true`) || !strings.Contains(adminPackages.Body.String(), `"business_key":"package_publish:publish-http-v1"`) {
		t.Fatalf("admin package history status=%d body=%s", adminPackages.Code, adminPackages.Body.String())
	}
	adminStatus := env.Request(http.MethodGet, "/api/admin/points/payments/status", adminCookie, "", 0, nil)
	if adminStatus.Code != http.StatusOK || strings.Contains(adminStatus.Body.String(), identity.MerchantID) || strings.Contains(adminStatus.Body.String(), identity.AppID) {
		t.Fatalf("admin payment status exposed identity or failed: %d %s", adminStatus.Code, adminStatus.Body.String())
	}
	csrf := env.Request(http.MethodGet, "/api/payments/csrf", customerCookie, "", 0, nil)
	if csrf.Code != http.StatusOK || !strings.Contains(csrf.Body.String(), "csrf_token") {
		t.Fatalf("payments csrf status=%d body=%s", csrf.Code, csrf.Body.String())
	}
	withoutCSRF := env.Request(http.MethodPost, "/api/payments/orders", customerCookie, `{"package_id":"starter-http","channel":"wechat"}`, 0, map[string]string{"Idempotency-Key": "checkout-http-1"})
	if withoutCSRF.Code != http.StatusForbidden {
		t.Fatalf("missing csrf status=%d", withoutCSRF.Code)
	}
	first := env.Request(http.MethodPost, "/api/payments/orders", customerCookie, `{"package_id":"starter-http","channel":"wechat"}`, 41, map[string]string{"Idempotency-Key": "checkout-http-1"})
	if first.Code != http.StatusCreated {
		t.Fatalf("first order status=%d body=%s", first.Code, first.Body.String())
	}
	// A committed order remains recoverable by its idempotency key even if
	// later catalog or model changes disable new orders for this account.
	if err := env.DB.Where("package_id = ?", "starter-http").Delete(&dbmodel.PointActivePackage{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := env.DB.Model(&dbmodel.Ability{}).Where("channel_id = ?", 901).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	var firstBody struct {
		Order    pointOrderJSON   `json:"order"`
		Checkout payment.Checkout `json:"checkout"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil {
		t.Fatal(err)
	}
	second := env.Request(http.MethodPost, "/api/payments/orders", customerCookie, `{"package_id":"starter-http","channel":"wechat"}`, 41, map[string]string{"Idempotency-Key": "checkout-http-1"})
	if second.Code != http.StatusOK || provider.creates.Load() != 1 {
		t.Fatalf("replay status=%d create calls=%d body=%s", second.Code, provider.creates.Load(), second.Body.String())
	}
	var order dbmodel.PointPurchaseOrder
	if err := env.DB.First(&order, "order_key = ?", firstBody.Order.OrderKey).Error; err != nil {
		t.Fatal(err)
	}
	if order.ProviderMerchantID != identity.MerchantID || order.ProviderAppID != identity.AppID || order.ProviderCreateState != "ready" {
		t.Fatalf("order snapshot not saved: %+v", order)
	}
	checkout := env.Request(http.MethodGet, "/api/payments/orders/"+firstBody.Order.OrderKey+"/checkout", customerCookie, "", 0, nil)
	if checkout.Code != http.StatusOK || !strings.Contains(checkout.Body.String(), "weixin://fixture") || provider.creates.Load() != 1 {
		t.Fatalf("saved checkout read status=%d create calls=%d body=%s", checkout.Code, provider.creates.Load(), checkout.Body.String())
	}
	if err := env.DB.Model(&dbmodel.PointPurchaseOrder{}).Where("order_key = ?", firstBody.Order.OrderKey).Update("expires_at", time.Now().Add(-time.Minute).Unix()).Error; err != nil {
		t.Fatal(err)
	}
	expiredCheckout := env.Request(http.MethodGet, "/api/payments/orders/"+firstBody.Order.OrderKey+"/checkout", customerCookie, "", 0, nil)
	if expiredCheckout.Code != http.StatusConflict || provider.creates.Load() != 1 {
		t.Fatalf("expired checkout status=%d create calls=%d body=%s", expiredCheckout.Code, provider.creates.Load(), expiredCheckout.Body.String())
	}
	if got := env.Request(http.MethodGet, "/api/payments/orders/"+firstBody.Order.OrderKey, env.Login(42), "", 0, nil).Code; got != http.StatusNotFound {
		t.Fatalf("other customer order status=%d", got)
	}
	if err := env.DB.Create(&dbmodel.PointPurchaseOrder{OrderKey: "query-http-order-001", UserID: 41, Channel: "wechat", ProviderMerchantID: identity.MerchantID, ProviderAppID: identity.AppID, Currency: "CNY", AmountFen: 500, PurchaseMicro: 500 * dbmodel.PointMicroPerPoint, State: "pending"}).Error; err != nil {
		t.Fatal(err)
	}
	provider.query = payment.Trade{Provider: "wechat", OrderKey: "some-other-order", TransactionID: "wrong-transaction", MerchantID: identity.MerchantID, AppID: identity.AppID, AmountFen: 500, Currency: "CNY", Status: "SUCCESS"}
	queryResult := env.Request(http.MethodPost, "/api/payments/orders/query-http-order-001/query", customerCookie, "", 41, nil)
	if queryResult.Code != http.StatusConflict {
		t.Fatalf("mismatched query status=%d body=%s", queryResult.Code, queryResult.Body.String())
	}
	var events int64
	env.DB.Model(&dbmodel.PaymentEvent{}).Count(&events)
	if events != 0 {
		t.Fatalf("mismatched query stored %d events", events)
	}
}

func TestPaymentOrderCursorAndCheckoutOwnership(t *testing.T) {
	env := paymentHTTPFixture(t, false)
	for _, key := range []string{"cursor-order-1", "cursor-order-2", "cursor-order-3"} {
		if err := env.DB.Create(&dbmodel.PointPurchaseOrder{OrderKey: key, UserID: 41, Channel: "wechat", Currency: "CNY", AmountFen: 100, PurchaseMicro: 100 * dbmodel.PointMicroPerPoint, State: "pending", ExpiresAt: ptrUnix(time.Now().Add(time.Hour).Unix())}).Error; err != nil {
			t.Fatal(err)
		}
	}
	cookie := env.Login(41)
	first := env.Request(http.MethodGet, "/api/payments/orders?limit=1", cookie, "", 0, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("first page status=%d body=%s", first.Code, first.Body.String())
	}
	var page struct {
		Orders     []pointOrderJSON `json:"orders"`
		NextCursor string           `json:"next_cursor"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Orders) != 1 || page.NextCursor == "" {
		t.Fatalf("first page missing cursor: %+v", page)
	}
	firstKey := page.Orders[0].OrderKey
	second := env.Request(http.MethodGet, "/api/payments/orders?limit=1&cursor="+page.NextCursor, cookie, "", 0, nil)
	if second.Code != http.StatusOK {
		t.Fatalf("second page status=%d body=%s", second.Code, second.Body.String())
	}
	if err := json.Unmarshal(second.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Orders) != 1 || page.Orders[0].OrderKey == "" || page.Orders[0].OrderKey == firstKey {
		t.Fatalf("second page invalid: %+v", page)
	}
	if got := env.Request(http.MethodGet, "/api/payments/orders/cursor-order-1/checkout", env.Login(42), "", 0, nil).Code; got != http.StatusNotFound {
		t.Fatalf("other customer checkout status=%d", got)
	}
	if got := env.Request(http.MethodGet, "/api/payments/orders?limit=1&cursor=not-a-cursor", cookie, "", 0, nil).Code; got != http.StatusBadRequest {
		t.Fatalf("invalid cursor status=%d", got)
	}
	for i := 0; i < 102; i++ {
		order := dbmodel.PointPurchaseOrder{OrderKey: fmt.Sprintf("cursor-large-%03d", i), UserID: 41, Channel: "alipay", Currency: "CNY", AmountFen: 100, PurchaseMicro: 100 * dbmodel.PointMicroPerPoint, State: "pending", ExpiresAt: ptrUnix(time.Now().Add(time.Hour).Unix())}
		if err := env.DB.Create(&order).Error; err != nil {
			t.Fatal(err)
		}
	}
	largeFirst := env.Request(http.MethodGet, "/api/payments/orders?limit=100", cookie, "", 0, nil)
	if largeFirst.Code != http.StatusOK || json.Unmarshal(largeFirst.Body.Bytes(), &page) != nil || len(page.Orders) != 100 || page.NextCursor == "" {
		t.Fatalf("large first page status=%d count=%d cursor=%q body=%s", largeFirst.Code, len(page.Orders), page.NextCursor, largeFirst.Body.String())
	}
	largeSecond := env.Request(http.MethodGet, "/api/payments/orders?limit=100&cursor="+page.NextCursor, cookie, "", 0, nil)
	if largeSecond.Code != http.StatusOK || json.Unmarshal(largeSecond.Body.Bytes(), &page) != nil || len(page.Orders) != 5 || page.NextCursor != "" {
		t.Fatalf("large final page status=%d count=%d cursor=%q body=%s", largeSecond.Code, len(page.Orders), page.NextCursor, largeSecond.Body.String())
	}
}

func ptrUnix(value int64) *int64 { return &value }

func seedPaymentSale(t *testing.T, env paymentHTTPTestEnv, identity payments.MerchantIdentity) {
	t.Helper()
	if err := dbmodel.PublishPointPriceVersion(42, &dbmodel.PointPriceVersion{ModelID: "model-http", Version: "price-http-v1", Source: "published integration price", InputMicroPer1K: 1000, CachedInputMicroPer1K: 500, OutputMicroPer1K: 2000}); err != nil {
		t.Fatal(err)
	}
	channel := dbmodel.Channel{Id: 902, Type: channeltype.OpenAI, Status: dbmodel.ChannelStatusEnabled, Name: "text upstream", Models: "model-http", Group: "default"}
	if err := env.DB.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if err := env.DB.Create(&dbmodel.Ability{Group: "default", Model: "model-http", ChannelId: channel.Id, Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	res := env.Request(http.MethodPost, "/api/admin/points/payments/packages", env.Login(42), `{"package_id":"starter-http","version":"v1","name":"测试套餐","amount_fen":500,"activate":true,"business_key":"publish-http-v1"}`, 42, nil)
	if res.Code != http.StatusCreated {
		t.Fatalf("publish sale package status=%d body=%s", res.Code, res.Body.String())
	}
	_ = identity
}

func TestPaymentUnknownCreateIsNotRepeatedAndCloseRecoversLocalState(t *testing.T) {
	for _, scenario := range []string{"concurrent-create", "unknown-query", "closed-query", "expired-not-found"} {
		t.Run(scenario, func(t *testing.T) {
			env := paymentHTTPFixture(t, true)
			provider := &routePaymentProvider{}
			identity := payments.MerchantIdentity{Provider: "wechat", MerchantID: "merchant-http", AppID: "app-http"}
			restore := payments.ReplaceProvidersForTest(map[string]payments.RuntimeProvider{"wechat": {Identity: identity, Provider: provider}})
			defer restore()
			customer := env.Login(41)
			if scenario == "concurrent-create" || scenario == "unknown-query" {
				seedPaymentSale(t, env, identity)
			}
			switch scenario {
			case "concurrent-create":
				provider.createStarted = make(chan struct{})
				provider.createGate = make(chan struct{})
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					done <- env.Request(http.MethodPost, "/api/payments/orders", customer, `{"package_id":"starter-http","channel":"wechat"}`, 41, map[string]string{"Idempotency-Key": "concurrent-checkout"})
				}()
				select {
				case <-provider.createStarted:
				case <-time.After(2 * time.Second):
					t.Fatal("first provider create did not start")
				}
				second := env.Request(http.MethodPost, "/api/payments/orders", customer, `{"package_id":"starter-http","channel":"wechat"}`, 41, map[string]string{"Idempotency-Key": "concurrent-checkout"})
				if second.Code != http.StatusAccepted || provider.creates.Load() != 1 {
					t.Fatalf("concurrent retry duplicated provider create: status=%d calls=%d body=%s", second.Code, provider.creates.Load(), second.Body.String())
				}
				close(provider.createGate)
				select {
				case first := <-done:
					if first.Code != http.StatusCreated {
						t.Fatalf("first checkout status=%d body=%s", first.Code, first.Body.String())
					}
				case <-time.After(2 * time.Second):
					t.Fatal("first provider create did not finish")
				}
			case "unknown-query":
				order, err := dbmodel.CreatePointPurchaseOrder(dbmodel.CreatePointPurchaseOrderRequest{UserID: 41, PackageID: "starter-http", Channel: "wechat", MerchantID: identity.MerchantID, AppID: identity.AppID, IdempotencyKey: "old-unknown-create"})
				if err != nil {
					t.Fatal(err)
				}
				stale := time.Now().UTC().Add(-time.Minute).Unix()
				if err := env.DB.Model(&dbmodel.PointPurchaseOrder{}).Where("order_key = ?", order.OrderKey).Updates(map[string]any{"state": "pending", "provider_create_state": "started", "provider_create_started_at": stale}).Error; err != nil {
					t.Fatal(err)
				}
				provider.queryErr = payment.ErrUnknownStatus
				res := env.Request(http.MethodPost, "/api/payments/orders", customer, `{"package_id":"starter-http","channel":"wechat"}`, 41, map[string]string{"Idempotency-Key": "old-unknown-create"})
				if res.Code != http.StatusAccepted || provider.creates.Load() != 0 {
					t.Fatalf("unknown create status retried externally: status=%d calls=%d body=%s", res.Code, provider.creates.Load(), res.Body.String())
				}
			case "closed-query":
				order := dbmodel.PointPurchaseOrder{OrderKey: "closed-query-order", UserID: 41, Channel: "wechat", ProviderMerchantID: identity.MerchantID, ProviderAppID: identity.AppID, Currency: "CNY", AmountFen: 500, PurchaseMicro: 500 * dbmodel.PointMicroPerPoint, State: "pending"}
				if err := env.DB.Create(&order).Error; err != nil {
					t.Fatal(err)
				}
				provider.query = payment.Trade{Provider: "wechat", OrderKey: order.OrderKey, MerchantID: identity.MerchantID, AppID: identity.AppID, Status: "CLOSED"}
				res := env.Request(http.MethodPost, "/api/payments/orders/"+order.OrderKey+"/close", customer, "", 41, nil)
				var saved dbmodel.PointPurchaseOrder
				_ = env.DB.First(&saved, "order_key = ?", order.OrderKey).Error
				if res.Code != http.StatusOK || saved.State != "closed" {
					t.Fatalf("provider-closed order did not recover locally: status=%d state=%s body=%s", res.Code, saved.State, res.Body.String())
				}
			case "expired-not-found":
				expired := time.Now().UTC().Add(-time.Minute).Unix()
				order := dbmodel.PointPurchaseOrder{OrderKey: "expired-not-found-order", UserID: 41, Channel: "wechat", ProviderMerchantID: identity.MerchantID, ProviderAppID: identity.AppID, Currency: "CNY", AmountFen: 500, PurchaseMicro: 500 * dbmodel.PointMicroPerPoint, ExpiresAt: &expired, ProviderCreateState: "ready", State: "pending"}
				if err := env.DB.Create(&order).Error; err != nil {
					t.Fatal(err)
				}
				provider.queryErr = payment.ErrNotPaid
				res := env.Request(http.MethodPost, "/api/payments/orders/"+order.OrderKey+"/close", customer, "", 41, nil)
				var saved dbmodel.PointPurchaseOrder
				_ = env.DB.First(&saved, "order_key = ?", order.OrderKey).Error
				if res.Code != http.StatusOK || saved.State != "closed" {
					t.Fatalf("expired absent order did not converge closed: status=%d state=%s body=%s", res.Code, saved.State, res.Body.String())
				}
			}
		})
	}
}

type pointOrderJSON struct {
	OrderKey string `json:"order_key"`
	State    string `json:"state"`
}

func TestWeChatSignedNotificationRouterCreditsEvenWhenBillingPaused(t *testing.T) {
	env := paymentHTTPFixture(t, false)
	merchantKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	platformKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	apiKey := []byte("0123456789abcdef0123456789abcdef")
	provider, err := wechat.New(wechat.Config{Enabled: true, MerchantID: "merchant-http", AppID: "app-http", MerchantSerial: "merchant-serial", PlatformSerial: "platform-serial", APIPrivateKey: merchantKey, PlatformPublicKey: &platformKey.PublicKey, APIv3Key: apiKey, NotifyURL: "https://payments.test/api/payments/notify/wechat"})
	if err != nil {
		t.Fatal(err)
	}
	identity := payments.MerchantIdentity{Provider: "wechat", MerchantID: "merchant-http", AppID: "app-http"}
	restore := payments.ReplaceProvidersForTest(map[string]payments.RuntimeProvider{"wechat": {Identity: identity, Provider: provider}})
	defer restore()
	order := dbmodel.PointPurchaseOrder{OrderKey: "wechat-http-order-001", UserID: 41, Channel: "wechat", ProviderMerchantID: identity.MerchantID, ProviderAppID: identity.AppID, Currency: "CNY", AmountFen: 500, PurchaseMicro: 500 * dbmodel.PointMicroPerPoint, State: "closed"}
	if err := env.DB.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	body := signedWechatNotice(t, platformKey, apiKey, identity, order.OrderKey)
	for attempt := 0; attempt < 2; attempt++ {
		req := httptest.NewRequest(http.MethodPost, "/api/payments/notify/wechat", strings.NewReader(string(body)))
		req.Header = signedWechatHeaders(t, platformKey, body)
		res := httptest.NewRecorder()
		env.Router.ServeHTTP(res, req)
		if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "SUCCESS") {
			t.Fatalf("callback attempt %d status=%d body=%s", attempt, res.Code, res.Body.String())
		}
	}
	var account dbmodel.PointAccount
	if err := env.DB.First(&account, "user_id = ?", 41).Error; err != nil {
		t.Fatal(err)
	}
	if account.AvailableMicro != 500*dbmodel.PointMicroPerPoint {
		t.Fatalf("wrong credited balance: %+v", account)
	}
	var ledgerCount int64
	env.DB.Model(&dbmodel.PointLedger{}).Where("business_key = ?", "credit:"+order.OrderKey).Count(&ledgerCount)
	if ledgerCount != 1 {
		t.Fatalf("duplicate callback produced %d credit ledgers", ledgerCount)
	}
	var saved dbmodel.PointPurchaseOrder
	if err := env.DB.First(&saved, "order_key = ?", order.OrderKey).Error; err != nil {
		t.Fatal(err)
	}
	if saved.State != "credited" {
		t.Fatalf("closed late payment was not credited: state=%s", saved.State)
	}
}

func TestPaymentRecoveryRouterWorksWithBillingPausedAndRequiresCSRF(t *testing.T) {
	env := paymentHTTPFixture(t, false)
	identity := payments.MerchantIdentity{Provider: "wechat", MerchantID: "merchant-http", AppID: "app-http"}
	provider := &routePaymentProvider{}
	restore := payments.ReplaceProvidersForTest(map[string]payments.RuntimeProvider{"wechat": {Identity: identity, Provider: provider}})
	defer restore()
	order := dbmodel.PointPurchaseOrder{OrderKey: "recover-http-order", UserID: 41, Channel: "wechat", ProviderMerchantID: identity.MerchantID, ProviderAppID: identity.AppID, Currency: "CNY", AmountFen: 500, PurchaseMicro: 500 * dbmodel.PointMicroPerPoint, State: "pending"}
	if err := env.DB.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	trade := payment.VerifiedNotification{Provider: "wechat", ProviderEventID: "recover-http-event", OrderKey: order.OrderKey, TransactionID: "recover-http-transaction", MerchantID: identity.MerchantID, AppID: identity.AppID, AmountFen: 500, Currency: "CNY", Status: "SUCCESS", ProviderOccurredAt: time.Now().UTC()}
	if _, err := payments.PersistVerifiedNotification(trade, []byte("verified durable notification")); err != nil {
		t.Fatal(err)
	}
	admin := env.Login(42)
	if res := env.Request(http.MethodPost, "/api/admin/points/payments/recover", admin, "{}", 0, nil); res.Code != http.StatusForbidden {
		t.Fatalf("recovery without csrf status=%d body=%s", res.Code, res.Body.String())
	}
	res := env.Request(http.MethodPost, "/api/admin/points/payments/recover?limit=10", admin, "{}", 42, nil)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"processed":1`) {
		t.Fatalf("recovery status=%d body=%s", res.Code, res.Body.String())
	}
	var saved dbmodel.PointPurchaseOrder
	if err := env.DB.First(&saved, "order_key = ?", order.OrderKey).Error; err != nil {
		t.Fatal(err)
	}
	if saved.State != "credited" {
		t.Fatalf("recovery did not credit durable event: %s", saved.State)
	}
}

func signedWechatNotice(t *testing.T, platform *rsa.PrivateKey, key []byte, identity payments.MerchantIdentity, orderKey string) []byte {
	t.Helper()
	nonce := []byte("123456789012")
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := json.Marshal(map[string]any{"appid": identity.AppID, "mchid": identity.MerchantID, "out_trade_no": orderKey, "transaction_id": "wx-http-transaction", "trade_state": "SUCCESS", "success_time": time.Now().UTC().Format(time.RFC3339), "amount": map[string]any{"total": 500, "currency": "CNY"}})
	ciphertext := gcm.Seal(nil, nonce, plain, []byte("associated"))
	envelope, _ := json.Marshal(map[string]any{"id": "notify-http-1", "create_time": time.Now().UTC().Format(time.RFC3339), "event_type": "TRANSACTION.SUCCESS", "resource": map[string]any{"algorithm": "AEAD_AES_256_GCM", "ciphertext": base64.StdEncoding.EncodeToString(ciphertext), "nonce": string(nonce), "associated_data": "associated"}})
	return envelope
}

func signedWechatHeaders(t *testing.T, key *rsa.PrivateKey, body []byte) http.Header {
	t.Helper()
	timestamp := fmt.Sprint(time.Now().Unix())
	nonce := "route-signature-nonce"
	message := []byte(timestamp + "\n" + nonce + "\n" + string(body) + "\n")
	digest := sha256.Sum256(message)
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	h := make(http.Header)
	h.Set("Wechatpay-Serial", "platform-serial")
	h.Set("Wechatpay-Timestamp", timestamp)
	h.Set("Wechatpay-Nonce", nonce)
	h.Set("Wechatpay-Signature", base64.StdEncoding.EncodeToString(sig))
	return h
}
