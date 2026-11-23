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

func TestBrandOptionRoutesRequireCurrentRootSessionAndCSRF(t *testing.T) {
	oldDB, oldAddress, oldSecret, oldBilling, oldRedis, oldMode := dbmodel.DB, config.ServerAddress, config.SessionSecret, config.PointsBillingEnabled, common.RedisEnabled, gin.Mode()
	oldQuota, oldTopUp, oldChatLink := config.QuotaForNewUser, config.TopUpLink, config.ChatLink
	oldOptions := make(map[string]string)
	config.OptionMapRWMutex.Lock()
	for key, value := range config.OptionMap {
		oldOptions[key] = value
	}
	config.OptionMap = make(map[string]string)
	config.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		dbmodel.DB, config.ServerAddress, config.SessionSecret, config.PointsBillingEnabled = oldDB, oldAddress, oldSecret, oldBilling
		config.QuotaForNewUser, config.TopUpLink = oldQuota, oldTopUp
		config.ChatLink = oldChatLink
		common.RedisEnabled = oldRedis
		gin.SetMode(oldMode)
		config.OptionMapRWMutex.Lock()
		config.OptionMap = oldOptions
		config.OptionMapRWMutex.Unlock()
	})
	config.ServerAddress = "https://brand-options.example.test"
	config.SessionSecret = "brand-options-session-test-secret"
	config.PointsBillingEnabled = true
	common.RedisEnabled = false
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/brand-options.db"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	dbmodel.DB = db
	if err := db.AutoMigrate(&dbmodel.User{}, &dbmodel.Option{}); err != nil {
		t.Fatal(err)
	}
	users := []dbmodel.User{
		{Id: 981, Username: "brand-root", Password: "x", AccessToken: "brand-root-token", AffCode: "brand-root", Role: dbmodel.RoleRootUser, Status: dbmodel.UserStatusEnabled},
		{Id: 982, Username: "brand-admin", Password: "x", AccessToken: "brand-admin-token", AffCode: "brand-admin", Role: dbmodel.RoleAdminUser, Status: dbmodel.UserStatusEnabled},
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&dbmodel.Option{Key: "QuotaForNewUser", Value: "123456"}).Error; err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(sessions.Sessions("session", cookie.NewStore([]byte(config.SessionSecret))))
	r.GET("/test-session/:id", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		session := sessions.Default(c)
		session.Set("id", id)
		session.Set("refund_auth_session", "brand-options-binding-0123456789abcdef")
		if err := session.Save(); err != nil {
			t.Fatal(err)
		}
		c.Status(http.StatusNoContent)
	})
	router.SetApiRouter(r)
	if sqlDB, err := db.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	login := func(id int) *http.Cookie {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/test-session/"+strconv.Itoa(id), nil))
		if w.Code != http.StatusNoContent || len(w.Result().Cookies()) == 0 {
			t.Fatalf("create test session: status=%d", w.Code)
		}
		return w.Result().Cookies()[0]
	}
	rootCookie, adminCookie := login(981), login(982)
	request := func(method, path string, cookie *http.Cookie, body string, csrf string, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if method != http.MethodGet {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", origin)
			if csrf != "" {
				req.Header.Set("X-CSRF-Token", csrf)
			}
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	legacyAuth := httptest.NewRequest(http.MethodGet, "/api/option/", nil)
	legacyAuth.Header.Set("Authorization", "Bearer brand-root-token")
	legacyAuthResponse := httptest.NewRecorder()
	r.ServeHTTP(legacyAuthResponse, legacyAuth)
	if got := legacyAuthResponse.Code; got != http.StatusUnauthorized {
		t.Fatalf("legacy bearer token read options: %d", got)
	}
	if got := request(http.MethodGet, "/api/option/", nil, "", "", "").Code; got != http.StatusUnauthorized {
		t.Fatalf("options allowed without browser session: %d", got)
	}
	if got := request(http.MethodGet, "/api/option/", adminCookie, "", "", "").Code; got != http.StatusForbidden {
		t.Fatalf("role 10 read options: %d", got)
	}
	rootOptions := request(http.MethodGet, "/api/option/", rootCookie, "", "", "")
	if got := rootOptions.Code; got != http.StatusOK {
		t.Fatalf("root option read failed: %d", got)
	}
	for _, deadKey := range []string{"Notice", "About", "HomePageContent", "SystemName", "Logo", "Footer", "Theme"} {
		if strings.Contains(rootOptions.Body.String(), `"key":"`+deadKey+`"`) {
			t.Fatalf("inactive setting %s still appears editable in options response", deadKey)
		}
	}
	csrfResponse := request(http.MethodGet, "/api/refund-auth/csrf", rootCookie, "", "", "")
	var csrfBody struct {
		Token string `json:"csrf_token"`
	}
	if csrfResponse.Code != http.StatusOK || json.Unmarshal(csrfResponse.Body.Bytes(), &csrfBody) != nil || csrfBody.Token == "" {
		t.Fatalf("CSRF retrieval failed: %d %s", csrfResponse.Code, csrfResponse.Body.String())
	}
	legacyBefore := dbmodel.Option{}
	if err := db.First(&legacyBefore, "key = ?", "QuotaForNewUser").Error; err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"QuotaForNewUser", "QuotaForInviter", "QuotaForInvitee", "QuotaRemindThreshold", "PreConsumedQuota", "ModelRatio", "GroupRatio", "CompletionRatio", "TopUpLink", "QuotaPerUnit", "DisplayInCurrencyEnabled", "DisplayTokenStatEnabled", "ApproximateTokenEnabled"} {
		body, _ := json.Marshal(map[string]string{"key": key, "value": "9"})
		if got := request(http.MethodPut, "/api/option/", rootCookie, string(body), csrfBody.Token, config.ServerAddress).Code; got != http.StatusConflict {
			t.Fatalf("legacy billing option %s was writable with points on: %d", key, got)
		}
	}
	legacyAfter := dbmodel.Option{}
	if err := db.First(&legacyAfter, "key = ?", "QuotaForNewUser").Error; err != nil || legacyAfter.Value != legacyBefore.Value {
		t.Fatalf("legacy value changed: before=%q after=%q err=%v", legacyBefore.Value, legacyAfter.Value, err)
	}
	if got := request(http.MethodPut, "/api/option/", rootCookie, `{"key":"ChatLink","value":"https://chat.example.test"}`, "", config.ServerAddress).Code; got != http.StatusForbidden {
		t.Fatalf("option write without CSRF accepted: %d", got)
	}
	if got := request(http.MethodPut, "/api/option/", rootCookie, `{"key":"ChatLink","value":"https://chat.example.test"}`, csrfBody.Token, "https://attacker.example.test").Code; got != http.StatusForbidden {
		t.Fatalf("cross-origin option write accepted: %d", got)
	}
	if got := request(http.MethodPut, "/api/option/", rootCookie, `{"key":"ChatLink","value":"https://chat.example.test"}`, csrfBody.Token, config.ServerAddress).Code; got != http.StatusOK {
		t.Fatalf("valid root option write failed: %d", got)
	}
	if err := db.Model(&dbmodel.User{}).Where("id = ?", 981).Update("role", dbmodel.RoleCommonUser).Error; err != nil {
		t.Fatal(err)
	}
	if got := request(http.MethodGet, "/api/option/", rootCookie, "", "", "").Code; got != http.StatusForbidden {
		t.Fatalf("stale root session kept options access after DB demotion: %d", got)
	}
	if err := db.Model(&dbmodel.User{}).Where("id = ?", 981).Updates(map[string]interface{}{"role": dbmodel.RoleRootUser, "status": dbmodel.UserStatusDisabled}).Error; err != nil {
		t.Fatal(err)
	}
	if got := request(http.MethodPut, "/api/option/", rootCookie, `{"key":"ChatLink","value":"https://disabled.example.test"}`, csrfBody.Token, config.ServerAddress).Code; got != http.StatusForbidden {
		t.Fatalf("disabled root account retained option write access: %d", got)
	}
	if err := db.Model(&dbmodel.User{}).Where("id = ?", 981).Updates(map[string]interface{}{"role": dbmodel.RoleRootUser, "status": dbmodel.UserStatusEnabled}).Error; err != nil {
		t.Fatal(err)
	}
	config.PointsBillingEnabled = false
	legacyStill := dbmodel.Option{}
	if err := db.First(&legacyStill, "key = ?", "QuotaForNewUser").Error; err != nil || legacyStill.Value != "123456" {
		t.Fatalf("switching legacy mode changed historical quota value: %#v err=%v", legacyStill, err)
	}
	if got := request(http.MethodPut, "/api/option/", rootCookie, `{"key":"QuotaForNewUser","value":"654321"}`, csrfBody.Token, config.ServerAddress).Code; got != http.StatusOK {
		t.Fatalf("legacy billing setting could not be updated with points off: %d", got)
	}
	if err := db.First(&legacyStill, "key = ?", "QuotaForNewUser").Error; err != nil || legacyStill.Value != "654321" {
		t.Fatalf("legacy setting update was not persisted when points off: %#v err=%v", legacyStill, err)
	}
}

func TestStatusReportsServerPointsMode(t *testing.T) {
	oldBilling, oldMode, oldRedis := config.PointsBillingEnabled, gin.Mode(), common.RedisEnabled
	t.Cleanup(func() { config.PointsBillingEnabled = oldBilling; gin.SetMode(oldMode); common.RedisEnabled = oldRedis })
	config.PointsBillingEnabled = true
	common.RedisEnabled = false
	gin.SetMode(gin.TestMode)
	r := gin.New()
	router.SetApiRouter(r)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	var body struct {
		Data struct {
			PointsBillingEnabled bool    `json:"points_billing_enabled"`
			Theme                string  `json:"theme"`
			TopUpLink            string  `json:"top_up_link"`
			QuotaPerUnit         float64 `json:"quota_per_unit"`
		} `json:"data"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil || !body.Data.PointsBillingEnabled || body.Data.Theme != "default" || body.Data.TopUpLink != "" || body.Data.QuotaPerUnit != 0 {
		t.Fatalf("status omitted effective brand/billing mode: code=%d body=%s", w.Code, w.Body.String())
	}
}
