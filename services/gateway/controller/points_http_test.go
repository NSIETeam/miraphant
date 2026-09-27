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
	"github.com/songquanpeng/one-api/middleware"
	dbmodel "github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/router"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestPointsHTTPRealRoutesAndCurrentAuthorization(t *testing.T) {
	oldDB, oldEnabled, oldAddress, oldSecret, oldMode, oldRedis := dbmodel.DB, config.PointsBillingEnabled, config.ServerAddress, config.SessionSecret, gin.Mode(), common.RedisEnabled
	t.Cleanup(func() {
		dbmodel.DB, config.PointsBillingEnabled, config.ServerAddress, config.SessionSecret = oldDB, oldEnabled, oldAddress, oldSecret
		common.RedisEnabled = oldRedis
		gin.SetMode(oldMode)
	})
	config.PointsBillingEnabled = true
	common.RedisEnabled = false
	config.ServerAddress = "http://example.test"
	config.SessionSecret = "points-test-session-secret"
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/points-http.db"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	dbmodel.DB = db
	if err := db.AutoMigrate(&dbmodel.User{}, &dbmodel.Token{}); err != nil {
		t.Fatal(err)
	}
	if err := dbmodel.MigratePointsSchema(); err != nil {
		t.Fatal(err)
	}
	users := []dbmodel.User{
		{Id: 41, Username: "customer", Password: "x", AccessToken: "customer-access-token", AffCode: "customer-code", Role: dbmodel.RoleCommonUser, Status: dbmodel.UserStatusEnabled},
		{Id: 42, Username: "admin", Password: "x", AccessToken: "admin-access-token", AffCode: "admin-code", Role: dbmodel.RoleAdminUser, Status: dbmodel.UserStatusEnabled},
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&[]dbmodel.Token{{Id: 77, UserId: 41, Key: "customer-token", Status: dbmodel.TokenStatusEnabled}, {Id: 78, UserId: 42, Key: "other-token", Status: dbmodel.TokenStatusEnabled}}).Error; err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(sessions.Sessions("session", cookie.NewStore([]byte(config.SessionSecret))))
	r.GET("/test-session/:id", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		s := sessions.Default(c)
		s.Set("id", id)
		if err := s.Save(); err != nil {
			t.Fatal(err)
		}
		c.Status(http.StatusNoContent)
	})
	router.SetApiRouter(r)
	defer func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() }()

	login := func(id int) *http.Cookie {
		res := httptest.NewRecorder()
		r.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/test-session/"+strconv.Itoa(id), nil))
		if res.Code != http.StatusNoContent || len(res.Result().Cookies()) == 0 {
			t.Fatalf("session setup status=%d cookies=%v", res.Code, res.Result().Cookies())
		}
		return res.Result().Cookies()[0]
	}
	adminCookie, customerCookie := login(42), login(41)
	request := func(method, path string, cookie *http.Cookie, body string, csrfID int) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if method != http.MethodGet {
			req.Header.Set("Origin", "http://example.test")
			if csrfID > 0 {
				req.Header.Set("X-CSRF-Token", middleware.PointsCSRFToken(csrfID))
			}
		}
		res := httptest.NewRecorder()
		r.ServeHTTP(res, req)
		return res
	}
	priceBody := `{"model":"fixture-model","version":"fixture-v1","source":"published test schedule","input_points_per_1k":"0.001","cached_input_points_per_1k":"0.0005","output_points_per_1k":"0.002","extra_points":"0"}`
	priceRes := request(http.MethodPost, "/api/admin/points/prices", adminCookie, priceBody, 42)
	if priceRes.Code != http.StatusCreated {
		t.Fatalf("publish price status=%d body=%s", priceRes.Code, priceRes.Body.String())
	}
	if got := request(http.MethodPost, "/api/admin/points/prices", adminCookie, priceBody, 42).Code; got != http.StatusCreated {
		t.Fatalf("same price replay status=%d", got)
	}
	grantBody := `{"user_id":41,"amount_points":"0.123456","business_key":"welcome-41","reason":"support grant"}`
	if got := request(http.MethodPost, "/api/admin/points/adjustments", adminCookie, grantBody, 42).Code; got != http.StatusCreated {
		t.Fatalf("grant status=%d", got)
	}
	if got := request(http.MethodPost, "/api/admin/points/adjustments", adminCookie, grantBody, 42).Code; got != http.StatusCreated {
		t.Fatalf("same grant replay status=%d", got)
	}
	changedGrant := strings.Replace(grantBody, "0.123456", "0.123457", 1)
	if got := request(http.MethodPost, "/api/admin/points/adjustments", adminCookie, changedGrant, 42).Code; got != http.StatusConflict {
		t.Fatalf("conflicting grant replay status=%d", got)
	}
	walletRes := request(http.MethodGet, "/api/points/wallet", customerCookie, "", 0)
	if walletRes.Code != http.StatusOK {
		t.Fatalf("wallet status=%d body=%s", walletRes.Code, walletRes.Body.String())
	}
	var wallet map[string]int64
	if err := json.Unmarshal(walletRes.Body.Bytes(), &wallet); err != nil {
		t.Fatal(err)
	}
	if wallet["available_micro"] != 123456 || wallet["gifted_available_micro"] != 123456 || wallet["gifted_total_micro"] != 123456 {
		t.Fatalf("wallet totals wrong: %v", wallet)
	}
	estimate := request(http.MethodPost, "/api/points/estimate", customerCookie, `{"model":"fixture-model","prompt":"hello world","max_output_tokens":10}`, 0)
	if estimate.Code != http.StatusOK {
		t.Fatalf("estimate status=%d body=%s", estimate.Code, estimate.Body.String())
	}
	var estimateFields map[string]any
	if err := json.Unmarshal(estimate.Body.Bytes(), &estimateFields); err != nil {
		t.Fatal(err)
	}
	if estimateFields["budget_micro"] != float64(31) || estimateFields["price_version"] != "fixture-v1" || estimateFields["final_bill"] != false {
		t.Fatalf("unexpected estimate: %v", estimateFields)
	}
	if got := request(http.MethodPut, "/api/points/tokens/77/budget", customerCookie, `{"limit_points":"1"}`, 41).Code; got != http.StatusOK {
		t.Fatalf("owner budget status=%d", got)
	}
	if got := request(http.MethodPut, "/api/points/tokens/77/budget", login(42), `{"limit_points":"1"}`, 42).Code; got != http.StatusNotFound {
		t.Fatalf("other user budget status=%d", got)
	}
	if err := db.Model(&dbmodel.User{}).Where("id = ?", 42).Update("role", dbmodel.RoleCommonUser).Error; err != nil {
		t.Fatal(err)
	}
	if got := request(http.MethodGet, "/api/admin/points/pending", adminCookie, "", 0).Code; got != http.StatusForbidden {
		t.Fatalf("stale admin cookie status=%d", got)
	}
	config.PointsBillingEnabled = false
	if got := request(http.MethodGet, "/api/points/prices", nil, "", 0).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("disabled points status=%d", got)
	}
}
