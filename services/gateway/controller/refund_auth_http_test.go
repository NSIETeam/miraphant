package controller_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/config"
	dbmodel "github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/router"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestRefundAuthorizationRealRoutesAndStepUp(t *testing.T) {
	oldDB, oldSecret, oldAddress, oldEnabled, oldRedis, oldMode := dbmodel.DB, config.SessionSecret, config.ServerAddress, config.PointRefundOperationsEnabled, common.RedisEnabled, gin.Mode()
	t.Cleanup(func() {
		dbmodel.DB, config.SessionSecret, config.ServerAddress, config.PointRefundOperationsEnabled = oldDB, oldSecret, oldAddress, oldEnabled
		common.RedisEnabled = oldRedis
		gin.SetMode(oldMode)
	})
	config.SessionSecret = "refund-auth-http-test-secret"
	config.ServerAddress = "http://refund-auth.example.test"
	config.PointRefundOperationsEnabled = false
	common.RedisEnabled = false
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/refund-auth-http.db"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	dbmodel.DB = db
	if err := db.AutoMigrate(&dbmodel.User{}, &dbmodel.RefundCapabilityGrant{}, &dbmodel.RefundAuthorizationAudit{}, &dbmodel.RefundStepUpTicket{}); err != nil {
		t.Fatal(err)
	}
	hash, err := common.Password2Hash("local-password-123")
	if err != nil {
		t.Fatal(err)
	}
	users := []dbmodel.User{
		{Id: 951, Username: "http-root", Password: hash, Role: dbmodel.RoleRootUser, Status: dbmodel.UserStatusEnabled, AffCode: "http-root", AccessToken: "http-root-access"},
		{Id: 952, Username: "http-admin", Password: hash, Role: dbmodel.RoleAdminUser, Status: dbmodel.UserStatusEnabled, AffCode: "http-admin", AccessToken: "http-admin-access"},
		{Id: 953, Username: "http-finance", Password: hash, Role: dbmodel.RoleCommonUser, Status: dbmodel.UserStatusEnabled, AffCode: "http-finance", AccessToken: "http-finance-access"},
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(sessions.Sessions("session", cookie.NewStore([]byte(config.SessionSecret))))
	r.GET("/test-session/:id", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		s := sessions.Default(c)
		s.Set("id", id)
		s.Set("refund_auth_session", "session-binding-012345678901234567890123")
		if err := s.Save(); err != nil {
			t.Fatal(err)
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
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/test-session/"+strconv.Itoa(id), nil))
		if w.Code != http.StatusNoContent || len(w.Result().Cookies()) == 0 {
			t.Fatalf("create test session: status=%d", w.Code)
		}
		return w.Result().Cookies()[0]
	}
	rootCookie, adminCookie, financeCookie := login(951), login(952), login(953)
	request := func(method, path string, cookie *http.Cookie, body, csrfToken string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if method != http.MethodGet {
			req.Header.Set("Origin", config.ServerAddress)
			if csrfToken != "" {
				req.Header.Set("X-CSRF-Token", csrfToken)
			}
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	csrfResponse := request(http.MethodGet, "/api/refund-auth/csrf", rootCookie, "", "")
	var csrfBody struct {
		Token string `json:"csrf_token"`
	}
	if csrfResponse.Code != http.StatusOK || json.Unmarshal(csrfResponse.Body.Bytes(), &csrfBody) != nil || csrfBody.Token == "" {
		t.Fatalf("refund auth CSRF route failed: %d %s", csrfResponse.Code, csrfResponse.Body.String())
	}
	rootSelf := request(http.MethodGet, "/api/refund-auth/self", rootCookie, "", "")
	if rootSelf.Code != http.StatusOK || !strings.Contains(rootSelf.Body.String(), `"capability.manage"`) || !strings.Contains(rootSelf.Body.String(), `"refund_operations_enabled":false`) {
		t.Fatalf("root capabilities/gate mismatch: status=%d body=%s", rootSelf.Code, rootSelf.Body.String())
	}
	adminStepUp := request(http.MethodPost, "/api/refund-auth/step-up", adminCookie, `{"action":"capability.grant","target_user_id":953,"capabilities":["refund.review"],"reason":"approval","password":"local-password-123"}`, "")
	if adminStepUp.Code != http.StatusForbidden {
		t.Fatalf("role 10 acquired manager access: %d %s", adminStepUp.Code, adminStepUp.Body.String())
	}
	body := `{"action":"capability.grant","target_user_id":953,"capabilities":["refund.review"],"reason":"职责变更单 HTTP-1","password":"local-password-123"}`
	noCSRF := request(http.MethodPost, "/api/refund-auth/step-up", rootCookie, body, "")
	if noCSRF.Code != http.StatusForbidden {
		t.Fatalf("step-up accepted without CSRF: %d", noCSRF.Code)
	}
	oversized := `{"action":"capability.grant","target_user_id":953,"capabilities":["refund.review"],"reason":"oversized","password":"` + strings.Repeat("x", 9*1024) + `"}`
	oversizedResponse := request(http.MethodPost, "/api/refund-auth/step-up", rootCookie, oversized, csrfBody.Token)
	if oversizedResponse.Code != http.StatusBadRequest {
		t.Fatalf("oversized password request was not rejected: %d", oversizedResponse.Code)
	}
	stepUp := request(http.MethodPost, "/api/refund-auth/step-up", rootCookie, body, csrfBody.Token)
	if stepUp.Code != http.StatusOK {
		t.Fatalf("valid root step-up failed: %d %s", stepUp.Code, stepUp.Body.String())
	}
	var ticketResponse struct {
		Ticket string `json:"ticket"`
	}
	if err := json.Unmarshal(stepUp.Body.Bytes(), &ticketResponse); err != nil || ticketResponse.Ticket == "" {
		t.Fatalf("step-up response missing one-time ticket: %v", err)
	}
	grantBody, _ := json.Marshal(map[string]interface{}{"action": "capability.grant", "target_user_id": 953, "capabilities": []string{"refund.review"}, "reason": "职责变更单 HTTP-1", "step_up_ticket": ticketResponse.Ticket})
	grant := request(http.MethodPost, "/api/admin/refund-auth/grants", rootCookie, string(grantBody), csrfBody.Token)
	if grant.Code != http.StatusOK {
		t.Fatalf("root grant failed: %d %s", grant.Code, grant.Body.String())
	}
	financeSelf := request(http.MethodGet, "/api/refund-auth/self", financeCookie, "", "")
	if financeSelf.Code != http.StatusOK || !strings.Contains(financeSelf.Body.String(), `"refund.review"`) {
		t.Fatalf("recipient does not see current grant: %d %s", financeSelf.Code, financeSelf.Body.String())
	}
	grantList := request(http.MethodGet, "/api/admin/refund-auth/users/953/grants", rootCookie, "", "")
	if grantList.Code != http.StatusOK || strings.Contains(grantList.Body.String(), "credential_digest") || strings.Contains(grantList.Body.String(), "password_digest") {
		t.Fatalf("grant DTO leaked internal binding fields: %d %s", grantList.Code, grantList.Body.String())
	}
	if err := db.Model(&dbmodel.User{}).Where("id = ?", 951).Update("role", dbmodel.RoleCommonUser).Error; err != nil {
		t.Fatal(err)
	}
	revoked := request(http.MethodGet, "/api/admin/refund-auth/users/953/grants", rootCookie, "", "")
	if revoked.Code != http.StatusForbidden {
		t.Fatalf("stale root cookie retained manager permission: %d %s", revoked.Code, revoked.Body.String())
	}
}
