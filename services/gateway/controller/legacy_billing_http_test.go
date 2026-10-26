package controller_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/config"
	dbmodel "github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/router"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestLegacyBillingEndpointsDisabledOnlyInPointsMode(t *testing.T) {
	oldDB, oldPoints, oldRedis, oldMode := dbmodel.DB, config.PointsBillingEnabled, common.RedisEnabled, gin.Mode()
	oldDisplayToken, oldDisplayCurrency, oldQuotaPerUnit := config.DisplayTokenStatEnabled, config.DisplayInCurrencyEnabled, config.QuotaPerUnit
	t.Cleanup(func() {
		dbmodel.DB = oldDB
		config.PointsBillingEnabled = oldPoints
		config.DisplayTokenStatEnabled = oldDisplayToken
		config.DisplayInCurrencyEnabled = oldDisplayCurrency
		config.QuotaPerUnit = oldQuotaPerUnit
		common.RedisEnabled = oldRedis
		gin.SetMode(oldMode)
	})
	config.PointsBillingEnabled = true
	config.DisplayTokenStatEnabled = false
	config.DisplayInCurrencyEnabled = false
	config.QuotaPerUnit = 500000
	common.RedisEnabled = false
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/legacy-billing-http.db"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	dbmodel.DB = db
	if err := db.AutoMigrate(&dbmodel.User{}, &dbmodel.Token{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&dbmodel.User{Id: 93, Username: "billuser", Password: "password", Role: dbmodel.RoleCommonUser, Status: dbmodel.UserStatusEnabled, Quota: 12000, UsedQuota: 3000, Group: "default"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&dbmodel.Token{Id: 94, UserId: 93, Key: "legacybillingtestkey", Status: dbmodel.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 9000, UsedQuota: 1000}).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	engine := gin.New()
	router.SetDashboardRouter(engine)
	paths := []string{
		"/dashboard/billing/subscription",
		"/v1/dashboard/billing/subscription",
		"/dashboard/billing/usage",
		"/v1/dashboard/billing/usage",
	}
	request := func(path, authorization string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		res := httptest.NewRecorder()
		engine.ServeHTTP(res, req)
		return res
	}

	for _, path := range paths {
		t.Run("points_mode_rejects_"+strings.ReplaceAll(path, "/", "_"), func(t *testing.T) {
			unauthorized := request(path, "")
			if unauthorized.Code != http.StatusUnauthorized {
				t.Fatalf("unauthorized status=%d, want 401", unauthorized.Code)
			}
			res := request(path, "Bearer legacybillingtestkey")
			if res.Code != http.StatusConflict {
				t.Fatalf("points mode status=%d body=%s, want 409", res.Code, res.Body.String())
			}
			var body struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if body.Error.Code != "legacy_billing_disabled" || !strings.Contains(body.Error.Message, "积分") {
				t.Fatalf("unexpected error body: %s", res.Body.String())
			}
			for _, forbidden := range []string{"soft_limit_usd", "hard_limit_usd", "system_hard_limit_usd", "total_usage", "12000", "9000"} {
				if strings.Contains(res.Body.String(), forbidden) {
					t.Fatalf("disabled response leaks legacy billing data %q: %s", forbidden, res.Body.String())
				}
			}
		})
	}

	config.PointsBillingEnabled = false
	for _, path := range paths {
		t.Run("legacy_mode_preserves_"+strings.ReplaceAll(path, "/", "_"), func(t *testing.T) {
			res := request(path, "Bearer legacybillingtestkey")
			if res.Code != http.StatusOK {
				t.Fatalf("legacy mode status=%d body=%s, want 200", res.Code, res.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode legacy response: %v", err)
			}
			if strings.Contains(path, "subscription") {
				if body["object"] != "billing_subscription" || body["has_payment_method"] != true || body["soft_limit_usd"] != float64(12000) || body["hard_limit_usd"] != float64(12000) || body["system_hard_limit_usd"] != float64(12000) {
					t.Fatalf("legacy subscription contract changed: %s", res.Body.String())
				}
			} else if body["object"] != "list" || body["total_usage"] != float64(300000) {
				t.Fatalf("legacy usage contract changed: %s", res.Body.String())
			}
		})
	}
}
