package controller_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	dbmodel "github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/payment/bill"
	"github.com/songquanpeng/one-api/router"
	"github.com/songquanpeng/one-api/service/payments"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestAdminReconciliationRoutesCapabilitiesImportAuditAndRedaction(t *testing.T) {
	oldDB, oldSecret, oldAddress := dbmodel.DB, config.SessionSecret, config.ServerAddress
	oldBilling, oldRefundOps, oldRedis, oldMode := config.PointsBillingEnabled, config.PointRefundOperationsEnabled, common.RedisEnabled, gin.Mode()
	oldKeyID, oldKey := config.PointReconciliationSourceKeyID, config.PointReconciliationSourceKeyBase64
	t.Cleanup(func() {
		dbmodel.DB, config.SessionSecret, config.ServerAddress = oldDB, oldSecret, oldAddress
		config.PointsBillingEnabled, config.PointRefundOperationsEnabled, common.RedisEnabled = oldBilling, oldRefundOps, oldRedis
		config.PointReconciliationSourceKeyID, config.PointReconciliationSourceKeyBase64 = oldKeyID, oldKey
		gin.SetMode(oldMode)
	})
	config.SessionSecret, config.ServerAddress = "reconciliation-http-test-session-secret", "http://reconciliation.example.test"
	config.PointsBillingEnabled, config.PointRefundOperationsEnabled, common.RedisEnabled = false, false, false
	gin.SetMode(gin.TestMode)
	const sourceKey = "0123456789abcdef0123456789abcdef"
	config.PointReconciliationSourceKeyID = "http-test-key-v1"
	config.PointReconciliationSourceKeyBase64 = base64.StdEncoding.EncodeToString([]byte(sourceKey))
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/reconciliation-http.db?_txlock=immediate&_busy_timeout=5000"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	dbmodel.DB = db
	if err := db.AutoMigrate(&dbmodel.User{}, &dbmodel.RefundCapabilityGrant{}, &dbmodel.RefundAuthorizationAudit{}, &dbmodel.RefundStepUpTicket{}); err != nil {
		t.Fatal(err)
	}
	if err := dbmodel.MigratePointsSchema(); err != nil {
		t.Fatal(err)
	}
	hash, err := common.Password2Hash("local-password-123")
	if err != nil {
		t.Fatal(err)
	}
	root := dbmodel.User{Id: 99101, Username: "recon-root", Password: hash, Role: dbmodel.RoleRootUser, Status: dbmodel.UserStatusEnabled, AffCode: "recon-root", AccessToken: "must-not-leak-root"}
	finance := dbmodel.User{Id: 99102, Username: "recon-finance", Password: hash, Role: dbmodel.RoleCommonUser, Status: dbmodel.UserStatusEnabled, AffCode: "recon-finance", AccessToken: "must-not-leak-finance"}
	customer := dbmodel.User{Id: 99103, Username: "recon-customer", Password: hash, Role: dbmodel.RoleCommonUser, Status: dbmodel.UserStatusEnabled, AffCode: "recon-customer", AccessToken: "must-not-leak-customer"}
	legacyAdmin := dbmodel.User{Id: 99104, Username: "recon-old-admin", Password: hash, Role: dbmodel.RoleAdminUser, Status: dbmodel.UserStatusEnabled, AffCode: "recon-old-admin", AccessToken: "must-not-leak-admin"}
	viewer := dbmodel.User{Id: 99105, Username: "recon-viewer", Password: hash, Role: dbmodel.RoleCommonUser, Status: dbmodel.UserStatusEnabled, AffCode: "recon-viewer", AccessToken: "must-not-leak-viewer"}
	if err := db.Create(&[]dbmodel.User{root, finance, customer, legacyAdmin, viewer}).Error; err != nil {
		t.Fatal(err)
	}
	const sessionBinding = "reconciliation-session-binding-0123456789"
	scope := dbmodel.RefundStepUpScope{Action: "capability.grant", TargetUserID: finance.Id, Capabilities: []string{dbmodel.ReconciliationCapabilityRead, dbmodel.ReconciliationCapabilityImport, dbmodel.ReconciliationCapabilityNote}, Reason: "对账职责授权单 RECON-1"}
	ticket, err := dbmodel.IssueRefundCapabilityStepUpTicket(root.Id, sessionBinding, "local-password-123", scope.Action, scope.TargetUserID, scope.Capabilities, scope.Reason)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbmodel.ApplyRefundCapabilityChange(root.Id, sessionBinding, ticket, scope); err != nil {
		t.Fatal(err)
	}
	viewerScope := dbmodel.RefundStepUpScope{Action: "capability.grant", TargetUserID: viewer.Id, Capabilities: []string{dbmodel.ReconciliationCapabilityRead}, Reason: "对账只读授权单 RECON-READ-1"}
	viewerTicket, err := dbmodel.IssueRefundCapabilityStepUpTicket(root.Id, sessionBinding, "local-password-123", viewerScope.Action, viewerScope.TargetUserID, viewerScope.Capabilities, viewerScope.Reason)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbmodel.ApplyRefundCapabilityChange(root.Id, sessionBinding, viewerTicket, viewerScope); err != nil {
		t.Fatal(err)
	}

	day := time.Now().In(time.FixedZone("Asia/Shanghai", 8*60*60)).AddDate(0, 0, -1).Format("2006-01-02")
	secretSource := []byte("synthetic opaque alipay statement bytes")
	raw := bill.RawBill{Provider: "alipay", BillDate: day, Timezone: "Asia/Shanghai", FormatVersion: "alipay-trade-raw-unparsed-v1", RequestedMerchantID: "server-merchant-1", RequestedAppID: "server-app-1", Bytes: secretSource, SHA256: bill.SHA256(secretSource)}
	input := dbmodel.PointReconciliationInputFromRaw(raw, "trade")
	var fetchCalls atomic.Int32
	fetchCleanup := payments.ReplaceReconciliationFetcherForTest(func(ctx context.Context, provider, billDate string) (dbmodel.PointReconciliationBillInput, payments.MerchantIdentity, error) {
		fetchCalls.Add(1)
		if provider != "alipay" || billDate != day {
			t.Errorf("unexpected server-side provider scope: %s %s", provider, billDate)
		}
		return input, payments.MerchantIdentity{Provider: "alipay", MerchantID: "server-merchant-1", AppID: "server-app-1"}, nil
	})
	t.Cleanup(fetchCleanup)

	r := gin.New()
	r.Use(sessions.Sessions("session", cookie.NewStore([]byte(config.SessionSecret))))
	r.GET("/test-session/:id", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		s := sessions.Default(c)
		s.Set("id", id)
		s.Set("refund_auth_session", sessionBinding)
		if err := s.Save(); err != nil {
			t.Error(err)
		}
		c.Status(http.StatusNoContent)
	})
	router.SetApiRouter(r)
	defer func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() }()
	login := func(id int) *http.Cookie {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/test-session/"+strconv.Itoa(id), nil))
		if w.Code != http.StatusNoContent || len(w.Result().Cookies()) == 0 {
			t.Fatalf("session setup failed: %d", w.Code)
		}
		return w.Result().Cookies()[0]
	}
	rootCookie, financeCookie, customerCookie, oldAdminCookie, viewerCookie := login(root.Id), login(finance.Id), login(customer.Id), login(legacyAdmin.Id), login(viewer.Id)
	request := func(method, path string, cookie *http.Cookie, body string, csrf bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if method != http.MethodGet {
			req.Header.Set("Origin", config.ServerAddress)
			if csrf {
				req.Header.Set("X-CSRF-Token", middleware.PointsCSRFToken(finance.Id))
			}
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	status := request(http.MethodGet, "/api/admin/reconciliation/status", financeCookie, "", false)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"source_encryption_ready":true`) || strings.Contains(status.Body.String(), sourceKey) || strings.Contains(status.Body.String(), config.PointReconciliationSourceKeyBase64) {
		t.Fatalf("safe reconciliation status failed: %d %s", status.Code, status.Body.String())
	}
	if got := request(http.MethodGet, "/api/admin/reconciliation/batches", customerCookie, "", false); got.Code != http.StatusForbidden {
		t.Fatalf("customer read was allowed: %d", got.Code)
	}
	if got := request(http.MethodGet, "/api/admin/reconciliation/batches", oldAdminCookie, "", false); got.Code != http.StatusForbidden {
		t.Fatalf("legacy role 10 inherited access: %d", got.Code)
	}
	if got := request(http.MethodGet, "/api/admin/reconciliation/batches", viewerCookie, "", false); got.Code != http.StatusOK {
		t.Fatalf("delegated read capability was denied: %d %s", got.Code, got.Body.String())
	}
	viewerWrite := request(http.MethodPost, "/api/admin/reconciliation/import", viewerCookie, `{"provider":"alipay","bill_date":"`+day+`"}`, true)
	if viewerWrite.Code != http.StatusForbidden || fetchCalls.Load() != 0 {
		t.Fatalf("read-only capability could import: %d calls=%d", viewerWrite.Code, fetchCalls.Load())
	}
	noCSRF := request(http.MethodPost, "/api/admin/reconciliation/import", financeCookie, `{"provider":"alipay","bill_date":"`+day+`"}`, false)
	if noCSRF.Code != http.StatusForbidden || fetchCalls.Load() != 0 {
		t.Fatalf("import without CSRF reached adapter: %d calls=%d", noCSRF.Code, fetchCalls.Load())
	}
	forged := request(http.MethodPost, "/api/admin/reconciliation/import", financeCookie, `{"provider":"alipay","bill_date":"`+day+`","merchant_id":"attacker","url":"https://example.test"}`, true)
	if forged.Code != http.StatusBadRequest || fetchCalls.Load() != 0 {
		t.Fatalf("client supplied provider scope was accepted: %d calls=%d", forged.Code, fetchCalls.Load())
	}
	importBody := `{"provider":"alipay","bill_date":"` + day + `"}`
	first := request(http.MethodPost, "/api/admin/reconciliation/import", financeCookie, importBody, true)
	if first.Code != http.StatusCreated || !strings.Contains(first.Body.String(), `"status":"unsupported_format"`) {
		t.Fatalf("synthetic import failed: %d %s", first.Code, first.Body.String())
	}
	if strings.Contains(first.Body.String(), string(secretSource)) || strings.Contains(first.Body.String(), "source_ciphertext") || strings.Contains(first.Body.String(), "download_url") || strings.Contains(first.Body.String(), sourceKey) {
		t.Fatalf("import response exposed source/config: %s", first.Body.String())
	}
	var firstBody struct {
		Batch struct {
			BatchKey string `json:"batch_key"`
		} `json:"batch"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil || firstBody.Batch.BatchKey == "" {
		t.Fatalf("batch key missing: %v", err)
	}
	second := request(http.MethodPost, "/api/admin/reconciliation/import", financeCookie, importBody, true)
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), firstBody.Batch.BatchKey) {
		t.Fatalf("same source import not replayed: %d %s", second.Code, second.Body.String())
	}
	config.PointReconciliationSourceKeyBase64 = ""
	missingKey := request(http.MethodPost, "/api/admin/reconciliation/import", financeCookie, `{"provider":"alipay","bill_date":"`+day+`"}`, true)
	if missingKey.Code != http.StatusServiceUnavailable || fetchCalls.Load() != 2 || strings.Contains(missingKey.Body.String(), sourceKey) {
		t.Fatalf("missing key did not disable import safely: %d calls=%d %s", missingKey.Code, fetchCalls.Load(), missingKey.Body.String())
	}
	if stillReadable := request(http.MethodGet, "/api/admin/reconciliation/batches", financeCookie, "", false); stillReadable.Code != http.StatusOK {
		t.Fatalf("missing encryption key blocked metadata reads: %d %s", stillReadable.Code, stillReadable.Body.String())
	}
	config.PointReconciliationSourceKeyBase64 = base64.StdEncoding.EncodeToString([]byte(sourceKey))
	failedFetchCleanup := payments.ReplaceReconciliationFetcherForTest(func(context.Context, string, string) (dbmodel.PointReconciliationBillInput, payments.MerchantIdentity, error) {
		return dbmodel.PointReconciliationBillInput{}, payments.MerchantIdentity{}, errors.New("secret-download-token-and-url")
	})
	failedFetch := request(http.MethodPost, "/api/admin/reconciliation/import", financeCookie, `{"provider":"alipay","bill_date":"2026-01-01"}`, true)
	failedFetchCleanup()
	if failedFetch.Code != http.StatusBadGateway || strings.Contains(failedFetch.Body.String(), "secret-download-token-and-url") {
		t.Fatalf("downloader error leaked: %d %s", failedFetch.Code, failedFetch.Body.String())
	}
	var batchCount, importedAuditCount int64
	db.Model(&dbmodel.PointReconciliationBatch{}).Count(&batchCount)
	db.Model(&dbmodel.PointReconciliationImportAudit{}).Where("phase = ? AND outcome IN ?", "result", []string{"imported", "replayed", "unsupported"}).Count(&importedAuditCount)
	if batchCount != 1 || importedAuditCount != 2 {
		t.Fatalf("import replay evidence mismatch: batches=%d audits=%d", batchCount, importedAuditCount)
	}

	var batch dbmodel.PointReconciliationBatch
	if err := db.First(&batch).Error; err != nil {
		t.Fatal(err)
	}
	diff := dbmodel.PointReconciliationDifference{DifferenceKey: strings.Repeat("a", 64), BatchID: batch.ID, Classification: "other_scope", Severity: "informational", EvidenceFingerprint: strings.Repeat("b", 64)}
	if err := db.Create(&diff).Error; err != nil {
		t.Fatal(err)
	}
	noteBody := `{"action_key":"recon-note-http-key-1","action":"note","reason":"已核对渠道账单","business_ref":"case-2026-1"}`
	viewerNote := request(http.MethodPost, "/api/admin/reconciliation/differences/"+strconv.Itoa(int(diff.ID))+"/actions", viewerCookie, noteBody, true)
	if viewerNote.Code != http.StatusForbidden {
		t.Fatalf("read-only capability could add a note: %d %s", viewerNote.Code, viewerNote.Body.String())
	}
	note := request(http.MethodPost, "/api/admin/reconciliation/differences/"+strconv.Itoa(int(diff.ID))+"/actions", financeCookie, noteBody, true)
	if note.Code != http.StatusCreated {
		t.Fatalf("delegated note failed: %d %s", note.Code, note.Body.String())
	}
	replay := request(http.MethodPost, "/api/admin/reconciliation/differences/"+strconv.Itoa(int(diff.ID))+"/actions", financeCookie, noteBody, true)
	if replay.Code != http.StatusCreated {
		t.Fatalf("identical action replay failed: %d %s", replay.Code, replay.Body.String())
	}
	conflict := request(http.MethodPost, "/api/admin/reconciliation/differences/"+strconv.Itoa(int(diff.ID))+"/actions", financeCookie, strings.Replace(noteBody, "已核对渠道账单", "修改后的记录", 1), true)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("action key mutation was accepted: %d %s", conflict.Code, conflict.Body.String())
	}
	financeAudits := request(http.MethodGet, "/api/admin/reconciliation/import-attempts", financeCookie, "", false)
	if financeAudits.Code != http.StatusOK || strings.Contains(financeAudits.Body.String(), sourceKey) || strings.Contains(financeAudits.Body.String(), string(secretSource)) {
		t.Fatalf("import audit list failed/redacted incorrectly: %d %s", financeAudits.Code, financeAudits.Body.String())
	}
	revokeScope := dbmodel.RefundStepUpScope{Action: "capability.revoke", TargetUserID: finance.Id, Capabilities: []string{dbmodel.ReconciliationCapabilityRead}, Reason: "对账职责撤销 RECON-2"}
	revokeTicket, err := dbmodel.IssueRefundCapabilityStepUpTicket(root.Id, sessionBinding, "local-password-123", revokeScope.Action, revokeScope.TargetUserID, revokeScope.Capabilities, revokeScope.Reason)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbmodel.ApplyRefundCapabilityChange(root.Id, sessionBinding, revokeTicket, revokeScope); err != nil {
		t.Fatal(err)
	}
	if denied := request(http.MethodGet, "/api/admin/reconciliation/batches", financeCookie, "", false); denied.Code != http.StatusForbidden {
		t.Fatalf("revoked read capability stayed active: %d", denied.Code)
	}

	// A database read failure must not be misreported as invalid filters.
	if err := db.Migrator().DropTable(&dbmodel.PointReconciliationBatch{}); err != nil {
		t.Fatal(err)
	}
	readFailure := request(http.MethodGet, "/api/admin/reconciliation/batches?provider=alipay", rootCookie, "", false)
	if readFailure.Code != http.StatusInternalServerError {
		t.Fatalf("database failure was not reported as a read failure: %d %s", readFailure.Code, readFailure.Body.String())
	}
}
