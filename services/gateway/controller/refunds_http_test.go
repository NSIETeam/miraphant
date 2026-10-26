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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/middleware"
	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/payment"
	"github.com/songquanpeng/one-api/payment/wechat"
	"github.com/songquanpeng/one-api/router"
	"github.com/songquanpeng/one-api/service/payments"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestCustomerRefundRequestConcurrentSameIdempotencyKeyUsesSingleReservation(t *testing.T) {
	oldDB, oldSecret, oldAddress := model.DB, config.SessionSecret, config.ServerAddress
	oldBilling, oldRefunds, oldRedis, oldMode := config.PointsBillingEnabled, config.PointRefundOperationsEnabled, common.RedisEnabled, gin.Mode()
	t.Cleanup(func() {
		model.DB, config.SessionSecret, config.ServerAddress = oldDB, oldSecret, oldAddress
		config.PointsBillingEnabled, config.PointRefundOperationsEnabled = oldBilling, oldRefunds
		common.RedisEnabled = oldRedis
		gin.SetMode(oldMode)
	})
	config.SessionSecret = "refund-request-http-test-session-secret"
	config.ServerAddress = "http://refund-request.example.test"
	config.PointsBillingEnabled = true
	config.PointRefundOperationsEnabled = true
	common.RedisEnabled = false
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/refund-http.db?_txlock=immediate&_busy_timeout=5000"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	model.DB = db
	if err := db.AutoMigrate(&model.User{}, &model.RefundCapabilityGrant{}, &model.RefundAuthorizationAudit{}, &model.RefundStepUpTicket{}); err != nil {
		t.Fatal(err)
	}
	if err := model.MigratePointsSchema(); err != nil {
		t.Fatal(err)
	}
	userID := 86101
	if err := db.Create(&model.User{Id: userID, Username: "refund-customer-http", Role: model.RoleCommonUser, Status: model.UserStatusEnabled, AffCode: "refund-customer-http", AccessToken: "refund-customer-http-token"}).Error; err != nil {
		t.Fatal(err)
	}
	const orderKey = "refund-http-order-1"
	const balance = int64(1000) * model.PointMicroPerPoint
	if err := db.Create(&model.PointPurchaseOrder{OrderKey: orderKey, UserID: userID, Channel: "alipay", Currency: "CNY", AmountFen: 1000, PurchaseMicro: balance,
		ProviderTransactionID: "trade-http-1", ProviderMerchantID: "merchant-http", ProviderAppID: "app-http", State: "credited"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.PointAccount{UserID: userID, AvailableMicro: balance, UpdatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.PointLot{UserID: userID, BusinessKey: "purchase:" + orderKey, Kind: "purchase", SourceRef: orderKey, AmountFen: 1000, InitialMicro: balance, AvailableMicro: balance, CreatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}

	r := gin.New()
	r.Use(sessions.Sessions("session", cookie.NewStore([]byte(config.SessionSecret))))
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	r.Use(func(c *gin.Context) {
		if c.Request.Method == http.MethodPost && c.Request.URL.Path == "/api/payments/orders/"+orderKey+"/refund-requests" {
			arrived <- struct{}{}
			select {
			case <-release:
			case <-time.After(5 * time.Second):
				c.AbortWithStatus(http.StatusGatewayTimeout)
				return
			}
		}
		c.Next()
	})
	r.GET("/test-session/:id", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		s := sessions.Default(c)
		s.Set("id", id)
		if err := s.Save(); err != nil {
			t.Error(err)
		}
		c.Status(http.StatusNoContent)
	})
	router.SetApiRouter(r)
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()

	sessionResponse := httptest.NewRecorder()
	r.ServeHTTP(sessionResponse, httptest.NewRequest(http.MethodGet, "/test-session/"+strconv.Itoa(userID), nil))
	if sessionResponse.Code != http.StatusNoContent || len(sessionResponse.Result().Cookies()) == 0 {
		t.Fatalf("session setup failed: %d", sessionResponse.Code)
	}
	cookie := sessionResponse.Result().Cookies()[0]
	body := `{"amount_fen":100,"reason":"重复请求并发验证","idempotency_key":"same-refund-http-key-001"}`
	csrf := middleware.PointsCSRFToken(userID)
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			req := httptest.NewRequest(http.MethodPost, "/api/payments/orders/"+orderKey+"/refund-requests", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", config.ServerAddress)
			req.Header.Set("X-CSRF-Token", csrf)
			req.AddCookie(cookie)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			responses <- w
		}()
	}
	close(start)
	for i := 0; i < 2; i++ {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("concurrent requests did not reach the router barrier")
		}
	}
	close(release)
	first, second := <-responses, <-responses
	if first.Code != http.StatusCreated && first.Code != http.StatusOK {
		t.Fatalf("first concurrent request failed: %d %s", first.Code, first.Body.String())
	}
	if second.Code != http.StatusCreated && second.Code != http.StatusOK {
		t.Fatalf("second concurrent idempotent request failed: %d %s", second.Code, second.Body.String())
	}
	var one, two struct {
		RefundKey string `json:"refund_key"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &one); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second.Body.Bytes(), &two); err != nil {
		t.Fatal(err)
	}
	if one.RefundKey == "" || one.RefundKey != two.RefundKey {
		t.Fatalf("concurrent same-key requests did not resolve to one refund: %q %q", one.RefundKey, two.RefundKey)
	}
	var refunds int64
	if err := db.Model(&model.PointRefund{}).Where("user_id = ? AND order_key = ? AND idempotency_key = ?", userID, orderKey, "same-refund-http-key-001").Count(&refunds).Error; err != nil {
		t.Fatal(err)
	}
	if refunds != 1 {
		t.Fatalf("expected one refund row, got %d", refunds)
	}
	var allocations int64
	if err := db.Model(&model.PointRefundAllocation{}).Where("refund_id = (SELECT id FROM point_refunds WHERE refund_key = ?)", one.RefundKey).Count(&allocations).Error; err != nil {
		t.Fatal(err)
	}
	if allocations != 1 {
		t.Fatalf("same-key requests froze more than one allocation set: %d", allocations)
	}
	var account model.PointAccount
	if err := db.First(&account, "user_id = ?", userID).Error; err != nil {
		t.Fatal(err)
	}
	if account.HeldMicro != 100*model.PointMicroPerPoint || account.AvailableMicro != 900*model.PointMicroPerPoint {
		t.Fatalf("reservation was not applied exactly once: available=%d held=%d", account.AvailableMicro, account.HeldMicro)
	}
	serve := func(method, path string, session *http.Cookie, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(session)
		if method != http.MethodGet {
			req.Header.Set("Origin", config.ServerAddress)
			req.Header.Set("X-CSRF-Token", csrf)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if response := serve(http.MethodGet, "/api/payments/refunds/"+one.RefundKey, cookie, ""); response.Code != http.StatusOK {
		t.Fatalf("owner could not read refund status: %d %s", response.Code, response.Body.String())
	}
	otherID := userID + 1
	if err := db.Create(&model.User{Id: otherID, Username: "refund-other-http", Role: model.RoleCommonUser, Status: model.UserStatusEnabled, AffCode: "refund-other-http", AccessToken: "refund-other-http-token"}).Error; err != nil {
		t.Fatal(err)
	}
	otherSessionResponse := httptest.NewRecorder()
	r.ServeHTTP(otherSessionResponse, httptest.NewRequest(http.MethodGet, "/test-session/"+strconv.Itoa(otherID), nil))
	if otherSessionResponse.Code != http.StatusNoContent || len(otherSessionResponse.Result().Cookies()) == 0 {
		t.Fatalf("other user session setup failed: %d", otherSessionResponse.Code)
	}
	if response := serve(http.MethodGet, "/api/payments/refunds/"+one.RefundKey, otherSessionResponse.Result().Cookies()[0], ""); response.Code != http.StatusNotFound {
		t.Fatalf("another user could read the refund: %d %s", response.Code, response.Body.String())
	}
	config.PointsBillingEnabled = false
	config.PointRefundOperationsEnabled = false
	if response := serve(http.MethodGet, "/api/payments/refunds/"+one.RefundKey, cookie, ""); response.Code != http.StatusOK {
		t.Fatalf("historical refund read was blocked with new refunds disabled: %d %s", response.Code, response.Body.String())
	}
	closedGateBody := `{"amount_fen":100,"reason":"新申请应关闭","idempotency_key":"closed-refund-http-key-001"}`
	if response := serve(http.MethodPost, "/api/payments/orders/"+orderKey+"/refund-requests", cookie, closedGateBody); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("new refund request passed while gate disabled: %d %s", response.Code, response.Body.String())
	}
}

type refundHTTPProvider struct {
	applyCalls atomic.Int32
	queryCalls atomic.Int32
}

func (p *refundHTTPProvider) Create(context.Context, payment.Order) (payment.Checkout, error) {
	return payment.Checkout{}, nil
}
func (p *refundHTTPProvider) VerifyNotification(http.Header, []byte) (payment.VerifiedNotification, error) {
	return payment.VerifiedNotification{}, payment.ErrInvalidNotice
}
func (p *refundHTTPProvider) Query(context.Context, string) (payment.Trade, error) {
	return payment.Trade{}, payment.ErrUnknownStatus
}
func (p *refundHTTPProvider) Close(context.Context, string) error { return nil }
func (p *refundHTTPProvider) ApplyRefund(_ context.Context, request payment.RefundRequest) (payment.RefundResult, error) {
	p.applyCalls.Add(1)
	return refundHTTPResult(request, payment.RefundAccepted, "PROCESSING"), nil
}
func (p *refundHTTPProvider) QueryRefund(_ context.Context, request payment.RefundRequest) (payment.RefundResult, error) {
	p.queryCalls.Add(1)
	return refundHTTPResult(request, payment.RefundUnknown, "PROCESSING"), nil
}

func refundHTTPResult(request payment.RefundRequest, outcome payment.RefundOutcome, status string) payment.RefundResult {
	return payment.RefundResult{Provider: "alipay", Outcome: outcome, Status: status, RefundKey: request.RefundKey, ProviderRefundKey: request.ProviderRefundKey,
		OrderKey: request.OrderKey, TransactionID: request.TransactionID, MerchantID: request.MerchantID, AppID: request.AppID,
		AmountFen: request.AmountFen, TotalFen: request.TotalFen, Currency: request.Currency}
}

func TestAdminRefundApprovalRequiresSeparateSubmitAndReplaysDurableIntent(t *testing.T) {
	oldDB, oldSecret, oldAddress := model.DB, config.SessionSecret, config.ServerAddress
	oldBilling, oldRefunds, oldRedis, oldMode := config.PointsBillingEnabled, config.PointRefundOperationsEnabled, common.RedisEnabled, gin.Mode()
	t.Cleanup(func() {
		model.DB, config.SessionSecret, config.ServerAddress = oldDB, oldSecret, oldAddress
		config.PointsBillingEnabled, config.PointRefundOperationsEnabled = oldBilling, oldRefunds
		common.RedisEnabled = oldRedis
		gin.SetMode(oldMode)
	})
	config.SessionSecret = "refund-decision-http-test-session-secret"
	config.ServerAddress = "http://refund-decision.example.test"
	config.PointsBillingEnabled = true
	config.PointRefundOperationsEnabled = true
	common.RedisEnabled = false
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/refund-decision-http.db?_txlock=immediate&_busy_timeout=5000"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	model.DB = db
	if err := db.AutoMigrate(&model.User{}, &model.RefundCapabilityGrant{}, &model.RefundAuthorizationAudit{}, &model.RefundStepUpTicket{}); err != nil {
		t.Fatal(err)
	}
	if err := model.MigratePointsSchema(); err != nil {
		t.Fatal(err)
	}
	passwordHash, err := common.Password2Hash("decision-password-123")
	if err != nil {
		t.Fatal(err)
	}
	rootID, customerID := 86201, 86202
	users := []model.User{
		{Id: rootID, Username: "refund-decision-root", Password: passwordHash, Role: model.RoleRootUser, Status: model.UserStatusEnabled, AffCode: "refund-decision-root", AccessToken: "refund-decision-root-token"},
		{Id: customerID, Username: "refund-decision-customer", Role: model.RoleCommonUser, Status: model.UserStatusEnabled, AffCode: "refund-decision-customer", AccessToken: "refund-decision-customer-token"},
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatal(err)
	}
	const orderKey = "refund-decision-order"
	const purchase = int64(1000) * model.PointMicroPerPoint
	if err := db.Create(&model.PointPurchaseOrder{OrderKey: orderKey, UserID: customerID, Channel: "alipay", Currency: "CNY", AmountFen: 1000, PurchaseMicro: purchase,
		ProviderTransactionID: "decision-trade", ProviderMerchantID: "decision-merchant", ProviderAppID: "decision-app", State: "credited"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.PointAccount{UserID: customerID, AvailableMicro: purchase, UpdatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.PointLot{UserID: customerID, BusinessKey: "purchase:" + orderKey, Kind: "purchase", SourceRef: orderKey, AmountFen: 1000, InitialMicro: purchase, AvailableMicro: purchase, CreatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	refund, err := model.RequestPointRefund(model.PointRefundRequest{UserID: customerID, OrderKey: orderKey, IdempotencyKey: "decision-request-idem-001", RefundKey: "decision-refund-key", ProviderRefundKey: "Rdecisionrefund001", AmountFen: 100, Reason: "customer request"})
	if err != nil {
		t.Fatal(err)
	}
	fake := &refundHTTPProvider{}
	restoreProviders := payments.ReplaceProvidersForTest(map[string]payments.RuntimeProvider{"alipay": {Identity: payments.MerchantIdentity{Provider: "alipay", MerchantID: "decision-merchant", AppID: "decision-app"}, Provider: fake}})
	defer restoreProviders()

	r := gin.New()
	r.Use(sessions.Sessions("session", cookie.NewStore([]byte(config.SessionSecret))))
	r.GET("/test-session/:id", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		s := sessions.Default(c)
		s.Set("id", id)
		s.Set("refund_auth_session", "decision-session-012345678901234567")
		if err := s.Save(); err != nil {
			t.Error(err)
		}
		c.Status(http.StatusNoContent)
	})
	router.SetApiRouter(r)
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()
	sessionResponse := httptest.NewRecorder()
	r.ServeHTTP(sessionResponse, httptest.NewRequest(http.MethodGet, "/test-session/"+strconv.Itoa(rootID), nil))
	if sessionResponse.Code != http.StatusNoContent || len(sessionResponse.Result().Cookies()) == 0 {
		t.Fatalf("root session setup failed: %d", sessionResponse.Code)
	}
	rootCookie := sessionResponse.Result().Cookies()[0]
	csrf := middleware.PointsCSRFToken(rootID)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(rootCookie)
		if method != http.MethodGet {
			req.Header.Set("Origin", config.ServerAddress)
			req.Header.Set("X-CSRF-Token", csrf)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	issue := func(action, businessKey, reason string) string {
		bodyBytes, _ := json.Marshal(map[string]string{"action": action, "refund_key": refund.RefundKey, "business_key": businessKey, "reason": reason, "password": "decision-password-123"})
		response := request(http.MethodPost, "/api/refund-auth/step-up", string(bodyBytes))
		if response.Code != http.StatusOK {
			t.Fatalf("issue %s ticket: %d %s", action, response.Code, response.Body.String())
		}
		var payload struct {
			Ticket string `json:"ticket"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload.Ticket == "" {
			t.Fatalf("ticket missing: %v", err)
		}
		return payload.Ticket
	}
	approveKey, approveReason := "decision-approve-business-001", "核对退款材料"
	approveTicket := issue("refund.approve", approveKey, approveReason)
	approveBody, _ := json.Marshal(map[string]string{"business_key": approveKey, "reason": approveReason, "step_up_ticket": approveTicket})
	approvePath := "/api/admin/refunds/" + refund.RefundKey + "/approve"
	approved := request(http.MethodPost, approvePath, string(approveBody))
	if approved.Code != http.StatusOK || !strings.Contains(approved.Body.String(), `"state":"review_approved"`) {
		t.Fatalf("review did not stop before provider submission: %d %s", approved.Code, approved.Body.String())
	}
	replayedApprove := request(http.MethodPost, approvePath, string(approveBody))
	if replayedApprove.Code != http.StatusOK || !strings.Contains(replayedApprove.Body.String(), `"state":"review_approved"`) {
		t.Fatalf("committed approval could not be safely replayed: %d %s", replayedApprove.Code, replayedApprove.Body.String())
	}
	if _, err := model.ClaimPointRefundOperation(refund.RefundKey, "review-must-not-dispatch", time.Now().UTC().Unix(), 60); !errors.Is(err, model.ErrPointRefundState) {
		t.Fatalf("review_approved entered provider dispatch: %v", err)
	}
	submitKey, submitReason := "decision-submit-business-001", "已复核，提交原路退款"
	submitTicket, err := model.IssueRefundDecisionStepUpTicket(rootID, "decision-session-012345678901234567", "decision-password-123", refund.RefundKey, "refund.submit", submitKey, submitReason)
	if err != nil {
		t.Fatal(err)
	}
	submitBody, _ := json.Marshal(map[string]string{"business_key": submitKey, "reason": submitReason, "step_up_ticket": submitTicket})
	submitPath := "/api/admin/refunds/" + refund.RefundKey + "/submit"
	// Commit the scoped ticket and submit intent, then simulate a process exit
	// before the HTTP handler can dispatch the provider operation.
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := model.ConsumeRefundDecisionStepUpTx(tx, submitTicket, rootID, "decision-session-012345678901234567", refund.RefundKey, "refund.submit", submitKey, submitReason, time.Now().UTC()); err != nil {
			return err
		}
		return model.DecidePointRefundTx(tx, refund.RefundKey, submitKey, rootID, "submit", submitReason, true)
	}); err != nil {
		t.Fatal(err)
	}
	resumedSubmit := request(http.MethodPost, submitPath, string(submitBody))
	if resumedSubmit.Code != http.StatusAccepted {
		t.Fatalf("idempotent submit did not resume durable intent: %d %s", resumedSubmit.Code, resumedSubmit.Body.String())
	}
	replayedSubmit := request(http.MethodPost, submitPath, string(submitBody))
	if replayedSubmit.Code != http.StatusAccepted {
		t.Fatalf("replayed submit did not return existing operation: %d %s", replayedSubmit.Code, replayedSubmit.Body.String())
	}
	if fake.applyCalls.Load() != 1 {
		t.Fatalf("durable submit replay called apply %d times", fake.applyCalls.Load())
	}
	if fake.queryCalls.Load() > 1 {
		t.Fatalf("replayed submit made too many provider queries: %d", fake.queryCalls.Load())
	}
}

func TestFinanceReconcileRecoversPreviouslyAuthorizedSubmitIntent(t *testing.T) {
	oldDB, oldSecret, oldAddress := model.DB, config.SessionSecret, config.ServerAddress
	oldBilling, oldRefunds, oldRedis, oldMode := config.PointsBillingEnabled, config.PointRefundOperationsEnabled, common.RedisEnabled, gin.Mode()
	t.Cleanup(func() {
		model.DB, config.SessionSecret, config.ServerAddress = oldDB, oldSecret, oldAddress
		config.PointsBillingEnabled, config.PointRefundOperationsEnabled = oldBilling, oldRefunds
		common.RedisEnabled = oldRedis
		gin.SetMode(oldMode)
	})
	config.SessionSecret = "refund-recovery-http-test-session-secret"
	config.ServerAddress = "http://refund-recovery.example.test"
	config.PointsBillingEnabled = true
	config.PointRefundOperationsEnabled = true
	common.RedisEnabled = false
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/refund-recovery-http.db?_txlock=immediate&_busy_timeout=5000"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	model.DB = db
	if err := db.AutoMigrate(&model.User{}, &model.RefundCapabilityGrant{}, &model.RefundAuthorizationAudit{}, &model.RefundStepUpTicket{}); err != nil {
		t.Fatal(err)
	}
	if err := model.MigratePointsSchema(); err != nil {
		t.Fatal(err)
	}
	passwordHash, err := common.Password2Hash("recovery-password-123")
	if err != nil {
		t.Fatal(err)
	}
	rootID, financeID, customerID := 86301, 86302, 86303
	users := []model.User{
		{Id: rootID, Username: "refund-recovery-root", Password: passwordHash, Role: model.RoleRootUser, Status: model.UserStatusEnabled, AffCode: "refund-recovery-root", AccessToken: "refund-recovery-root-token"},
		{Id: financeID, Username: "refund-recovery-finance", Password: passwordHash, Role: model.RoleCommonUser, Status: model.UserStatusEnabled, AffCode: "refund-recovery-finance", AccessToken: "refund-recovery-finance-token"},
		{Id: customerID, Username: "refund-recovery-customer", Role: model.RoleCommonUser, Status: model.UserStatusEnabled, AffCode: "refund-recovery-customer", AccessToken: "refund-recovery-customer-token"},
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatal(err)
	}
	const orderKey = "refund-recovery-order"
	const purchase = int64(1000) * model.PointMicroPerPoint
	if err := db.Create(&model.PointPurchaseOrder{OrderKey: orderKey, UserID: customerID, Channel: "alipay", Currency: "CNY", AmountFen: 1000, PurchaseMicro: purchase,
		ProviderTransactionID: "recovery-trade", ProviderMerchantID: "recovery-merchant", ProviderAppID: "recovery-app", State: "credited"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.PointAccount{UserID: customerID, AvailableMicro: purchase, UpdatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.PointLot{UserID: customerID, BusinessKey: "purchase:" + orderKey, Kind: "purchase", SourceRef: orderKey, AmountFen: 1000, InitialMicro: purchase, AvailableMicro: purchase, CreatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	refund, err := model.RequestPointRefund(model.PointRefundRequest{UserID: customerID, OrderKey: orderKey, IdempotencyKey: "recovery-request-idem-001", RefundKey: "recovery-refund-key", ProviderRefundKey: "Rrecoveryrefund001", AmountFen: 100, Reason: "customer request"})
	if err != nil {
		t.Fatal(err)
	}
	fake := &refundHTTPProvider{}
	restoreProviders := payments.ReplaceProvidersForTest(map[string]payments.RuntimeProvider{"alipay": {Identity: payments.MerchantIdentity{Provider: "alipay", MerchantID: "recovery-merchant", AppID: "recovery-app"}, Provider: fake}})
	defer restoreProviders()

	r := gin.New()
	r.Use(sessions.Sessions("session", cookie.NewStore([]byte(config.SessionSecret))))
	r.GET("/test-session/:id", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		s := sessions.Default(c)
		s.Set("id", id)
		s.Set("refund_auth_session", "recovery-session-012345678901234567")
		if err := s.Save(); err != nil {
			t.Error(err)
		}
		c.Status(http.StatusNoContent)
	})
	router.SetApiRouter(r)
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()
	login := func(id int) *http.Cookie {
		response := httptest.NewRecorder()
		r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/test-session/"+strconv.Itoa(id), nil))
		if response.Code != http.StatusNoContent || len(response.Result().Cookies()) == 0 {
			t.Fatalf("user %d session setup failed: %d", id, response.Code)
		}
		return response.Result().Cookies()[0]
	}
	rootCookie, financeCookie := login(rootID), login(financeID)
	request := func(method, path, body string, actorID int, session *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(session)
		if method != http.MethodGet {
			req.Header.Set("Origin", config.ServerAddress)
			req.Header.Set("X-CSRF-Token", middleware.PointsCSRFToken(actorID))
		}
		response := httptest.NewRecorder()
		r.ServeHTTP(response, req)
		return response
	}

	approveKey, approveReason := "recovery-approve-business-001", "退款材料已核实"
	approveTicket, err := model.IssueRefundDecisionStepUpTicket(rootID, "recovery-session-012345678901234567", "recovery-password-123", refund.RefundKey, "refund.approve", approveKey, approveReason)
	if err != nil {
		t.Fatal(err)
	}
	approveBody, _ := json.Marshal(map[string]string{"business_key": approveKey, "reason": approveReason, "step_up_ticket": approveTicket})
	approve := request(http.MethodPost, "/api/admin/refunds/"+refund.RefundKey+"/approve", string(approveBody), rootID, rootCookie)
	if approve.Code != http.StatusOK || !strings.Contains(approve.Body.String(), `"state":"review_approved"`) {
		t.Fatalf("approval failed: %d %s", approve.Code, approve.Body.String())
	}

	grantReason := "指定财务核实退款状态"
	grantTicket, err := model.IssueRefundCapabilityStepUpTicket(rootID, "recovery-session-012345678901234567", "recovery-password-123", model.RefundStepUpGrant, financeID, []string{model.RefundCapabilityReconcile}, grantReason)
	if err != nil {
		t.Fatal(err)
	}
	grantBody, _ := json.Marshal(map[string]any{"action": model.RefundStepUpGrant, "target_user_id": financeID, "capabilities": []string{model.RefundCapabilityReconcile}, "reason": grantReason, "step_up_ticket": grantTicket})
	grant := request(http.MethodPost, "/api/admin/refund-auth/grants", string(grantBody), rootID, rootCookie)
	if grant.Code != http.StatusOK {
		t.Fatalf("grant reconcile capability: %d %s", grant.Code, grant.Body.String())
	}

	submitKey, submitReason := "recovery-submit-business-001", "按批准方案提交退款"
	submitTicket, err := model.IssueRefundDecisionStepUpTicket(rootID, "recovery-session-012345678901234567", "recovery-password-123", refund.RefundKey, "refund.submit", submitKey, submitReason)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := model.ConsumeRefundDecisionStepUpTx(tx, submitTicket, rootID, "recovery-session-012345678901234567", refund.RefundKey, "refund.submit", submitKey, submitReason, time.Now().UTC()); err != nil {
			return err
		}
		return model.DecidePointRefundTx(tx, refund.RefundKey, submitKey, rootID, "submit", submitReason, true)
	}); err != nil {
		t.Fatal(err)
	}
	var before model.PointRefund
	if err := db.First(&before, "refund_key = ?", refund.RefundKey).Error; err != nil || before.State != "approved" || before.RecoveryAction != "apply" {
		t.Fatalf("submit intent was not durably committed: refund=%+v err=%v", before, err)
	}
	// The submitter is now unavailable; a separately authorized financial
	// operator can safely resume only this already authorized intent.
	if err := db.Model(&model.User{}).Where("id = ?", rootID).Update("status", model.UserStatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	resumed := request(http.MethodPost, "/api/admin/refunds/"+refund.RefundKey+"/reconcile", `{}`, financeID, financeCookie)
	if resumed.Code != http.StatusOK {
		t.Fatalf("finance could not recover prior submit intent: %d %s", resumed.Code, resumed.Body.String())
	}
	if fake.applyCalls.Load() != 1 {
		t.Fatalf("recovery did not issue exactly one original-number apply: %d", fake.applyCalls.Load())
	}
	var after model.PointRefund
	if err := db.First(&after, "refund_key = ?", refund.RefundKey).Error; err != nil || after.State != "submitted" {
		t.Fatalf("recovered refund state mismatch: %+v err=%v", after, err)
	}
}

func TestWeChatRefundNotificationRouterProcessesHistoricalNoticeWhenNewRefundsPaused(t *testing.T) {
	oldDB, oldSecret, oldAddress := model.DB, config.SessionSecret, config.ServerAddress
	oldBilling, oldRefunds, oldRedis, oldMode := config.PointsBillingEnabled, config.PointRefundOperationsEnabled, common.RedisEnabled, gin.Mode()
	t.Cleanup(func() {
		model.DB, config.SessionSecret, config.ServerAddress = oldDB, oldSecret, oldAddress
		config.PointsBillingEnabled, config.PointRefundOperationsEnabled = oldBilling, oldRefunds
		common.RedisEnabled = oldRedis
		gin.SetMode(oldMode)
	})
	config.SessionSecret = "refund-notify-http-test-session-secret"
	config.ServerAddress = "http://refund-notify.example.test"
	config.PointsBillingEnabled = false
	config.PointRefundOperationsEnabled = false
	common.RedisEnabled = false
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/refund-notify-http.db?_txlock=immediate&_busy_timeout=5000"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	model.DB = db
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatal(err)
	}
	if err := model.MigratePointsSchema(); err != nil {
		t.Fatal(err)
	}
	const userID = 86401
	const orderKey = "refund-notify-order"
	const merchantID = "refund-notify-merchant"
	const appID = "refund-notify-app"
	const transactionID = "refund-notify-transaction"
	if err := db.Create(&model.User{Id: userID, Username: "refund-notify-user", Role: model.RoleCommonUser, Status: model.UserStatusEnabled, AffCode: "refund-notify-user", AccessToken: "refund-notify-user-token"}).Error; err != nil {
		t.Fatal(err)
	}
	const purchase = int64(1000) * model.PointMicroPerPoint
	if err := db.Create(&model.PointPurchaseOrder{OrderKey: orderKey, UserID: userID, Channel: "wechat", Currency: "CNY", AmountFen: 1000, PurchaseMicro: purchase,
		ProviderTransactionID: transactionID, ProviderMerchantID: merchantID, ProviderAppID: appID, State: "credited"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.PointAccount{UserID: userID, AvailableMicro: purchase, UpdatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.PointLot{UserID: userID, BusinessKey: "purchase:" + orderKey, Kind: "purchase", SourceRef: orderKey, AmountFen: 1000, InitialMicro: purchase, AvailableMicro: purchase, CreatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	refund, err := model.RequestPointRefund(model.PointRefundRequest{UserID: userID, OrderKey: orderKey, IdempotencyKey: "refund-notify-request-001", RefundKey: "refund-notify-refund", ProviderRefundKey: "Rrefundnotify001", AmountFen: 100, Reason: "refund notification test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := model.ApprovePointRefund(refund.RefundKey, "refund-notify-approve-001", 1, "test approval"); err != nil {
		t.Fatal(err)
	}
	merchantKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	platformKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	apiKey := []byte("0123456789abcdef0123456789abcdef")
	provider, err := wechat.New(wechat.Config{Enabled: true, MerchantID: merchantID, AppID: appID, MerchantSerial: "merchant-refund-serial", PlatformSerial: "platform-refund-serial",
		APIPrivateKey: merchantKey, PlatformPublicKey: &platformKey.PublicKey, APIv3Key: apiKey, NotifyURL: "https://merchant.example.test/payments", RefundNotifyURL: "https://merchant.example.test/refunds"})
	if err != nil {
		t.Fatal(err)
	}
	restoreProviders := payments.ReplaceProvidersForTest(map[string]payments.RuntimeProvider{"wechat": {Identity: payments.MerchantIdentity{Provider: "wechat", MerchantID: merchantID, AppID: appID}, Provider: provider}})
	defer restoreProviders()
	r := gin.New()
	router.SetApiRouter(r)
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()
	body, headers := signedRefundHTTPNotice(t, platformKey, apiKey, merchantID, orderKey, transactionID, refund.ProviderRefundKey)
	for attempt := 0; attempt < 2; attempt++ {
		req := httptest.NewRequest(http.MethodPost, "/api/payments/refunds/notify/wechat", strings.NewReader(string(body)))
		req.Header = headers.Clone()
		response := httptest.NewRecorder()
		r.ServeHTTP(response, req)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"code":"SUCCESS"`) {
			t.Fatalf("historical refund notification attempt %d failed: %d %s", attempt, response.Code, response.Body.String())
		}
	}
	var saved model.PointRefund
	if err := db.First(&saved, "refund_key = ?", refund.RefundKey).Error; err != nil || saved.State != "succeeded" {
		var inbox model.PointRefundInbox
		_ = db.Where("refund_key = ?", refund.RefundKey).First(&inbox).Error
		_, processErr := payments.ProcessPointRefundInbox(inbox.ID)
		t.Fatalf("verified refund was not finalized with new-refund gate off: state=%s err=%v process=%v inbox=%+v", saved.State, err, processErr, inbox)
	}
	var account model.PointAccount
	if err := db.First(&account, "user_id = ?", userID).Error; err != nil {
		t.Fatal(err)
	}
	if account.AvailableMicro != 900*model.PointMicroPerPoint || account.HeldMicro != 0 {
		t.Fatalf("refund ledger outcome incorrect after duplicate notices: available=%d held=%d", account.AvailableMicro, account.HeldMicro)
	}
	var rows int64
	if err := db.Model(&model.PointLedger{}).Where("business_key = ?", "refund-success:"+refund.RefundKey).Count(&rows).Error; err != nil || rows != 1 {
		t.Fatalf("duplicate notification produced refund ledger count=%d err=%v", rows, err)
	}
}

func signedRefundHTTPNotice(t *testing.T, platformKey *rsa.PrivateKey, apiKey []byte, merchantID, orderKey, transactionID, providerRefundKey string) ([]byte, http.Header) {
	t.Helper()
	nonce := []byte("nonce-123456")
	plain, err := json.Marshal(map[string]any{"mchid": merchantID, "out_trade_no": orderKey, "transaction_id": transactionID, "out_refund_no": providerRefundKey,
		"refund_id": "wx-refund-notify-001", "refund_status": "SUCCESS", "amount": map[string]any{"total": 1000, "refund": 100, "currency": "CNY"}})
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(apiKey)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	associated := ""
	ciphertext := gcm.Seal(nil, nonce, plain, []byte(associated))
	body, err := json.Marshal(map[string]any{"id": "refund-notify-event-001", "create_time": time.Now().UTC().Format(time.RFC3339), "event_type": "REFUND.SUCCESS", "summary": "refund success",
		"resource_type": "encrypt-resource", "resource": map[string]any{"original_type": "refund", "algorithm": "AEAD_AES_256_GCM", "ciphertext": base64.StdEncoding.EncodeToString(ciphertext), "nonce": string(nonce), "associated_data": associated}})
	if err != nil {
		t.Fatal(err)
	}
	timestamp := fmt.Sprint(time.Now().UTC().Unix())
	signatureNonce := "refund-http-signature-nonce"
	message := []byte(timestamp + "\n" + signatureNonce + "\n" + string(body) + "\n")
	digest := sha256.Sum256(message)
	signature, err := rsa.SignPKCS1v15(rand.Reader, platformKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	headers := make(http.Header)
	headers.Set("Wechatpay-Serial", "platform-refund-serial")
	headers.Set("Wechatpay-Timestamp", timestamp)
	headers.Set("Wechatpay-Nonce", signatureNonce)
	headers.Set("Wechatpay-Signature", base64.StdEncoding.EncodeToString(signature))
	return body, headers
}
