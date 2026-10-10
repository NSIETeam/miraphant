package router

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/config"
	dbmodel "github.com/songquanpeng/one-api/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestPointsRelayRouterRejectsOversizedDecompressedBodyBeforeBilling(t *testing.T) {
	oldDB, oldSQLite, oldRedis, oldEnabled, oldLimit := dbmodel.DB, common.UsingSQLite, common.RedisEnabled, config.PointsBillingEnabled, config.PointsMaxInputBytes
	t.Cleanup(func() {
		dbmodel.DB, common.UsingSQLite, common.RedisEnabled = oldDB, oldSQLite, oldRedis
		config.PointsBillingEnabled, config.PointsMaxInputBytes = oldEnabled, oldLimit
	})
	common.RedisEnabled = false
	common.UsingSQLite = true
	config.PointsBillingEnabled = true
	config.PointsMaxInputBytes = 1024
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "relay-router.db")+"?_busy_timeout=5000"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	dbmodel.DB = db
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&dbmodel.User{}, &dbmodel.Token{}); err != nil {
		t.Fatal(err)
	}
	if err := dbmodel.MigratePointsSchema(); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&dbmodel.User{Id: 91, Username: "body-limit-user", Password: "fixture", AccessToken: "body-limit-user-access", AffCode: "body-limit-user-code", Role: dbmodel.RoleCommonUser, Status: dbmodel.UserStatusEnabled}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&dbmodel.Token{Id: 92, UserId: 91, Key: "bodylimittoken", Status: dbmodel.TokenStatusEnabled, RemainQuota: 1_000_000}).Error; err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	SetRelayRouter(engine)
	plainBody, err := json.Marshal(map[string]any{
		"model":    "public-model",
		"messages": []map[string]string{{"role": "user", "content": strings.Repeat("x", 64*1024)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	zipWriter := gzip.NewWriter(&compressed)
	if _, err := zipWriter.Write(plainBody); err != nil {
		t.Fatal(err)
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(compressed.Bytes()))
	req.Header.Set("Authorization", "Bearer bodylimittoken")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("oversized decompressed body status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var holdCount int64
	if err := db.Model(&dbmodel.PointHold{}).Count(&holdCount).Error; err != nil {
		t.Fatal(err)
	}
	if holdCount != 0 {
		t.Fatalf("oversized request created %d billing holds", holdCount)
	}
}
